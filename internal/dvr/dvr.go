// Package dvr records over-the-air TV. The user's rules (one airing, or a
// series by Gracenote series ID on one channel) pick airings from the
// guide; Airwaves records them itself, on the tuners it shares with
// viewers, where a recording comes first. Recordings are files in the
// recordings folder, described in the same state file as the rules.
package dvr

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/tuner"
)

// Kind is the type of rule.
type Kind string

// Rule kinds.
const (
	Once   Kind = "once"
	Series Kind = "series"
)

// Statuses of a recording, as Item shows them.
const (
	// Scheduled is an airing a rule wants that hasn't started.
	Scheduled = "scheduled"
	// Conflict is one that won't get a tuner: others record from every
	// tuner then.
	Conflict = "conflict"
	// Unavailable is one on a channel the tuner doesn't have.
	Unavailable = "unavailable"
	Recording   = "recording"
	Completed   = "completed"
	Failed      = "failed"
)

// Padding around an airing: a minute early, three late, and half an hour
// late for sports, which overrun.
const (
	early      = time.Minute
	late       = 3 * time.Minute
	lateSports = 30 * time.Minute
)

// Request asks to record an airing picked in the guide.
type Request struct {
	Kind     Kind      `json:"kind"`
	Channel  string    `json:"channel"` // virtual channel number
	CallSign string    `json:"callSign"`
	Start    time.Time `json:"start"`
	// NewOnly limits a series rule to first-run episodes.
	NewOnly bool `json:"newOnly"`
}

// Rule is something the user asked to record.
type Rule struct {
	ID       string    `json:"id"`
	Kind     Kind      `json:"kind"`
	Title    string    `json:"title"`
	Channel  string    `json:"channel"`
	CallSign string    `json:"callSign"`
	GuideID  string    `json:"guideId"`
	SeriesID string    `json:"seriesId,omitempty"`
	NewOnly  bool      `json:"newOnly,omitempty"`
	Image    string    `json:"image,omitempty"`
	Created  time.Time `json:"created"`

	// Once rules carry the airing itself.
	Start       time.Time `json:"start,omitzero"`
	End         time.Time `json:"end,omitzero"`
	Subtitle    string    `json:"subtitle,omitempty"`
	Description string    `json:"description,omitempty"`
	ProgramID   string    `json:"programId,omitempty"`
	Sports      bool      `json:"sports,omitempty"`

	// Skip lists airing keys removed from a series by the user.
	Skip []string `json:"skip,omitempty"`
	// Keep, for series, is how many recordings to keep (newest first);
	// zero keeps them all.
	Keep int `json:"keep,omitempty"`
}

// Rec is a recording Airwaves made or is making: an airing, and the file
// it went to.
type Rec struct {
	ID          string `json:"id"`
	Key         string `json:"key"` // the airing's, "7.1@1791058239"
	RuleID      string `json:"ruleId,omitempty"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle,omitempty"`
	Description string `json:"description,omitempty"`
	Image       string `json:"image,omitempty"`
	ProgramID   string `json:"programId,omitempty"`
	Channel     string `json:"channel"`
	CallSign    string `json:"callSign,omitempty"`
	// Start and End are the airing's; From and Until what is recorded,
	// with padding. Until moves when the recording is extended or stopped.
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
	// File is where it is, in the recordings folder.
	File   string `json:"file"`
	Status string `json:"status"` // Recording, Completed or Failed
	// Detail says what's missing or what went wrong.
	Detail string `json:"detail,omitempty"`
	// Began and Ended are when data first and last arrived; Covered how
	// much of the time between them was recorded.
	Began   time.Time     `json:"began,omitzero"`
	Ended   time.Time     `json:"ended,omitzero"`
	Covered time.Duration `json:"covered,omitempty"`
	Size    int64         `json:"size,omitempty"`
}

// Item is a recording as shown to the user.
type Item struct {
	// ID is a recording's own; for an airing not started, its key.
	ID          string    `json:"id"`
	Key         string    `json:"key"`
	RuleID      string    `json:"ruleId,omitempty"`
	Title       string    `json:"title"`
	Subtitle    string    `json:"subtitle,omitempty"`
	Description string    `json:"description,omitempty"`
	Image       string    `json:"image,omitempty"`
	Channel     string    `json:"channel"`
	CallSign    string    `json:"callSign,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	// Status is Scheduled, Conflict, Unavailable, Recording, Completed
	// or Failed.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Until is when a recording in progress stops: the airing's end with
	// its padding, later when extended.
	Until     time.Time `json:"until,omitzero"`
	SizeBytes int64     `json:"sizeBytes,omitempty"`
	// Duration of what's recorded so far, Position the resume point and
	// Watched whether it has been seen.
	Duration float64 `json:"duration,omitempty"`
	Position float64 `json:"position,omitempty"`
	Watched  bool    `json:"watched,omitempty"`
}

// Progress is how far a recording has been watched.
type Progress struct {
	Position float64   `json:"position"`
	Watched  bool      `json:"watched"`
	Updated  time.Time `json:"updated"`
}

// Prefs are library-wide settings.
type Prefs struct {
	// DeleteWatchedAfterDays removes watched recordings this many days after
	// they were finished; zero keeps them.
	DeleteWatchedAfterDays int `json:"deleteWatchedAfterDays"`
}

// State is everything the DVR view needs.
type State struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Rules     []Rule `json:"rules"`
	Upcoming  []Item `json:"upcoming"`
	Recorded  []Item `json:"recorded"`
	Prefs     Prefs  `json:"prefs"`
}

type fileState struct {
	Rules      []Rule              `json:"rules"`
	Recordings []Rec               `json:"recordings"`
	Progress   map[string]Progress `json:"progress"` // by recording ID
	Prefs      Prefs               `json:"prefs"`
}

// Tuners is where recordings get their tuner.
type Tuners interface {
	Open(ctx context.Context, r tuner.Request) (*tuner.Sub, error)
	Count() int
}

// Locate finds where a channel is broadcast: its RF channel and program.
type Locate func(ctx context.Context, number string) (freqHz int64, program int, err error)

// Listings supplies the guide and the tuner's channels, for the rules.
type Listings func(ctx context.Context) (*guide.Guide, []lineup.Channel, error)

// Manager owns the rules and the recordings.
type Manager struct {
	path   string
	dir    string
	tuners Tuners
	locate Locate

	ctx    context.Context // until Close, for the recordings
	cancel context.CancelCauseFunc

	mu   sync.Mutex
	st   fileState
	jobs map[string]*job // recordings in progress, by ID
}

// Open loads the rules and recordings kept at path (created on first
// save), recording into dir. A file that can't be read is set aside, as
// path.bad, and the DVR starts empty rather than not at all.
func Open(path, dir string, tuners Tuners, locate Locate) (*Manager, error) {
	m := &Manager{path: path, dir: dir, tuners: tuners, locate: locate, jobs: map[string]*job{},
		st: fileState{Progress: map[string]Progress{}}}
	m.ctx, m.cancel = context.WithCancelCause(context.Background())
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &m.st); err != nil {
			log.Printf("recordings: %s can't be read (%v); set aside as %s.bad, starting empty", path, err, path)
			_ = os.Rename(path, path+".bad")
			m.st = fileState{}
		}
	}
	if m.st.Progress == nil {
		m.st.Progress = map[string]Progress{}
	}
	return m, nil
}

// save writes the state. Called with m.mu held.
func (m *Manager) save() error {
	raw, err := json.MarshalIndent(m.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), filepath.Base(m.path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), m.path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// saveLogged saves, logging a failure. Called with m.mu held.
func (m *Manager) saveLogged() {
	if err := m.save(); err != nil {
		log.Printf("recordings: %v", err)
	}
}

// Add creates a rule from a guide selection. An airing that's on now
// starts recording at once.
func (m *Manager) Add(ctx context.Context, req Request, g *guide.Guide, chans []lineup.Channel) (*Rule, error) {
	ch := findChannel(chans, req.Channel, req.CallSign)
	if ch == nil || ch.GuideID == "" || g == nil {
		return nil, fmt.Errorf("no listings for channel %s", req.Channel)
	}
	var p *guide.Program
	for i, x := range g.Programs[ch.GuideID] {
		if x.Start.Equal(req.Start) {
			p = &g.Programs[ch.GuideID][i]
			break
		}
	}
	if p == nil {
		return nil, fmt.Errorf("nothing starts at %s on %s", req.Start.Format(time.Kitchen), req.Channel)
	}
	r := Rule{
		ID: newID(), Kind: req.Kind, Title: p.Title, Channel: ch.Number, CallSign: ch.CallSign,
		GuideID: ch.GuideID, Image: p.Image, Created: time.Now(),
	}
	switch req.Kind {
	case Series:
		if p.SeriesID == "" {
			return nil, fmt.Errorf("%q has no series information; record it once instead", p.Title)
		}
		r.SeriesID, r.NewOnly = p.SeriesID, req.NewOnly
	case Once:
		r.Start, r.End = p.Start, p.End
		r.Subtitle, r.Description, r.ProgramID = p.EpisodeTitle, p.Description, p.ProgramID
		r.Sports = slices.Contains(p.Genres, "sports")
	default:
		return nil, fmt.Errorf("unknown rule kind %q", req.Kind)
	}

	m.mu.Lock()
	for _, x := range m.st.Rules {
		if x.Kind == r.Kind && x.Channel == r.Channel && (r.Kind == Series && x.SeriesID == r.SeriesID || r.Kind == Once && x.Start.Equal(r.Start)) {
			m.mu.Unlock()
			return &x, nil
		}
	}
	m.st.Rules = append(m.st.Rules, r)
	err := m.save()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m.Start(g, chans, time.Now())
	return &r, nil
}

// DeleteRule removes a rule. Its recordings in progress carry on.
func (m *Manager) DeleteRule(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.st.Rules, func(r Rule) bool { return r.ID == id })
	if i < 0 {
		return fmt.Errorf("no rule %s", id)
	}
	m.st.Rules = slices.Delete(m.st.Rules, i, i+1)
	return m.save()
}

// DeleteEntry removes one recording, by its ID or, for an airing not
// started, its key: an airing to come is skipped (a once rule for it
// goes), one recording now is stopped and discarded, and a finished one
// is deleted with its file. A series never records an airing deleted
// from it again.
func (m *Manager) DeleteEntry(_ context.Context, id string) error {
	m.mu.Lock()
	i := slices.IndexFunc(m.st.Recordings, func(r Rec) bool { return r.ID == id })
	if i < 0 {
		// An airing to come.
		defer m.mu.Unlock()
		m.skip(id)
		return m.save()
	}
	rec := m.st.Recordings[i]
	j := m.jobs[id]
	m.mu.Unlock()
	if j != nil {
		j.cancel(errDiscard)
		<-j.done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skip(rec.Key)
	m.remove(rec.ID)
	return m.save()
}

// skip keeps the airing key from being recorded: a once rule for it goes,
// and series rules on its channel skip it. Called with m.mu held.
func (m *Manager) skip(key string) {
	channel, _, _ := strings.Cut(key, "@")
	m.st.Rules = slices.DeleteFunc(m.st.Rules, func(r Rule) bool { return r.Kind == Once && airingKey(r.Channel, r.Start) == key })
	for i := range m.st.Rules {
		if r := &m.st.Rules[i]; r.Kind == Series && r.Channel == channel && !slices.Contains(r.Skip, key) {
			r.Skip = append(r.Skip, key)
		}
	}
}

// remove deletes a finished recording and its file. Called with m.mu held.
func (m *Manager) remove(id string) {
	i := slices.IndexFunc(m.st.Recordings, func(r Rec) bool { return r.ID == id })
	if i < 0 {
		return
	}
	if f := m.st.Recordings[i].File; f != "" {
		path := filepath.Join(m.dir, f)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("recordings: %v", err)
		}
		_ = os.Remove(filepath.Dir(path)) // the show's folder, once empty
	}
	m.st.Recordings = slices.Delete(m.st.Recordings, i, i+1)
	delete(m.st.Progress, id)
}

type airing struct {
	key  string
	rule *Rule
	p    guide.Program
	ch   lineup.Channel
}

// window is what's recorded of the airing: from a minute before it to a
// few minutes after, half an hour for sports.
func (a airing) window() (from, until time.Time) {
	after := late
	if slices.Contains(a.p.Genres, "sports") {
		after = lateSports
	}
	return a.p.Start.Add(-early), a.p.End.Add(after)
}

// desired lists the airings the rules call for that haven't ended,
// earliest first. recorded holds the episodes already recorded.
// Called with m.mu held.
func (m *Manager) desired(g *guide.Guide, chans []lineup.Channel, now time.Time, recorded map[string]bool) []airing {
	var out []airing
	seen := make(map[string]bool) // episode IDs already claimed
	for i := range m.st.Rules {
		r := &m.st.Rules[i]
		switch r.Kind {
		case Once:
			p := guide.Program{
				Start: r.Start, End: r.End, Title: r.Title, EpisodeTitle: r.Subtitle,
				Description: r.Description, Image: r.Image, ProgramID: r.ProgramID,
			}
			if r.Sports {
				p.Genres = []string{"sports"}
			}
			a := airing{key: airingKey(r.Channel, r.Start), rule: r, p: p, ch: lineup.Channel{Number: r.Channel, CallSign: r.CallSign, GuideID: r.GuideID}}
			if _, until := a.window(); until.After(now) && !slices.Contains(r.Skip, a.key) {
				out = append(out, a)
			}
		case Series:
			if g == nil {
				continue
			}
			ch := findChannel(chans, r.Channel, r.CallSign)
			if ch == nil {
				continue
			}
			for _, p := range g.Programs[ch.GuideID] {
				if p.SeriesID != r.SeriesID || !p.End.After(now) {
					continue
				}
				if r.NewOnly && !slices.Contains(p.Flags, "New") {
					continue
				}
				key := airingKey(ch.Number, p.Start)
				if slices.Contains(r.Skip, key) {
					continue
				}
				if episodeID(p.ProgramID) && (recorded[p.ProgramID] || seen[p.ProgramID]) {
					continue // already have, or will get, this episode
				}
				seen[p.ProgramID] = true
				out = append(out, airing{key: key, rule: r, p: p, ch: *ch})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b airing) int { return a.p.Start.Compare(b.p.Start) })
	return out
}

// recorded lists the episodes recorded, or being recorded, by program ID.
// Called with m.mu held.
func (m *Manager) recorded() map[string]bool {
	out := map[string]bool{}
	for _, r := range m.st.Recordings {
		if r.Status != Failed && episodeID(r.ProgramID) {
			out[r.ProgramID] = true
		}
	}
	return out
}

// has reports whether the airing key has a recording, made or being made.
// Called with m.mu held.
func (m *Manager) has(key string) bool {
	return slices.ContainsFunc(m.st.Recordings, func(r Rec) bool { return r.Key == key })
}

// State describes rules, schedule and library for the UI.
func (m *Manager) State(ctx context.Context, g *guide.Guide, chans []lineup.Channel) (*State, error) {
	now := time.Now()
	m.mu.Lock()
	st := &State{Available: true, Rules: slices.Clone(m.st.Rules), Prefs: m.st.Prefs, Upcoming: []Item{}, Recorded: []Item{}}
	want := m.desired(g, chans, now, m.recorded())
	var active []Rec
	for _, r := range m.st.Recordings {
		if r.Status == Recording {
			active = append(active, r)
			st.Upcoming = append(st.Upcoming, m.item(r, now))
		} else {
			st.Recorded = append(st.Recorded, m.item(r, now))
		}
	}
	var todo []airing
	for _, a := range want {
		if !m.has(a.key) {
			todo = append(todo, a)
		}
	}
	m.mu.Unlock()

	for _, p := range m.plan(ctx, active, todo) {
		a := p.airing
		st.Upcoming = append(st.Upcoming, Item{
			ID: a.key, Key: a.key, RuleID: a.rule.ID, Title: a.p.Title, Subtitle: a.p.EpisodeTitle,
			Description: a.p.Description, Image: a.p.Image, Channel: a.ch.Number, CallSign: a.ch.CallSign,
			Start: a.p.Start, End: a.p.End, Status: p.status, Detail: p.detail,
		})
	}
	slices.SortStableFunc(st.Upcoming, func(a, b Item) int { return a.Start.Compare(b.Start) })
	slices.SortStableFunc(st.Recorded, func(a, b Item) int { return b.Start.Compare(a.Start) })
	return st, nil
}

// planned is an airing to come and whether it will get a tuner.
type planned struct {
	airing
	status, detail string
}

// plan says which airings to come will get a tuner: each RF channel
// takes one, whatever is recorded from it, and earlier airings (then
// older rules) come first. Recordings in progress hold theirs.
func (m *Manager) plan(ctx context.Context, active []Rec, todo []airing) []planned {
	type slot struct {
		from, until time.Time
		freq        int64
		title       string
	}
	tuners := m.tuners.Count()
	var taken []slot
	for _, r := range active {
		if freq, _, err := m.locate(ctx, r.Channel); err == nil {
			taken = append(taken, slot{r.From, r.Until, freq, r.Title})
		}
	}
	out := make([]planned, 0, len(todo))
	for _, a := range todo {
		p := planned{airing: a, status: Scheduled}
		freq, _, err := m.locate(ctx, a.ch.Number)
		if err != nil {
			p.status, p.detail = Unavailable, err.Error()
			out = append(out, p)
			continue
		}
		from, until := a.window()
		// The moments the tuners in use could change within the window.
		moments := []time.Time{from}
		for _, s := range taken {
			if s.from.After(from) && s.from.Before(until) {
				moments = append(moments, s.from)
			}
		}
		for _, t := range moments {
			freqs := map[int64]bool{}
			var titles []string
			for _, s := range taken {
				if !s.from.After(t) && s.until.After(t) {
					freqs[s.freq] = true
					titles = append(titles, s.title)
				}
			}
			if !freqs[freq] && len(freqs) >= tuners {
				p.status = Conflict
				slices.Sort(titles)
				p.detail = "Every tuner is recording then: " + strings.Join(slices.Compact(titles), ", ")
				break
			}
		}
		if p.status == Scheduled {
			taken = append(taken, slot{from, until, freq, a.p.Title})
		}
		out = append(out, p)
	}
	return out
}

// item shows a recording. Called with m.mu held.
func (m *Manager) item(r Rec, now time.Time) Item {
	it := Item{
		ID: r.ID, Key: r.Key, RuleID: r.RuleID, Title: r.Title, Subtitle: r.Subtitle, Description: r.Description,
		Image: r.Image, Channel: r.Channel, CallSign: r.CallSign, Start: r.Start, End: r.End,
		Status: r.Status, Detail: r.Detail, SizeBytes: r.Size, Duration: r.Duration(now).Seconds(),
	}
	if r.Status == Recording {
		it.Until = r.Until
		if j := m.jobs[r.ID]; j != nil {
			it.SizeBytes, it.Detail = j.size(), cmp.Or(j.problem(), r.Detail)
		}
	}
	if p, ok := m.st.Progress[r.ID]; ok {
		it.Position, it.Watched = p.Position, p.Watched
	}
	return it
}

// Duration is how long the recording runs: from its first data to its
// last, or to now while it records.
func (r Rec) Duration(now time.Time) time.Duration {
	switch {
	case r.Began.IsZero():
		return 0
	case r.Status == Recording:
		return now.Sub(r.Began)
	case r.Ended.After(r.Began):
		return r.Ended.Sub(r.Began)
	}
	return 0
}

// Get returns a recording and the path of its file.
func (m *Manager) Get(id string) (Rec, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.st.Recordings {
		if r.ID == id {
			return r, filepath.Join(m.dir, r.File), true
		}
	}
	return Rec{}, "", false
}

// Prune drops watch progress of recordings that no longer exist.
func (m *Manager) Prune(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.st.Progress {
		if !slices.ContainsFunc(m.st.Recordings, func(r Rec) bool { return r.ID == id }) {
			delete(m.st.Progress, id)
		}
	}
	return m.save()
}

// SaveProgress records how far a recording has been watched. Reaching the
// last two minutes (or 95%) marks it watched.
func (m *Manager) SaveProgress(id string, position, duration float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.st.Progress[id]
	p.Position, p.Updated = max(position, 0), time.Now()
	if duration > 0 && (position >= duration-120 || position >= duration*0.95) {
		p.Watched = true
	}
	m.st.Progress[id] = p
	return m.save()
}

// MarkWatched sets or clears a recording's watched state; clearing it also
// resets the resume point.
func (m *Manager) MarkWatched(id string, watched bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.st.Progress[id]
	p.Watched, p.Updated = watched, time.Now()
	if !watched {
		p.Position = 0
	}
	m.st.Progress[id] = p
	return m.save()
}

// RuleUpdate changes a series rule.
type RuleUpdate struct {
	Keep    int  `json:"keep"`
	NewOnly bool `json:"newOnly"`
}

// UpdateRule changes how many recordings a series keeps and whether it
// records new episodes only.
func (m *Manager) UpdateRule(id string, u RuleUpdate) (*Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.st.Rules {
		if m.st.Rules[i].ID == id {
			m.st.Rules[i].Keep, m.st.Rules[i].NewOnly = max(u.Keep, 0), u.NewOnly
			r := m.st.Rules[i]
			return &r, m.save()
		}
	}
	return nil, fmt.Errorf("no rule %s", id)
}

// SetPrefs changes library-wide settings.
func (m *Manager) SetPrefs(p Prefs) (Prefs, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p.DeleteWatchedAfterDays = max(p.DeleteWatchedAfterDays, 0)
	m.st.Prefs = p
	return p, m.save()
}

// Retain deletes recordings beyond a series' Keep count (oldest first) and
// watched recordings older than the library's limit.
func (m *Manager) Retain(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	keep := make(map[string]int)
	for _, r := range m.st.Rules {
		if r.Kind == Series && r.Keep > 0 {
			keep[r.ID] = r.Keep
		}
	}
	days := m.st.Prefs.DeleteWatchedAfterDays
	done := slices.DeleteFunc(slices.Clone(m.st.Recordings), func(r Rec) bool { return r.Status == Recording })
	slices.SortFunc(done, func(a, b Rec) int { return b.Start.Compare(a.Start) })
	var remove []string
	seen := make(map[string]int)
	for _, r := range done {
		if keep[r.RuleID] > 0 {
			if seen[r.RuleID]++; seen[r.RuleID] > keep[r.RuleID] {
				remove = append(remove, r.ID)
				continue
			}
		}
		if p := m.st.Progress[r.ID]; days > 0 && p.Watched && time.Since(r.End) > time.Duration(days)*24*time.Hour {
			remove = append(remove, r.ID)
		}
	}
	if len(remove) == 0 {
		return nil
	}
	for _, id := range remove {
		m.remove(id)
	}
	return m.save()
}

// AiringKey identifies an airing: "7.1@1791058239".
func airingKey(channel string, start time.Time) string {
	return channel + "@" + strconv.FormatInt(start.Unix(), 10)
}

// episodeID reports whether a Gracenote ID names one specific episode or
// movie (EP..., MV...), as opposed to a generic show (SH...) or event.
func episodeID(id string) bool { return strings.HasPrefix(id, "EP") || strings.HasPrefix(id, "MV") }

func findChannel(chans []lineup.Channel, number, callSign string) *lineup.Channel {
	var fallback *lineup.Channel
	for i := range chans {
		if chans[i].Number != number || chans[i].GuideID == "" {
			continue
		}
		if chans[i].CallSign == callSign {
			return &chans[i]
		}
		if fallback == nil {
			fallback = &chans[i]
		}
	}
	return fallback
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
