package main

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"airwaves/internal/api"
	"airwaves/internal/dvr"
	"airwaves/internal/service"
	"airwaves/internal/store"
	"airwaves/internal/stream"
	"airwaves/internal/weather"
)

const appName = "airwaves"

// errNoServer is returned until a server address is set.
var errNoServer = errors.New("no Airwaves server set")

// App is the desktop remote for an airwavesd server, which owns the tuner
// (an HDHomeRun), the lineup, the guide and recordings.
type App struct {
	ctx context.Context

	mu       sync.Mutex
	settings store.Settings
	client   *api.Client
}

// NewApp loads settings.
func NewApp() *App {
	s, err := store.LoadSettings(appName)
	if err != nil {
		log.Printf("settings: %v", err)
	}
	// Persist a generated client ID so the server sees a stable client.
	if s, err = store.SaveSettings(appName, s); err != nil {
		log.Printf("save settings: %v", err)
	}
	a := &App{settings: s}
	a.connectLocked()
	return a
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

func (a *App) shutdown(context.Context) {
	c, s, err := a.server()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Stop(ctx, s.ClientID)
}

func (a *App) connectLocked() {
	a.client = nil
	if a.settings.Server != "" {
		a.client = api.NewClient(a.settings.Server, a.settings.Token)
	}
}

func (a *App) server() (*api.Client, store.Settings, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil {
		return nil, a.settings, errNoServer
	}
	return a.client, a.settings, nil
}

// Boot is the frontend's first call.
type Boot struct {
	Settings store.Settings `json:"settings"`
	Info     service.Info   `json:"info"`
	Config   service.Config `json:"config"`
	Error    string         `json:"error,omitempty"` // server unset or unreachable
	// ServerURL is the server's base URL, for images and the WX channel.
	ServerURL string `json:"serverUrl,omitempty"`
	View      string `json:"view"` // initial view override
	// Lite is AIRWAVES_LITE: "1" or "0" turns the frontend's lite mode (no
	// blurs or animations, the default on Linux) on or off.
	Lite string `json:"lite,omitempty"`
	// AutoScale is the interface size Auto stands for, in percent.
	AutoScale int `json:"autoScale"`
	// NativeZoom: SetZoom sizes the interface (WebKitGTK's page zoom);
	// otherwise the frontend uses CSS zoom.
	NativeZoom bool `json:"nativeZoom,omitempty"`
	// Version is the app's.
	Version string `json:"version"`
}

// Boot returns settings and the server's description. AIRWAVES_VIEW opens a
// specific view at launch (tv, info, guide, antenna, recordings, settings).
func (a *App) Boot() Boot {
	c, s, err := a.server()
	out := Boot{Settings: s, View: os.Getenv("AIRWAVES_VIEW"), Lite: os.Getenv("AIRWAVES_LITE"),
		AutoScale: autoScale(), NativeZoom: nativeZoom, Version: version}
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.ServerURL = c.Base
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	if out.Info, err = c.Info(ctx); err != nil {
		out.Error = err.Error()
		return out
	}
	if out.Config, err = c.Config(ctx); err != nil {
		out.Error = err.Error()
	}
	return out
}

// SaveSettings persists app settings, reconnecting when the server
// changed, and returns a fresh Boot.
func (a *App) SaveSettings(s store.Settings) (Boot, error) {
	a.mu.Lock()
	prev := a.settings
	saved, err := store.SaveSettings(appName, s)
	if err != nil {
		a.mu.Unlock()
		return Boot{}, err
	}
	a.settings = saved
	old := a.client
	if saved.Server != prev.Server || saved.Token != prev.Token || a.client == nil {
		a.connectLocked()
	}
	a.mu.Unlock()
	if old != nil && old != a.client {
		_ = old.Stop(a.ctx, prev.ClientID)
	}
	return a.Boot(), nil
}

// SetZoom sizes the interface where the page is zoomed natively (Linux):
// 1.25 is 125%.
func (a *App) SetZoom(zoom float64) {
	if zoom >= 0.5 && zoom <= 3 {
		setPageZoom(zoom)
	}
}

// scale is the interface size in percent: the setting, or Auto's.
func (a *App) scale() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settings.Scale > 0 {
		return a.settings.Scale
	}
	return autoScale()
}

// SetConfig changes server settings the app exposes (hours of listings).
func (a *App) SetConfig(c service.Config) (service.Config, error) {
	cl, _, err := a.server()
	if err != nil {
		return c, err
	}
	return cl.SetConfig(a.ctx, c)
}

// TestServer checks a server address without switching to it.
func (a *App) TestServer(server, token string) (service.Info, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
	defer cancel()
	return api.NewClient(server, token).Info(ctx)
}

// Scan returns the lineup and listings. With refresh the server ignores
// cached FCC, ATSC 3.0 and listings data.
func (a *App) Scan(refresh bool) (*service.Snapshot, error) {
	c, s, err := a.server()
	if err != nil {
		return nil, err
	}
	runtime.EventsEmit(a.ctx, "progress", "Loading lineup and listings from "+s.Server)
	return c.Snapshot(a.ctx, refresh)
}

// Signal returns what the server's tuners measured, by RF channel and by
// channel, and how Measure now is going.
func (a *App) Signal() (*service.SignalReport, error) {
	c, _, err := a.server()
	if err != nil {
		return nil, err
	}
	return c.Signal(a.ctx)
}

// Measure starts measuring every RF channel on an idle tuner.
func (a *App) Measure() (*service.SweepStatus, error) {
	c, _, err := a.server()
	if err != nil {
		return nil, err
	}
	return c.Measure(a.ctx)
}

// ScanChannels has the server's tuner scan for channels again.
func (a *App) ScanChannels() (service.TunerInfo, error) {
	c, _, err := a.server()
	if err != nil {
		return service.TunerInfo{}, err
	}
	return c.ScanChannels(a.ctx)
}

// Weather returns the local weather report from the server.
func (a *App) Weather() (*weather.Report, error) {
	c, _, err := a.server()
	if err != nil {
		return nil, err
	}
	return c.Weather(a.ctx)
}

// CopyText puts text on the clipboard.
func (a *App) CopyText(text string) error {
	return runtime.ClipboardSetText(a.ctx, text)
}

// Tune starts playback of a virtual channel.
func (a *App) Tune(number string) (*stream.Playback, error) {
	c, s, err := a.server()
	if err != nil {
		return nil, err
	}
	pb, err := c.Tune(a.ctx, s.ClientID, number)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.settings.LastChannel = number
	saved := a.settings
	a.mu.Unlock()
	if _, err := store.SaveSettings(appName, saved); err != nil {
		log.Printf("save last channel: %v", err)
	}
	return pb, nil
}

// StopTV ends playback.
func (a *App) StopTV() {
	if c, s, err := a.server(); err == nil {
		_ = c.Stop(a.ctx, s.ClientID)
	}
}

// DVR returns recording rules, schedule and library.
func (a *App) DVR() (*dvr.State, error) {
	c, _, err := a.server()
	if err != nil {
		return nil, err
	}
	return c.DVR(a.ctx)
}

// Record adds a recording rule for an airing picked in the guide.
func (a *App) Record(req dvr.Request) (*dvr.Rule, error) {
	c, _, err := a.server()
	if err != nil {
		return nil, err
	}
	return c.Record(a.ctx, req)
}

// DeleteRule removes a recording rule.
func (a *App) DeleteRule(id string) error {
	c, _, err := a.server()
	if err != nil {
		return err
	}
	return c.DeleteRule(a.ctx, id)
}

// DeleteRecording skips, cancels or deletes one recording.
func (a *App) DeleteRecording(id string) error {
	c, _, err := a.server()
	if err != nil {
		return err
	}
	return c.DeleteRecording(a.ctx, id)
}

// StopRecording ends a recording in progress early, keeping it.
func (a *App) StopRecording(id string) error {
	c, _, err := a.server()
	if err != nil {
		return err
	}
	return c.StopRecording(a.ctx, id)
}

// ExtendRecording makes a recording in progress run minutes longer, and
// says until when.
func (a *App) ExtendRecording(id string, minutes int) (time.Time, error) {
	c, _, err := a.server()
	if err != nil {
		return time.Time{}, err
	}
	return c.ExtendRecording(a.ctx, id, minutes)
}

// PlayRecording streams a recording from about from seconds in.
func (a *App) PlayRecording(id string, from float64) (*stream.Playback, error) {
	c, s, err := a.server()
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, errors.New("no recording selected")
	}
	return c.PlayRecording(a.ctx, s.ClientID, id, from)
}

// SaveProgress records how far a recording has been watched.
func (a *App) SaveProgress(id string, position, duration float64) error {
	c, _, err := a.server()
	if err != nil {
		return err
	}
	return c.SaveProgress(a.ctx, id, position, duration)
}

// MarkWatched sets a recording's watched state.
func (a *App) MarkWatched(id string, watched bool) error {
	c, _, err := a.server()
	if err != nil {
		return err
	}
	return c.MarkWatched(a.ctx, id, watched)
}

// UpdateRule changes a series rule's keep count and new-only setting.
func (a *App) UpdateRule(id string, u dvr.RuleUpdate) (*dvr.Rule, error) {
	c, _, err := a.server()
	if err != nil {
		return nil, err
	}
	return c.UpdateRule(a.ctx, id, u)
}

// SetDVRPrefs changes library-wide recording settings.
func (a *App) SetDVRPrefs(p dvr.Prefs) (dvr.Prefs, error) {
	c, _, err := a.server()
	if err != nil {
		return p, err
	}
	return c.SetDVRPrefs(a.ctx, p)
}

// OpenURL opens a link in the system browser.
func (a *App) OpenURL(u string) { runtime.BrowserOpenURL(a.ctx, u) }

// Log records a frontend message on stderr.
func (a *App) Log(level, msg string) { log.Printf("ui %s: %s", level, msg) }
