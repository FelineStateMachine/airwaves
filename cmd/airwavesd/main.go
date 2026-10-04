// Command airwavesd runs the Airwaves engine on a home server: lineup,
// reception and guide, live TV and recordings through Tvheadend, served to
// the desktop app over HTTP. With AIRWAVES_ANTENNA=off it serves the custom
// channels only (Jellyfin, YouTube, folders of videos and the weather), with
// no antenna or Tvheadend; see docs/self-hosting.md.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"airwaves/internal/admin"
	"airwaves/internal/api"
	"airwaves/internal/hdhr"
	"airwaves/internal/service"
	"airwaves/internal/tvh"
	"airwaves/internal/vchan"
	"airwaves/internal/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func env(key, def string) string { return cmp.Or(os.Getenv(key), def) }

func envFloat(key string) float64 {
	v, _ := strconv.ParseFloat(os.Getenv(key), 64)
	return v
}

func run() error {
	host, _ := os.Hostname()
	listen := flag.String("listen", env("AIRWAVES_LISTEN", ":"+strconv.Itoa(api.DefaultPort)), "HTTP listen address")
	data := flag.String("data", env("AIRWAVES_DATA", "/data"), "directory for config, caches and DVR rules")
	antenna := flag.String("antenna", os.Getenv("AIRWAVES_ANTENNA"), "on (the default): the over-the-air lineup, guide, tuners and recording through Tvheadend; off: custom channels only")
	tvhURL := flag.String("tvheadend", env("AIRWAVES_TVHEADEND", "http://tvheadend:9981"), "Tvheadend base URL; empty disables tuners and recording")
	// Location is deployment configuration; clients never see or change it.
	zip := flag.String("zip", os.Getenv("AIRWAVES_ZIP"), "US ZIP code of the antenna, for its lineup and listings (required with an antenna); without an antenna, of the weather (default none)")
	lat := flag.Float64("lat", envFloat("AIRWAVES_LAT"), "antenna latitude; with -lon, overrides the ZIP centroid")
	lon := flag.Float64("lon", envFloat("AIRWAVES_LON"), "antenna longitude")
	name := flag.String("name", env("AIRWAVES_NAME", host), "server name shown in the app")
	token := flag.String("token", env("AIRWAVES_TOKEN", ""), "optional bearer token for the API")
	demo := flag.Bool("demo", env("AIRWAVES_DEMO", "true") == "true", "give Tvheadend generated channels until a tuner is attached")
	demoFFmpeg := flag.String("demo-ffmpeg", env("AIRWAVES_DEMO_FFMPEG", "/usr/bin/ffmpeg"), "ffmpeg path inside the Tvheadend container")
	weatherStar := flag.Int("weatherstar-port", int(envFloat("AIRWAVES_WEATHERSTAR_PORT")), "host port of a WeatherStar 4000+ container; 0 disables the WX channel")
	weatherStarURL := flag.String("weatherstar-url", os.Getenv("AIRWAVES_WEATHERSTAR_URL"), "where airwavesd reaches the WeatherStar display, when not at 127.0.0.1 on -weatherstar-port (http://weatherstar:8080 for a container of its own)")
	chromium := flag.String("chromium", env("AIRWAVES_CHROMIUM", findChromium()), "Chromium for rendering the weather channel; empty disables it")
	music := flag.String("music", env("AIRWAVES_MUSIC", "/music"), "folder of MP3s for the weather channel")
	channels := flag.String("channels", env("AIRWAVES_CHANNELS", "/channels"), "folder of folder channels, a subfolder of videos each")
	hdhrSetting := flag.String("hdhr", os.Getenv("AIRWAVES_HDHR"), "emulated HDHomeRun for Jellyfin, Channels DVR and others: on or off (default on, off without an antenna)")
	hdhrListen := flag.String("hdhr-listen", env("AIRWAVES_HDHR_LISTEN", ":5004"), "address for the emulated HDHomeRun; empty disables it")
	discovery := flag.String("hdhr-discovery", os.Getenv("AIRWAVES_HDHR_DISCOVERY"), "answer HDHomeRun discovery on the LAN (UDP 65001): on or off (default on, off without an antenna)")
	ytdlp := flag.String("ytdlp", env("AIRWAVES_YTDLP", ""), "yt-dlp for YouTube channels; empty keeps an up-to-date copy in <data>/tools")
	loudnessSetting := flag.String("loudness", env("AIRWAVES_LOUDNESS", "off"), "even custom channels' sound out to this level in LUFS as it streams (-24 is broadcast TV's), or off")
	health := flag.Bool("healthcheck", false, "check that the airwavesd at -listen answers, then exit (for container health checks)")
	flag.Parse()

	if *health {
		return healthcheck(*listen)
	}
	feat, err := resolveFeatures(*antenna, *hdhrSetting, *discovery)
	if err != nil {
		return err
	}
	// The admin password comes from the environment only, never a flag, so
	// it stays out of process listings, and it's dropped from the
	// environment so the tools airwavesd runs don't inherit it.
	adminPassword := os.Getenv("AIRWAVES_ADMIN_PASSWORD")
	_ = os.Unsetenv("AIRWAVES_ADMIN_PASSWORD")
	loudness, err := parseLoudness(*loudnessSetting)
	if err != nil {
		return err
	}
	if feat.antenna && *zip == "" {
		return errors.New("the antenna's lineup and listings need its ZIP code: set AIRWAVES_ZIP (or AIRWAVES_ANTENNA=off for custom channels only)")
	}
	if err := writable(*data); err != nil {
		return err
	}
	if *channels != "" {
		if err := writable(*channels); err != nil {
			log.Printf("channels: %v; the admin page can't save channels", err)
		}
	}

	// YouTube changes faster than images are rebuilt, so by default
	// airwavesd keeps the latest yt-dlp release itself.
	yt := &vchan.YtDlp{Path: *ytdlp, HTTP: web.NewClient()}
	if yt.Path == "" {
		yt.Path, yt.Update = filepath.Join(*data, "tools", "yt-dlp"), true
	}

	opt := service.Options{
		Name:            *name,
		Mode:            "server",
		CacheDir:        filepath.Join(*data, "cache"),
		ConfigPath:      filepath.Join(*data, "config.json"),
		Config:          service.Config{ZIP: *zip, GuideHours: 48},
		DVRPath:         filepath.Join(*data, "dvr.json"),
		Demo:            *demo,
		DemoFFmpeg:      *demoFFmpeg,
		NoAntenna:       !feat.antenna,
		WeatherStarPort: *weatherStar,
		WeatherStarURL:  *weatherStarURL,
		Chromium:        *chromium,
		MusicDir:        *music,
		ChannelsDir:     *channels,
		YtDlp:           yt,
		Loudness:        loudness,
		Progress:        func(s string) { log.Print(s) },
	}
	if feat.antenna && *tvhURL != "" {
		opt.Tvheadend = tvh.New(*tvhURL, web.NewClient())
	}
	svc, err := service.New(opt)
	if err != nil {
		return err
	}
	defer svc.Close()
	// Apply the configured location over anything saved earlier; the saved
	// config keeps only what clients may change (hours of listings).
	cfg, _ := svc.Config(context.Background())
	cfg.ZIP, cfg.Lat, cfg.Lon = *zip, *lat, *lon
	if _, err := svc.SetConfig(context.Background(), cfg); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	apiServer := &api.Server{
		Backend: svc, Streams: svc.StreamHandler(), Extra: svc.WeatherHandler(), Logos: svc.LogoHandler(),
		Token: *token, AdminPassword: adminPassword,
		// Where the folders are on the host, as the compose file mounts them.
		Admin: adminPage(ctx, svc, filepath.Join(*data, "backups"), env("AIRWAVES_HOST_CHANNELS", *channels), env("AIRWAVES_HOST_MUSIC", *music)),
	}
	srv := &http.Server{
		Addr:              *listen,
		Handler:           logRequests(apiServer.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	log.Printf("airwavesd %s listening on %s", service.Version, *listen)
	if feat.antenna {
		log.Printf("antenna: on, Tvheadend %q", *tvhURL)
	} else {
		log.Print("antenna: off, custom channels only")
	}
	switch {
	case *lat != 0 && *lon != 0:
		log.Printf("location: %.4f, %.4f", *lat, *lon)
	case *zip != "":
		log.Printf("location: ZIP %s", *zip)
	default:
		log.Print("location: none, so no weather (set AIRWAVES_ZIP, or AIRWAVES_LAT and AIRWAVES_LON)")
	}
	switch {
	case *weatherStar == 0:
		log.Print("weather channel: off, no WeatherStar display")
	case *weatherStarURL != "":
		log.Printf("weather channel: WeatherStar display at %s (port %d for the app), on while it answers", *weatherStarURL, *weatherStar)
	default:
		log.Printf("weather channel: WeatherStar display on port %d", *weatherStar)
	}
	if svc.Library() != nil {
		log.Printf("custom channels: %s", *channels)
	} else {
		log.Print("custom channels: off, they need a channels folder and ffmpeg")
	}
	switch {
	case apiServer.Admin == nil:
		log.Print("admin page: off")
	case adminPassword != "":
		log.Print("admin page: /admin/, with a password")
	default:
		log.Print("admin page: /admin/, with no password: keep this server on a trusted network or set AIRWAVES_ADMIN_PASSWORD")
	}
	log.Printf("app API token: %s", onOrOff(*token != ""))
	if yt.Update {
		log.Printf("yt-dlp: %s, kept up to date", yt.Path)
	}
	if feat.hdhr && *hdhrListen != "" {
		startHDHR(ctx, svc, *hdhrListen, *name, feat.discovery)
	} else {
		log.Print("HDHomeRun: off")
	}
	go svc.Run(ctx)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// logRequests logs API calls, skipping the steady stream of HLS fetches.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.code, time.Since(start).Round(time.Millisecond))
		}
	})
}

// adminPage is the channel admin page, when there's a channels folder. It
// keeps backups of the channel setup in backups, making the daily ones
// until ctx ends. channels and music are the folders as the host sees
// them, for its hints.
func adminPage(ctx context.Context, svc *service.Service, backups, channels, music string) http.Handler {
	lib := svc.Library()
	if lib == nil {
		return nil
	}
	page := &admin.Server{Library: lib, Weather: svc.WeatherChannel(), Antenna: svc.AntennaChannels, HTTP: web.NewClient(),
		Backups: backups, Version: service.Version, ChannelsDir: channels, MusicDir: music}
	go page.Run(ctx)
	return page.Handler()
}

// startHDHR serves the emulated HDHomeRun and, with discover, answers
// discovery on the LAN.
func startHDHR(ctx context.Context, svc *service.Service, addr, name string, discover bool) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		log.Printf("hdhr: bad listen address %q: %v", addr, err)
		return
	}
	port, _ := strconv.Atoi(portStr)
	dev := &hdhr.Server{
		Backend:      svc.HDHR(),
		DeviceID:     hdhr.DeviceIDFor(name),
		FriendlyName: "Airwaves",
		Tuners:       svc.TunerCount,
		Logos:        svc.LogoHandler(),
		FromHost:     !discover,
	}
	srv := &http.Server{Addr: addr, Handler: dev.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	log.Printf("HDHomeRun: on %s (device %08X), LAN discovery %s", addr, dev.DeviceID, onOrOff(discover))
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Printf("hdhr: %v", err)
		}
	}()
	if !discover {
		return
	}
	go func() {
		if err := dev.Discover(ctx, port); err != nil {
			log.Printf("hdhr discovery: %v", err)
		}
	}()
}

// features are what this server does.
type features struct {
	antenna   bool // the over-the-air lineup, guide and Tvheadend
	hdhr      bool // the emulated HDHomeRun
	discovery bool // answering HDHomeRun discovery on the LAN
}

// resolveFeatures reads the on and off settings. The HDHomeRun and its
// discovery default to on with an antenna, as on a home server on the LAN,
// and off without one, as in a container for someone's custom channels.
func resolveFeatures(antenna, hdhr, discovery string) (features, error) {
	var f features
	var err error
	if f.antenna, err = onOff("AIRWAVES_ANTENNA", antenna, true); err != nil {
		return f, err
	}
	if f.hdhr, err = onOff("AIRWAVES_HDHR", hdhr, f.antenna); err != nil {
		return f, err
	}
	if f.discovery, err = onOff("AIRWAVES_HDHR_DISCOVERY", discovery, f.antenna); err != nil {
		return f, err
	}
	f.discovery = f.discovery && f.hdhr
	return f, nil
}

// onOff reads an on or off setting, def when it's empty.
func onOff(name, v string, def bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return def, nil
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s is on or off, not %q", name, v)
}

func onOrOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// writable makes sure dir exists and airwavesd can write in it. In a
// container, a folder mounted from the host often belongs to someone else.
func writable(dir string) error {
	err := os.MkdirAll(dir, 0o755)
	if err == nil {
		var f *os.File
		if f, err = os.CreateTemp(dir, ".airwaves-*"); err == nil {
			f.Close()
			_ = os.Remove(f.Name())
			return nil
		}
	}
	return fmt.Errorf("can't write to %s (%w): airwavesd runs as user %d and needs to (on the host: sudo chown -R %d:%d <that folder>)",
		dir, err, os.Getuid(), os.Getuid(), os.Getgid())
}

// healthcheck asks the airwavesd listening on addr whether it's up.
func healthcheck(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("airwavesd health: HTTP %d", resp.StatusCode)
	}
	return nil
}

func findChromium() string {
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// parseLoudness reads AIRWAVES_LOUDNESS: a level in LUFS, from -40 to -5,
// or "off" (0).
func parseLoudness(s string) (float64, error) {
	if strings.EqualFold(strings.TrimSpace(s), "off") {
		return 0, nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < -40 || v > -5 {
		return 0, fmt.Errorf("loudness %q: give a level in LUFS from -40 to -5 (-24 is broadcast TV's), or off", s)
	}
	return v, nil
}
