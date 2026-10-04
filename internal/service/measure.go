package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"airwaves/internal/fcc"
	"airwaves/internal/lineup"
	"airwaves/internal/signal"
	"airwaves/internal/tuner"
	"airwaves/internal/tvh"
)

// sweepWeight is the weight of the sweep's subscriptions: the lowest a
// user's can be in Tvheadend, so any viewer or recording takes the tuner
// over, while Tvheadend's own scans (weights 1 to 7) don't.
const sweepWeight = 10

// measureTiming holds the measuring's pauses; tests shorten them.
type measureTiming struct {
	// sample is how often the inputs are read while tuners are in use.
	sample time.Duration
	// appear is how long the sweep waits for its tuner to show the
	// multiplex, acquire how long it then gives the tuner to lock, settle
	// how long it waits after a lock before reading, and interval the time
	// between its readings, readings of them (one without a lock).
	appear, acquire, settle, interval time.Duration
	readings                          int
	// idleWait is how long the sweep waits for an idle tuner before it
	// gives up.
	idleWait time.Duration
	// poll is how often the tuner is read while a channel starts, and
	// noLock how long it may go without a lock before the start fails.
	poll, noLock time.Duration
}

var defaultTiming = measureTiming{
	sample: 10 * time.Second,
	// An HDHomeRun locks to a good signal in a second or two, to a weak
	// one in three or four; its quality reads 0 for its first second on a
	// multiplex. So about 5 s for an RF channel with a lock, 7 s for one
	// without.
	appear: 5 * time.Second, acquire: 6 * time.Second, settle: time.Second, interval: time.Second, readings: 3,
	idleWait: 30 * time.Second,
	poll:     500 * time.Millisecond, noLock: 10 * time.Second,
}

// SweepStatus is how Measure now is going, or went.
type SweepStatus struct {
	Running    bool       `json:"running"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	// Total is how many RF channels it measures, Done how many it has.
	// The first Found of them are those Tvheadend's scan found, measured
	// first; the rest are those a licensed station nearby uses.
	Total int `json:"total"`
	Found int `json:"found"`
	Done  int `json:"done"`
	// RF is the RF channel being measured now.
	RF int `json:"rf,omitempty"`
	// Note says why it stopped early or what it left out.
	Note string `json:"note,omitempty"`
}

// ---------- inputs ----------

// tunerStatus is what Tvheadend says of its inputs at one moment: every
// input, tuned or idle, and the subscriptions using them.
type tunerStatus struct {
	inputs []tvh.InputStatus
	subs   []tvh.Subscription
}

// busy counts the inputs something subscribes to.
func (t tunerStatus) busy() int {
	seen := map[string]bool{}
	for _, in := range t.inputs {
		if in.InUse() {
			seen[in.Input] = true
		}
	}
	return len(seen)
}

// on finds an input in use on the antenna multiplex named mux.
func (t tunerStatus) on(mux string) *tvh.InputStatus {
	for i := range t.inputs {
		if name, net, ok := t.inputs[i].Mux(); ok && name == mux && net == tvh.ATSCNetwork && t.inputs[i].InUse() {
			return &t.inputs[i]
		}
	}
	return nil
}

// input finds an input by name.
func (t tunerStatus) input(name string) *tvh.InputStatus {
	for i := range t.inputs {
		if t.inputs[i].Input == name {
			return &t.inputs[i]
		}
	}
	return nil
}

// reading is what in measures now.
func (t tunerStatus) reading(in tvh.InputStatus, at time.Time, source string) signal.Reading {
	return signal.FromInput(in, t.subs, at, source)
}

// ownScansOnly reports whether everything using the inputs is Tvheadend's
// own scanning (weights below a user's).
func (t tunerStatus) ownScansOnly() bool {
	own := false
	for _, in := range t.inputs {
		if in.InUse() {
			if in.Weight >= sweepWeight {
				return false
			}
			own = true
		}
	}
	return own
}

// statusNow returns the inputs and subscriptions as read in the last few
// seconds; ok is false when Tvheadend can't be read.
func (s *Service) statusNow() (tunerStatus, bool) {
	if s.opt.Tvheadend == nil {
		return tunerStatus{}, false
	}
	s.mu.Lock()
	st, at := s.status, s.statusAt
	s.mu.Unlock()
	if !at.IsZero() && time.Since(at) < 5*time.Second {
		return st, true
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	st, err := s.readStatus(ctx)
	return st, err == nil
}

// readStatus reads the inputs and subscriptions from Tvheadend, and keeps
// them for statusNow.
func (s *Service) readStatus(ctx context.Context) (tunerStatus, error) {
	ins, err := s.opt.Tvheadend.Inputs(ctx)
	if err != nil {
		return tunerStatus{}, err
	}
	subs, err := s.opt.Tvheadend.Subscriptions(ctx)
	if err != nil {
		return tunerStatus{}, err
	}
	st := tunerStatus{inputs: ins, subs: subs}
	s.mu.Lock()
	s.status, s.statusAt = st, time.Now()
	s.mu.Unlock()
	return st, nil
}

// antennaMuxes maps the antenna network's multiplexes by name.
func (s *Service) antennaMuxes(ctx context.Context) (map[string]tvh.Mux, error) {
	l, err := s.tvhTuner.Lineup(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]tvh.Mux{}
	for _, m := range l.NetworkMuxes(tvh.ATSCNetwork) {
		out[m.Name] = m
	}
	return out, nil
}

// sampleInputs records a reading of every antenna multiplex a tuner has
// been on since the last call, whoever tuned it: a viewer, a recording or
// Tvheadend's guide scan. An input just tuned isn't read yet, as it may
// not have locked; the sweep reads its own.
func (s *Service) sampleInputs(ctx context.Context) {
	st, err := s.readStatus(ctx)
	if err != nil {
		return
	}
	muxes, err := s.antennaMuxes(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	prev, prevAt := s.lastSample, s.lastSampleAt
	sweeping := s.sweepMux
	s.mu.Unlock()
	now := time.Now()
	seen := map[string]tvh.InputStatus{}
	for _, in := range st.inputs {
		name, net, ok := in.Mux()
		if !ok || net != tvh.ATSCNetwork || !in.InUse() {
			continue
		}
		seen[in.Input] = in
		m, ok := muxes[name]
		was, stable := prev[in.Input]
		if !ok || !stable || was.Stream != in.Stream || name == sweeping {
			continue
		}
		r := st.reading(in, now, signal.Active)
		r.SetErrors(was, in, now.Sub(prevAt).Seconds())
		s.signals.Record(m.FrequencyHz, signal.RF(m.FrequencyHz), r)
	}
	s.mu.Lock()
	s.lastSample, s.lastSampleAt = seen, now
	s.mu.Unlock()
}

// syncScans notes Tvheadend's scan results of the antenna multiplexes.
func (s *Service) syncScans(ctx context.Context) {
	if l, err := s.tvhTuner.Lineup(ctx); err == nil {
		s.noteScans(l)
	}
}

// noteScans notes the scan results in l.
func (s *Service) noteScans(l *tvh.Lineup) {
	now := time.Now()
	for _, m := range l.NetworkMuxes(tvh.ATSCNetwork) {
		if sc, ok := signal.ScanOf(m, now); ok && m.FrequencyHz > 0 {
			s.signals.SetScan(m.FrequencyHz, signal.RF(m.FrequencyHz), sc)
		}
	}
}

// sample reads the inputs every timing.sample until ctx ends, saving the
// measurements every minute.
func (s *Service) sample(ctx context.Context) {
	if s.opt.Tvheadend == nil {
		return
	}
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
		s.sampleInputs(ctx)
		if err := s.signals.SaveEvery(time.Minute); err != nil {
			log.Printf("signal measurements: %v", err)
		}
	}
}

// ---------- Measure now ----------

var (
	errTakenOver = errors.New("a viewer or a recording needed the tuner")
	errNotTuned  = errors.New("Tvheadend didn't tune it")
)

// Measure starts a sweep that measures every RF channel worth measuring,
// one at a time, on an idle tuner: those the scan found channels on, and
// those a licensed station nearby uses. It never takes a tuner that's in
// use, and stops as soon as a viewer or a recording needs the one it
// uses. It returns at once; Signal reports progress and the readings.
func (s *Service) Measure(ctx context.Context) (*SweepStatus, error) {
	if s.opt.NoAntenna {
		return nil, ErrNoAntenna
	}
	if s.opt.Tvheadend == nil {
		return nil, errors.New("no tuner: the server has no Tvheadend")
	}
	fes, err := s.frontends(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("Tvheadend is not reachable: %w", err)
	}
	if len(fes) == 0 {
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
	go s.runSweep(s.ctx, len(fes))
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

// sweepTargets lists the antenna multiplexes worth measuring: first those
// Tvheadend's scan found (by RF), then those a licensed station within the
// report's radius broadcasts on (nearest station first); found is how
// many of them are of the first kind. It also names a channel on each
// found one, for servers that won't stream a whole multiplex.
func (s *Service) sweepTargets(ctx context.Context) (muxes []tvh.Mux, found int, channelOn map[string]string, err error) {
	s.tvhTuner.Invalidate()
	l, err := s.tvhTuner.Lineup(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	s.mu.Lock()
	rep := s.report
	s.mu.Unlock()
	nearest := map[int]float64{} // km to the nearest licensed station, by RF
	if rep != nil {
		for _, st := range rep.Stations {
			if d, ok := nearest[st.RFChannel]; !ok || st.DistanceKm < d {
				nearest[st.RFChannel] = st.DistanceKm
			}
		}
	}
	channelOn = map[string]string{}
	for _, c := range l.Channels {
		if _, m, ok := l.Source(c); ok && c.Enabled && channelOn[m.UUID] == "" {
			channelOn[m.UUID] = c.UUID
		}
	}
	var scanned, licensed []tvh.Mux
	for _, m := range l.NetworkMuxes(tvh.ATSCNetwork) {
		if !bool(m.Enabled) || m.FrequencyHz <= 0 {
			continue
		}
		_, near := nearest[signal.RF(m.FrequencyHz)]
		switch {
		case m.NumSvc > 0 || m.ScanResult == tvh.ScanOK || m.ScanResult == tvh.ScanPartial:
			scanned = append(scanned, m)
		case near:
			licensed = append(licensed, m)
		}
	}
	slices.SortFunc(scanned, func(a, b tvh.Mux) int { return cmp.Compare(a.FrequencyHz, b.FrequencyHz) })
	slices.SortFunc(licensed, func(a, b tvh.Mux) int {
		return cmp.Or(cmp.Compare(nearest[signal.RF(a.FrequencyHz)], nearest[signal.RF(b.FrequencyHz)]), cmp.Compare(a.FrequencyHz, b.FrequencyHz))
	})
	return append(scanned, licensed...), len(scanned), channelOn, nil
}

func (s *Service) runSweep(ctx context.Context, tuners int) {
	note := ""
	skipped := 0
	defer func() {
		if skipped > 0 {
			note = strings.TrimPrefix(note+fmt.Sprintf("; %d RF channels skipped: Tvheadend won't stream them", skipped), "; ")
		}
		if err := s.signals.Save(); err != nil {
			log.Printf("signal measurements: %v", err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		s.mu.Lock()
		s.sweepMux = ""
		s.sweep.Running, s.sweep.FinishedAt, s.sweep.RF, s.sweep.Note = false, &now, 0, note
		s.mu.Unlock()
		log.Printf("measuring done: %+v", s.sweepStatus())
	}()
	muxes, found, channelOn, err := s.sweepTargets(ctx)
	if err != nil {
		note = "Tvheadend is not reachable: " + err.Error()
		return
	}
	s.setSweep(func(st *SweepStatus) { st.Total, st.Found = len(muxes), found })
	log.Printf("measuring %d RF channels, the %d found first", len(muxes), found)
	for _, m := range muxes {
		if ctx.Err() != nil {
			note = "stopped: the server is shutting down"
			return
		}
		rf := signal.RF(m.FrequencyHz)
		s.setSweep(func(st *SweepStatus) { st.RF = rf })
		st, err := s.readStatus(ctx)
		if on := st.on(m.Name); err == nil && on != nil {
			// A tuner is on it already (a viewer, a recording): read that,
			// once it has locked, rather than take another.
			if r := st.reading(*on, time.Now(), signal.Sweep); r.Lock {
				s.signals.Record(m.FrequencyHz, rf, r)
			}
			s.setSweep(func(st *SweepStatus) { st.Done++ })
			continue
		}
		if st, ok := s.waitIdle(ctx, tuners); !ok {
			note = fmt.Sprintf("stopped at RF %d: %s", rf, noTunerFree(st))
			return
		}
		err = s.measureMux(ctx, m, channelOn[m.UUID])
		var se *tvh.StreamError
		switch {
		case errors.Is(err, errTakenOver):
			note = fmt.Sprintf("stopped at RF %d: %v", rf, err)
			return
		case errors.As(err, &se) && se.Status == http.StatusServiceUnavailable:
			note = fmt.Sprintf("stopped at RF %d: %s", rf, noTunerFree(tunerStatus{}))
			return
		case errors.As(err, &se):
			skipped++
		case err != nil:
			log.Printf("measure RF %d: %v", rf, err)
		}
		s.setSweep(func(st *SweepStatus) { st.Done++ })
	}
}

// waitIdle waits up to timing.idleWait for a tuner nobody subscribes to,
// and returns the status then; ok is false when none came free.
func (s *Service) waitIdle(ctx context.Context, tuners int) (tunerStatus, bool) {
	deadline := time.Now().Add(s.timing.idleWait)
	for {
		st, err := s.readStatus(ctx)
		if err == nil && st.busy() < tuners {
			return st, true
		}
		if time.Now().After(deadline) || sleep(ctx, s.timing.poll) != nil {
			return st, false
		}
	}
}

// noTunerFree says why the sweep found no tuner: in use by viewers or
// recordings, which it leaves alone, or by Tvheadend's own guide or
// channel scans (weights below a user's), which end on their own.
func noTunerFree(st tunerStatus) string {
	if st.ownScansOnly() {
		return "every tuner is busy with Tvheadend's own guide or channel scan; try again in a few minutes"
	}
	return "no tuner was free (they're left to viewers and recordings)"
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

// measureMux subscribes to a multiplex at the sweep's weight, waits for a
// tuner to take it, and records timing.readings readings of it: with a
// lock, or without one, with whatever strength the tuner measures. A
// subscription Tvheadend refuses (a server that won't stream whole
// multiplexes) is retried on channelUUID, a channel of the multiplex,
// when it has one.
func (s *Service) measureMux(ctx context.Context, m tvh.Mux, channelUUID string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.sweepMux = m.Name
	s.mu.Unlock()
	rf := signal.RF(m.FrequencyHz)

	// Tvheadend answers a multiplex it can't lock with no headers at all,
	// so this waits on its own while the tuner is read; what comes is
	// drained, so the stream keeps flowing.
	opened := make(chan error, 1)
	go func() {
		body, err := s.opt.Tvheadend.OpenMux(ctx, m.UUID, sweepWeight)
		var se *tvh.StreamError
		if errors.As(err, &se) && se.Status != http.StatusServiceUnavailable && channelUUID != "" {
			body, err = s.opt.Tvheadend.OpenChannel(ctx, channelUUID, sweepWeight)
		}
		opened <- err
		if err != nil {
			return
		}
		defer body.Close()
		_, _ = io.Copy(io.Discard, body)
	}()

	// Wait for a tuner to show the multiplex.
	var input string
	deadline := time.Now().Add(s.timing.appear)
	for input == "" {
		select {
		case err := <-opened:
			if err != nil {
				return err
			}
		default:
		}
		if st, err := s.readStatus(ctx); err == nil {
			if on := st.on(m.Name); on != nil {
				input = on.Input
				break
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("RF %d: %w", rf, errNotTuned)
		}
		if err := sleep(ctx, s.timing.poll); err != nil {
			return err
		}
	}
	// Give the tuner time to lock: a weak signal can take seconds. What it
	// measures meanwhile, past its first second on the multiplex (which
	// reads what it measured before, or nothing), is the reading when it
	// doesn't lock.
	locked := false
	var tried *signal.Reading
	start := time.Now()
	for !locked && time.Since(start) < s.timing.acquire {
		if err := sleep(ctx, s.timing.poll); err != nil {
			return err
		}
		st, err := s.readStatus(ctx)
		if err != nil {
			continue
		}
		on := st.on(m.Name)
		if on == nil {
			if in := st.input(input); in != nil && in.InUse() {
				return errTakenOver
			}
			break // Tvheadend gave up on it
		}
		input = on.Input
		r := st.reading(*on, time.Now(), signal.Sweep)
		locked = r.Lock
		if !locked && time.Since(start) >= s.timing.settle {
			tried = &r
		}
	}
	if !locked {
		if tried == nil {
			return fmt.Errorf("RF %d: the tuner left it before a reading", rf)
		}
		s.signals.Record(m.FrequencyHz, rf, *tried)
		return nil
	}
	if err := sleep(ctx, s.timing.settle); err != nil {
		return err
	}
	readings := s.timing.readings
	var prev *tvh.InputStatus
	var prevAt time.Time
	for i := range readings {
		if i > 0 {
			if err := sleep(ctx, s.timing.interval); err != nil {
				return err
			}
		}
		st, err := s.readStatus(ctx)
		if err != nil {
			return err
		}
		on := st.on(m.Name)
		if on == nil {
			if in := st.input(input); in != nil && in.InUse() {
				// Its tuner is on another multiplex now: someone else's
				// subscription took it over.
				return errTakenOver
			}
			// Tvheadend let the multiplex go (or is between
			// subscriptions): there's nothing to read, and nothing is
			// made up.
			return fmt.Errorf("RF %d: the tuner left it after %d readings", rf, i)
		}
		// Tvheadend may move a subscription that can't lock to another
		// tuner; follow it there.
		input = on.Input
		now := time.Now()
		r := st.reading(*on, now, signal.Sweep)
		if prev != nil {
			r.SetErrors(*prev, *on, now.Sub(prevAt).Seconds())
		}
		prev, prevAt = on, now
		s.signals.Record(m.FrequencyHz, rf, r)
	}
	return nil
}

// ---------- tuning ----------

// tuneWatch follows the tuner while a channel starts.
type tuneWatch struct {
	done chan struct{}

	mu sync.Mutex
	// reading is the latest of the channel's multiplex, taken at last; a
	// tuner has been on it since since.
	reading     *signal.Reading
	since, last time.Time
	// busy is set when every tuner was in use by others while it waited.
	busy bool
}

// errNoLock cancels a start that has had no lock for timing.noLock.
var errNoLock = errors.New("no lock")

// watchTune reads the tuner every timing.poll while ctx lasts, and
// cancels it when the tuner has been on mux without a lock for
// timing.noLock.
func (s *Service) watchTune(ctx context.Context, cancel context.CancelCauseFunc, mux tvh.Mux, tuners int) *tuneWatch {
	w := &tuneWatch{done: make(chan struct{})}
	go func() {
		defer close(w.done)
		var unlocked time.Time
		for {
			if sleep(ctx, s.timing.poll) != nil {
				return
			}
			st, err := s.readStatus(ctx)
			if err != nil {
				continue
			}
			on := st.on(mux.Name)
			w.mu.Lock()
			if on == nil {
				w.busy = w.busy || tuners > 0 && st.busy() >= tuners
				w.mu.Unlock()
				continue
			}
			now := time.Now()
			if w.since.IsZero() {
				w.since = now
			}
			r := st.reading(*on, now, signal.Active)
			w.reading, w.last = &r, now
			w.mu.Unlock()
			switch {
			case r.Lock:
				unlocked = time.Time{}
			case unlocked.IsZero():
				unlocked = now
			case now.Sub(unlocked) >= s.timing.noLock:
				cancel(errNoLock)
				return
			}
		}
	}()
	return w
}

// result waits for the watch to end and returns what it saw.
func (w *tuneWatch) result() (r *signal.Reading, on time.Duration, busy bool) {
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reading != nil {
		on = w.last.Sub(w.since)
	}
	return w.reading, on, w.busy
}

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
		if r.StrengthDBm != nil {
			parts = append(parts, fmt.Sprintf("strength %.1f dBm", *r.StrengthDBm))
		}
		if r.QualityPct != nil {
			parts = append(parts, fmt.Sprintf("quality %.0f%%", *r.QualityPct))
		}
		if r.SNRdB != nil {
			parts = append(parts, fmt.Sprintf("SNR %.1f dB", *r.SNRdB))
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

// tuneAntenna starts an antenna channel, watching the tuner meanwhile so
// a channel that won't lock fails with what was measured.
func (s *Service) tuneAntenna(ctx context.Context, client, number string, start func(context.Context, tuner.Input) error) error {
	ch, err := s.tvhTuner.Channel(ctx, number)
	if errors.Is(err, tuner.ErrNotFound) && len(s.offeredNetworks()) == 0 {
		return ErrNoTuner
	}
	if err != nil {
		return err
	}
	in, err := s.tvhTuner.Input(ctx, number)
	if err != nil {
		return err
	}
	fes, _ := s.frontends(ctx, false)
	tctx, cancel := context.WithCancelCause(ctx)
	w := s.watchTune(tctx, cancel, ch.Mux, len(fes))
	err = start(tctx, in)
	cancel(nil)
	reading, on, busy := w.result()
	// A tuner that has been on it a while has had time to lock, or not.
	if reading != nil && on >= 3*s.timing.poll && ch.Mux.FrequencyHz > 0 {
		s.signals.Record(ch.Mux.FrequencyHz, signal.RF(ch.Mux.FrequencyHz), *reading)
	}
	if err == nil {
		return nil
	}
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
	rf := signal.RF(ch.Mux.FrequencyHz)
	switch {
	case reading != nil && !reading.Lock:
		return &NoSignalError{Channel: number, RF: rf, Label: label, Reading: reading}
	case reading == nil && busy:
		return fmt.Errorf("every tuner is in use right now (by a viewer or a recording), so %s can't be tuned", number)
	}
	return fmt.Errorf("%s (RF %d) didn't start: %w", number, rf, err)
}
