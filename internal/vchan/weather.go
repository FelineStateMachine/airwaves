package vchan

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"airwaves/internal/weather"
)

// Weather renders the Airwaves Weather display (WeatherStar 4000+) to video
// with background music, using headless Chromium and ffmpeg.
type Weather struct {
	// Record is the channel's record, WeatherFile in the channels folder:
	// its number and name as well as its details. Empty keeps the
	// defaults (1.1 Airwaves Weather, WX).
	Record string
	// PageURL returns the display's kiosk URL for the server's location.
	PageURL func(ctx context.Context) (string, error)
	// MusicDir holds the display's bundled tracks (an HTTP directory
	// listing), used when LocalMusic has no MP3s.
	MusicDir   string
	LocalMusic string
	FFmpeg     string
	Chromium   string
	HTTP       *http.Client
	// Forecast supplies the weather report the guide is written from.
	Forecast func(ctx context.Context) (*weather.Report, error)
	// Loudness is the level the music is evened out to, in LUFS; 0 leaves
	// it as it is.
	Loudness float64

	record recordFile
	music  memoryLevels // the music's loudness as it played, by tracks
}

const (
	weatherWidth  = 1280
	weatherHeight = 720
	captureFPS    = 15
)

// Number implements Channel.
func (wx *Weather) Number() string { return wx.Details().Number }

// Name implements Channel.
func (wx *Weather) Name() string { return wx.Details().Name }

// Details implements Channel, from the record. A number that isn't one,
// or a blank name, leaves the default.
func (wx *Weather) Details() Details {
	d := Details{Number: WeatherNumber, Name: WeatherName, CallSign: WeatherCallSign, Category: "Weather",
		Description: weatherBlurb, Enabled: true}
	if wx.Record == "" {
		return d
	}
	rec := wx.record.read(wx.Record)
	if n, ok := ParseNumber(rec.Number); ok {
		d.Number = n
	}
	if name := strings.TrimSpace(rec.Name); name != "" {
		d.Name = name
	}
	return rec.details(filepath.Dir(wx.Record), WeatherLogoName, d)
}

// Programs implements Channel. Like a cable weather channel, the guide has
// an hour-long "Local Forecast" block each hour, and each block carries that
// hour's forecast, so browsing ahead in the guide reads as the forecast.
func (wx *Weather) Programs(from, to time.Time) []Program {
	var rep *weather.Report
	if wx.Forecast != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		rep, _ = wx.Forecast(ctx)
		cancel()
	}
	var out []Program
	for t := from.Truncate(time.Hour); t.Before(to); t = t.Add(time.Hour) {
		out = append(out, ForecastProgram(rep, t))
	}
	return out
}

// ForecastProgram is the guide entry for the hour starting at t.
func ForecastProgram(rep *weather.Report, t time.Time) Program {
	p := Program{
		Start: t, End: t.Add(time.Hour), Title: "Local Forecast", Category: "Weather",
		Description: "Current conditions, the local and extended forecast, and radar.",
	}
	if rep == nil {
		return p
	}
	for _, h := range rep.Hourly {
		if !h.Start.After(t) && t.Before(h.Start.Add(time.Hour)) {
			p.Subtitle = fmt.Sprintf("%d° and %s", h.TempF, h.Short)
			break
		}
	}
	for i, per := range rep.Periods {
		end := per.Start.Add(12 * time.Hour)
		if i+1 < len(rep.Periods) {
			end = rep.Periods[i+1].Start
		}
		if !per.Start.After(t) && t.Before(end) {
			p.Description = per.Name + ": " + per.Detailed
			break
		}
	}
	for _, a := range rep.Alerts {
		if (a.Onset.IsZero() || !a.Onset.After(t.Add(time.Hour))) && (a.Ends.IsZero() || a.Ends.After(t)) {
			p.Subtitle = a.Event + ". " + p.Subtitle
			break
		}
	}
	return p
}

// Stream implements Channel.
func (wx *Weather) Stream(ctx context.Context, w io.Writer) error {
	pageURL, err := wx.PageURL(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	tracks := wx.Tracks(ctx)
	audio, cleanup, err := wx.audioInput(tracks)
	if err != nil {
		return err
	}
	defer cleanup()
	var music *os.File // the leveled music, for ffmpeg's fd 3
	if wx.Loudness != 0 && len(tracks) > 0 {
		if music, err = wx.levelMusic(ctx, audio, strings.Join(tracks, "\x00")); err != nil {
			return err
		}
		defer music.Close()
		audio = []string{"-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ac", "2", "-i", "pipe:3"}
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "image2pipe", "-framerate", fmt.Sprint(captureFPS), "-c:v", "mjpeg", "-i", "pipe:0",
	}
	args = append(args, audio...)
	args = append(args,
		"-map", "0:v", "-map", "1:a",
		"-vf", fmt.Sprintf("scale=%d:%d,fps=30,format=yuv420p", weatherWidth, weatherHeight),
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "60",
		"-b:v", "3M", "-maxrate", "4M", "-bufsize", "6M",
		"-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2",
		"-f", "mpegts", "pipe:1",
	)
	ff := exec.CommandContext(ctx, wx.FFmpeg, args...)
	if music != nil {
		ff.ExtraFiles = []*os.File{music}
	}
	ff.Stdout = w
	var stderr strings.Builder
	ff.Stderr = &stderr
	frames, err := ff.StdinPipe()
	if err != nil {
		return err
	}
	if err := ff.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	defer func() { _ = ff.Wait() }()
	defer frames.Close()

	// Chromium runs in its own process group so the whole browser (renderer
	// and helper processes included) can be killed when the viewer leaves;
	// cancelling chromedp's context alone leaves it running.
	var procMu sync.Mutex
	var procs []*exec.Cmd
	alloc, cancelAlloc := chromedp.NewExecAllocator(ctx,
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			procMu.Lock()
			procs = append(procs, cmd)
			procMu.Unlock()
		}),
		chromedp.ExecPath(wx.Chromium),
		chromedp.Headless,
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.WindowSize(weatherWidth, weatherHeight),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true),
	)
	defer cancelAlloc()
	browser, cancelBrowser := chromedp.NewContext(alloc)
	defer cancelBrowser()
	defer func() {
		closing, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = chromedp.Cancel(browser)
		<-closing.Done()
		procMu.Lock()
		defer procMu.Unlock()
		for _, p := range procs {
			if p.Process != nil {
				_ = syscall.Kill(-p.Process.Pid, syscall.SIGKILL)
			}
		}
	}()

	if _, err := chromedp.Call(browser, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
		Width: weatherWidth, Height: weatherHeight, DeviceScaleFactor: 1,
	}); err != nil {
		return fmt.Errorf("start chromium: %w", err)
	}
	if err := chromedp.Do(browser, chromedp.Navigate(pageURL)); err != nil {
		return fmt.Errorf("open weather display: %w", err)
	}

	// Chromium pushes a frame whenever the page changes; keep the newest
	// and feed ffmpeg at a steady rate so time keeps moving on still pages.
	var mu sync.Mutex
	var latest []byte
	frameEvents := chromedp.Events(browser, page.ScreencastFrame)
	if _, err := chromedp.Call(browser, page.StartScreencast, page.StartScreencastParams{
		Format: "jpeg", Quality: 85, MaxWidth: weatherWidth, MaxHeight: weatherHeight,
	}); err != nil {
		return fmt.Errorf("start capture: %w", err)
	}
	go func() {
		for f, err := range frameEvents {
			if err != nil {
				return
			}
			mu.Lock()
			latest = f.Data
			mu.Unlock()
			_, _ = chromedp.Call(browser, page.ScreencastFrameAck, page.ScreencastFrameAckParams{SessionID: f.SessionID})
		}
	}()

	tick := time.NewTicker(time.Second / captureFPS)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			mu.Lock()
			frame := latest
			mu.Unlock()
			if frame == nil {
				continue
			}
			if _, err := frames.Write(frame); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("ffmpeg: %v: %s", err, strings.TrimSpace(stderr.String()))
			}
		}
	}
}

// levelMusic decodes the music (input) to raw sound and levels it as it
// plays, metering it the way videos are, and returns the leveled sound
// for the encoder to read. key names the tracks, whose loudness is
// remembered for the next viewing.
func (wx *Weather) levelMusic(ctx context.Context, input []string, key string) (*os.File, error) {
	dec := exec.CommandContext(ctx, wx.FFmpeg, append(append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, input...),
		"-vn", "-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ac", "2", "pipe:1")...)
	src, err := dec.StdoutPipe()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	if err := dec.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	lev := newLeveler(&itemLevel{Target: wx.Loudness, Key: key, Memory: &wx.music})
	go func() {
		defer func() { _ = dec.Wait() }()
		defer lev.finish()
		defer w.Close()
		buf := make([]byte, relayChunk)
		for {
			n, err := io.ReadFull(src, buf)
			whole := buf[:n-n%4]
			lev.process(whole)
			if _, werr := w.Write(whole); werr != nil || err != nil {
				return
			}
		}
	}()
	return r, nil
}

// Tracks lists the music: the MP3 files in LocalMusic, else the display's
// bundled tracks as URLs.
func (wx *Weather) Tracks(ctx context.Context) []string {
	if wx.LocalMusic != "" {
		if files, _ := filepath.Glob(filepath.Join(wx.LocalMusic, "*.mp3")); len(files) > 0 {
			return files
		}
	}
	if wx.MusicDir != "" {
		return wx.remoteTracks(ctx)
	}
	return nil
}

var mp3Link = regexp.MustCompile(`href="([^"]+\.mp3)"`)

// musicRepeats is how many times the music playlist plays.
const musicRepeats = 500

// audioInput returns ffmpeg input options for a looping music playlist of
// tracks (see Tracks), or silence without any.
func (wx *Weather) audioInput(tracks []string) ([]string, func(), error) {
	silence := []string{"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo"}
	if len(tracks) == 0 {
		return silence, func() {}, nil
	}
	list, err := os.CreateTemp("", "airwaves-music-*.txt")
	if err != nil {
		return nil, nil, err
	}
	// The playlist over and over: -stream_loop doesn't loop the concat
	// demuxer (it fails at the end of the list), so the list repeats,
	// longer than anyone watches.
	for range musicRepeats {
		for _, t := range tracks {
			fmt.Fprintf(list, "file '%s'\n", strings.ReplaceAll(t, "'", `'\''`))
		}
	}
	list.Close()
	cleanup := func() { os.Remove(list.Name()) }
	return []string{
		"-re", "-protocol_whitelist", "file,http,https,tcp,tls,crypto",
		"-f", "concat", "-safe", "0", "-i", list.Name(),
	}, cleanup, nil
}

func (wx *Weather) remoteTracks(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wx.MusicDir, nil)
	if err != nil {
		return nil
	}
	resp, err := wx.HTTP.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	base, err := url.Parse(wx.MusicDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, m := range mp3Link.FindAllStringSubmatch(string(body), -1) {
		if ref, err := url.Parse(m[1]); err == nil {
			out = append(out, base.ResolveReference(ref).String())
		}
	}
	return out
}
