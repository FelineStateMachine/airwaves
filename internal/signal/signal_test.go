package signal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"airwaves/internal/hdhr"
	"airwaves/internal/tuner"
)

// TestFromTuner reads a tuner: whether data arrives, and the HDHomeRun's
// percentages; an idle tuner has nothing to read.
func TestFromTuner(t *testing.T) {
	at := time.Date(2026, 10, 4, 18, 0, 0, 400_000_000, time.UTC)
	ss, snq, seq := 96, 0, 0
	since := at.Add(-time.Minute)
	st := tuner.State{Number: 1, FrequencyHz: 189_000_000, Since: since, Locked: true, Packets: 9000, Errors: 40,
		Device: hdhr.TunerStatus{Tuner: 1, FrequencyHz: 189_000_000, StrengthPct: &ss, QualityPct: &snq, SymbolPct: &seq}}
	r := FromTuner(st, at, Active)
	if !r.Lock || *r.StrengthPct != 96 || *r.QualityPct != 0 || *r.SymbolPct != 0 || r.Source != Active || !r.At.Equal(at.Truncate(time.Second)) || r.ErrorsPerSec != nil {
		t.Fatalf("reading = %+v", r)
	}

	// Errors a second, against an earlier reading of the same tuning.
	prev := st
	prev.Errors = 10
	r.SetErrors(prev, st, 10)
	if r.ErrorsPerSec == nil || *r.ErrorsPerSec != 3 {
		t.Fatalf("errors a second = %v", r.ErrorsPerSec)
	}
	retuned := prev
	retuned.Since = since.Add(-time.Hour)
	if r.SetErrors(retuned, st, 10); r.ErrorsPerSec != nil {
		t.Fatal("errors counted across tunings")
	}

	if r := FromTuner(tuner.State{Number: 0}, at, Sweep); r.Lock || r.StrengthPct != nil || r.QualityPct != nil || r.SymbolPct != nil {
		t.Fatalf("idle tuner reading = %+v", r)
	}
}

func TestFrequency(t *testing.T) {
	for rf := 2; rf <= 36; rf++ {
		if got := RF(Frequency(rf)); got != rf {
			t.Errorf("RF(Frequency(%d)) = %d", rf, got)
		}
	}
	if Frequency(1) != 0 || Frequency(37) != 0 {
		t.Error("not broadcast RF channels have frequencies")
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
		h.LockedPct != 85.7 || h.SymbolPct != nil || !h.From.Equal(t0) || !h.To.Equal(t0.Add(time.Hour)) || len(h.Windows) != 2 {
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

// TestStoreSaves: measurements outlive a restart.
func TestStoreSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signal.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	s.Record(605_000_000, 36, reading(t0, Sweep, true, 85, 83))
	s.Record(177_000_000, 7, reading(t0, Sweep, false, 40, 0))
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
	if m := all[0]; m.Latest.Lock || *m.Latest.StrengthPct != 40 {
		t.Errorf("RF 7 = %+v", m)
	}
	if m := all[1]; !m.Latest.Lock || *m.Latest.StrengthPct != 85 || m.History().Samples != 1 {
		t.Errorf("RF 36 = %+v", m)
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
