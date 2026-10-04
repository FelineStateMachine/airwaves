package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/hdhr"
	"airwaves/internal/lineup/lineuptest"
	"airwaves/internal/reception"
	"airwaves/internal/signal"
	"airwaves/internal/tuner"
	"airwaves/internal/tvh"
	"airwaves/internal/tvh/tvhtest"
)

// fastTiming measures in milliseconds rather than seconds.
var fastTiming = measureTiming{
	sample: 20 * time.Millisecond,
	appear: 2 * time.Second, settle: 20 * time.Millisecond, interval: 20 * time.Millisecond, readings: 3,
	idleWait: 300 * time.Millisecond,
	poll:     20 * time.Millisecond, noLock: 200 * time.Millisecond,
}

// newTunerService is a server with the fake Tvheadend (an HDHomeRun whose
// scan found 38 channels around Denver), and Denver's records and
// listings already loaded.
func newTunerService(t *testing.T, change ...func(*Options)) (*Service, *tvhtest.Server) {
	t.Helper()
	fake := tvhtest.New(t)
	dir := t.TempDir()
	opt := Options{
		Name: "nas", Mode: "server", CacheDir: filepath.Join(dir, "cache"), Config: Config{ZIP: "80302"},
		Tvheadend: tvh.New(fake.URL, fake.Client()), DVRPath: filepath.Join(dir, "dvr.json"), SignalPath: filepath.Join(dir, "signal.json"),
	}
	for _, f := range change {
		f(&opt)
	}
	s, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.timing = fastTiming
	s.mu.Lock()
	s.report, s.guide, s.built = lineuptest.Report(), lineuptest.Guide(), time.Now()
	s.mu.Unlock()
	return s, fake
}

// tvhNumbers lists the fake's channel numbers, in order.
func tvhNumbers(fake *tvhtest.Server) []string {
	var out []string
	for _, c := range fake.Entries("channel") {
		out = append(out, c["number"].(string))
	}
	slices.SortFunc(out, guide.CompareNumbers)
	return out
}

// TestTunerLineup: the app's antenna channels are exactly the tuner's, the
// records and listings only describing them; nothing estimated is served.
func TestTunerLineup(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	snap, err := s.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	a := snap.Antenna
	var numbers []string
	by := map[string]AntennaChannel{}
	for _, c := range a.Channels {
		numbers = append(numbers, c.Number)
		by[c.Number] = c
	}
	if want := tvhNumbers(fake); len(want) != 38 || !slices.Equal(numbers, want) {
		t.Fatalf("antenna channels %v, want the tuner's %v", numbers, want)
	}
	// Listed and licensed, but not found by the scan.
	for _, n := range []string{"7.1", "9.1"} {
		if _, ok := by[n]; ok {
			t.Errorf("%s listed without the tuner having it", n)
		}
	}
	if c := by["20.1"]; c.CallSign != "KTVDDT" || c.Transmitter != "KTVD" || c.RF != 31 || c.Signal != nil || c.Name != "KTVD-HD" || c.Demo {
		t.Errorf("20.1 = %+v", c)
	}
	if c := by["2.1"]; c.Via != "KDVR" || c.NextGen == nil || c.NextGen.HostCall != "KWGN-TV" {
		t.Errorf("2.1 = %+v", c)
	}
	ti := a.Tuner
	if !ti.Ready || ti.Tuners != 2 || ti.Model != "hdhomerun_dvr_atsc" || !slices.Equal(ti.Standards, []string{"ATSC 1.0"}) || ti.ATSC3 ||
		ti.InUse != 0 || ti.Reason != "" || ti.Demo || ti.Scanning {
		t.Errorf("tuner = %+v", ti)
	}

	// Every RF channel Tvheadend scans, with its scan and what's licensed
	// on it.
	if len(a.Muxes) != 36 {
		t.Fatalf("%d multiplexes", len(a.Muxes))
	}
	mux := map[int]MuxSignal{}
	for _, m := range a.Muxes {
		mux[m.RF] = m
	}
	if m := mux[31]; m.Name != "575MHz" || m.FrequencyMHz != 575 || m.Band != "UHF" || m.Scan == nil || !m.Scan.Lock || m.Scan.At.Unix() != 1791129355 ||
		!slices.Equal(m.Channels, []string{"9.4", "9.7", "20.1", "20.2", "20.3", "20.4"}) || len(m.Stations) != 2 ||
		m.Stations[0].CallSign != "K31AB-D" || m.Stations[1].CallSign != "KTVD" || m.Signal != nil || m.History != nil || m.Tuned {
		t.Errorf("RF 31 = %+v", m)
	}
	if m := mux[7]; m.Scan == nil || m.Scan.Lock || len(m.Channels) != 0 || len(m.Stations) != 1 || m.Stations[0].CallSign != "KMGH-TV" || m.Band != "VHF-Hi" {
		t.Errorf("RF 7 = %+v", m)
	}
	if m := mux[34]; len(m.Stations) != 1 || !m.Stations[0].ATSC3 || m.Scan.Lock {
		t.Errorf("RF 34 = %+v", m)
	}

	// The report: the same channels for app versions that read it, with
	// a measured tier; the records with no estimates; the listings of the
	// tuner's channels only.
	var compat []string
	for _, c := range snap.Report.Channels {
		compat = append(compat, c.Number)
		if c.Tier["indoor"] != reception.Good || c.Tier["rooftop"] != reception.Good {
			t.Errorf("%s tier %v", c.Number, c.Tier)
		}
	}
	if !slices.Equal(compat, numbers) {
		t.Errorf("report channels %v", compat)
	}
	var ids []string
	for _, c := range snap.Guide.Channels {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, []string{"g2", "g4", "g6", "g94", "g14", "g20", "g31"}) || snap.Guide.Programs["g7"] != nil || len(snap.Guide.Programs["g20"]) != 2 {
		t.Errorf("guide channels %v, programs for 7.1: %v", ids, snap.Guide.Programs["g7"])
	}
	if len(s.guide.Channels) != 9 {
		t.Error("the server's own listings were trimmed")
	}
	raw, _ := json.Marshal(snap)
	for _, bad := range []string{"noiseMarginDb", `"presets"`, `"strong"`, `"fair"`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("snapshot has %s", bad)
		}
	}
	for _, want := range []string{`"signal":{}`, `"antenna":{"tuner":{"ready":true`, `"standards":["ATSC 1.0"],"atsc3":false`, `"history":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("snapshot lacks %s", want)
		}
	}

	info, _ := s.Info(ctx)
	if info.Tuner.Tuners != 2 || info.Tuner.Model != "hdhomerun_dvr_atsc" || !slices.Equal(info.Tuner.Standards, []string{"ATSC 1.0"}) || info.Tuner.ATSC3 {
		t.Errorf("info tuner = %+v", info.Tuner)
	}
	if got := s.AntennaChannels(ctx); len(got) != 38 || got["20.1"] != "KTVDDT" || got["9.7"] != "KUSA" || got["7.1"] != "" {
		t.Errorf("antenna channels = %v", got)
	}
}

// TestNoTuner: no tuner, no antenna channels; nothing made up in their
// place, and tuning one says why.
func TestNoTuner(t *testing.T) {
	s, fake := newTunerService(t)
	fake.SetTuner(false)
	ctx := t.Context()
	snap, err := s.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if a := snap.Antenna; len(a.Channels) != 0 || a.Tuner.Ready || a.Tuner.Reason != "Tvheadend sees no tuner right now" || len(snap.Report.Channels) != 0 {
		t.Errorf("antenna = %+v", a)
	}
	if !slices.Contains(snap.Report.Warnings, "No antenna channels: Tvheadend sees no tuner right now") {
		t.Errorf("warnings = %q", snap.Report.Warnings)
	}
	if err := s.tuneAntenna(ctx, "tv", "20.1", nil); !errors.Is(err, ErrNoTuner) {
		t.Errorf("tune: %v", err)
	}
	if _, err := s.Measure(ctx); !errors.Is(err, ErrNoTuner) {
		t.Errorf("measure: %v", err)
	}
	if l, err := s.HDHR().Lineup(ctx); err != nil || len(l) != 0 {
		t.Errorf("HDHomeRun lineup = %v, %v", l, err)
	}

	// No Tvheadend at all.
	bare, err := New(Options{Mode: "server", CacheDir: t.TempDir(), Config: Config{ZIP: "80302"}})
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	bare.report, bare.guide, bare.built = lineuptest.Report(), lineuptest.Guide(), time.Now()
	snap, err = bare.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if a := snap.Antenna; len(a.Channels) != 0 || a.Tuner.Reason != "no tuner: the server has no Tvheadend" || len(snap.Report.Channels) != 0 || len(snap.Guide.Channels) != 0 {
		t.Errorf("no Tvheadend: %+v, %d report channels", a, len(snap.Report.Channels))
	}
	if _, err := bare.tuner.Input(ctx, "20.1"); !errors.Is(err, ErrNoTuner) {
		t.Errorf("tune without Tvheadend: %v", err)
	}
	if info, _ := bare.Info(ctx); info.Tuner.Kind != "none" {
		t.Errorf("info = %+v", info.Tuner)
	}
}

// TestDemo: demo channels stand in for the antenna only in demo mode
// before a tuner; once a tuner's network exists they're removed, and they
// don't come back while the tuner is away.
func TestDemo(t *testing.T) {
	s, fake := newTunerService(t, func(o *Options) { o.Demo = true })
	ctx := t.Context()
	// Before the tuner: a Tvheadend with the demo network only.
	fake.SetTuner(false)
	for _, n := range fake.Entries("network") {
		if err := s.opt.Tvheadend.RemoveNetwork(ctx, n["networkname"].(string)); err != nil {
			t.Fatal(err)
		}
	}
	fake.Add("network", map[string]any{"uuid": "demo-net", "networkname": tvh.DemoNetwork})
	fake.Add("mux", map[string]any{"uuid": "demo-mux", "name": "Demo 7.1 KMGH", "network": tvh.DemoNetwork, "network_uuid": "demo-net", "enabled": true, "iptv_url": "pipe://ffmpeg"})
	fake.Add("service", map[string]any{"uuid": "demo-svc", "svcname": "KMGH", "network": tvh.DemoNetwork, "multiplex": "Demo 7.1 KMGH", "multiplex_uuid": "demo-mux", "enabled": true, "channel": []any{"demo-ch"}})
	fake.Add("channel", map[string]any{"uuid": "demo-ch", "name": "KMGH", "number": "7.1", "enabled": true, "services": []any{"demo-svc"}})
	s.tvhTuner.Invalidate()

	snap, err := s.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if a := snap.Antenna; len(a.Channels) != 1 || a.Channels[0].Number != "7.1" || !a.Channels[0].Demo || !a.Tuner.Demo || a.Tuner.Ready {
		t.Fatalf("demo antenna = %+v", a)
	}
	if err := s.setupTuners(ctx); err != nil {
		t.Fatal(err)
	}

	// The tuner arrives: its network is made, and the demo channels go.
	fake.SetTuner(true)
	s.mu.Lock()
	s.fesKnown = false
	s.mu.Unlock()
	s.maintain(ctx, false)
	var nets []string
	for _, n := range fake.Entries("network") {
		nets = append(nets, n["networkname"].(string))
	}
	if !slices.Equal(nets, []string{tvh.ATSCNetwork}) || len(fake.Entries("mux")) != 0 {
		t.Errorf("networks %v, muxes %v", nets, fake.Entries("mux"))
	}
	for _, c := range fake.Entries("channel") {
		if c["uuid"] == "demo-ch" {
			t.Errorf("demo channel left: %v", c)
		}
	}

	// The tuner goes away: no demo channels made, none offered.
	fake.SetTuner(false)
	before := len(fake.Requests())
	s.mu.Lock()
	s.fesKnown = false
	s.mu.Unlock()
	if err := s.setupTuners(ctx); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.Requests()[before:] {
		if strings.Contains(r, "network/create") || strings.Contains(r, "mux_create") {
			t.Errorf("demo channels made again: %s", r)
		}
	}
	s.tvhTuner.Invalidate()
	if got := s.offeredNetworks(); got != nil {
		t.Errorf("offered %v", got)
	}
}

// TestSampleInputs: while a tuner is in use, its readings are recorded for
// its RF channel, once it has been on it a while, and saved.
func TestSampleInputs(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	fake.Busy(tvhtest.Tuner0, "575MHz", 150)
	s.sampleInputs(ctx)
	if _, ok := s.signals.Get(575_000_000); ok {
		t.Fatal("read a tuner just tuned")
	}
	s.sampleInputs(ctx)
	m, ok := s.signals.Get(575_000_000)
	if !ok || m.RF != 31 || m.Latest == nil || !m.Latest.Lock || *m.Latest.StrengthPct != 85 || *m.Latest.QualityPct != 83 || m.Latest.Source != signal.Active {
		t.Fatalf("RF 31 = %+v", m)
	}

	sig, err := s.Signal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Tuner.InUse != 1 {
		t.Errorf("in use: %d", sig.Tuner.InUse)
	}
	for _, c := range sig.Channels {
		if (c.RF == 31) != (c.Signal != nil) {
			t.Errorf("%s (RF %d) signal %+v", c.Number, c.RF, c.Signal)
		}
	}
	for _, m := range sig.Muxes {
		if m.RF == 31 && (!m.Tuned || m.History == nil || m.History.Samples != 1 || m.History.LockedPct != 100) {
			t.Errorf("RF 31 = %+v", m)
		}
	}
	raw, _ := json.Marshal(sig)
	if !strings.Contains(string(raw), `"number":"4.1","rf":35,"signal":null`) {
		t.Errorf("unmeasured channel: %s", raw)
	}

	// Saved on the way out, and read back.
	s.Close()
	again, err := signal.Open(s.opt.SignalPath)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := again.Get(575_000_000); !ok || m.Latest == nil || m.Scan == nil || !m.Scan.Lock {
		t.Errorf("saved RF 31 = %+v", m)
	}
}

// waitSweep waits for Measure now to finish.
func waitSweep(t *testing.T, s *Service) SweepStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := s.sweepStatus()
		if !st.Running {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("sweep still running: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMeasure: Measure now reads every RF channel with channels or a
// licensed station, one at a time on an idle tuner, at the lowest weight.
func TestMeasure(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	st, err := s.Measure(ctx)
	if err != nil || !st.Running || st.StartedAt == nil {
		t.Fatalf("measure: %+v, %v", st, err)
	}
	if again, _ := s.Measure(ctx); !again.Running || !again.StartedAt.Equal(*st.StartedAt) {
		t.Errorf("a second sweep started: %+v", again)
	}
	done := waitSweep(t, s)
	// The 8 multiplexes with channels, and RF 7 (KMGH), 16 (KUSA) and 34
	// (KWGN's ATSC 3.0), licensed but not found.
	if done.Total != 11 || done.Done != 11 || done.Note != "" || done.FinishedAt == nil {
		t.Errorf("sweep = %+v", done)
	}
	opened, most := fake.Opened()
	if len(opened) != 11 || most != 1 {
		t.Errorf("opened %v, %d at once", opened, most)
	}
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, "GET /stream/") && !strings.HasSuffix(r, "?pids=0&weight=10") {
			t.Errorf("subscribed as %s", r)
		}
	}
	for rf, freq := range map[int]int64{31: 575_000_000, 36: 605_000_000, 7: 177_000_000, 34: 593_000_000} {
		m, ok := s.signals.Get(freq)
		if !ok || m.Latest == nil || m.Latest.Source != signal.Sweep || m.History().Samples != 3 || m.Latest.Lock != (rf == 31 || rf == 36) {
			t.Errorf("RF %d = %+v", rf, m)
		}
	}
	if _, ok := s.signals.Get(473_000_000); ok {
		t.Error("measured RF 14, which nothing uses")
	}
}

// TestMeasureLeavesTunersAlone: the sweep reads a viewer's tuner rather
// than tune another to the same RF channel, waits for a free tuner and
// stops when there's none, and stops when a viewer takes its tuner.
func TestMeasureLeavesTunersAlone(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()

	// Someone watches 31.1 on tuner 0: the sweep uses tuner 1 only, and
	// reads RF 36 from tuner 0.
	fake.Busy(tvhtest.Tuner0, "605MHz", 150)
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if st := waitSweep(t, s); st.Done != 11 || st.Note != "" {
		t.Errorf("sweep = %+v", st)
	}
	opened, _ := fake.Opened()
	for _, o := range opened {
		if !strings.HasPrefix(o, tvhtest.Tuner1) || strings.HasSuffix(o, "605MHz") {
			t.Errorf("opened %s", o)
		}
	}
	if m, _ := s.signals.Get(605_000_000); m.Latest == nil || !m.Latest.Lock || m.Latest.Source != signal.Sweep {
		t.Errorf("RF 36 = %+v", m)
	}

	// Both tuners in use: nothing tuned, and the sweep says why.
	fake.Busy(tvhtest.Tuner1, "587MHz", 300)
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if st := waitSweep(t, s); st.Done != 0 || !strings.Contains(st.Note, "no tuner was free") {
		t.Errorf("busy sweep = %+v", st)
	}
	if now, _ := fake.Opened(); len(now) != len(opened) {
		t.Errorf("tuned a busy tuner: %v", now[len(opened):])
	}

	// A viewer takes the sweep's tuner: it stops there.
	fake.Free(tvhtest.Tuner0)
	fake.Free(tvhtest.Tuner1)
	fake.OnStream = func(mux string) {
		if mux == "575MHz" {
			go func() {
				time.Sleep(30 * time.Millisecond)
				fake.TakeOver("587MHz")
			}()
		}
	}
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if st := waitSweep(t, s); st.Done >= st.Total || !strings.Contains(st.Note, "stopped at RF 31: a viewer or a recording needed the tuner") {
		t.Errorf("taken over: %+v", st)
	}
}

// TestTuneNoSignal: a channel whose RF channel won't lock fails with what
// the tuner measured, not ffmpeg's complaint; one that locks starts and
// leaves a reading.
func TestTuneNoSignal(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	fake.SetSignal("575MHz", tvhtest.Signal{Strength: 41, Quality: 0})
	// start reads the channel's stream as ffmpeg would, until data comes
	// or the start is called off.
	start := func(ctx context.Context, in tuner.Input) error {
		url := in.Args[len(in.Args)-1]
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New("ffmpeg exited: Server returned 503 Service Unavailable")
		}
		buf := make([]byte, 188)
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			return errors.New("stream did not start in time: ")
		}
		return nil
	}
	err := s.tuneAntenna(ctx, "tv", "20.1", start)
	var ns *NoSignalError
	if !errors.As(err, &ns) || ns.RF != 31 || ns.Label != "KTVD 20.x" ||
		err.Error() != "No signal on RF 31 (KTVD 20.x) right now: the tuner measured strength 41%, quality 0% and couldn't lock" {
		t.Fatalf("tune 20.1: %v", err)
	}
	if m, ok := s.signals.Get(575_000_000); !ok || m.Latest.Lock || *m.Latest.StrengthPct != 41 || m.Latest.Source != signal.Active {
		t.Errorf("RF 31 = %+v", m)
	}
	if err := s.tuneAntenna(ctx, "tv", "9.4", start); err == nil || !strings.HasPrefix(err.Error(), "No signal on RF 31 (KUSA 9.x on KTVD) right now") {
		t.Errorf("tune 9.4: %v", err)
	}

	// Locked: it starts.
	if err := s.tuneAntenna(ctx, "tv", "31.1", start); err != nil {
		t.Errorf("tune 31.1: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); len(fake.Streams()) > 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	// Not a channel the tuner found.
	if err := s.tuneAntenna(ctx, "tv", "7.1", start); !errors.Is(err, tuner.ErrNotFound) || err.Error() != "channel 7.1: not a channel the tuner found" {
		t.Errorf("tune 7.1: %v", err)
	}
	// Both tuners busy.
	fake.Busy(tvhtest.Tuner0, "587MHz", 300)
	fake.Busy(tvhtest.Tuner1, "599MHz", 300)
	slow := func(ctx context.Context, in tuner.Input) error {
		time.Sleep(5 * fastTiming.poll)
		return start(ctx, in)
	}
	if err := s.tuneAntenna(ctx, "tv", "31.1", slow); err == nil || !strings.HasPrefix(err.Error(), "every tuner is in use right now") {
		t.Errorf("tune with both tuners busy: %v", err)
	}
}

// TestHDHomeRunAntenna: the emulated HDHomeRun lists the tuner's channels,
// named and guided as the app has them, and nothing else.
func TestHDHomeRunAntenna(t *testing.T) {
	s, fake := newTunerService(t)
	dev := &hdhr.Server{Backend: s.HDHR(), FriendlyName: "Airwaves", Tuners: s.TunerCount}
	srv := httptest.NewServer(dev.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/lineup.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	err = json.NewDecoder(resp.Body).Decode(&entries)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var numbers []string
	names := map[string]string{}
	for _, e := range entries {
		n := e["GuideNumber"].(string)
		numbers = append(numbers, n)
		names[n] = e["GuideName"].(string)
	}
	if !slices.Equal(numbers, tvhNumbers(fake)) || names["20.1"] != "KTVDDT" || names["20.2"] != "KTVD" || names["9.7"] != "KUSA" {
		t.Errorf("lineup %v, names %v", numbers, names)
	}
	if s.TunerCount() != 2 {
		t.Errorf("tuners %d", s.TunerCount())
	}

	resp, err = http.Get(srv.URL + "/xmltv.xml")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var doc xmltvOut
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	progs := map[string]int{}
	for _, p := range doc.Programmes {
		progs[p.Channel]++
	}
	if len(doc.Channels) != 38 || progs["airwaves.20.1"] != 2 || progs["airwaves.7.1"] != 0 || len(progs) != 7 {
		t.Errorf("%d channels, programmes %v", len(doc.Channels), progs)
	}
}

// TestMeasureFallbacks: a Tvheadend that won't stream whole multiplexes
// is measured through a channel on each, and the RF channels with none
// are skipped, saying so; tuners busy with Tvheadend's own scans are left
// to them, saying that.
func TestMeasureFallbacks(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	fake.NoMuxStreams = true
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	st := waitSweep(t, s)
	if st.Done != 11 || st.Note != "3 RF channels skipped: Tvheadend won't stream them" {
		t.Errorf("sweep = %+v", st)
	}
	channels := 0
	for _, r := range fake.Requests() {
		if strings.HasPrefix(r, "GET /stream/channel/") {
			channels++
			if !strings.HasSuffix(r, "?profile=pass&weight=10") {
				t.Errorf("subscribed as %s", r)
			}
		}
	}
	if m, _ := s.signals.Get(575_000_000); channels != 8 || m.Latest == nil || !m.Latest.Lock {
		t.Errorf("%d channel streams; RF 31 = %+v", channels, m)
	}

	fake.Busy(tvhtest.Tuner0, "587MHz", 4)
	fake.Busy(tvhtest.Tuner1, "605MHz", 4)
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if st := waitSweep(t, s); !strings.Contains(st.Note, "busy with Tvheadend's own guide or channel scan") {
		t.Errorf("sweep = %+v", st)
	}
}
