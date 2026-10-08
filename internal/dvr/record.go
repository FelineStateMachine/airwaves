package dvr

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/tuner"
)

// Why a recording ended before its time.
var (
	errStopped  = errors.New("stopped")
	errDiscard  = errors.New("discarded")
	errShutdown = errors.New("the server stopped")
)

// retry is how long a recording waits after losing its tuner or signal
// before trying again.
var retry = 5 * time.Second

// job is a recording in progress.
type job struct {
	id     string
	ctx    context.Context
	cancel context.CancelCauseFunc
	extend chan struct{}
	done   chan struct{}

	mu      sync.Mutex
	written int64
	began   time.Time // first data
	last    time.Time // latest data
	// covered is how much time the stretches so far recorded, and cur the
	// stretch now.
	covered, cur time.Duration
	trouble      string // the latest problem
}

func (j *job) size() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.written
}

func (j *job) problem() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.trouble
}

func (j *job) note(err error) {
	j.mu.Lock()
	j.trouble = err.Error()
	j.mu.Unlock()
}

// Run starts recordings as their airings begin, until ctx ends. It first
// picks up recordings the server was making when it last stopped.
func (m *Manager) Run(ctx context.Context, listings Listings) {
	m.resume(time.Now())
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		if g, chans, err := listings(ctx); err == nil {
			m.Start(g, chans, time.Now())
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Close stops the recordings in progress, keeping them to pick up again.
func (m *Manager) Close() {
	m.cancel(errShutdown)
	m.mu.Lock()
	var jobs []*job
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()
	for _, j := range jobs {
		<-j.done
	}
}

// Start begins recording every airing the rules want that's on at now.
func (m *Manager) Start(g *guide.Guide, chans []lineup.Channel, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() != nil {
		return
	}
	n := len(m.st.Rules)
	// Once rules whose airing is over are done.
	m.st.Rules = slices.DeleteFunc(m.st.Rules, func(r Rule) bool { return r.Kind == Once && !r.End.Add(lateSports).After(now) })
	changed := len(m.st.Rules) != n
	for _, a := range m.desired(g, chans, now, m.recorded()) {
		from, until := a.window()
		if now.Before(from) || m.has(a.key) {
			continue
		}
		r := Rec{
			ID: newID(), Key: a.key, RuleID: a.rule.ID, Title: a.p.Title, Subtitle: a.p.EpisodeTitle,
			Description: a.p.Description, Image: cmp.Or(a.p.Image, a.rule.Image), ProgramID: a.p.ProgramID,
			Channel: a.ch.Number, CallSign: a.ch.CallSign, Start: a.p.Start, End: a.p.End, From: from, Until: until,
			Status: Recording,
		}
		r.File = m.fileName(r)
		m.st.Recordings = append(m.st.Recordings, r)
		m.run(r)
		changed = true
		log.Printf("recording %s on %s until %s", r.Title, r.Channel, until.Format(time.Kitchen))
	}
	if changed {
		m.saveLogged()
	}
}

// resume picks up the recordings that were in progress when the server
// stopped: those whose time isn't up carry on, appending to their files;
// the rest are finished as they are.
func (m *Manager) resume(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for i := range m.st.Recordings {
		r := &m.st.Recordings[i]
		if r.Status != Recording || m.jobs[r.ID] != nil {
			continue
		}
		changed = true
		fi, err := os.Stat(filepath.Join(m.dir, r.File))
		if err == nil {
			r.Size = fi.Size()
		}
		if now.Before(r.Until) {
			log.Printf("recording %s again, after the server stopped", r.Title)
			m.run(*r)
			continue
		}
		if r.Size == 0 {
			r.Status, r.Detail = Failed, "The server stopped before it recorded anything"
			continue
		}
		r.Status, r.Ended = Completed, fi.ModTime()
		r.Detail = describe(*r, "the server stopped", false)
	}
	if changed {
		m.saveLogged()
	}
}

// run starts recording r. Called with m.mu held.
func (m *Manager) run(r Rec) {
	ctx, cancel := context.WithCancelCause(m.ctx)
	j := &job{id: r.ID, ctx: ctx, cancel: cancel, extend: make(chan struct{}, 1), done: make(chan struct{}),
		written: r.Size, began: r.Began, last: r.Ended, covered: r.Covered}
	m.jobs[r.ID] = j
	go m.record(j, r)
}

// until is when the recording id stops.
func (m *Manager) until(id string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.st.Recordings {
		if r.ID == id {
			return r.Until
		}
	}
	return time.Time{}
}

// record records r until its time is up or it's stopped. Losing the tuner
// or the signal makes a gap, not an end: it tries again until its time is
// up.
func (m *Manager) record(j *job, r Rec) {
	defer close(j.done)
	path := filepath.Join(m.dir, r.File)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		j.note(err)
	} else {
		for j.ctx.Err() == nil {
			left := time.Until(m.until(j.id))
			if left <= 0 {
				break
			}
			if err := m.stretch(j, r, path); err != nil && j.ctx.Err() == nil {
				j.note(err)
				log.Printf("recording %s: %v", r.Title, err)
				select {
				case <-j.ctx.Done():
				case <-j.extend:
				case <-time.After(min(retry, left)):
				}
			}
		}
	}
	m.finish(j)
}

// stretch records r on one tuner for as long as it can: to its end, or
// until the tuner or the signal is lost. Extending the recording moves
// the end while it records.
func (m *Manager) stretch(j *job, r Rec, path string) error {
	ctx := j.ctx
	freq, program, err := m.locate(ctx, r.Channel)
	if err != nil {
		return err
	}
	sub, err := m.tuners.Open(ctx, tuner.Request{FrequencyHz: freq, Program: program, Priority: tuner.Recording, Who: r.Title})
	if err != nil {
		return err
	}
	defer sub.Close()
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, err = sub.Wait(wait)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("no signal on %s", r.Channel)
	}
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := &stretchWriter{f: f, j: j, began: func(at time.Time) { m.began(j.id, at) }}
	defer func() {
		w.end()
		f.Close()
	}()
	for {
		until := m.until(j.id)
		part, stop := context.WithDeadline(ctx, until)
		go func() {
			select {
			case <-j.extend:
				stop()
			case <-part.Done():
			}
		}()
		err := sub.Copy(part, w)
		stop()
		switch {
		case ctx.Err() != nil:
			return nil // stopped
		case !time.Now().Before(m.until(j.id)):
			return nil // done
		case errors.Is(err, context.Canceled) && sub.Err() == nil:
			continue // extended
		}
		if err == nil {
			err = errors.New("the tuner stopped")
		}
		return err
	}
}

// began notes when a recording's first data arrived.
func (m *Manager) began(id string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.st.Recordings {
		if r := &m.st.Recordings[i]; r.ID == id && r.Began.IsZero() {
			r.Began = at
			m.saveLogged()
		}
	}
}

// stretchWriter writes a recording's file and counts what it wrote.
type stretchWriter struct {
	f     *os.File
	j     *job
	first time.Time
	began func(time.Time)
}

func (w *stretchWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	now := time.Now()
	w.j.mu.Lock()
	first := w.j.began.IsZero()
	if first {
		w.j.began = now
	}
	if w.first.IsZero() {
		w.first = now
	}
	w.j.written += int64(n)
	w.j.last, w.j.cur = now, now.Sub(w.first)
	w.j.mu.Unlock()
	if first {
		w.began(now)
	}
	return n, err
}

// end adds the stretch to what's covered.
func (w *stretchWriter) end() {
	w.j.mu.Lock()
	w.j.covered += w.j.cur
	w.j.cur = 0
	w.j.mu.Unlock()
}

// finish records how a recording went, once it has ended.
func (m *Manager) finish(j *job) {
	cause := context.Cause(j.ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, j.id)
	i := slices.IndexFunc(m.st.Recordings, func(r Rec) bool { return r.ID == j.id })
	if i < 0 || errors.Is(cause, errDiscard) {
		return // DeleteEntry removes it
	}
	r := &m.st.Recordings[i]
	j.mu.Lock()
	r.Size, r.Began, r.Ended, r.Covered = j.written, j.began, j.last, j.covered+j.cur
	trouble := j.trouble
	j.mu.Unlock()
	switch {
	case errors.Is(cause, errShutdown):
		// Still recording, as far as the next start is concerned.
	case r.Size == 0:
		r.Status, r.Detail = Failed, "Nothing was recorded"
		if trouble != "" {
			r.Detail += ": " + trouble
		}
		log.Printf("recording %s failed: %s", r.Title, r.Detail)
	default:
		r.Status = Completed
		r.Detail = describe(*r, trouble, errors.Is(cause, errStopped))
		log.Printf("recorded %s (%d MB)%s", r.Title, r.Size>>20, cmp.Or(": "+r.Detail, ""))
	}
	m.saveLogged()
}

// describe says what a finished recording is missing, if anything: a late
// start, an early stop, gaps. trouble is the latest problem it had.
func describe(r Rec, trouble string, stopped bool) string {
	mins := func(d time.Duration) string {
		n := int(d.Round(time.Minute).Minutes())
		if n == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", n)
	}
	var out []string
	if d := r.Began.Sub(r.Start); !r.Began.IsZero() && d >= time.Minute {
		out = append(out, "Began "+mins(d)+" into the program")
	}
	if d := r.End.Sub(r.Ended); !r.Ended.IsZero() && d >= time.Minute {
		if stopped {
			out = append(out, "Stopped "+mins(d)+" before the end")
		} else {
			out = append(out, "Ends "+mins(d)+" before the program did")
		}
	}
	if gap := r.Ended.Sub(r.Began) - r.Covered; r.Covered > 0 && gap >= time.Minute {
		out = append(out, "About "+mins(gap)+" are missing in between")
	}
	if len(out) > 0 && trouble != "" && !stopped {
		out[len(out)-1] += ": " + trouble
	}
	return strings.Join(out, ". ")
}

// Stop ends a recording in progress early, keeping what it recorded.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	j := m.jobs[id]
	m.mu.Unlock()
	if j == nil {
		return fmt.Errorf("%s isn't recording", id)
	}
	j.cancel(errStopped)
	<-j.done
	return nil
}

// Extend makes a recording in progress run d longer.
func (m *Manager) Extend(id string, d time.Duration) (time.Time, error) {
	if d <= 0 || d > 6*time.Hour {
		return time.Time{}, fmt.Errorf("can't extend a recording by %s", d)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	i := slices.IndexFunc(m.st.Recordings, func(r Rec) bool { return r.ID == id })
	if j == nil || i < 0 {
		return time.Time{}, fmt.Errorf("%s isn't recording", id)
	}
	r := &m.st.Recordings[i]
	r.Until = r.Until.Add(d)
	select {
	case j.extend <- struct{}{}:
	default:
	}
	return r.Until, m.save()
}

// fileName picks r's file in the recordings folder: a folder per show,
// named by date and episode, and never one already there. Called with
// m.mu held.
func (m *Manager) fileName(r Rec) string {
	name := r.Start.Local().Format("2006-01-02 15.04")
	if s := clean(r.Subtitle); s != "" {
		name += " " + s
	}
	base := filepath.Join(cmp.Or(clean(r.Title), "Recording"), name)
	for n := 1; ; n++ {
		f := base + ".ts"
		if n > 1 {
			f = fmt.Sprintf("%s (%d).ts", base, n)
		}
		_, err := os.Stat(filepath.Join(m.dir, f))
		taken := slices.ContainsFunc(m.st.Recordings, func(x Rec) bool { return x.File == f })
		if errors.Is(err, os.ErrNotExist) && !taken {
			return f
		}
	}
}

// clean makes s fit for a file name: no separators or control characters,
// not hidden, and not too long.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\':
			return '-'
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	s = strings.TrimLeft(strings.TrimSpace(s), ".")
	for len(s) > 80 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return strings.TrimSpace(s)
}
