package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"airwaves/internal/dvr"
	"airwaves/internal/geo"
	"airwaves/internal/store"
	"airwaves/internal/tvh"
	"airwaves/internal/web"
)

// TestNoAntenna: a server with custom channels only has no Tvheadend (even
// when given one), lineup, listings, reception or recording; its snapshot
// is the custom channels, and Info says there's no antenna.
func TestNoAntenna(t *testing.T) {
	ctx := t.Context()
	s, err := New(Options{
		Name: "friend", Mode: "server", NoAntenna: true, CacheDir: t.TempDir(),
		Tvheadend: tvh.New("http://127.0.0.1:1", web.NewClient()), Demo: true,
		WeatherStarPort: 8090, Config: Config{Lat: 39.74, Lon: -104.99},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.opt.Tvheadend != nil || s.tvhTuner != nil || s.dvr != nil {
		t.Error("Tvheadend kept without an antenna")
	}

	info, _ := s.Info(ctx)
	if info.Antenna || info.DVR || info.Tuner.Kind != "none" || !info.WeatherStar {
		t.Errorf("info = %+v", info)
	}
	if raw, _ := json.Marshal(info); !strings.Contains(string(raw), `"antenna":false`) {
		t.Errorf("info JSON: %s", raw)
	}

	snap, err := s.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Report.Point != (geo.Point{Lat: 39.74, Lon: -104.99}) || snap.Guide != nil {
		t.Errorf("report point %+v, guide %v", snap.Report.Point, snap.Guide)
	}
	raw, _ := json.Marshal(snap)
	for _, want := range []string{`"stations":[]`, `"channels":[]`, `"sources":[]`, `"warnings":[]`, `"kind":"weather"`, `"antenna":{"tuner":{"ready":false`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("snapshot JSON lacks %s: %s", want, raw)
		}
	}

	if s.streamErr == nil {
		if _, err := s.Tune(ctx, "mac", "7.1"); !errors.Is(err, ErrNoAntenna) {
			t.Errorf("tune an antenna channel: %v", err)
		}
	}
	if _, err := s.Signal(ctx); !errors.Is(err, ErrNoAntenna) {
		t.Errorf("signal: %v", err)
	}
	if _, err := s.Measure(ctx); !errors.Is(err, ErrNoAntenna) {
		t.Errorf("measure: %v", err)
	}
	if snap.Antenna == nil || len(snap.Antenna.Channels) != 0 || snap.Antenna.Tuner.Ready || snap.Antenna.Tuner.Reason != ErrNoAntenna.Error() {
		t.Errorf("antenna = %+v", snap.Antenna)
	}
	if st, err := s.DVR(ctx); err != nil || st.Available || st.Reason != ErrNoAntenna.Error() {
		t.Errorf("dvr = %+v, %v", st, err)
	}
	if _, err := s.Record(ctx, dvr.Request{Channel: "7.1"}); !errors.Is(err, ErrNoAntenna) {
		t.Errorf("record an antenna channel: %v", err)
	}
	if _, err := s.Record(ctx, dvr.Request{Channel: "1.1"}); !errors.Is(err, ErrNotRecordable) {
		t.Errorf("record the weather channel: %v", err)
	}
	if got := s.AntennaChannels(ctx); len(got) != 0 {
		t.Errorf("antenna channels = %v", got)
	}
	if lineup, err := s.HDHR().Lineup(ctx); err != nil || len(lineup) != 0 {
		t.Errorf("HDHomeRun lineup = %v, %v (no Chromium, so no weather channel either)", lineup, err)
	}
	if err := s.HDHR().Stream(ctx, "7.1", nil); err == nil {
		t.Error("HDHomeRun streamed an antenna channel")
	}
	if cfg, _ := s.Config(ctx); cfg.ZIP != "" {
		t.Errorf("ZIP %q made up without an antenna", cfg.ZIP)
	}

	// Nor with an antenna (the default): airwavesd asks for one.
	if a, _ := New(Options{Mode: "server", CacheDir: t.TempDir()}); a.cfg.ZIP != "" {
		t.Errorf("antenna ZIP = %q", a.cfg.ZIP)
	} else if info, _ := a.Info(ctx); !info.Antenna {
		t.Errorf("antenna info = %+v", info)
	}
}

// TestNoAntennaLocation: the location is only for the weather; without one
// there's no weather channel and the weather says what to set.
func TestNoAntennaLocation(t *testing.T) {
	ctx := t.Context()
	s, err := New(Options{Mode: "server", NoAntenna: true, CacheDir: t.TempDir(), WeatherStarPort: 8090})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := s.Info(ctx); info.WeatherStar {
		t.Error("weather display offered without a location")
	}
	snap, err := s.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Custom) != 0 || !slices.Contains(snap.Report.Warnings, ErrNoLocation.Error()) {
		t.Errorf("custom %+v, warnings %q", snap.Custom, snap.Report.Warnings)
	}
	if _, err := s.Weather(ctx); !errors.Is(err, ErrNoLocation) {
		t.Errorf("weather: %v", err)
	}

	// A ZIP code is looked up (here, from the cache) for its centroid.
	dir := t.TempDir()
	cache, err := store.OpenCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	place := geo.Place{ZIP: "12345", Name: "Schenectady", State: "NY", Point: geo.Point{Lat: 42.81, Lon: -73.94}}
	if err := cache.Put("zip-12345", place); err != nil {
		t.Fatal(err)
	}
	s, err = New(Options{Mode: "server", NoAntenna: true, CacheDir: dir, WeatherStarPort: 8090, Config: Config{ZIP: "12345"}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err = s.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Report.Place != place || snap.Report.Point != place.Point || len(snap.Report.Warnings) != 0 ||
		len(snap.Custom) != 1 || snap.Custom[0].Kind != "weather" {
		t.Errorf("report %+v, custom %+v", snap.Report, snap.Custom)
	}
}

// TestWeatherStarElsewhere: a display in a container of its own counts only
// while it answers, and clients are sent to it on the port they can reach.
func TestWeatherStarElsewhere(t *testing.T) {
	ctx := t.Context()
	display := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/music/default/" {
			fmt.Fprint(w, `<a href="Track%201.mp3">Track 1.mp3</a>`)
		}
	}))
	defer display.Close()
	s, err := New(Options{
		Mode: "server", NoAntenna: true, CacheDir: t.TempDir(), Config: Config{Lat: 39.74, Lon: -104.99},
		WeatherStarPort: 8090, WeatherStarURL: display.URL + "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.wxOn() {
		t.Error("display counted before it answered")
	}
	s.checkWeatherStar(ctx)
	if !s.wxOn() {
		t.Fatal("display not counted once it answered")
	}
	if info, _ := s.Info(ctx); !info.WeatherStar {
		t.Errorf("info = %+v", info)
	}

	h := s.WeatherHandler()
	req := httptest.NewRequest(http.MethodGet, "http://tv.local:8089/weatherstar", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || !strings.HasPrefix(loc, "http://tv.local:8090/?latLon=") {
		t.Errorf("redirect %d to %q", rec.Code, loc)
	}
	if page, err := s.wx.PageURL(ctx); err != nil || !strings.HasPrefix(page, display.URL+"/?latLon=") {
		t.Errorf("page for the renderer = %q, %v", page, err)
	}
	req = httptest.NewRequest(http.MethodGet, "http://tv.local:8089/weatherstar/music", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if body := rec.Body.String(); !strings.Contains(body, `"http://tv.local:8090/music/default/Track%201.mp3"`) {
		t.Errorf("music = %s", body)
	}

	display.Close()
	s.checkWeatherStar(ctx)
	if s.wxOn() || len(s.custom()) != 0 {
		t.Error("display still counted once it stopped answering")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://tv.local:8089/weatherstar", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("redirect to a display that's gone: %d", rec.Code)
	}
}
