// Package signal keeps what the tuners measured on each multiplex (each RF
// channel): readings of signal strength and quality taken while a tuner
// was on it. Nothing here is estimated: a value is there because a tuner
// reported it, and is null otherwise.
//
// The HDHomeRun reports strength, signal-to-noise quality and symbol
// quality as percentages; whether data arrived, and the damage in it,
// Airwaves sees for itself.
package signal

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"airwaves/internal/tuner"
)

// Sources of readings.
const (
	// Active readings are taken while something had a tuner on the
	// multiplex: a viewer or a recording.
	Active = "active"
	// Sweep readings are taken by Measure now, on an idle tuner.
	Sweep = "sweep"
)

// Reading is one sample of a tuner on a multiplex.
type Reading struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"` // Active or Sweep
	// Lock is whether data arrived: the tuner locked to the signal.
	Lock bool `json:"lock"`
	// StrengthPct is signal strength, QualityPct the signal-to-noise
	// quality and SymbolPct the share of symbols received whole or
	// corrected over the last second, all as the tuner reports them, 0
	// to 100.
	StrengthPct *float64 `json:"strengthPct"`
	QualityPct  *float64 `json:"qualityPct"`
	SymbolPct   *float64 `json:"symbolPct"`
	// ErrorsPerSec is the rate of transport and continuity errors in what
	// arrived since the reading before, on the same tuning: a picture
	// breaking up. Null for a first reading.
	ErrorsPerSec *float64 `json:"errorsPerSec"`
}

// FromTuner reads a tuner as it is now: whether data arrives, and what
// the HDHomeRun measures of the signal.
func FromTuner(t tuner.State, at time.Time, source string) Reading {
	pct := func(v *int) *float64 {
		if v == nil {
			return nil
		}
		return ptr(float64(*v))
	}
	return Reading{
		At: at.UTC().Truncate(time.Second), Source: source, Lock: t.Locked,
		StrengthPct: pct(t.Device.StrengthPct), QualityPct: pct(t.Device.QualityPct), SymbolPct: pct(t.Device.SymbolPct),
	}
}

// SetErrors sets the error rate from the tuner's state at an earlier
// reading of the same tuning, secs before.
func (r *Reading) SetErrors(prev, now tuner.State, secs float64) {
	r.ErrorsPerSec = nil
	same := prev.Number == now.Number && prev.FrequencyHz == now.FrequencyHz && prev.Since.Equal(now.Since)
	if secs <= 0 || !same || now.Errors < prev.Errors {
		return
	}
	r.ErrorsPerSec = ptr(round1(float64(now.Errors-prev.Errors) / secs))
}

func ptr[T any](v T) *T { return &v }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Stat is the spread of one quantity over readings.
type Stat struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	Sum float64 `json:"sum"`
	N   int     `json:"n"`
}

func (s *Stat) add(v *float64) *Stat {
	if v == nil {
		return s
	}
	if s == nil {
		return &Stat{Min: *v, Max: *v, Sum: *v, N: 1}
	}
	s.Min, s.Max, s.Sum, s.N = min(s.Min, *v), max(s.Max, *v), s.Sum+*v, s.N+1
	return s
}

func merge(a, b *Stat) *Stat {
	switch {
	case a == nil && b == nil:
		return nil
	case a == nil:
		c := *b
		return &c
	case b == nil:
		c := *a
		return &c
	}
	return &Stat{Min: min(a.Min, b.Min), Max: max(a.Max, b.Max), Sum: a.Sum + b.Sum, N: a.N + b.N}
}

// Window gathers consecutive readings of one source: a stretch of viewing,
// or one sweep's readings.
type Window struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Source      string    `json:"source"`
	Samples     int       `json:"samples"`
	Locked      int       `json:"locked"`
	StrengthPct *Stat     `json:"strengthPct,omitempty"`
	QualityPct  *Stat     `json:"qualityPct,omitempty"`
	SymbolPct   *Stat     `json:"symbolPct,omitempty"`
}

func (w *Window) add(r Reading) {
	w.To = r.At
	w.Samples++
	if r.Lock {
		w.Locked++
	}
	w.StrengthPct = w.StrengthPct.add(r.StrengthPct)
	w.QualityPct = w.QualityPct.add(r.QualityPct)
	w.SymbolPct = w.SymbolPct.add(r.SymbolPct)
}

// Limits of the history kept per multiplex.
const (
	// MaxWindows is how many windows a multiplex keeps.
	MaxWindows = 24
	// windowSpan is the longest a window grows; windowGap the longest
	// pause between readings within one.
	windowSpan = 30 * time.Minute
	windowGap  = 2 * time.Minute
)

// Mux is what was measured on one multiplex.
type Mux struct {
	FrequencyHz int64    `json:"frequencyHz"`
	RF          int      `json:"rf"`
	Latest      *Reading `json:"latest,omitempty"`
	Windows     []Window `json:"windows,omitempty"`
}

func (m *Mux) add(r Reading) {
	m.Latest = &r
	if n := len(m.Windows); n > 0 {
		w := &m.Windows[n-1]
		if w.Source == r.Source && r.At.Sub(w.To) <= windowGap && r.At.Sub(w.From) < windowSpan && !r.At.Before(w.To) {
			w.add(r)
			return
		}
	}
	w := Window{From: r.At, Source: r.Source}
	w.add(r)
	m.Windows = append(m.Windows, w)
	if len(m.Windows) > MaxWindows {
		m.Windows = slices.Delete(m.Windows, 0, len(m.Windows)-MaxWindows)
	}
}

// Range is a quantity's spread for the app: lowest, mean and highest.
type Range struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
}

func (s *Stat) rng() *Range {
	if s == nil || s.N == 0 {
		return nil
	}
	return &Range{Min: s.Min, Avg: round1(s.Sum / float64(s.N)), Max: s.Max}
}

// Period summarizes readings over a span: all the history a multiplex
// keeps, or one window of it.
type Period struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Source  string    `json:"source,omitempty"` // set on a window
	Samples int       `json:"samples"`
	// LockedPct is the share of readings with a lock.
	LockedPct   float64 `json:"lockedPct"`
	StrengthPct *Range  `json:"strengthPct"`
	QualityPct  *Range  `json:"qualityPct"`
	SymbolPct   *Range  `json:"symbolPct"`
}

func period(w Window) Period {
	p := Period{From: w.From, To: w.To, Source: w.Source, Samples: w.Samples,
		StrengthPct: w.StrengthPct.rng(), QualityPct: w.QualityPct.rng(), SymbolPct: w.SymbolPct.rng()}
	if w.Samples > 0 {
		p.LockedPct = round1(float64(w.Locked) * 100 / float64(w.Samples))
	}
	return p
}

// History is a multiplex's kept readings for the app: their summary and
// each window, oldest first.
type History struct {
	Period
	Windows []Period `json:"windows"`
}

// Recent summarizes the latest window of readings (the last sweep of the
// multiplex, or the last stretch of watching it), steadier than any one
// reading; nil when there are none.
func (m *Mux) Recent() *Period {
	if len(m.Windows) == 0 {
		return nil
	}
	p := period(m.Windows[len(m.Windows)-1])
	return &p
}

// History summarizes the multiplex's readings; nil when there are none.
func (m *Mux) History() *History {
	if len(m.Windows) == 0 {
		return nil
	}
	var all Window
	h := &History{Windows: make([]Period, 0, len(m.Windows))}
	for i, w := range m.Windows {
		if i == 0 {
			all.From = w.From
		}
		if w.To.After(all.To) {
			all.To = w.To
		}
		all.Samples += w.Samples
		all.Locked += w.Locked
		all.StrengthPct = merge(all.StrengthPct, w.StrengthPct)
		all.QualityPct = merge(all.QualityPct, w.QualityPct)
		all.SymbolPct = merge(all.SymbolPct, w.SymbolPct)
		h.Windows = append(h.Windows, period(w))
	}
	h.Period = period(all)
	return h
}

// Store keeps measurements by multiplex frequency, and saves them to a
// file so they outlive restarts.
type Store struct {
	path string

	mu    sync.Mutex
	muxes map[int64]*Mux
	dirty bool
	saved time.Time
}

// file is the saved form.
type file struct {
	Version int    `json:"version"`
	Muxes   []*Mux `json:"muxes"`
}

// Open loads the measurements saved at path, if any. An empty path keeps
// them in memory only. A file that can't be read is set aside (as
// path.bad) and the store starts empty.
func Open(path string) (*Store, error) {
	s := &Store{path: path, muxes: map[int64]*Mux{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		_ = os.Rename(path, path+".bad")
		return s, fmt.Errorf("signal measurements in %s unreadable, set aside: %w", path, err)
	}
	for _, m := range f.Muxes {
		if m != nil && m.FrequencyHz > 0 {
			s.muxes[m.FrequencyHz] = m
		}
	}
	return s, nil
}

func (s *Store) mux(freq int64, rf int) *Mux {
	m := s.muxes[freq]
	if m == nil {
		m = &Mux{FrequencyHz: freq}
		s.muxes[freq] = m
	}
	if rf > 0 {
		m.RF = rf
	}
	return m
}

// Record adds a reading of the multiplex at freq (Hz), RF channel rf.
func (s *Store) Record(freq int64, rf int, r Reading) {
	if freq <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mux(freq, rf).add(r)
	s.dirty = true
}

// Get returns a copy of what was measured at freq; ok is false when
// nothing was.
func (s *Store) Get(freq int64) (Mux, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.muxes[freq]
	if m == nil {
		return Mux{}, false
	}
	return clone(m), true
}

// All returns copies of every multiplex's measurements, by frequency.
func (s *Store) All() []Mux {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Mux, 0, len(s.muxes))
	for _, m := range s.muxes {
		out = append(out, clone(m))
	}
	slices.SortFunc(out, func(a, b Mux) int { return cmp.Compare(a.FrequencyHz, b.FrequencyHz) })
	return out
}

func clone(m *Mux) Mux {
	c := *m
	if m.Latest != nil {
		r := *m.Latest
		c.Latest = &r
	}
	c.Windows = make([]Window, len(m.Windows))
	for i, w := range m.Windows {
		c.Windows[i] = w
		for _, p := range []**Stat{&c.Windows[i].StrengthPct, &c.Windows[i].QualityPct, &c.Windows[i].SymbolPct} {
			if *p != nil {
				st := **p
				*p = &st
			}
		}
	}
	return c
}

// Save writes the measurements when they changed since the last save.
func (s *Store) Save() error {
	s.mu.Lock()
	if !s.dirty || s.path == "" {
		s.mu.Unlock()
		return nil
	}
	f := file{Version: 1}
	for _, m := range s.muxes {
		c := clone(m)
		f.Muxes = append(f.Muxes, &c)
	}
	s.dirty = false
	s.saved = time.Now()
	s.mu.Unlock()
	slices.SortFunc(f.Muxes, func(a, b *Mux) int { return cmp.Compare(a.FrequencyHz, b.FrequencyHz) })
	raw, err := json.MarshalIndent(f, "", " ")
	if err == nil {
		err = writeAtomic(s.path, raw)
	}
	if err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
	return err
}

// SaveEvery saves when there are changes and the last save was at least
// every ago, so frequent readings don't mean frequent writes.
func (s *Store) SaveEvery(every time.Duration) error {
	s.mu.Lock()
	due := s.dirty && time.Since(s.saved) >= every
	s.mu.Unlock()
	if !due {
		return nil
	}
	return s.Save()
}

func writeAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".signal-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Frequency is the center frequency (Hz) of US broadcast RF channel rf,
// 0 for one that isn't.
func Frequency(rf int) int64 {
	switch {
	case rf >= 2 && rf <= 4:
		return int64(57+6*(rf-2)) * 1e6
	case rf >= 5 && rf <= 6:
		return int64(79+6*(rf-5)) * 1e6
	case rf >= 7 && rf <= 13:
		return int64(177+6*(rf-7)) * 1e6
	case rf >= 14 && rf <= 36:
		return int64(473+6*(rf-14)) * 1e6
	}
	return 0
}

// RF is the US broadcast RF channel centered on freq (Hz), 0 when freq
// isn't one.
func RF(freq int64) int {
	mhz := float64(freq) / 1e6
	switch {
	case mhz > 54 && mhz < 72:
		return 2 + int(math.Round((mhz-57)/6))
	case mhz > 76 && mhz < 88:
		return 5 + int(math.Round((mhz-79)/6))
	case mhz > 174 && mhz < 216:
		return 7 + int(math.Round((mhz-177)/6))
	case mhz > 470 && mhz < 698:
		return 14 + int(math.Round((mhz-473)/6))
	}
	return 0
}
