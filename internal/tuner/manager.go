package tuner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"airwaves/internal/hdhr"
	"airwaves/internal/ts"
)

// Priority orders who gets a tuner when there aren't enough: a recording
// takes one from a viewer, and a viewer one from a measurement.
type Priority int

// Priorities, lowest first.
const (
	// Idle is a tuner left on its RF channel for a moment after its last
	// user, so coming back to it is instant. Anyone can have it.
	Idle Priority = iota
	Measuring
	Watching
	Recording
)

func (p Priority) String() string {
	switch p {
	case Measuring:
		return "measuring"
	case Watching:
		return "watching"
	case Recording:
		return "recording"
	}
	return "idle"
}

// ErrNoDevice is returned while no HDHomeRun has been found.
var ErrNoDevice = errors.New("no tuner: no HDHomeRun found on the network")

// ErrNoProgram is returned for a program the RF channel doesn't carry
// (any more).
var ErrNoProgram = errors.New("the RF channel doesn't carry that program")

// errIdle ends a tuner that stayed unused for Linger.
var errIdle = errors.New("no one is using it")

// errTooSlow ends a user that fell too far behind the broadcast.
var errTooSlow = errors.New("fell too far behind the broadcast")

// BusyError is a tuner asked for when every one is in use by others at
// the same priority or higher.
type BusyError struct {
	// Users says who has the tuners, "watching 7.1".
	Users []string
}

func (e *BusyError) Error() string {
	if len(e.Users) == 0 {
		return "every tuner is in use"
	}
	return "every tuner is in use (" + strings.Join(e.Users, "; ") + ")"
}

// PreemptedError ends a user whose tuner was given to someone at a higher
// priority.
type PreemptedError struct {
	By string // "recording The News"
}

func (e *PreemptedError) Error() string { return e.By + " needed the tuner" }

// Hardware is the tuners: an HDHomeRun.
type Hardware interface {
	OpenRF(ctx context.Context, tuner int, freqHz int64) (*hdhr.Stream, error)
	Status(ctx context.Context) ([]hdhr.TunerStatus, error)
}

// Manager shares an HDHomeRun's tuners among everything that wants antenna
// TV: viewers in the app, HDHomeRun clients, recordings and Measure now.
// It is the device's only client. Each RF channel in use is read once, on
// one tuner, and handed to everyone on it, each getting their own program,
// so watching 9.2 while recording 9.1 takes one tuner.
type Manager struct {
	// Linger is how long a tuner stays on its RF channel after its last
	// user leaves.
	Linger time.Duration
	// StatusAge is how long a reading of the device's status is used:
	// it's asked at most this often.
	StatusAge time.Duration

	mu     sync.Mutex
	dev    Hardware
	tuners int
	muxes  map[int]*mux // by tuner
	// away marks tuners the device said someone else has, until then.
	away map[int]time.Time

	statusMu sync.Mutex
	status   []hdhr.TunerStatus
	statusAt time.Time
}

// NewManager returns a manager with no device yet.
func NewManager() *Manager {
	return &Manager{Linger: 5 * time.Second, StatusAge: 500 * time.Millisecond, muxes: map[int]*mux{}, away: map[int]time.Time{}}
}

// SetDevice makes dev, with its count of tuners, the one to use; nil for
// none. Tuners in use on a device that goes away end.
func (m *Manager) SetDevice(dev Hardware, tuners int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dev == m.dev && tuners == m.tuners {
		return
	}
	for _, x := range m.muxes {
		x.stop(errors.New("the tuner went away"))
	}
	m.dev, m.tuners = dev, tuners
}

// Count is how many tuners the device has; 0 without one.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dev == nil {
		return 0
	}
	return m.tuners
}

// Request asks for a tuner.
type Request struct {
	// FrequencyHz is the RF channel's center frequency.
	FrequencyHz int64
	// Program is the program to receive, 0 for none: only to keep the
	// tuner on the RF channel, as measuring does.
	Program  int
	Priority Priority
	// Who says what it's for, "watching 7.1".
	Who string
	// Owner, when set, is who asks (a viewer's app): a tuner only they
	// use is theirs to retune, so changing channels never finds every
	// tuner busy with their own last one.
	Owner string
}

// Open puts a tuner on the RF channel r asks for and subscribes to its
// program there. A tuner already on that RF channel is shared. With no
// tuner free, it takes the one whose users all have a lower priority
// (or are all r.Owner), ending them, or fails with a *BusyError. It
// returns once the tuner is asked for; Wait waits for the program.
func (m *Manager) Open(ctx context.Context, r Request) (*Sub, error) {
	for {
		m.mu.Lock()
		if m.dev == nil {
			m.mu.Unlock()
			return nil, ErrNoDevice
		}
		for _, x := range m.muxes {
			if x.freq == r.FrequencyHz && x.alive() {
				s := x.add(r)
				m.mu.Unlock()
				return s, nil
			}
		}
		tuner, victim, err := m.pick(r)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if victim == nil {
			x := m.start(tuner, r.FrequencyHz)
			s := x.add(r)
			m.mu.Unlock()
			return s, nil
		}
		victim.stop(&PreemptedError{By: r.Priority.String() + " " + r.Who})
		m.mu.Unlock()
		// The device frees the tuner once its stream is closed.
		select {
		case <-victim.done:
		case <-time.After(3 * time.Second):
			return nil, fmt.Errorf("tuner %d didn't come free", victim.tuner)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// pick finds a tuner for r: a free one, or failing that one in use only
// by r's owner, or the one in use at the lowest priority below r's, to
// take over. Of those alike, the one with the fewest users goes, then the
// one tuned last: the least to lose.
func (m *Manager) pick(r Request) (tuner int, victim *mux, err error) {
	now := time.Now()
	for t := range m.tuners {
		if _, used := m.muxes[t]; !used && !now.Before(m.away[t]) {
			return t, nil, nil
		}
	}
	for _, x := range m.muxes {
		if x.ownedBy(r.Owner) {
			return x.tuner, x, nil
		}
	}
	for _, x := range m.muxes {
		if x.top() < r.Priority && (victim == nil || x.lessThan(victim)) {
			victim = x
		}
	}
	if victim != nil {
		return victim.tuner, victim, nil
	}
	busy := &BusyError{}
	for t := range m.tuners {
		if x := m.muxes[t]; x != nil {
			busy.Users = append(busy.Users, x.users()...)
		}
	}
	slices.Sort(busy.Users)
	return 0, nil, busy
}

// start puts tuner on freqHz. Called with m.mu held.
func (m *Manager) start(tuner int, freqHz int64) *mux {
	ctx, cancel := context.WithCancelCause(context.Background())
	x := &mux{
		m: m, tuner: tuner, freq: freqHz, since: time.Now(), ctx: ctx, cancel: cancel,
		subs: map[*Sub]bool{}, demux: ts.NewDemux(), changed: make(chan struct{}), done: make(chan struct{}),
	}
	m.muxes[tuner] = x
	go x.run(m.dev)
	return x
}

// ended forgets x once its stream has closed.
func (m *Manager) ended(x *mux, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.muxes[x.tuner] == x {
		delete(m.muxes, x.tuner)
	}
	var he *hdhr.Error
	if errors.As(err, &he) && he.Busy() {
		// Someone else (the device's own scan, another app) has it.
		m.away[x.tuner] = time.Now().Add(30 * time.Second)
	}
}

// User is someone using a tuner.
type User struct {
	Who      string   `json:"who"`
	Priority Priority `json:"priority"`
}

// State is what one tuner is doing now.
type State struct {
	Number int
	// FrequencyHz is the RF channel it's on; 0 while idle.
	FrequencyHz int64
	Since       time.Time
	Users       []User
	// Top is the highest priority of its users; Idle with none.
	Top Priority
	// Locked is set while data arrives: the tuner has a lock.
	Locked bool
	// Packets and Errors count what arrived since it was tuned, and the
	// transport and continuity errors in it.
	Packets, Errors int64
	// Device is what the HDHomeRun itself reports of the tuner: strength,
	// quality and symbol quality, while it's tuned.
	Device hdhr.TunerStatus
	// Elsewhere is set for a tuner the device says is tuned by something
	// other than this manager.
	Elsewhere bool
}

// Tuners reports every tuner: what this manager has it doing, and what
// the device measures. The device is asked at most every StatusAge.
func (m *Manager) Tuners(ctx context.Context) ([]State, error) {
	m.mu.Lock()
	dev, n := m.dev, m.tuners
	muxes := make(map[int]*mux, len(m.muxes))
	for t, x := range m.muxes {
		muxes[t] = x
	}
	m.mu.Unlock()
	if dev == nil {
		return nil, ErrNoDevice
	}
	status, err := m.deviceStatus(ctx, dev)
	out := make([]State, n)
	for t := range out {
		out[t] = State{Number: t}
		if x := muxes[t]; x != nil {
			out[t] = x.state()
		}
	}
	for _, st := range status {
		if st.Tuner < 0 || st.Tuner >= n {
			continue
		}
		out[st.Tuner].Device = st
		if out[st.Tuner].FrequencyHz == 0 && st.Tuned() {
			out[st.Tuner].FrequencyHz, out[st.Tuner].Elsewhere = st.FrequencyHz, true
		}
	}
	return out, err
}

func (m *Manager) deviceStatus(ctx context.Context, dev Hardware) ([]hdhr.TunerStatus, error) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if !m.statusAt.IsZero() && time.Since(m.statusAt) < m.StatusAge {
		return m.status, nil
	}
	st, err := dev.Status(ctx)
	if err != nil {
		return nil, err
	}
	m.status, m.statusAt = st, time.Now()
	return st, nil
}

// mux is a tuner on an RF channel, read once for all its users.
type mux struct {
	m      *Manager
	tuner  int
	freq   int64
	since  time.Time
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu       sync.Mutex
	subs     map[*Sub]bool
	idle     *time.Timer
	demux    *ts.Demux
	counter  ts.Counter
	answered bool // the device answered: it locked
	lastData time.Time
	// changed is closed, and replaced, whenever there's more to see.
	changed chan struct{}
}

func (x *mux) alive() bool { return x.ctx.Err() == nil }

// stop ends the tuner and everyone on it with cause.
func (x *mux) stop(cause error) { x.cancel(cause) }

func (x *mux) run(dev Hardware) {
	defer close(x.done)
	err := x.read(dev)
	x.cancel(err)
	cause := context.Cause(x.ctx)
	x.mu.Lock()
	for s := range x.subs {
		s.end(cause)
	}
	if x.idle != nil {
		x.idle.Stop()
	}
	x.notify()
	x.mu.Unlock()
	x.m.ended(x, cause)
}

func (x *mux) read(dev Hardware) error {
	st, err := dev.OpenRF(x.ctx, x.tuner, x.freq)
	if err != nil {
		return err
	}
	defer st.Close()
	x.mu.Lock()
	x.answered = true
	x.notify()
	x.mu.Unlock()
	var split ts.Splitter
	buf := make([]byte, 64<<10)
	for {
		n, err := st.Read(buf)
		if n > 0 {
			x.mu.Lock()
			x.lastData = time.Now()
			split.Write(buf[:n], func(pkt []byte) {
				x.demux.Packet(pkt)
				x.counter.Packet(pkt)
				for s := range x.subs {
					s.packet(pkt)
				}
			})
			for s := range x.subs {
				s.flush()
			}
			x.notify()
			x.mu.Unlock()
		}
		if err != nil {
			if x.ctx.Err() != nil {
				return context.Cause(x.ctx)
			}
			return fmt.Errorf("the tuner stopped: %w", err)
		}
	}
}

// notify wakes whoever waits on x. Called with x.mu held.
func (x *mux) notify() {
	close(x.changed)
	x.changed = make(chan struct{})
}

// add subscribes r's asker to its program. Called with m.mu held.
func (x *mux) add(r Request) *Sub {
	ctx, cancel := context.WithCancelCause(x.ctx)
	s := &Sub{x: x, program: r.Program, prio: r.Priority, who: r.Who, owner: r.Owner, ctx: ctx, cancel: cancel, data: make(chan []byte, 256)}
	if r.Program > 0 {
		s.filter = ts.NewFilter(&s.pending, r.Program)
	}
	x.mu.Lock()
	x.subs[s] = true
	if x.idle != nil {
		x.idle.Stop()
		x.idle = nil
	}
	x.mu.Unlock()
	return s
}

// remove unsubscribes s; with no one left, the tuner lingers, then ends.
func (x *mux) remove(s *Sub) {
	x.m.mu.Lock()
	defer x.m.mu.Unlock()
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.subs[s] {
		return
	}
	delete(x.subs, s)
	if len(x.subs) > 0 || !x.alive() {
		return
	}
	if x.m.Linger <= 0 {
		x.stop(errIdle)
		return
	}
	x.idle = time.AfterFunc(x.m.Linger, func() {
		x.mu.Lock()
		unused := len(x.subs) == 0
		x.mu.Unlock()
		if unused {
			x.stop(errIdle)
		}
	})
}

// ownedBy reports whether x is used by owner alone (and not idle).
// Called with m.mu held.
func (x *mux) ownedBy(owner string) bool {
	if owner == "" {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for s := range x.subs {
		if s.owner != owner {
			return false
		}
	}
	return len(x.subs) > 0
}

// lessThan reports whether x has less to lose than y: lower priority
// users, fewer of them, or tuned later. Called with m.mu held.
func (x *mux) lessThan(y *mux) bool {
	if a, b := x.top(), y.top(); a != b {
		return a < b
	}
	if a, b := x.count(), y.count(); a != b {
		return a < b
	}
	return x.since.After(y.since)
}

func (x *mux) count() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.subs)
}

// top is the highest priority among x's users. Called with m.mu held.
func (x *mux) top() Priority {
	x.mu.Lock()
	defer x.mu.Unlock()
	top := Idle
	for s := range x.subs {
		top = max(top, s.prio)
	}
	return top
}

// users describes who is on x, "watching 7.1". Called with m.mu held.
func (x *mux) users() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []string
	for s := range x.subs {
		out = append(out, s.prio.String()+" "+s.who)
	}
	return out
}

func (x *mux) state() State {
	x.mu.Lock()
	defer x.mu.Unlock()
	t := State{
		Number: x.tuner, FrequencyHz: x.freq, Since: x.since, Packets: x.counter.Packets, Errors: x.counter.Errors(),
		Locked: x.answered && time.Since(x.lastData) < 2*time.Second,
	}
	for s := range x.subs {
		t.Users = append(t.Users, User{Who: s.who, Priority: s.prio})
		t.Top = max(t.Top, s.prio)
	}
	slices.SortFunc(t.Users, func(a, b User) int { return strings.Compare(a.Who, b.Who) })
	return t
}

// maxPending is how far a user may fall behind before it's dropped.
const maxPending = 16 << 20

// Sub is one user of a tuner: a program it receives, or only the tuner
// held on an RF channel.
type Sub struct {
	x       *mux
	program int
	prio    Priority
	who     string
	owner   string
	ctx     context.Context
	cancel  context.CancelCauseFunc
	filter  *ts.Filter
	// pending is what the filter wrote since the last flush; data hands
	// it to whoever copies the program. Both are kept under x.mu.
	pending buffer
	data    chan []byte
	once    sync.Once
}

// buffer collects the filter's output.
type buffer []byte

func (b *buffer) Write(p []byte) (int, error) {
	*b = append(*b, p...)
	return len(p), nil
}

// packet passes one of the multiplex's packets to s. Called with x.mu held.
func (s *Sub) packet(pkt []byte) {
	if s.filter != nil && s.ctx.Err() == nil {
		_ = s.filter.Packet(pkt)
	}
}

// flush hands on what s collected, without ever blocking the tuner: a
// user that has fallen too far behind is dropped. Called with x.mu held.
func (s *Sub) flush() {
	if s.filter != nil {
		_ = s.filter.Flush()
	}
	if len(s.pending) == 0 {
		return
	}
	select {
	case s.data <- s.pending:
		s.pending = nil
	default:
		if len(s.pending) > maxPending {
			s.pending = nil
			s.end(errTooSlow)
		}
	}
}

// end ends s with cause.
func (s *Sub) end(cause error) { s.cancel(cause) }

// Tuner is the tuner s is on.
func (s *Sub) Tuner() int { return s.x.tuner }

// FrequencyHz is the RF channel s is on.
func (s *Sub) FrequencyHz() int64 { return s.x.freq }

// Done is closed when s ends: closed, its tuner taken over, or the tuner
// gone. Err says why.
func (s *Sub) Done() <-chan struct{} { return s.ctx.Done() }

// Err is why s ended; nil while it hasn't.
func (s *Sub) Err() error {
	if s.ctx.Err() == nil {
		return nil
	}
	return context.Cause(s.ctx)
}

// Locked reports whether data is arriving: the tuner has a lock.
func (s *Sub) Locked() bool {
	s.x.mu.Lock()
	defer s.x.mu.Unlock()
	return s.x.answered && time.Since(s.x.lastData) < 2*time.Second
}

// Wait waits until s's program can be read, and describes it: once its
// PMT has arrived, or for a subscription without a program, once the
// tuner locked. It fails when s ends first, when the RF channel turns out
// not to carry the program, or when ctx ends.
func (s *Sub) Wait(ctx context.Context) (ts.Program, error) {
	for {
		s.x.mu.Lock()
		changed := s.x.changed
		var p ts.Program
		ok := false
		switch {
		case s.program == 0:
			ok = s.x.answered && !s.x.lastData.IsZero()
		default:
			// Once every PMT the PAT lists is in, a program without one
			// isn't there.
			if p, ok = s.x.demux.Program(s.program); !ok && s.x.demux.Ready() {
				s.x.mu.Unlock()
				return ts.Program{}, fmt.Errorf("program %d: %w", s.program, ErrNoProgram)
			}
		}
		s.x.mu.Unlock()
		if ok {
			return p, nil
		}
		select {
		case <-changed:
		case <-s.ctx.Done():
			return ts.Program{}, context.Cause(s.ctx)
		case <-ctx.Done():
			return ts.Program{}, ctx.Err()
		}
	}
}

// Copy writes s's program to w until s ends, w fails or ctx ends, and
// returns why it stopped.
func (s *Sub) Copy(ctx context.Context, w io.Writer) error {
	for {
		select {
		case b := <-s.data:
			if _, err := w.Write(b); err != nil {
				return err
			}
		case <-s.ctx.Done():
			return context.Cause(s.ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close lets go of the tuner. It's safe to call more than once.
func (s *Sub) Close() {
	s.once.Do(func() {
		s.cancel(context.Canceled)
		s.x.remove(s)
	})
}
