package service

import (
	"bytes"
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
	"sync"
	"testing"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/hdhr"
	"airwaves/internal/hdhr/hdhrtest"
	"airwaves/internal/lineup/lineuptest"
	"airwaves/internal/signal"
	"airwaves/internal/ts"
	"airwaves/internal/ts/tstest"
	"airwaves/internal/tuner"
)

// fastTiming measures in milliseconds rather than seconds.
var fastTiming = measureTiming{
	sample:  20 * time.Millisecond,
	acquire: 300 * time.Millisecond, settle: 20 * time.Millisecond, interval: 20 * time.Millisecond, readings: 3,
	idleWait: 300 * time.Millisecond,
	poll:     20 * time.Millisecond, noLock: 300 * time.Millisecond,
}

// fakeTuner is an HDHomeRun whose scan found 38 channels around Denver,
// with RF channels that can be made to lose their signal.
type fakeTuner struct {
	*hdhrtest.Server
	mu     sync.Mutex
	muxes  map[int64][]byte
	signal map[int64][3]int
}

func (f *fakeTuner) mux(freq int64) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.muxes[freq]
}

func (f *fakeTuner) sig(freq int64) (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.signal[freq]; ok {
		return v[0], v[1], v[2]
	}
	if f.muxes[freq] != nil {
		return 90, 95, 100
	}
	return 40, 0, 0
}

// set makes the RF channel at freq carry mux (nil: nothing to lock), with
// the given signal.
func (f *fakeTuner) set(freq int64, mux []byte, strength, quality, symbol int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.muxes[freq] = mux
	f.signal[freq] = [3]int{strength, quality, symbol}
}

// newTunerService is a server with the fake tuner, and Denver's records
// and listings already loaded.
func newTunerService(t *testing.T, change ...func(*Options)) (*Service, *fakeTuner) {
	t.Helper()
	fake := &fakeTuner{muxes: lineuptest.Muxes(), signal: map[int64][3]int{}}
	fake.Server = hdhrtest.New(t, hdhrtest.Device{Lineup: lineuptest.Lineup(), Mux: fake.mux, Signal: fake.sig})
	dir := t.TempDir()
	opt := Options{
		Name: "nas", Mode: "server", CacheDir: filepath.Join(dir, "cache"), Config: Config{ZIP: "80302"}, HDHomeRun: fake.URL(),
		DVRPath: filepath.Join(dir, "dvr.json"), RecordingsDir: filepath.Join(dir, "recordings"), SignalPath: filepath.Join(dir, "signal.json"),
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
	if s.tuners != nil {
		s.tuners.Linger, s.tuners.StatusAge = 0, 0
	}
	s.findTuner(t.Context())
	s.mu.Lock()
	s.report, s.guide, s.built = lineuptest.Report(), lineuptest.Guide(), time.Now()
	s.mu.Unlock()
	return s, fake
}

func lineupNumbers() []string {
	var out []string
	for _, c := range lineuptest.Lineup() {
		out = append(out, c.Number)
	}
	return out
}

// TestTunerLineup: the app's antenna channels are exactly the tuner's, the
// records and listings only describing them; nothing estimated is served.
func TestTunerLineup(t *testing.T) {
	s, _ := newTunerService(t)
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
	want := lineupNumbers()
	slices.SortFunc(want, guide.CompareNumbers)
	if len(want) != 38 || !slices.Equal(numbers, want) {
		t.Fatalf("antenna channels %v, want the tuner's %v", numbers, want)
	}
	// Listed and licensed, but not found by the scan.
	for _, n := range []string{"7.1", "9.1"} {
		if _, ok := by[n]; ok {
			t.Errorf("%s listed without the tuner having it", n)
		}
	}
	if c := by["20.1"]; c.CallSign != "KTVD" || c.GuideCallSign != "KTVDDT" || c.Transmitter != "KTVD" || c.RF != 31 || c.Signal != nil || c.Name != "KTVD-HD" {
		t.Errorf("20.1 = %+v", c)
	}
	if c := by["2.1"]; c.Via != "KDVR" || c.NextGen == nil || c.NextGen.HostCall != "KWGN-TV" {
		t.Errorf("2.1 = %+v", c)
	}
	ti := a.Tuner
	if !ti.Ready || ti.Tuners != 2 || ti.Name != "HDHomeRun FLEX DUO" || ti.Model != "HDFX-2US" || !slices.Equal(ti.Standards, []string{"ATSC 1.0"}) || ti.ATSC3 ||
		ti.InUse != 0 || ti.Reason != "" || ti.Scanning {
		t.Errorf("tuner = %+v", ti)
	}

	// The RF channels the scan found channels on, and those licensed
	// nearby, with what's licensed on each.
	mux := map[int]MuxSignal{}
	var rfs []int
	for _, m := range a.Muxes {
		mux[m.RF] = m
		rfs = append(rfs, m.RF)
	}
	if !slices.Equal(rfs, []int{7, 15, 16, 28, 29, 31, 32, 33, 34, 35, 36}) {
		t.Fatalf("RF channels %v", rfs)
	}
	if m := mux[31]; m.FrequencyMHz != 575 || m.Band != "UHF" || !slices.Equal(m.Channels, []string{"9.4", "9.7", "20.1", "20.2", "20.3", "20.4"}) ||
		len(m.Stations) != 2 || m.Stations[0].CallSign != "K31AB-D" || m.Stations[1].CallSign != "KTVD" || m.Signal != nil || m.History != nil || m.Tuned {
		t.Errorf("RF 31 = %+v", m)
	}
	if m := mux[7]; len(m.Channels) != 0 || len(m.Stations) != 1 || m.Stations[0].CallSign != "KMGH-TV" || m.Band != "VHF-Hi" {
		t.Errorf("RF 7 = %+v", m)
	}
	if m := mux[34]; len(m.Stations) != 1 || !m.Stations[0].ATSC3 || len(m.Channels) != 0 {
		t.Errorf("RF 34 = %+v", m)
	}

	// The report: the records with no estimates and no channels of its
	// own (they're the tuner's); the listings of the tuner's channels only.
	if snap.Report.Channels == nil || len(snap.Report.Channels) != 0 {
		t.Errorf("report channels %v", snap.Report.Channels)
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
	for _, bad := range []string{"noiseMarginDb", `"presets"`, `"strong"`, `"fair"`, `"tier"`, `"signal":{}`, `"scan"`, "169.254"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("snapshot has %s", bad)
		}
	}
	for _, want := range []string{`"channels":[]`, `"antenna":{"tuner":{"ready":true`, `"standards":["ATSC 1.0"],"atsc3":false`, `"history":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("snapshot lacks %s", want)
		}
	}

	info, _ := s.Info(ctx)
	if info.Tuner.Kind != "hdhomerun" || info.Tuner.Tuners != 2 || info.Tuner.Model != "HDFX-2US" || info.Tuner.Name != "HDHomeRun FLEX DUO" ||
		!slices.Equal(info.Tuner.Standards, []string{"ATSC 1.0"}) || info.Tuner.ATSC3 || !info.DVR {
		t.Errorf("info = %+v", info)
	}
	if got := s.AntennaChannels(ctx); len(got) != 38 || got["20.1"] != "KTVD" || got["9.7"] != "KUSA" || got["7.1"] != "" {
		t.Errorf("antenna channels = %v", got)
	}
}

// TestNoTuner: no tuner, no antenna channels; nothing made up in their
// place, and tuning one says why.
func TestNoTuner(t *testing.T) {
	s, _ := newTunerService(t, func(o *Options) { o.HDHomeRun = "http://127.0.0.1:1" })
	ctx := t.Context()
	snap, err := s.Snapshot(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if a := snap.Antenna; len(a.Channels) != 0 || a.Tuner.Ready || !strings.HasPrefix(a.Tuner.Reason, "no tuner: ") || len(snap.Report.Channels) != 0 || len(snap.Guide.Channels) != 0 {
		t.Errorf("antenna = %+v", a)
	}
	if !slices.ContainsFunc(snap.Report.Warnings, func(w string) bool { return strings.HasPrefix(w, "No antenna channels: no tuner") }) {
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
	if info, _ := s.Info(ctx); info.Tuner.Kind != "none" || info.Tuner.Tuners != 0 {
		t.Errorf("info = %+v", info.Tuner)
	}
	if s.TunerCount() != 2 {
		t.Errorf("emulated tuners %d before the real ones are found", s.TunerCount())
	}
}

// readProgram reads in's stream for a moment and lists its programs.
func readProgram(t *testing.T, in tuner.Input) []ts.Program {
	t.Helper()
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_ = in.Source(ctx, &buf)
	d := ts.NewDemux()
	var split ts.Splitter
	split.Write(buf.Bytes(), d.Packet)
	return d.Programs()
}

// TestTune: a channel starts with its program alone and its audio tracks
// described; one whose RF channel won't lock fails with what the tuner
// measured, not ffmpeg's complaint; with every tuner busy, it says who
// has them.
func TestTune(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	var got tuner.Input
	start := func(_ context.Context, in tuner.Input) error {
		got = in
		return nil
	}
	if err := s.tuneAntenna(ctx, "den", "4.1", start); err != nil {
		t.Fatal(err)
	}
	if len(got.Audio) != 2 || !got.Audio[1].Described || got.Audio[0].Lang != "eng" || !got.Broadcast || got.Args[len(got.Args)-1] != "pipe:0" {
		t.Errorf("input = %+v", got)
	}
	if ps := readProgram(t, got); len(ps) != 1 || ps[0].Number != 1 {
		t.Errorf("4.1 carries %+v, want its program alone", ps)
	}

	// No lock on KTVD's RF channel.
	fake.set(575_000_000, nil, 41, 0, 0)
	err := s.tuneAntenna(ctx, "den", "20.1", start)
	var ns *NoSignalError
	if !errors.As(err, &ns) || ns.RF != 31 || ns.Label != "KTVD 20.x" ||
		err.Error() != "No signal on RF 31 (KTVD 20.x) right now: the tuner measured strength 41%, quality 0% and couldn't lock" {
		t.Fatalf("tune 20.1: %v", err)
	}
	if m, ok := s.signals.Get(575_000_000); !ok || m.Latest.Lock || *m.Latest.StrengthPct != 41 || m.Latest.Source != signal.Active {
		t.Errorf("RF 31 = %+v", m)
	}
	if err := s.tuneAntenna(ctx, "den", "9.4", start); err == nil || !strings.HasPrefix(err.Error(), "No signal on RF 31 (KUSA 9.x on KTVD) right now") {
		t.Errorf("tune 9.4: %v", err)
	}
	// Not a channel the tuner found.
	if err := s.tuneAntenna(ctx, "den", "7.1", start); err == nil || err.Error() != "channel 7.1 isn't in the tuner's lineup" {
		t.Errorf("tune 7.1: %v", err)
	}

	// Both tuners recording: no one can watch something else, but the
	// den can still watch what's recording.
	for _, n := range []string{"6.1", "50.1"} {
		freq, prog, _ := s.tuning(ctx, n)
		r, err := s.tuners.Open(ctx, tuner.Request{FrequencyHz: freq, Program: prog, Priority: tuner.Recording, Who: "news on " + n})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
	}
	if err := s.tuneAntenna(ctx, "den", "31.1", start); err == nil || !strings.HasPrefix(err.Error(), "every tuner is in use right now (recording news on 50.1; recording news on 6.1)") {
		t.Errorf("tune with both tuners recording: %v", err)
	}
	if err := s.tuneAntenna(ctx, "den", "6.2", start); err != nil {
		t.Errorf("tune what shares a recording's RF channel: %v", err)
	}
}

// TestSampleTuners: while a tuner is in use, its readings are recorded
// for its RF channel, once it has been on it a while, and saved.
func TestSampleTuners(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	// KTVD's RF channel arrives with errors.
	damaged := fake.mux(575_000_000)
	for i := 50; i < len(damaged)/188; i += 50 {
		tstest.SetTEI(damaged, i)
	}
	fake.set(575_000_000, damaged, 85, 83, 97)
	sub, err := s.tuners.Open(ctx, tuner.Request{FrequencyHz: 575_000_000, Program: 3, Priority: tuner.Watching, Who: "20.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, err := sub.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	s.sampleTuners(ctx)
	if _, ok := s.signals.Get(575_000_000); ok {
		t.Fatal("read a tuner just tuned")
	}
	time.Sleep(50 * time.Millisecond)
	s.sampleTuners(ctx)
	m, ok := s.signals.Get(575_000_000)
	if !ok || m.RF != 31 || m.Latest == nil || !m.Latest.Lock || *m.Latest.StrengthPct != 85 || *m.Latest.QualityPct != 83 || *m.Latest.SymbolPct != 97 ||
		m.Latest.Source != signal.Active || m.Latest.ErrorsPerSec == nil || *m.Latest.ErrorsPerSec <= 0 {
		t.Fatalf("RF 31 = %+v, %+v", m, m.Latest)
	}

	sig, err := s.Signal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Tuner.InUse != 1 {
		t.Errorf("in use: %d", sig.Tuner.InUse)
	}
	for _, c := range sig.Channels {
		if (c.RF == 31) != (c.Signal != nil) || (c.RF == 31) != (c.Recent != nil) {
			t.Errorf("%s (RF %d) signal %+v, recent %+v", c.Number, c.RF, c.Signal, c.Recent)
		}
	}
	for _, m := range sig.Muxes {
		if m.RF == 31 && (!m.Tuned || m.History == nil || m.History.Samples != 1 || m.History.LockedPct != 100) {
			t.Errorf("RF 31 = %+v", m)
		}
	}
	raw, _ := json.Marshal(sig)
	if !strings.Contains(string(raw), `"number":"4.1","rf":35,"signal":null,"recent":null`) {
		t.Errorf("unmeasured channel: %s", raw)
	}

	// Saved on the way out, and read back.
	s.Close()
	again, err := signal.Open(s.opt.SignalPath)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := again.Get(575_000_000); !ok || m.Latest == nil || !m.Latest.Lock {
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

// TestMeasure: Measure now reads every RF channel with channels, then
// those with a licensed station, one at a time on a free tuner.
func TestMeasure(t *testing.T) {
	s, fake := newTunerService(t)
	ctx := t.Context()
	// KMGH's VHF signal arrives strong, but the tuner can't lock to it.
	fake.set(177_000_000, nil, 81, 0, 0)
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	st := waitSweep(t, s)
	if st.Total != 11 || st.Found != 8 || st.Done != 11 || st.Note != "" || st.FinishedAt == nil {
		t.Fatalf("sweep = %+v", st)
	}
	if m, ok := s.signals.Get(587_000_000); !ok || len(m.Windows) != 1 || m.Windows[0].Samples != 3 || !m.Latest.Lock || m.Latest.Source != signal.Sweep {
		t.Errorf("RF 33 = %+v", m)
	}
	if m, ok := s.signals.Get(177_000_000); !ok || m.Latest.Lock || *m.Latest.StrengthPct != 81 {
		t.Errorf("RF 7 = %+v", m)
	}
	if n := fake.Streams(); n != 0 {
		t.Errorf("%d streams left open", n)
	}
}

// TestMeasureLeavesTunersAlone: measuring never takes a tuner someone is
// using, and stops when someone needs the one it has.
func TestMeasureLeavesTunersAlone(t *testing.T) {
	s, _ := newTunerService(t)
	ctx := t.Context()
	open := func(n string) *tuner.Sub {
		freq, prog, _ := s.tuning(ctx, n)
		sub, err := s.tuners.Open(ctx, tuner.Request{FrequencyHz: freq, Program: prog, Priority: tuner.Watching, Who: n})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sub.Close)
		return sub
	}
	a, b := open("4.1"), open("6.1")
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	if st := waitSweep(t, s); st.Note != "stopped at RF 15: no tuner was free (they're left to viewers and recordings)" {
		t.Errorf("sweep with both tuners watched = %+v", st)
	}
	if a.Err() != nil || b.Err() != nil {
		t.Fatalf("measuring ended a viewer: %v, %v", a.Err(), b.Err())
	}

	// One free: measuring takes it, until a viewer wants it.
	b.Close()
	s.timing.settle = 200 * time.Millisecond
	if _, err := s.Measure(ctx); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); s.sweepStatus().Done < 9; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("sweep = %+v", s.sweepStatus())
		}
	}
	open("50.1")
	if st := waitSweep(t, s); !strings.Contains(st.Note, "a viewer or a recording needed the tuner") && !strings.Contains(st.Note, "no tuner was free") {
		t.Errorf("sweep = %+v", st)
	}
}

// TestHDHomeRunAntenna: the emulated HDHomeRun lists the tuner's channels,
// named and guided as the app has them, and streams them as broadcast.
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
	want := lineupNumbers()
	slices.SortFunc(want, guide.CompareNumbers)
	if !slices.Equal(numbers, want) || names["20.1"] != "KTVDDT" || names["20.2"] != "H & I" || names["9.7"] != "TBD" {
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

	// A stream is the channel's program alone, and frees its tuner after.
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/auto/v20.2", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	d := ts.NewDemux()
	var split ts.Splitter
	split.Write(body, d.Packet)
	if ps := d.Programs(); len(ps) != 1 || ps[0].Number != 4 {
		t.Errorf("20.2's stream carries %+v", ps)
	}
	for deadline := time.Now().Add(2 * time.Second); fake.Streams() > 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the tuner wasn't freed")
		}
	}
}

// TestScanChannels: a scan lets go of the tuners, and its result becomes
// the lineup.
func TestScanChannels(t *testing.T) {
	fake := &fakeTuner{muxes: lineuptest.Muxes(), signal: map[int64][3]int{}}
	rescan := append(lineuptest.Lineup(), hdhr.Channel{Number: "9.1", Name: "KUSA-HD", FrequencyHz: 189_000_000, Program: 3})
	fake.Server = hdhrtest.New(t, hdhrtest.Device{Lineup: lineuptest.Lineup(), Rescan: rescan, ScanTime: 200 * time.Millisecond, Mux: fake.mux, Signal: fake.sig})
	s, _ := newTunerService(t, func(o *Options) { o.HDHomeRun = fake.URL() })
	ctx := t.Context()
	sub, err := s.tuners.Open(ctx, tuner.Request{FrequencyHz: 575_000_000, Program: 3, Priority: tuner.Watching, Who: "20.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ti, err := s.ScanChannels(ctx)
	if err != nil || !ti.Scanning {
		t.Fatalf("scan: %+v, %v", ti, err)
	}
	<-sub.Done()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		s.mu.Lock()
		s.scanAt = time.Time{}
		s.mu.Unlock()
		if !s.tunerInfo(ctx).Scanning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still scanning")
		}
	}
	if got := s.AntennaChannels(ctx); got["9.1"] == "" || len(got) != 39 {
		t.Errorf("after the scan: %d channels, 9.1 %q", len(got), got["9.1"])
	}
}
