// Package signal keeps what the tuners measured on each multiplex (each RF
// channel): Tvheadend's scan results, and readings of signal strength and
// quality taken while a tuner was on it. Nothing here is estimated: a
// value is there because a tuner reported it, and is null otherwise.
//
// Readings come from Tvheadend's input status. An HDHomeRun reports
// strength and signal quality (its SNQ) as percentages, so their dB fields
// stay null; tuners that report decibels fill those in instead.
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

	"airwaves/internal/tvh"
)

// Sources of readings.
const (
	// Active readings are taken while something had a tuner on the
	// multiplex: a viewer, a recording, or Tvheadend's own guide scan.
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
	// StrengthPct is signal strength on the tuner's relative scale, 0 to
	// 100; StrengthDBm is set instead by tuners that report dBm.
	StrengthPct *float64 `json:"strengthPct"`
	StrengthDBm *float64 `json:"strengthDbm"`
	// QualityPct is the signal-to-noise quality on the tuner's relative
	// scale (an HDHomeRun's SNQ); SNRdB is set instead by tuners that
	// report the ratio in dB.
	QualityPct *float64 `json:"qualityPct"`
	SNRdB      *float64 `json:"snrDb"`
	// BER is the bit error ratio, and UNC the uncorrected blocks, from
	// tuners that count them.
	BER *float64 `json:"ber"`
	UNC *int64   `json:"unc"`
	// ErrorsPerSec is the rate of transport and continuity errors in what
	// arrived since the reading before, on the same tuning: a picture
	// breaking up. Null for a first reading.
	ErrorsPerSec *float64 `json:"errorsPerSec"`
}

// FromInput reads an input's status as Tvheadend reports it, with the
// subscriptions now. Whether the tuner holds a lock is first what its
// subscriptions say: "Running" once data has arrived, "Testing" while none
// has. The input's data rate says less: it reads 0 now and then on an
// input with little to send, and can show the last tuning's for a moment;
// it decides only for an input with no subscription listed. Then a tuner
// that reports quality on the relative scale (an HDHomeRun) has none, 0,
// exactly while its demodulator has no lock (its own status page reads
// "Modulation Lock none, Signal Quality none" then), even while a
// subscription that had data still reads "Running".
func FromInput(in tvh.InputStatus, subs []tvh.Subscription, at time.Time, source string) Reading {
	r := Reading{At: at.UTC().Truncate(time.Second), Source: source, Lock: in.BPS > 0}
	if mux, net, ok := in.Mux(); ok {
		listed, running := false, false
		for _, sub := range subs {
			if sub.On(in.Input, net, mux) {
				listed, running = true, running || sub.Running()
			}
		}
		if listed {
			r.Lock = running
		}
	}
	if in.SNRScale == tvh.ScaleRelative && in.SNR == 0 {
		r.Lock = false
	}
	switch in.SignalScale {
	case tvh.ScaleRelative:
		r.StrengthPct = ptr(round1(float64(in.Signal) * 100 / 65535))
	case tvh.ScaleDecibel:
		r.StrengthDBm = ptr(round1(float64(in.Signal) / 1000))
	}
	switch in.SNRScale {
	case tvh.ScaleRelative:
		r.QualityPct = ptr(round1(float64(in.SNR) * 100 / 65535))
	case tvh.ScaleDecibel:
		r.SNRdB = ptr(round1(float64(in.SNR) / 1000))
	}
	// A driver's own BER and UNC counters read 0 when it has none, so only
	// a count Tvheadend made, or one above zero, says anything.
	if in.TCBit > 0 {
		r.BER = ptr(float64(in.ECBit) / float64(in.TCBit))
	}
	switch {
	case in.TCBlock > 0:
		r.UNC = ptr(in.ECBlock)
	case in.UNC > 0:
		r.UNC = ptr(in.UNC)
	}
	return r
}

// SetErrors sets the error rate from the input's status at an earlier
// reading of the same tuning, secs before.
func (r *Reading) SetErrors(prev, now tvh.InputStatus, secs float64) {
	r.ErrorsPerSec = nil
	was, is := prev.TE+prev.CC, now.TE+now.CC
	if secs <= 0 || is < was || prev.Stream != now.Stream || prev.Input != now.Input {
		return
	}
	r.ErrorsPerSec = ptr(round1(float64(is-was) / secs))
}

func ptr[T any](v T) *T { return &v }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Scan is how Tvheadend's last scan of a multiplex went.
type Scan struct {
	// Lock is whether the scan found the multiplex and its services.
	Lock bool `json:"lock"`
	// Partial is set when it locked but missed some of the tables.
	Partial bool `json:"partial,omitempty"`
	// At is when the scan found it, from Tvheadend; for a scan that found
	// nothing, which Tvheadend doesn't date, when Airwaves first saw that.
	At time.Time `json:"at"`
}

// ScanOf turns a multiplex's scan result into a Scan; ok is false while
// it has none. seen is when the result was read.
func ScanOf(m tvh.Mux, seen time.Time) (Scan, bool) {
	switch m.ScanResult {
	case tvh.ScanOK, tvh.ScanPartial:
		at := seen
		if m.ScanLast > 0 {
			at = time.Unix(m.ScanLast, 0)
		}
		return Scan{Lock: true, Partial: m.ScanResult == tvh.ScanPartial, At: at.UTC()}, true
	case tvh.ScanFail:
		return Scan{At: seen.UTC().Truncate(time.Second)}, true
	}
	return Scan{}, false
}

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
	StrengthDBm *Stat     `json:"strengthDbm,omitempty"`
	QualityPct  *Stat     `json:"qualityPct,omitempty"`
	SNRdB       *Stat     `json:"snrDb,omitempty"`
}

func (w *Window) add(r Reading) {
	w.To = r.At
	w.Samples++
	if r.Lock {
		w.Locked++
	}
	w.StrengthPct = w.StrengthPct.add(r.StrengthPct)
	w.StrengthDBm = w.StrengthDBm.add(r.StrengthDBm)
	w.QualityPct = w.QualityPct.add(r.QualityPct)
	w.SNRdB = w.SNRdB.add(r.SNRdB)
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
	Scan        *Scan    `json:"scan,omitempty"`
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
	StrengthDBm *Range  `json:"strengthDbm"`
	QualityPct  *Range  `json:"qualityPct"`
	SNRdB       *Range  `json:"snrDb"`
}

func period(w Window) Period {
	p := Period{From: w.From, To: w.To, Source: w.Source, Samples: w.Samples,
		StrengthPct: w.StrengthPct.rng(), StrengthDBm: w.StrengthDBm.rng(), QualityPct: w.QualityPct.rng(), SNRdB: w.SNRdB.rng()}
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
		all.StrengthDBm = merge(all.StrengthDBm, w.StrengthDBm)
		all.QualityPct = merge(all.QualityPct, w.QualityPct)
		all.SNRdB = merge(all.SNRdB, w.SNRdB)
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

// SetScan notes a scan result. A result that hasn't changed keeps its
// date, so a failed scan stays dated from when it was first seen.
func (s *Store) SetScan(freq int64, rf int, sc Scan) {
	if freq <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.mux(freq, rf)
	if old := m.Scan; old != nil && old.Lock == sc.Lock && old.Partial == sc.Partial && (!sc.Lock || !sc.At.After(old.At)) {
		return
	}
	m.Scan = &sc
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
	if m.Scan != nil {
		sc := *m.Scan
		c.Scan = &sc
	}
	if m.Latest != nil {
		r := *m.Latest
		c.Latest = &r
	}
	c.Windows = make([]Window, len(m.Windows))
	for i, w := range m.Windows {
		c.Windows[i] = w
		for _, p := range []**Stat{&c.Windows[i].StrengthPct, &c.Windows[i].StrengthDBm, &c.Windows[i].QualityPct, &c.Windows[i].SNRdB} {
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
