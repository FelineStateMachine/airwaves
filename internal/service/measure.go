package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	"airwaves/internal/fcc"
	"airwaves/internal/hdhr"
	"airwaves/internal/lineup"
	"airwaves/internal/signal"
	"airwaves/internal/tuner"
)

// measureTiming holds the measuring's pauses; tests shorten them.
type measureTiming struct {
	// sample is how often the tuners are read while in use.
	sample time.Duration
	// acquire is how long the sweep gives a tuner to lock, settle how
	// long it waits after a lock before reading, and interval the time
	// between its readings, readings of them (one without a lock).
	acquire, settle, interval time.Duration
	readings                  int
	// idleWait is how long the sweep waits for a free tuner before it
	// gives up.
	idleWait time.Duration
	// poll is how often the tuner is read while a channel starts or the
	// sweep waits, and noLock how long a channel may take to start.
	poll, noLock time.Duration
}

var defaultTiming = measureTiming{
	sample: 10 * time.Second,
	// An HDHomeRun locks to a good signal in a second or two, to a weak
	// one in three or four; its quality reads 0 for its first second on a
	// multiplex. So about 5 s for an RF channel with a lock, 7 s for one
	// without.
	acquire: 6 * time.Second, settle: time.Second, interval: time.Second, readings: 3,
	idleWait: 30 * time.Second,
	poll:     500 * time.Millisecond, noLock: 10 * time.Second,
}

// SweepStatus is how Measure now is going, or went.
type SweepStatus struct {
	Running    bool       `json:"running"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	// Total is how many RF channels it measures, Done how many it has.
	// The first Found of them are those the tuner's scan found channels
	// on, measured first; the rest are those a licensed station nearby
	// uses.
	Total int `json:"total"`
	Found int `json:"found"`
	Done  int `json:"done"`
	// RF is the RF channel being measured now.
	RF int `json:"rf,omitempty"`
	// Note says why it stopped early or what it left out.
	Note string `json:"note,omitempty"`
}

// ---------- tuners in use ----------

// sampleTuners records a reading of every RF channel a tuner has been on
// since the last call, for whoever tuned it, with the errors in what
// arrived meanwhile. A tuning just started isn't read yet, as it may not
// have locked; the sweep reads its own.
func (s *Service) sampleTuners(ctx context.Context) {
	tuners, err := s.tuners.Tuners(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	prev, prevAt := s.lastSample, s.lastSampleAt
	sweeping := s.sweepFreq
	s.mu.Unlock()
	now := time.Now()
	seen := map[int]tuner.State{}
	for _, t := range tuners {
		if t.FrequencyHz == 0 || t.Elsewhere {
			continue
		}
		seen[t.Number] = t
		was, stable := prev[t.Number]
		if !stable || was.FrequencyHz != t.FrequencyHz || !was.Since.Equal(t.Since) || t.FrequencyHz == sweeping {
			continue
		}
		r := signal.FromTuner(t, now, signal.Active)
		r.SetErrors(was, t, now.Sub(prevAt).Seconds())
		s.signals.Record(t.FrequencyHz, signal.RF(t.FrequencyHz), r)
	}
	s.mu.Lock()
	s.lastSample, s.lastSampleAt = seen, now
	s.mu.Unlock()
}

// sample reads the tuners every timing.sample until ctx ends, saving the
// measurements every minute.
func (s *Service) sample(ctx context.Context) {
	tick := time.NewTicker(s.timing.sample)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := s.signals.Save(); err != nil {
				log.Printf("signal measurements: %v", err)
			}
			return
		case <-tick.C:
		}
		s.sampleTuners(ctx)
		if err := s.signals.SaveEvery(time.Minute); err != nil {
			log.Printf("signal measurements: %v", err)
		}
	}
}

// ---------- Measure now ----------

// errTakenOver ends a measurement whose tuner someone needed.
var errTakenOver = errors.New("a viewer or a recording needed the tuner")

// Measure starts a sweep that measures every RF channel worth measuring,
// one at a time, on a free tuner: those the scan found channels on, and
// those a licensed station nearby uses. It never takes a tuner that's in
// use, and stops as soon as a viewer or a recording needs the one it
// uses. It returns at once; Signal reports progress and the readings.
func (s *Service) Measure(ctx context.Context) (*SweepStatus, error) {
	if s.opt.NoAntenna {
		return nil, ErrNoAntenna
	}
	if s.device() == nil {
		return nil, ErrNoTuner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sweep.Running {
		st := s.sweep
		return &st, nil
	}
	now := time.Now().UTC().Truncate(time.Second)
	s.sweep = SweepStatus{Running: true, StartedAt: &now}
	st := s.sweep
	go s.runSweep(s.ctx)
	return &st, nil
}

func (s *Service) sweepStatus() SweepStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweep
}

func (s *Service) setSweep(f func(*SweepStatus)) {
	s.mu.Lock()
	f(&s.sweep)
	s.mu.Unlock()
}

// sweepTargets lists the RF channels worth measuring, by frequency: first
// those the tuner's scan found channels on, then those a licensed station
// within the report's radius broadcasts on (nearest station first); found
// is how many are of the first kind.
func (s *Service) sweepTargets(ctx context.Context) (freqs []int64, found int, err error) {
	s.mu.Lock()
	s.lineupAt = time.Time{}
	rep := s.report
	s.mu.Unlock()
	l, err := s.tunerLineup(ctx)
	if err != nil {
		return nil, 0, err
	}
	have := map[int64]bool{}
	for _, c := range l {
		if !have[c.FrequencyHz] {
			have[c.FrequencyHz] = true
			freqs = append(freqs, c.FrequencyHz)
		}
	}
	slices.Sort(freqs)
	found = len(freqs)
	nearest := map[int64]float64{} // km to the nearest licensed station, by frequency
	if rep != nil {
		for _, st := range rep.Stations {
			f := signal.Frequency(st.RFChannel)
			if d, ok := nearest[f]; f > 0 && !have[f] && (!ok || st.DistanceKm < d) {
				nearest[f] = st.DistanceKm
			}
		}
	}
	licensed := slices.Collect(func(yield func(int64) bool) {
		for f := range nearest {
			if !yield(f) {
				return
			}
		}
	})
	slices.SortFunc(licensed, func(a, b int64) int { return cmp.Or(cmp.Compare(nearest[a], nearest[b]), cmp.Compare(a, b)) })
	return append(freqs, licensed...), found, nil
}

func (s *Service) runSweep(ctx context.Context) {
	note := ""
	defer func() {
		if err := s.signals.Save(); err != nil {
			log.Printf("signal measurements: %v", err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		s.mu.Lock()
		s.sweepFreq = 0
		s.sweep.Running, s.sweep.FinishedAt, s.sweep.RF, s.sweep.Note = false, &now, 0, note
		s.mu.Unlock()
		log.Printf("measuring done: %+v", s.sweepStatus())
	}()
	freqs, found, err := s.sweepTargets(ctx)
	if err != nil {
		note = "the tuner's lineup can't be read: " + err.Error()
		return
	}
	s.setSweep(func(st *SweepStatus) { st.Total, st.Found = len(freqs), found })
	log.Printf("measuring %d RF channels, the %d found first", len(freqs), found)
	for _, f := range freqs {
		if ctx.Err() != nil {
			note = "stopped: the server is shutting down"
			return
		}
		rf := signal.RF(f)
		s.setSweep(func(st *SweepStatus) { st.RF = rf })
		err := s.measureRF(ctx, f)
		var busy *tuner.BusyError
		switch {
		case errors.As(err, &busy):
			note = fmt.Sprintf("stopped at RF %d: no tuner was free (they're left to viewers and recordings)", rf)
			return
		case errors.Is(err, errTakenOver):
			note = fmt.Sprintf("stopped at RF %d: %v", rf, err)
			return
		case err != nil:
			log.Printf("measure RF %d: %v", rf, err)
		}
		s.setSweep(func(st *SweepStatus) { st.Done++ })
	}
}

// measureRF records timing.readings readings of the RF channel at freq:
// with a lock, or without one, with whatever strength the tuner measures.
// A tuner already on it is read where it is; otherwise it waits up to
// timing.idleWait for a free one, which any viewer or recording takes
// back.
func (s *Service) measureRF(ctx context.Context, freq int64) error {
	rf := signal.RF(freq)
	if t, ok := s.tunerOn(ctx, freq); ok {
		if r := signal.FromTuner(t, time.Now(), signal.Sweep); r.Lock {
			s.signals.Record(freq, rf, r)
		}
		return nil
	}
	var sub *tuner.Sub
	deadline := time.Now().Add(s.timing.idleWait)
	for {
		var err error
		sub, err = s.tuners.Open(ctx, tuner.Request{FrequencyHz: freq, Priority: tuner.Measuring, Who: fmt.Sprintf("RF %d", rf)})
		var busy *tuner.BusyError
		if err == nil || !errors.As(err, &busy) || time.Now().After(deadline) {
			if err != nil {
				return err
			}
			break
		}
		if err := sleep(ctx, s.timing.poll); err != nil {
			return err
		}
	}
	defer sub.Close()
	s.mu.Lock()
	s.sweepFreq = freq
	s.mu.Unlock()

	// Give the tuner time to lock: a weak signal can take seconds. What it
	// measures meanwhile, past its first second on the RF channel, is the
	// reading when it doesn't lock.
	read := func() (tuner.State, error) {
		select {
		case <-sub.Done():
			return tuner.State{}, errTakenOver
		default:
		}
		tuners, err := s.tuners.Tuners(ctx)
		if err != nil {
			return tuner.State{}, err
		}
		return tuners[sub.Tuner()], nil
	}
	var tried *signal.Reading
	start := time.Now()
	for !sub.Locked() && time.Since(start) < s.timing.acquire {
		if err := sleep(ctx, s.timing.poll); err != nil {
			return err
		}
		t, err := read()
		if err != nil {
			return err
		}
		if r := signal.FromTuner(t, time.Now(), signal.Sweep); time.Since(start) >= s.timing.settle {
			tried = &r
		}
	}
	if !sub.Locked() {
		if tried == nil {
			return fmt.Errorf("RF %d: no reading", rf)
		}
		s.signals.Record(freq, rf, *tried)
		return nil
	}
	if err := sleep(ctx, s.timing.settle); err != nil {
		return err
	}
	var prev *tuner.State
	var prevAt time.Time
	for i := range s.timing.readings {
		if i > 0 {
			if err := sleep(ctx, s.timing.interval); err != nil {
				return err
			}
		}
		t, err := read()
		if err != nil {
			return err
		}
		now := time.Now()
		r := signal.FromTuner(t, now, signal.Sweep)
		if prev != nil {
			r.SetErrors(*prev, t, now.Sub(prevAt).Seconds())
		}
		prev, prevAt = &t, now
		s.signals.Record(freq, rf, r)
	}
	return nil
}

// tunerOn finds a tuner of ours already on the RF channel at freq.
func (s *Service) tunerOn(ctx context.Context, freq int64) (tuner.State, bool) {
	tuners, err := s.tuners.Tuners(ctx)
	if err != nil {
		return tuner.State{}, false
	}
	for _, t := range tuners {
		if t.FrequencyHz == freq && !t.Elsewhere {
			return t, true
		}
	}
	return tuner.State{}, false
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---------- tuning ----------

// NoSignalError is a channel that didn't start because its RF channel had
// no lock.
type NoSignalError struct {
	Channel string
	RF      int
	Label   string // "KTVD 20.x"
	Reading *signal.Reading
}

func (e *NoSignalError) Error() string {
	msg := fmt.Sprintf("No signal on RF %d", e.RF)
	if e.Label != "" {
		msg += " (" + e.Label + ")"
	}
	msg += " right now"
	if r := e.Reading; r != nil {
		var parts []string
		if r.StrengthPct != nil {
			parts = append(parts, fmt.Sprintf("strength %.0f%%", *r.StrengthPct))
		}
		if r.QualityPct != nil {
			parts = append(parts, fmt.Sprintf("quality %.0f%%", *r.QualityPct))
		}
		if len(parts) > 0 {
			msg += ": the tuner measured " + strings.Join(parts, ", ") + " and couldn't lock"
		}
	}
	return msg
}

// rfLabel names what a channel's RF channel carries: "KTVD 20.x", or
// "KUSA 9.x on KTVD" when another station's transmitter carries it.
func rfLabel(c lineup.TunerChannel) string {
	call := c.BaseCall
	if call == "" {
		call = fcc.BaseCall(c.Transmitter)
	}
	label := strings.TrimSpace(fmt.Sprintf("%s %d.x", call, c.Major))
	if c.Via != "" {
		label += " on " + fcc.BaseCall(c.Via)
	}
	return label
}

// tuneAntenna starts an antenna channel for client: a tuner (client's own
// last one, when every tuner is busy), its program, and start, which
// reads it. A channel that won't lock fails with what was measured.
func (s *Service) tuneAntenna(ctx context.Context, client, number string, start func(context.Context, tuner.Input) error) error {
	freq, program, err := s.tuning(ctx, number)
	if err != nil {
		return err
	}
	sub, err := s.tuners.Open(ctx, tuner.Request{FrequencyHz: freq, Program: program, Priority: tuner.Watching, Who: number, Owner: client})
	var busy *tuner.BusyError
	if errors.As(err, &busy) {
		return fmt.Errorf("every tuner is in use right now (%s), so %s can't be tuned", strings.Join(busy.Users, "; "), number)
	}
	if err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, s.timing.noLock)
	p, err := sub.Wait(wait)
	cancel()
	if err != nil {
		defer sub.Close()
		return s.tuneFailed(ctx, sub, number, freq, err)
	}
	audio, note := tuner.AudioTracks(p)
	in := tuner.Input{
		Args:  []string{"-fflags", "+genpts", "-f", "mpegts", "-i", "pipe:0"},
		Video: "0:v:0", Audio: audio, Note: note, Broadcast: true,
		Source: func(ctx context.Context, w io.Writer) error {
			defer sub.Close()
			return sub.Copy(ctx, w)
		},
	}
	if err := start(ctx, in); err != nil {
		sub.Close()
		return err
	}
	return nil
}

// tuneFailed says why a channel didn't start, with what the tuner measured
// of its RF channel.
func (s *Service) tuneFailed(ctx context.Context, sub *tuner.Sub, number string, freq int64, err error) error {
	rf := signal.RF(freq)
	var reading *signal.Reading
	if tuners, terr := s.tuners.Tuners(ctx); terr == nil && sub.Tuner() < len(tuners) {
		if t := tuners[sub.Tuner()]; t.FrequencyHz == freq && t.Device.StrengthPct != nil {
			r := signal.FromTuner(t, time.Now(), signal.Active)
			reading = &r
			s.signals.Record(freq, rf, r)
		}
	}
	var he *hdhr.Error
	switch {
	case errors.As(err, &he) && he.Code == 803:
		return errors.New("the tuner is scanning for channels; try again in a few minutes")
	case errors.As(err, &he) && he.Busy():
		return fmt.Errorf("the tuner is busy with something else, so %s can't be tuned", number)
	case errors.Is(err, tuner.ErrNoProgram):
		return fmt.Errorf("RF %d no longer carries %s: the tuner needs to scan for channels again", rf, number)
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil && !sub.Locked():
		label := ""
		s.mu.Lock()
		rep, g := s.report, s.guide
		s.mu.Unlock()
		if chans, _, merr := s.matched(ctx, rep, g); merr == nil {
			for _, c := range chans {
				if c.Number == number {
					label = rfLabel(c)
				}
			}
		}
		return &NoSignalError{Channel: number, RF: rf, Label: label, Reading: reading}
	}
	return fmt.Errorf("%s (RF %d) didn't start: %w", number, rf, err)
}
