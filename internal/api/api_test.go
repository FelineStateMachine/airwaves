package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"airwaves/internal/dvr"
	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/service"
	"airwaves/internal/signal"
	"airwaves/internal/stream"
	"airwaves/internal/weather"
)

// fake records calls and returns canned values.
type fake struct {
	tuned, stopped, deleted, progress string
	cfg                               service.Config
	measured                          int
}

func (f *fake) Info(context.Context) (service.Info, error) {
	return service.Info{Name: "nas", Mode: "server", DVR: true}, nil
}
func (f *fake) Config(context.Context) (service.Config, error) { return f.cfg, nil }
func (f *fake) SetConfig(_ context.Context, c service.Config) (service.Config, error) {
	f.cfg = c
	return c, nil
}

func (f *fake) Snapshot(context.Context, bool) (*service.Snapshot, error) {
	return &service.Snapshot{Report: &lineup.Report{Channels: []lineup.Channel{{Number: "7.1"}}}, Custom: []service.CustomChannel{
		{Number: "1.1", Name: "Airwaves Weather", Kind: "weather", CallSign: "WX", Category: "Weather", Programs: []guide.Program{}},
		{Number: "1.4", Name: "Cartoons", Kind: "jellyfin", CallSign: "TOON", Category: "Kids", Logo: "/channel-logos/1.4?v=x"},
	}}, nil
}

func (f *fake) Signal(context.Context) (*service.SignalReport, error) {
	strength := 85.0
	return &service.SignalReport{
		Tuner:    service.TunerInfo{Ready: true, Tuners: 2, Standards: []string{"ATSC 1.0"}},
		Channels: []service.ChannelSignal{{Number: "20.1", RF: 31, Signal: &signal.Reading{Lock: true, StrengthPct: &strength}}},
	}, nil
}

func (f *fake) Measure(context.Context) (*service.SweepStatus, error) {
	f.measured++
	if f.measured > 1 {
		return nil, errors.New("tuner unplugged")
	}
	return &service.SweepStatus{Running: true, Total: 12}, nil
}

func (f *fake) Weather(context.Context) (*weather.Report, error) {
	return &weather.Report{Alerts: []weather.Alert{{Event: "Winter Storm Warning"}}}, nil
}

func (f *fake) Tune(_ context.Context, client, number string) (*stream.Playback, error) {
	f.tuned = client + ":" + number
	return &stream.Playback{ID: "abc", Path: "/live/abc/index.m3u8"}, nil
}
func (f *fake) Stop(_ context.Context, client string) error { f.stopped = client; return nil }
func (f *fake) DVR(context.Context) (*dvr.State, error)     { return &dvr.State{Available: true}, nil }
func (f *fake) Record(_ context.Context, r dvr.Request) (*dvr.Rule, error) {
	return &dvr.Rule{ID: "r1", Kind: r.Kind, Channel: r.Channel}, nil
}
func (f *fake) DeleteRule(context.Context, string) error { return nil }
func (f *fake) DeleteRecording(_ context.Context, id string) error {
	f.deleted = id
	return nil
}

func (f *fake) PlayRecording(_ context.Context, _, _ string, from float64) (*stream.Playback, error) {
	return &stream.Playback{Path: "/live/rec/master.m3u8", Offset: from}, nil
}

func (f *fake) SaveProgress(_ context.Context, id string, pos, _ float64) error {
	f.progress = fmt.Sprintf("%s@%.0f", id, pos)
	return nil
}

func (f *fake) MarkWatched(_ context.Context, id string, w bool) error {
	f.progress = fmt.Sprintf("%s watched=%v", id, w)
	return nil
}

func (f *fake) UpdateRule(_ context.Context, id string, u dvr.RuleUpdate) (*dvr.Rule, error) {
	return &dvr.Rule{ID: id, Keep: u.Keep}, nil
}

func (f *fake) SetDVRPrefs(_ context.Context, p dvr.Prefs) (dvr.Prefs, error) { return p, nil }

func TestClientServerRoundTrip(t *testing.T) {
	be := &fake{}
	srv := httptest.NewServer((&Server{Backend: be, Token: "s3cret"}).Handler())
	defer srv.Close()
	ctx := context.Background()

	if _, err := NewClient(srv.URL, "wrong").Info(ctx); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("wrong token accepted: %v", err)
	}
	c := NewClient(srv.URL, "s3cret")

	snap, err := c.Snapshot(ctx, false)
	if err != nil || len(snap.Report.Channels) != 1 {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}
	if len(snap.Custom) != 2 || snap.Custom[0].Kind != "weather" || snap.Custom[0].CallSign != "WX" ||
		snap.Custom[1].Logo != "/channel-logos/1.4?v=x" || snap.Custom[1].Category != "Kids" {
		t.Errorf("custom channels = %+v", snap.Custom)
	}
	pb, err := c.Tune(ctx, "mac", "7.1")
	if err != nil {
		t.Fatal(err)
	}
	if be.tuned != "mac:7.1" || pb.URL != srv.URL+"/live/abc/index.m3u8" {
		t.Errorf("tune: backend saw %q, playback URL %q", be.tuned, pb.URL)
	}
	if _, err := c.SetConfig(ctx, service.Config{ZIP: "80302"}); err != nil || be.cfg.ZIP != "80302" {
		t.Errorf("set config: %v, backend has %+v", err, be.cfg)
	}
	if err := c.DeleteRecording(ctx, "e/1"); err != nil || be.deleted != "e/1" {
		t.Errorf("delete: %v, backend saw %q", err, be.deleted)
	}
	if err := c.Stop(ctx, "mac"); err != nil || be.stopped != "mac" {
		t.Errorf("stop: %v, backend saw %q", err, be.stopped)
	}
	if pb, err := c.PlayRecording(ctx, "mac", "rec1", 754); err != nil || pb.Offset != 754 {
		t.Errorf("play recording: %+v, %v", pb, err)
	}
	if err := c.SaveProgress(ctx, "rec1", 754, 3600); err != nil || be.progress != "rec1@754" {
		t.Errorf("progress: %v, backend saw %q", err, be.progress)
	}
	if err := c.MarkWatched(ctx, "rec1", true); err != nil || be.progress != "rec1 watched=true" {
		t.Errorf("watched: %v, backend saw %q", err, be.progress)
	}
	if r, err := c.UpdateRule(ctx, "r9", dvr.RuleUpdate{Keep: 5}); err != nil || r.Keep != 5 {
		t.Errorf("update rule: %+v, %v", r, err)
	}
	if wx, err := c.Weather(ctx); err != nil || len(wx.Alerts) != 1 {
		t.Errorf("weather: %+v, %v", wx, err)
	}
	if sig, err := c.Signal(ctx); err != nil || !sig.Tuner.Ready || len(sig.Channels) != 1 || sig.Channels[0].RF != 31 ||
		!sig.Channels[0].Signal.Lock || *sig.Channels[0].Signal.StrengthPct != 85 || sig.Channels[0].Signal.QualityPct != nil {
		t.Errorf("signal: %+v, %v", sig, err)
	}
	if st, err := c.Measure(ctx); err != nil || !st.Running || st.Total != 12 {
		t.Errorf("measure: %+v, %v", st, err)
	}
	if _, err := c.Measure(ctx); err == nil || err.Error() != "tuner unplugged" {
		t.Errorf("backend error not passed through: %v", err)
	}
}

// TestNoEstimates: the reception estimates the app used to ask for, and
// the antenna presets they were for, are gone.
func TestNoEstimates(t *testing.T) {
	srv := httptest.NewServer((&Server{Backend: &fake{}}).Handler())
	defer srv.Close()
	for _, path := range []string{"/api/preview?zip=80302", "/api/profile?facility=1&rf=2", "/api/presets"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
}

// TestErrorStatus: what a server lacks is 501, a channel with no signal
// now 503.
func TestErrorStatus(t *testing.T) {
	for err, want := range map[error]int{
		service.ErrNoAntenna:                          http.StatusNotImplemented,
		fmt.Errorf("x: %w", service.ErrNoTuner):       http.StatusNotImplemented,
		&service.NoSignalError{Channel: "20.1"}:       http.StatusServiceUnavailable,
		fmt.Errorf("x: %w", &service.NoSignalError{}): http.StatusServiceUnavailable,
		errors.New("ffmpeg exited"):                   http.StatusInternalServerError,
	} {
		if got := errStatus(err); got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
}

// TestLogosNeedNoToken: players and guides fetch logos as images, with no
// way to send the API's token.
func TestLogosNeedNoToken(t *testing.T) {
	logos := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "logo "+r.URL.Path) })
	srv := httptest.NewServer((&Server{Backend: &fake{}, Token: "s3cret", Logos: logos}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/channel-logos/1.4?v=x")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "logo /channel-logos/1.4" {
		t.Errorf("logo: %d %q", resp.StatusCode, body)
	}
}

func TestNewClientDefaults(t *testing.T) {
	for in, want := range map[string]string{
		"nas":                    "http://nas:8089",
		"http://nas:8089/":       "http://nas:8089",
		"https://tv.example.com": "https://tv.example.com",
		"100.64.0.7:9000":        "http://100.64.0.7:9000",
	} {
		if got := NewClient(in, "").Base; got != want {
			t.Errorf("NewClient(%q).Base = %q, want %q", in, got, want)
		}
	}
}
