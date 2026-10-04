package signal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"airwaves/internal/tvh"
	"airwaves/internal/tvh/tvhtest"
)

// TestFromInput reads an HDHomeRun's status as Tvheadend reports it:
// strength and quality as percentages, no dB, and no BER or UNC since it
// counts none.
func TestFromInput(t *testing.T) {
	var body struct {
		Entries []tvh.InputStatus `json:"entries"`
	}
	if err := json.Unmarshal(tvhtest.Raw(t, "status_inputs.json"), &body); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	r := FromInput(body.Entries[0], nil, at, Active)
	if !r.Lock || *r.StrengthPct != 85 || *r.QualityPct != 83 || r.StrengthDBm != nil || r.SNRdB != nil ||
		r.BER != nil || r.UNC != nil || r.Source != Active || !r.At.Equal(at) {
		t.Errorf("reading = %+v", r)
	}
	raw, _ := json.Marshal(r)
	for _, want := range []string{`"lock":true`, `"strengthPct":85`, `"qualityPct":83`, `"snrDb":null`, `"strengthDbm":null`, `"ber":null`, `"unc":null`, `"errorsPerSec":null`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("JSON lacks %s: %s", want, raw)
		}
	}

	// A tuner reporting decibels, counting bits and blocks, with no lock.
	r = FromInput(tvh.InputStatus{Signal: -48250, SignalScale: tvh.ScaleDecibel, SNR: 21400, SNRScale: tvh.ScaleDecibel,
		ECBit: 3, TCBit: 1000, ECBlock: 7, TCBlock: 500}, nil, at, Sweep)
	if r.Lock || *r.StrengthDBm != -48.3 || *r.SNRdB != 21.4 || r.StrengthPct != nil || r.QualityPct != nil || *r.BER != 0.003 || *r.UNC != 7 {
		t.Errorf("decibel reading = %+v", r)
	}
	// Nothing reported: nothing claimed.
	r = FromInput(tvh.InputStatus{}, nil, at, Sweep)
	if r.StrengthPct != nil || r.StrengthDBm != nil || r.QualityPct != nil || r.SNRdB != nil || r.UNC != nil || r.Lock {
		t.Errorf("empty reading = %+v", r)
	}
}

// TestFromInputMeasuring reads two tuners measuring, as a real Tvheadend
// reported them: RF 31 locked (its subscription "Running", though its
// input sends little), RF 7 not (strength, but no quality, no data, and
// its subscription "Testing"). Idle inputs say nothing.
func TestFromInputMeasuring(t *testing.T) {
	var ins struct {
		Entries []tvh.InputStatus `json:"entries"`
	}
	var subs struct {
		Entries []tvh.Subscription `json:"entries"`
	}
	if err := json.Unmarshal(tvhtest.Raw(t, "status_inputs_measuring.json"), &ins); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tvhtest.Raw(t, "status_subscriptions_measuring.json"), &subs); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	rf7, rf31 := ins.Entries[0], ins.Entries[1]
	rf31.BPS = 0 // as it reads, now and then
	r := FromInput(rf31, subs.Entries, at, Sweep)
	if !r.Lock || *r.StrengthPct != 84 || *r.QualityPct != 67 {
		t.Errorf("RF 31 = %+v", r)
	}
	r = FromInput(rf7, subs.Entries, at, Sweep)
	if r.Lock || *r.StrengthPct != 81 || *r.QualityPct != 0 {
		t.Errorf("RF 7 = %+v", r)
	}
	// Data seems to arrive on RF 7 for a moment (the last tuning's rate),
	// but its subscription is still "Testing": no lock.
	rf7.BPS = 210560
	if r := FromInput(rf7, subs.Entries, at, Sweep); r.Lock {
		t.Errorf("RF 7 with a stale rate = %+v", r)
	}
	// Without a subscription listed, the rate decides.
	if r := FromInput(rf31, nil, at, Sweep); r.Lock {
		t.Errorf("RF 31 without subscriptions or data = %+v", r)
	}
	rf31.BPS = 157920
	if r := FromInput(rf31, nil, at, Sweep); !r.Lock {
		t.Errorf("RF 31 without subscriptions, with data = %+v", r)
	}
	// A marginal multiplex losing its lock for a moment: its subscription
	// still "Running", the HDHomeRun's quality none.
	gone := rf31
	gone.SNR = 0
	if r := FromInput(gone, subs.Entries, at, Sweep); r.Lock || *r.QualityPct != 0 {
		t.Errorf("RF 31 between locks = %+v", r)
	}
	// A marginal multiplex: transport and continuity errors climbing.
	later := rf31
	later.TE, later.CC = rf31.TE+60, rf31.CC+30
	r = FromInput(later, subs.Entries, at, Sweep)
	if r.SetErrors(rf31, later, 2); r.ErrorsPerSec == nil || *r.ErrorsPerSec != 45 {
		t.Errorf("errors = %v", r.ErrorsPerSec)
	}
	if r := FromInput(rf31, subs.Entries, at, Sweep); r.ErrorsPerSec != nil {
		t.Errorf("first reading errors = %v", *r.ErrorsPerSec)
	}
	r.SetErrors(rf7, rf31, 2) // another tuning: no rate
	if r.ErrorsPerSec != nil {
		t.Errorf("errors across tunings = %v", *r.ErrorsPerSec)
	}

	var idle struct {
		Entries []tvh.InputStatus `json:"entries"`
	}
	if err := json.Unmarshal(tvhtest.Raw(t, "status_inputs_idle.json"), &idle); err != nil {
		t.Fatal(err)
	}
	for _, in := range idle.Entries {
		if _, _, ok := in.Mux(); ok || in.InUse() {
			t.Errorf("idle input %+v", in)
		}
		if r := FromInput(in, nil, at, Active); r.Lock || r.StrengthPct != nil || r.QualityPct != nil {
			t.Errorf("idle reading %+v", r)
		}
	}
}

func TestScanOf(t *testing.T) {
	seen := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	if sc, ok := ScanOf(tvh.Mux{ScanResult: tvh.ScanOK, ScanLast: 1791129355}, seen); !ok || !sc.Lock || sc.Partial || sc.At.Unix() != 1791129355 {
		t.Errorf("ok: %+v %v", sc, ok)
	}
	if sc, ok := ScanOf(tvh.Mux{ScanResult: tvh.ScanPartial}, seen); !ok || !sc.Lock || !sc.Partial || !sc.At.Equal(seen) {
		t.Errorf("partial: %+v %v", sc, ok)
	}
	if sc, ok := ScanOf(tvh.Mux{ScanResult: tvh.ScanFail}, seen); !ok || sc.Lock || !sc.At.Equal(seen) {
		t.Errorf("fail: %+v %v", sc, ok)
	}
	if _, ok := ScanOf(tvh.Mux{ScanResult: tvh.ScanNone}, seen); ok {
		t.Error("an unscanned multiplex has a scan")
	}
}

func TestRF(t *testing.T) {
	for freq, want := range map[int64]int{
		57_000_000: 2, 69_000_000: 4, 79_000_000: 5, 85_000_000: 6, 177_000_000: 7, 213_000_000: 13,
		473_000_000: 14, 479_000_000: 15, 575_000_000: 31, 605_000_000: 36, 611_000_000: 37, 0: 0, 100_000_000: 0,
	} {
		if got := RF(freq); got != want {
			t.Errorf("RF(%d) = %d, want %d", freq, got, want)
		}
	}
}

func reading(at time.Time, source string, lock bool, strength, quality float64) Reading {
	return Reading{At: at, Source: source, Lock: lock, StrengthPct: &strength, QualityPct: &quality}
}

// TestHistory: readings gather into windows by source and time, the
// windows into a summary with lows and means, and only so many are kept.
func TestHistory(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	const freq = 575_000_000
	if _, ok := s.Get(freq); ok {
		t.Fatal("measured before any reading")
	}
	// Watching for a minute, every 10 s; the picture breaks up once.
	for i, q := range []float64{83, 82, 40, 83, 84, 83} {
		s.Record(freq, 31, reading(t0.Add(time.Duration(i)*10*time.Second), Active, q > 50, 85, q))
	}
	// Measure now, an hour later.
	s.Record(freq, 31, reading(t0.Add(time.Hour), Sweep, true, 86, 84))
	m, ok := s.Get(freq)
	if !ok || m.RF != 31 || len(m.Windows) != 2 || m.Latest.Source != Sweep || *m.Latest.QualityPct != 84 {
		t.Fatalf("mux = %+v", m)
	}
	h := m.History()
	if h.Samples != 7 || h.QualityPct.Min != 40 || h.QualityPct.Max != 84 || h.QualityPct.Avg != 77 || h.StrengthPct.Avg != 85.1 ||
		h.LockedPct != 85.7 || h.SNRdB != nil || !h.From.Equal(t0) || !h.To.Equal(t0.Add(time.Hour)) || len(h.Windows) != 2 {
		t.Errorf("history = %+v", h)
	}
	if w := h.Windows[0]; w.Source != Active || w.Samples != 6 || w.LockedPct != 83.3 || w.QualityPct.Min != 40 || !w.To.Equal(t0.Add(50*time.Second)) {
		t.Errorf("first window = %+v", w)
	}

	// A long stretch of watching splits into windows, and the oldest go.
	for i := range 3 * 60 * 6 {
		s.Record(freq, 31, reading(t0.Add(2*time.Hour+time.Duration(i)*10*time.Second), Active, true, 85, 83))
	}
	m, _ = s.Get(freq)
	if len(m.Windows) != 8 {
		t.Errorf("3 hours of watching: %d windows", len(m.Windows))
	}
	for i := range 3 * MaxWindows {
		s.Record(freq, 31, reading(t0.Add(6*time.Hour+time.Duration(i)*time.Hour), Sweep, true, 85, 83))
	}
	if m, _ = s.Get(freq); len(m.Windows) != MaxWindows {
		t.Errorf("windows kept = %d", len(m.Windows))
	}
}

// TestStoreSaves: measurements and scan results outlive a restart, and a
// scan that keeps failing keeps the date it was first seen failing.
func TestStoreSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signal.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	s.Record(605_000_000, 36, reading(t0, Sweep, true, 85, 83))
	s.SetScan(605_000_000, 36, Scan{Lock: true, At: t0.Add(-time.Hour)})
	s.SetScan(177_000_000, 7, Scan{At: t0})
	s.SetScan(177_000_000, 7, Scan{At: t0.Add(time.Hour)})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	all := again.All()
	if len(all) != 2 || all[0].RF != 7 || all[1].RF != 36 {
		t.Fatalf("muxes = %+v", all)
	}
	if sc := all[0].Scan; sc == nil || sc.Lock || !sc.At.Equal(t0) || all[0].Latest != nil || all[0].History() != nil {
		t.Errorf("RF 7 = %+v", all[0])
	}
	if m := all[1]; !m.Scan.Lock || !m.Latest.Lock || *m.Latest.StrengthPct != 85 || m.History().Samples != 1 {
		t.Errorf("RF 36 = %+v", m)
	}
	// A lock found later replaces the failure, dated by Tvheadend.
	again.SetScan(177_000_000, 7, Scan{Lock: true, At: t0.Add(2 * time.Hour)})
	if m, _ := again.Get(177_000_000); !m.Scan.Lock || !m.Scan.At.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("RF 7 found: %+v", m.Scan)
	}

	// An unreadable file is set aside rather than lost or fatal.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad, err := Open(path)
	if err == nil || bad == nil || len(bad.All()) != 0 {
		t.Errorf("unreadable: %v, %+v", err, bad)
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Errorf("not set aside: %v", err)
	}
}
