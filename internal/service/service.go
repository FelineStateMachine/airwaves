// Package service is the Airwaves engine: the tuner's lineup and what was
// measured of its signal, the guide, live streams and recordings. airwavesd
// runs it on a home server and exposes it over HTTP (see package api).
//
// What it serves the app is either measured (the tuner's channels and
// signal) or on record (the FCC's transmitters, the listings, the ATSC 3.0
// list). Reception estimates are for planning, in otascan, not here.
package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/dvr"
	"airwaves/internal/geo"
	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/phase"
	"airwaves/internal/signal"
	"airwaves/internal/store"
	"airwaves/internal/stream"
	"airwaves/internal/tuner"
	"airwaves/internal/tvh"
	"airwaves/internal/vchan"
	"airwaves/internal/weather"
	"airwaves/internal/web"
)

// Version is reported to clients.
const Version = "0.2.0"

// ErrNoDVR is returned by recording calls when no Tvheadend is configured.
var ErrNoDVR = errors.New("recording needs an Airwaves server with Tvheadend")

// ErrNotRecordable is returned when asked to record a custom channel.
var ErrNotRecordable = errors.New("custom channels play from the server and can't be recorded")

// ErrNoAntenna is returned for what needs an antenna (antenna channels,
// signal, recording) by a server that has only custom channels.
var ErrNoAntenna = errors.New("this Airwaves server has no antenna, only custom channels")

// ErrNoLocation is returned for the weather by a server without an
// antenna that wasn't told where it is.
var ErrNoLocation = errors.New("the weather needs a location: set AIRWAVES_ZIP, or AIRWAVES_LAT and AIRWAVES_LON, on the server")

// reportTTL is how long a built report is reused before rebuilding from
// (mostly cached) sources.
const reportTTL = 30 * time.Minute

// Config is the location the lineup is built for.
type Config struct {
	ZIP string `json:"zip"`
	// Lat and Lon override the ZIP centroid when both are non-zero.
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	RadiusKm   float64 `json:"radiusKm"`
	GuideHours int     `json:"guideHours"`
}

func (c Config) normalized() Config {
	if c.RadiusKm <= 0 {
		c.RadiusKm = 160
	}
	if c.GuideHours <= 0 {
		c.GuideHours = 24
	}
	c.GuideHours = min(c.GuideHours, 72)
	return c
}

// Info describes the engine a client is talking to.
type Info struct {
	Name     string       `json:"name"`
	Mode     string       `json:"mode"` // "local" or "server"
	Version  string       `json:"version"`
	Tuner    tuner.Device `json:"tuner"`
	DVR      bool         `json:"dvr"`
	Playback string       `json:"playback,omitempty"` // why streaming is unavailable
	// WeatherStar is set when a WeatherStar 4000+ display is available at
	// /weatherstar on the server.
	WeatherStar bool `json:"weatherStar,omitempty"`
	// Antenna is false for a server with custom channels only: no antenna
	// channels, reception reports or recording.
	Antenna bool `json:"antenna"`
}

// Snapshot is the lineup with its listings.
type Snapshot struct {
	// Report is what is on record at the location: the FCC's transmitters
	// (with no estimates: their "signal" is empty), the ATSC 3.0 hosts and
	// the data sources. Its Channels are the tuner's lineup, as Antenna
	// has it, in the shape app versions before the measured lineup read.
	Report *lineup.Report `json:"report"`
	// Guide is the listings of the tuner's channels.
	Guide *guide.Guide `json:"guide"`
	// Custom lists the custom channels that are on, by number, with their
	// details and schedules: the weather channel and the folder channels.
	// It's always a list in a Snapshot, if an empty one, so the app can tell
	// a server that lists the weather channel here from one before that.
	Custom []CustomChannel `json:"custom"`
	// Antenna is the tuner, its channels and what was measured of them.
	Antenna *Antenna `json:"antenna"`
}

// CustomChannel is a custom channel and its schedule, for the app's guide.
type CustomChannel struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	// Kind is "weather", "folder", "jellyfin" or "youtube". The app shows
	// the weather channel itself, from /weatherstar, and writes its guide
	// from the weather report, so its Programs are empty.
	Kind        string `json:"kind"`
	CallSign    string `json:"callSign,omitempty"` // a short label, "WX"
	Category    string `json:"category"`           // one of vchan.Categories
	Description string `json:"description,omitempty"`
	// Logo is a path on the server, "/channel-logos/1.4?v=…", when the
	// channel has one.
	Logo     string          `json:"logo,omitempty"`
	Programs []guide.Program `json:"programs"`
}

// Options configure a Service.
type Options struct {
	Name     string
	Mode     string // "local" or "server"
	CacheDir string // defaults to the user cache dir
	// ConfigPath persists Config; empty keeps it in memory.
	ConfigPath string
	Config     Config
	// Loopback serves streams on 127.0.0.1 for an in-process player.
	Loopback bool
	Tuner    tuner.Tuner
	// Tvheadend enables server tuners and recording.
	Tvheadend *tvh.Client
	DVRPath   string
	// Demo fills Tvheadend with generated channels, for development before
	// there's a tuner. They're never made, and are removed, once a tuner's
	// antenna network exists.
	Demo bool
	// DemoFFmpeg is the ffmpeg path inside the Tvheadend container.
	DemoFFmpeg string
	// NoAntenna serves custom channels only: no Tvheadend, demo channels,
	// lineup or listings from the ZIP code, reception reports or recording.
	// The location (Config) is then only for the weather, and there's none
	// unless it's given.
	NoAntenna bool
	// WeatherStarPort is where a WeatherStar 4000+ container listens on the
	// server's host; zero disables the WX channel.
	WeatherStarPort int
	// WeatherStarURL is where the server itself reaches the display, when
	// that isn't 127.0.0.1 at WeatherStarPort: "http://weatherstar:8080"
	// for a container of its own. The weather channel is then on only
	// while the display answers there, since it may not have been started.
	WeatherStarURL string
	// Chromium, when set, renders Airwaves Weather to video as a custom
	// channel for HDHomeRun clients.
	Chromium string
	// MusicDir holds MP3s for the weather channel; empty or without MP3s
	// uses the display's bundled tracks.
	MusicDir string
	// ChannelsDir holds folder channels, a subfolder of videos each.
	ChannelsDir string
	// YtDlp runs yt-dlp for YouTube channels; yt-dlp from PATH when nil.
	YtDlp *vchan.YtDlp
	// Loudness is the level custom channels' sound is evened out to as
	// it streams, in LUFS (vchan.DefaultLoudness is antenna TV's); 0
	// leaves it as it is.
	Loudness float64
	// SignalPath keeps the signal measurements; empty keeps them in
	// memory.
	SignalPath string
	Progress   func(string)
}

// Service is the engine.
type Service struct {
	opt     Options
	http    *http.Client
	cache   *store.Cache
	signals *signal.Store
	timing  measureTiming
	// ctx lasts until Close, for work that outlives a request (sweeps).
	ctx       context.Context
	cancel    context.CancelFunc
	streams   *stream.Server
	streamErr error
	tvhTuner  *tuner.Tvheadend
	dvr       *dvr.Manager
	weather   *weather.Source
	// wx is the weather channel, when there's a WeatherStar display;
	// wxStreams when it can be rendered to video for HDHomeRun clients.
	// wxLocal is where the server reaches the display.
	wx        *vchan.Weather
	wxStreams bool
	wxLocal   string
	library   *vchan.Library

	buildMu sync.Mutex // serializes report builds

	mu     sync.Mutex
	cfg    Config
	tuner  tuner.Tuner
	report *lineup.Report
	guide  *guide.Guide
	built  time.Time
	wxUp   bool // the display at WeatherStarURL answered last time
	// fes are the ATSC tuners Tvheadend has, as of fesAt; fesKnown once
	// looked up.
	fes      []tvh.Frontend
	fesAt    time.Time
	fesKnown bool
	// status is the inputs and subscriptions as of statusAt; lastSample
	// the inputs at the last sample, and when.
	status       tunerStatus
	statusAt     time.Time
	lastSample   map[string]tvh.InputStatus
	lastSampleAt time.Time
	sweep        SweepStatus
	sweepMux     string // the multiplex the sweep is on
}

// New builds a Service. Streaming failures (no ffmpeg) are reported by
// Info rather than failing construction, so the guide still works.
func New(opt Options) (*Service, error) {
	if opt.NoAntenna {
		opt.Tvheadend, opt.Demo = nil, false
		opt.Tuner = noAntenna{}
	}
	s := &Service{opt: opt, http: web.NewClient(), tuner: opt.Tuner, timing: defaultTiming}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.cfg = s.withDefaults(opt.Config)
	if opt.ConfigPath != "" {
		if raw, err := os.ReadFile(opt.ConfigPath); err == nil {
			var c Config
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, fmt.Errorf("parse %s: %w", opt.ConfigPath, err)
			}
			s.cfg = s.withDefaults(c)
		}
	}
	var err error
	if opt.CacheDir != "" {
		s.cache, err = store.OpenCache(opt.CacheDir)
	} else {
		s.cache, err = store.NewCache("airwaves")
	}
	if err != nil {
		log.Printf("cache: %v; data will be refetched", err)
	}
	if s.signals, err = signal.Open(opt.SignalPath); err != nil {
		if s.signals == nil {
			return nil, err
		}
		log.Print(err)
	}
	s.weather = weather.NewSource(s.http)
	s.streams, s.streamErr = stream.NewServer(opt.Loopback)

	if opt.Tvheadend != nil {
		s.tvhTuner = tuner.NewTvheadend(opt.Tvheadend)
		s.tvhTuner.Networks = s.offeredNetworks
		s.tuner = s.tvhTuner
		s.dvr, err = dvr.Open(opt.DVRPath, opt.Tvheadend, s.tvhTuner)
		if err != nil {
			return nil, err
		}
	}
	if s.tuner == nil {
		s.tuner = noTuner{}
	}
	if opt.WeatherStarPort > 0 {
		s.wxLocal = strings.TrimRight(opt.WeatherStarURL, "/")
		if s.wxLocal == "" {
			s.wxLocal = fmt.Sprintf("http://127.0.0.1:%d", opt.WeatherStarPort)
		}
		s.wx = &vchan.Weather{
			PageURL:    func(ctx context.Context) (string, error) { return s.weatherStarURL(ctx, s.wxLocal, false) },
			Forecast:   s.Weather,
			MusicDir:   s.wxLocal + "/music/default/",
			LocalMusic: opt.MusicDir,
			Chromium:   opt.Chromium,
			HTTP:       s.http,
			Loudness:   opt.Loudness,
		}
		if opt.ChannelsDir != "" {
			s.wx.Record = filepath.Join(opt.ChannelsDir, vchan.WeatherFile)
		}
		if s.streamErr == nil {
			s.wx.FFmpeg = s.streams.FFmpeg()
			s.wxStreams = opt.Chromium != ""
		}
	}
	if opt.ChannelsDir != "" && s.streamErr == nil {
		s.library = &vchan.Library{
			Root: opt.ChannelsDir, FFmpeg: s.streams.FFmpeg(), FFprobe: s.streams.FFprobe(),
			Reserved: s.reserved, YtDlp: opt.YtDlp, Loudness: opt.Loudness,
		}
	}
	return s, nil
}

// Library is the folder channels, or nil when there's no channels folder.
func (s *Service) Library() *vchan.Library { return s.library }

// WeatherChannel is the weather channel, or nil when there's no WeatherStar
// display.
func (s *Service) WeatherChannel() *vchan.Weather { return s.wx }

// reserved is the number folder channels can't have: the weather
// channel's.
func (s *Service) reserved() []string {
	if s.wx == nil {
		return nil
	}
	return []string{s.wx.Number()}
}

// withDefaults fills in a Config's defaults. There's no default ZIP code:
// someone else's would be wrong.
func (s *Service) withDefaults(c Config) Config {
	return c.normalized()
}

// noAntenna is the tuner of a server with custom channels only.
type noAntenna struct{}

func (noAntenna) Device() tuner.Device {
	return tuner.Device{ID: "none", Name: "No antenna", Kind: "none", Detail: "custom channels only"}
}

func (noAntenna) Input(_ context.Context, number string) (tuner.Input, error) {
	return tuner.Input{}, fmt.Errorf("no channel %s: %w", number, ErrNoAntenna)
}

// noTuner is the tuner of a server with an antenna but no Tvheadend: it
// has no channels.
type noTuner struct{}

func (noTuner) Device() tuner.Device {
	return tuner.Device{ID: "none", Name: "No tuner", Kind: "none", Detail: "the server has no Tvheadend"}
}

func (noTuner) Input(_ context.Context, number string) (tuner.Input, error) {
	return tuner.Input{}, fmt.Errorf("no channel %s: %w", number, ErrNoTuner)
}

// wxOn reports whether the weather channel can be shown: there's a
// WeatherStar display, answering when it runs apart from the server, and a
// location for it to show.
func (s *Service) wxOn() bool {
	if s.wx == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	located := s.cfg.ZIP != "" || s.cfg.Lat != 0 && s.cfg.Lon != 0
	return located && (s.opt.WeatherStarURL == "" || s.wxUp)
}

// checkWeatherStar notes whether the display at WeatherStarURL answers.
func (s *Service) checkWeatherStar(ctx context.Context) {
	if s.wx == nil || s.opt.WeatherStarURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	up := false
	if req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.wxLocal+"/", nil); err == nil {
		if resp, err := s.http.Do(req); err == nil {
			resp.Body.Close()
			up = resp.StatusCode < http.StatusInternalServerError
		}
	}
	s.mu.Lock()
	changed := up != s.wxUp
	s.wxUp = up
	s.mu.Unlock()
	switch {
	case changed && up:
		log.Printf("weather channel: on, WeatherStar display at %s", s.wxLocal)
	case changed:
		log.Printf("weather channel: off, no WeatherStar display at %s", s.wxLocal)
	}
}

// customChannels lists the custom channels in the HDHomeRun lineup: the
// weather channel when it can be rendered, and the folder channels that
// have something to play, leaving out those turned off.
func (s *Service) customChannels() []vchan.Channel {
	var all []vchan.Channel
	if s.wxStreams && s.wxOn() {
		all = append(all, s.wx)
	}
	if s.library != nil {
		all = append(all, s.library.Channels()...)
	}
	var out []vchan.Channel
	for _, v := range all {
		if v.Details().Enabled {
			out = append(out, v)
		}
	}
	return out
}

func (s *Service) customChannel(number string) vchan.Channel {
	for _, v := range s.customChannels() {
		if vchan.SameNumber(v.Number(), number) {
			return v
		}
	}
	return nil
}

// Close stops all streams and measuring, and saves the measurements.
func (s *Service) Close() {
	s.cancel()
	if s.streams != nil {
		s.streams.Close()
	}
	if err := s.signals.Save(); err != nil {
		log.Printf("signal measurements: %v", err)
	}
}

// StreamHandler serves /live/ HLS files.
func (s *Service) StreamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.streams == nil {
			http.NotFound(w, r)
			return
		}
		s.streams.ServeHTTP(w, r)
	})
}

// Info implements the Backend contract.
func (s *Service) Info(context.Context) (Info, error) {
	s.mu.Lock()
	t := s.tuner
	s.mu.Unlock()
	i := Info{Name: s.opt.Name, Mode: s.opt.Mode, Version: Version, Tuner: t.Device(), DVR: s.dvr != nil, WeatherStar: s.wxOn(),
		Antenna: !s.opt.NoAntenna}
	if s.opt.Tvheadend != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()
		if fes, err := s.frontends(ctx, false); err == nil {
			i.Tuner.Tuners = len(fes)
			for _, fe := range fes {
				i.Tuner.Model = cmp.Or(i.Tuner.Model, fe.Model)
			}
			if len(fes) > 0 {
				// Tvheadend's ATSC tuners receive ATSC 1.0 only.
				i.Tuner.Standards = []string{"ATSC 1.0"}
			}
		}
	}
	if s.streamErr != nil {
		i.Playback = s.streamErr.Error()
	}
	return i, nil
}

// SetTuner switches the live source (local mode).
func (s *Service) SetTuner(t tuner.Tuner) {
	s.mu.Lock()
	s.tuner = t
	s.mu.Unlock()
}

// Config returns the location settings.
func (s *Service) Config(context.Context) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, nil
}

// SetConfig changes the location or listings window; the next Snapshot
// rebuilds. Clients only change the listings window; airwavesd sets the
// location from its configuration at startup.
func (s *Service) SetConfig(_ context.Context, c Config) (Config, error) {
	c = s.withDefaults(c)
	s.mu.Lock()
	changed := c != s.cfg
	s.cfg = c
	if changed {
		s.built = time.Time{}
	}
	s.mu.Unlock()
	if s.opt.ConfigPath != "" {
		raw, _ := json.MarshalIndent(c, "", "  ")
		if err := os.MkdirAll(filepath.Dir(s.opt.ConfigPath), 0o755); err != nil {
			return c, err
		}
		if err := os.WriteFile(s.opt.ConfigPath, raw, 0o644); err != nil {
			return c, err
		}
	}
	return c, nil
}

// Snapshot returns the tuner's lineup described from the records (the
// report, rebuilt when stale) with its listings, what was measured, and
// the custom channels' details and schedules. With refresh it also
// bypasses cached source data.
func (s *Service) Snapshot(ctx context.Context, refresh bool) (*Snapshot, error) {
	snap, err := s.snapshot(ctx, refresh)
	if err != nil {
		return nil, err
	}
	out := &Snapshot{Report: snap.Report, Guide: snap.Guide, Custom: s.custom()}
	if s.opt.NoAntenna {
		out.Antenna = s.antenna(ctx, nil, nil)
		return out, nil
	}
	a := s.antenna(ctx, snap.Report, snap.Guide)
	chans := make([]lineup.TunerChannel, len(a.Channels))
	for i, c := range a.Channels {
		chans[i] = c.TunerChannel
	}
	out.Report = lineup.ForTuner(snap.Report, chans)
	out.Report.Channels = compatChannels(a)
	if len(a.Channels) == 0 && a.Tuner.Reason != "" {
		out.Report.Warnings = append(slices.Clone(out.Report.Warnings), "No antenna channels: "+a.Tuner.Reason)
	}
	out.Guide = listedOnly(snap.Guide, a.Channels)
	out.Antenna = a
	return out, nil
}

// custom lists the custom channels for the app: the weather channel (which
// the app plays itself) whenever there's a WeatherStar display, and the
// folder channels with something to play; none that are turned off.
func (s *Service) custom() []CustomChannel {
	s.mu.Lock()
	hours := s.cfg.GuideHours
	s.mu.Unlock()
	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Duration(hours+1) * time.Hour)
	out := []CustomChannel{}
	if s.wxOn() {
		if d := s.wx.Details(); d.Enabled {
			out = append(out, customChannel(d, "weather"))
		}
	}
	if s.library != nil {
		for _, f := range s.library.Channels() {
			d := f.Details()
			if !d.Enabled {
				continue
			}
			c := customChannel(d, vchan.KindOf(f))
			for _, p := range f.Programs(from, to) {
				gp := guide.Program{
					Start: p.Start, End: p.End, Title: p.Title, EpisodeTitle: p.Subtitle, Description: p.Description,
					Season: p.Season, Episode: p.Episode, Image: p.Image, AudioLang: p.AudioLang,
				}
				if p.New {
					gp.Flags = []string{"New"}
				}
				if p.Captions {
					gp.Tags = []string{"CC"}
				}
				c.Programs = append(c.Programs, gp)
			}
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b CustomChannel) int { return guide.CompareNumbers(a.Number, b.Number) })
	return out
}

func customChannel(d vchan.Details, kind string) CustomChannel {
	return CustomChannel{
		Number: d.Number, Name: d.Name, Kind: kind, CallSign: d.CallSign, Category: d.Category,
		Description: d.Description, Logo: logoPath(d), Programs: []guide.Program{},
	}
}

// logoPath is where a channel's logo is served, with its version so a new
// logo is fetched again; "" when it has none.
func logoPath(d vchan.Details) string {
	if d.Logo == "" {
		return ""
	}
	tag := vchan.LogoTag(d.Logo)
	if tag == "" {
		return ""
	}
	return "/channel-logos/" + url.PathEscape(d.Number) + "?v=" + tag
}

// LogoHandler serves /channel-logos/<number>: custom channels' logos, off
// or on, for the app and HDHomeRun clients.
func (s *Service) LogoHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /channel-logos/{number}", func(w http.ResponseWriter, r *http.Request) {
		number := r.PathValue("number")
		var chans []vchan.Channel
		if s.wx != nil {
			chans = append(chans, s.wx)
		}
		if s.library != nil {
			for _, e := range s.library.Entries() {
				chans = append(chans, e.Channel)
			}
		}
		for _, v := range chans {
			if d := v.Details(); vchan.SameNumber(d.Number, number) && d.Logo != "" {
				vchan.ServeLogo(w, r, d.Logo)
				return
			}
		}
		http.NotFound(w, r)
	})
	return mux
}

// AntennaChannels lists the tuner's channels by number, with their call
// signs; none without a tuner. Custom channels take the place of antenna
// channels with the same number in the HDHomeRun lineup.
func (s *Service) AntennaChannels(ctx context.Context) map[string]string {
	out := map[string]string{}
	s.mu.Lock()
	rep, g := s.report, s.guide
	s.mu.Unlock()
	chans, _, err := s.matched(ctx, rep, g)
	if err != nil {
		log.Printf("antenna channels: %v", err)
	}
	for _, c := range chans {
		out[c.Number] = cmp.Or(c.CallSign, c.Name)
	}
	return out
}

// snapshot returns the current report and guide.
func (s *Service) snapshot(ctx context.Context, refresh bool) (*Snapshot, error) {
	if snap := s.current(refresh); snap != nil {
		return snap, nil
	}
	s.buildMu.Lock()
	defer s.buildMu.Unlock()
	if snap := s.current(refresh); snap != nil {
		return snap, nil // another caller just built it
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()

	if s.opt.NoAntenna {
		rep, ok := s.locate(context.WithoutCancel(ctx), cfg)
		s.mu.Lock()
		s.report, s.guide, s.built = rep, nil, time.Now()
		if !ok {
			// Look the ZIP code up again in a minute, not half an hour.
			s.built = s.built.Add(time.Minute - reportTTL)
		}
		s.mu.Unlock()
		return &Snapshot{Report: rep}, nil
	}

	// The records only: no terrain, so no estimates.
	b := &lineup.Builder{HTTP: s.http, Cache: s.cache, Progress: s.opt.Progress}
	opt := lineup.Options{ZIP: cfg.ZIP, RadiusKm: cfg.RadiusKm, GuideHours: cfg.GuideHours, Refresh: refresh}
	if cfg.Lat != 0 && cfg.Lon != 0 {
		opt.Point = &geo.Point{Lat: cfg.Lat, Lon: cfg.Lon}
	}
	// One client hanging up must not abort a build others are waiting on.
	rep, g, err := b.Build(context.WithoutCancel(ctx), opt)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.report, s.guide, s.built = rep, g, time.Now()
	s.mu.Unlock()
	return &Snapshot{Report: rep, Guide: g}, nil
}

// locate is the report of a server without an antenna: no transmitters,
// channels or listings, only where the weather is for, when that's set. It
// reports false when the ZIP code couldn't be looked up.
func (s *Service) locate(ctx context.Context, cfg Config) (*lineup.Report, bool) {
	rep := &lineup.Report{
		Generated: time.Now(), Stations: []lineup.Station{},
		Channels: []lineup.Channel{}, Sources: []lineup.Source{}, Warnings: []string{},
	}
	ok := true
	if cfg.ZIP != "" {
		place, err := store.Load(ctx, s.cache, "zip-"+cfg.ZIP, 365*24*time.Hour, false, func(ctx context.Context) (geo.Place, error) {
			return geo.LookupZIP(ctx, s.http, cfg.ZIP)
		})
		if err != nil {
			rep.Warnings = append(rep.Warnings, err.Error())
			ok = false
		} else {
			rep.Place, rep.Point = place, place.Point
			rep.Sources = append(rep.Sources, lineup.Source{Name: "Zippopotam.us", URL: "https://zippopotam.us/", Use: "Location lookup"})
		}
	}
	if cfg.Lat != 0 && cfg.Lon != 0 {
		rep.Point = geo.Point{Lat: cfg.Lat, Lon: cfg.Lon}
	}
	if rep.Point == (geo.Point{}) && ok && s.wx != nil {
		rep.Warnings = append(rep.Warnings, ErrNoLocation.Error())
	}
	return rep, ok
}

func (s *Service) current(refresh bool) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if refresh || s.report == nil || time.Since(s.built) > reportTTL {
		return nil
	}
	return &Snapshot{Report: s.report, Guide: s.guide}
}

// Weather returns the local weather report.
func (s *Service) Weather(ctx context.Context) (*weather.Report, error) {
	snap, err := s.snapshot(ctx, false)
	if err != nil {
		return nil, err
	}
	if snap.Report.Point == (geo.Point{}) {
		return nil, ErrNoLocation
	}
	return s.weather.Report(ctx, snap.Report.Point)
}

// WeatherHandler serves /wximg/<name> (radar loop and satellite images) and
// /weatherstar, which redirects to the WeatherStar 4000+ display set to the
// server's location so clients never need coordinates.
func (s *Service) WeatherHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /wximg/{name}", func(w http.ResponseWriter, r *http.Request) {
		snap, err := s.snapshot(r.Context(), false)
		if err == nil && snap.Report.Point == (geo.Point{}) {
			err = ErrNoLocation
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		data, kind, err := s.weather.Image(r.Context(), snap.Report.Point, r.PathValue("name"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", kind)
		w.Header().Set("Cache-Control", "max-age=120")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("GET /weatherstar", func(w http.ResponseWriter, r *http.Request) {
		if !s.wxOn() {
			http.NotFound(w, r)
			return
		}
		target, err := s.weatherStarURL(r.Context(), s.weatherStarBase(r), r.URL.Query().Get("music") == "1")
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})
	// The app plays the weather channel's music itself, so muting it is
	// instant rather than a reload of the display.
	mux.HandleFunc("GET /weatherstar/music", func(w http.ResponseWriter, r *http.Request) {
		tracks := []string{}
		if wx := s.weatherMusic(); wx != nil {
			for _, t := range wx.Tracks(r.Context()) {
				if strings.HasPrefix(t, "http") {
					// The display's own tracks, where the client reaches it.
					if rest, ok := strings.CutPrefix(t, s.wxLocal); ok {
						t = s.weatherStarBase(r) + rest
					}
					tracks = append(tracks, t)
				} else {
					tracks = append(tracks, "/wxmusic/"+url.PathEscape(filepath.Base(t)))
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(map[string]any{"tracks": tracks})
	})
	mux.HandleFunc("GET /wxmusic/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if s.opt.MusicDir == "" || name != filepath.Base(name) || !strings.HasSuffix(strings.ToLower(name), ".mp3") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.opt.MusicDir, name))
	})
	return mux
}

// weatherMusic is where the weather channel's music comes from.
func (s *Service) weatherMusic() *vchan.Weather { return s.wx }

// weatherStarBase is the display's address for the client making r: the
// host it reached the server at, on the display's port.
func (s *Service) weatherStarBase(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(s.opt.WeatherStarPort))
}

// Tune starts live playback of a channel for client.
func (s *Service) Tune(ctx context.Context, client, number string) (*stream.Playback, error) {
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	if v := s.customChannel(number); v != nil {
		ctx = phase.NewContext(ctx, phase.New("tune "+number+" ("+vchan.KindOf(v)+")", vchan.KindOf(v)))
		// The app gets subtitles as text, a WebVTT track it shows itself,
		// turning it on for programs not in English, so none are burned in.
		var script *cc.Script
		if vchan.HasCaptions(v) {
			script = &cc.Script{}
		}
		in := tuner.Input{
			Args: []string{"-f", "mpegts", "-i", "pipe:0"}, Video: "0:v:0",
			Audio: []tuner.AudioTrack{{Map: "0:a:0"}}, Captions: script != nil, Subtitles: script,
			Source: func(ctx context.Context, w io.Writer) error { return v.Stream(vchan.ForApp(ctx, script), w) },
		}
		return s.streams.Start(ctx, client, in)
	}
	if s.tvhTuner != nil {
		ctx = phase.NewContext(ctx, phase.New("tune "+number+" (antenna)", "antenna"))
		var pb *stream.Playback
		err := s.tuneAntenna(ctx, client, number, func(ctx context.Context, in tuner.Input) error {
			var err error
			pb, err = s.streams.Start(ctx, client, in)
			return err
		})
		return pb, err
	}
	s.mu.Lock()
	t := s.tuner
	s.mu.Unlock()
	in, err := t.Input(ctx, number)
	if err != nil {
		return nil, err
	}
	return s.streams.Start(ctx, client, in)
}

// Stop ends client's playback.
func (s *Service) Stop(_ context.Context, client string) error {
	if s.streams != nil {
		s.streams.Stop(client)
	}
	return nil
}

// DVR returns rules, schedule and recordings.
func (s *Service) DVR(ctx context.Context) (*dvr.State, error) {
	if s.dvr == nil {
		return &dvr.State{Reason: s.noDVR().Error()}, nil
	}
	g, chans, _ := s.recordingChannels(ctx)
	return s.dvr.State(ctx, g, chans)
}

// noDVR is why there's no recording.
func (s *Service) noDVR() error {
	if s.opt.NoAntenna {
		return ErrNoAntenna
	}
	return ErrNoDVR
}

// Record adds a rule from a guide selection.
func (s *Service) Record(ctx context.Context, req dvr.Request) (*dvr.Rule, error) {
	// Custom channels replay what is already on the server (or render the
	// forecast live); there is nothing to record.
	if req.Channel == "WX" || s.customChannel(req.Channel) != nil ||
		s.wx != nil && vchan.SameNumber(req.Channel, s.wx.Number()) && s.wx.Details().Enabled {
		return nil, ErrNotRecordable
	}
	if s.dvr == nil {
		return nil, s.noDVR()
	}
	if _, err := s.snapshot(ctx, false); err != nil {
		return nil, err
	}
	g, chans, err := s.recordingChannels(ctx)
	if err != nil {
		return nil, err
	}
	return s.dvr.Add(ctx, req, g, chans)
}

// DeleteRule removes a rule and its unstarted recordings.
func (s *Service) DeleteRule(ctx context.Context, id string) error {
	if s.dvr == nil {
		return ErrNoDVR
	}
	return s.dvr.DeleteRule(ctx, id)
}

// DeleteRecording skips, cancels or deletes one recording.
func (s *Service) DeleteRecording(ctx context.Context, id string) error {
	if s.dvr == nil {
		return ErrNoDVR
	}
	return s.dvr.DeleteEntry(ctx, id)
}

// PlayRecording streams a recording to client, starting near from seconds
// in (zero plays from the beginning).
func (s *Service) PlayRecording(ctx context.Context, client, id string, from float64) (*stream.Playback, error) {
	if s.dvr == nil {
		return nil, ErrNoDVR
	}
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	url := s.opt.Tvheadend.FileURL(id)
	audio, err := s.streams.ProbeAudio(ctx, url)
	if err != nil || len(audio) == 0 {
		audio = []tuner.AudioTrack{{Map: "0:a:0"}}
	}
	// Read the file front to back: Tvheadend drops the connection when
	// ffmpeg seeks to probe the end. To resume, start the HTTP read at the
	// matching byte offset instead; MPEG-TS resynchronizes on its own.
	args := []string{"-seekable", "0"}
	if size, dur := s.recordingSize(ctx, id); from > 5 && size > 0 && dur > 0 && from < dur {
		off := int64(float64(size)*from/dur) / 188 * 188
		args = append(args, "-offset", strconv.FormatInt(off, 10))
	} else {
		from = 0
	}
	args = append(args, "-i", url)
	in := tuner.Input{Args: args, Video: "0:v:0", Audio: audio, Broadcast: true, VOD: true, Offset: from}
	return s.streams.Start(ctx, client, in)
}

// recordingSize returns a recording's file size and duration in seconds.
func (s *Service) recordingSize(ctx context.Context, id string) (int64, float64) {
	entries, err := s.opt.Tvheadend.Finished(ctx)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.UUID == id {
			dur := float64(e.StopReal - e.StartReal)
			if dur <= 0 {
				dur = float64(e.Stop - e.Start)
			}
			return e.FileSize, dur
		}
	}
	return 0, 0
}

// SaveProgress records how far a recording has been watched.
func (s *Service) SaveProgress(_ context.Context, id string, position, duration float64) error {
	if s.dvr == nil {
		return ErrNoDVR
	}
	return s.dvr.SaveProgress(id, position, duration)
}

// MarkWatched sets a recording's watched state.
func (s *Service) MarkWatched(_ context.Context, id string, watched bool) error {
	if s.dvr == nil {
		return ErrNoDVR
	}
	return s.dvr.MarkWatched(id, watched)
}

// UpdateRule changes a series rule's keep count and new-only setting.
func (s *Service) UpdateRule(_ context.Context, id string, u dvr.RuleUpdate) (*dvr.Rule, error) {
	if s.dvr == nil {
		return nil, ErrNoDVR
	}
	return s.dvr.UpdateRule(id, u)
}

// SetDVRPrefs changes library-wide recording settings.
func (s *Service) SetDVRPrefs(_ context.Context, p dvr.Prefs) (dvr.Prefs, error) {
	if s.dvr == nil {
		return p, ErrNoDVR
	}
	return s.dvr.SetPrefs(p)
}

// recordingChannels is the listings and the tuner's channels, for
// recording.
func (s *Service) recordingChannels(ctx context.Context) (*guide.Guide, []lineup.Channel, error) {
	s.mu.Lock()
	rep, g := s.report, s.guide
	s.mu.Unlock()
	chans, _, err := s.matched(ctx, rep, g)
	return g, dvrChannels(chans), err
}

// Run keeps a server healthy until ctx ends: refreshes listings, keeps
// Tvheadend's channels matching the hardware (the antenna network once a
// tuner appears; demo channels before, in demo mode), measures the signal
// while tuners are in use, and reconciles recordings.
func (s *Service) Run(ctx context.Context) {
	go s.sample(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for n := 0; ; n++ {
		s.maintain(ctx, n%10 == 0)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// maintain runs once a minute. Reconciling is a few local Tvheadend calls,
// so it runs every time; mapping is asynchronous in Tvheadend, so channels
// mapped now are scheduled on the next pass.
func (s *Service) maintain(ctx context.Context, prune bool) {
	s.checkWeatherStar(ctx)
	_, snapErr := s.snapshot(ctx, false)
	if snapErr != nil {
		log.Printf("snapshot: %v", snapErr)
	}
	if s.opt.Tvheadend == nil {
		return
	}
	if err := s.setupTuners(ctx); err != nil {
		log.Printf("tvheadend setup: %v", err)
	}
	if n, err := s.opt.Tvheadend.RemoveOrphanChannels(ctx); err != nil {
		log.Printf("remove orphan channels: %v", err)
	} else if n > 0 {
		log.Printf("removed %d channels left without a service", n)
		s.tvhTuner.Invalidate()
	}
	if n, err := s.opt.Tvheadend.MapServices(ctx); err != nil {
		log.Printf("map services: %v", err)
	} else if n > 0 {
		log.Printf("mapped %d new channels", n)
		s.tvhTuner.Invalidate()
	}
	s.syncScans(ctx)
	if snapErr != nil {
		return
	}
	// Without the tuner's lineup, recordings stay as they are rather than
	// being unscheduled for channels that seem gone.
	g, chans, err := s.recordingChannels(ctx)
	if err != nil {
		log.Printf("reconcile recordings: %v", err)
		return
	}
	if err := s.dvr.Reconcile(ctx, g, chans); err != nil {
		log.Printf("reconcile recordings: %v", err)
	}
	if prune {
		if err := s.dvr.Retain(ctx); err != nil {
			log.Printf("retain recordings: %v", err)
		}
		if err := s.dvr.Prune(ctx); err != nil {
			log.Printf("prune recordings: %v", err)
		}
	}
}

// setupTuners attaches real tuners to the antenna network when present.
// Once that network exists (a tuner has been found), demo channels are
// removed and never made again, even while the tuner is away; before, in
// demo mode, they stand in for the antenna.
func (s *Service) setupTuners(ctx context.Context) error {
	c := s.opt.Tvheadend
	fes, err := s.frontends(ctx, true)
	if err != nil {
		return err
	}
	if len(fes) > 0 {
		changed, err := c.EnsureATSC(ctx, fes)
		if err != nil {
			return err
		}
		if changed {
			log.Printf("antenna network created for %d tuners; Tvheadend is scanning", len(fes))
			s.tvhTuner.Invalidate()
		}
	}
	nets, err := c.Networks(ctx)
	if err != nil {
		return err
	}
	var real, demo bool
	for _, n := range nets {
		real = real || n.Name == tvh.ATSCNetwork
		demo = demo || n.Name == tvh.DemoNetwork
	}
	switch {
	case real && demo:
		if err := c.RemoveNetwork(ctx, tvh.DemoNetwork); err != nil {
			return fmt.Errorf("remove the demo channels: %w", err)
		}
		log.Print("removed the demo channels: a tuner's antenna network exists")
		s.tvhTuner.Invalidate()
	case real, !s.opt.Demo:
	default:
		return c.EnsureDemo(ctx, s.demoChannels())
	}
	return nil
}

// demoChannels are the main channels of the stations in the listings (or,
// without listings, in the FCC's records) as generated test patterns.
func (s *Service) demoChannels() []tvh.DemoChannel {
	s.mu.Lock()
	rep, g := s.report, s.guide
	s.mu.Unlock()
	ffmpeg := cmp.Or(s.opt.DemoFFmpeg, "/usr/bin/ffmpeg")
	seen := make(map[int]bool)
	var out []tvh.DemoChannel
	add := func(major int, name string) {
		if major <= 0 || seen[major] {
			return
		}
		seen[major] = true
		// Tvheadend splits the command on spaces, so one word.
		if f := strings.Fields(name); len(f) > 0 {
			name = f[0]
		} else {
			name = "CH" + strconv.Itoa(major)
		}
		out = append(out, tvh.DemoChannel{
			Major: major, Minor: 1, Name: name,
			Command: tuner.DemoCommand(ffmpeg, fmt.Sprintf("%d.1", major), name),
		})
	}
	if g != nil {
		for _, c := range g.Channels {
			if major, minor := guide.SplitNumber(c.Number); minor == 1 {
				add(major, guide.BaseCall(c.CallSign))
			}
		}
	}
	if len(out) == 0 && rep != nil {
		for _, st := range rep.Stations {
			add(st.VirtualChannel, st.BaseCall)
		}
	}
	return out
}
