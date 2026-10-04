// Package dvr turns recording rules into timed Tvheadend recordings.
//
// Airwaves owns the rules and the guide (Gracenote, with series IDs and
// "new episode" flags); Tvheadend owns the tuners and writes the files.
// Reconcile keeps Tvheadend's schedule matching the rules.
package dvr

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/tvh"
)

// Kind is the type of rule.
type Kind string

// Rule kinds.
const (
	Once   Kind = "once"
	Series Kind = "series"
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

// Item is a recording as shown to the user.
type Item struct {
	ID          string    `json:"id"` // Tvheadend entry UUID; empty if not scheduled
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
	// Status is scheduled, recording, completed, failed or unavailable.
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	// Duration of the recorded file, Position the resume point and Watched
	// whether it has been seen, all for finished recordings.
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

// meta remembers guide details of an airing Airwaves scheduled, for
// artwork and episode de-duplication after the guide has moved on.
type meta struct {
	RuleID    string `json:"ruleId"`
	Channel   string `json:"channel"`
	CallSign  string `json:"callSign"`
	Image     string `json:"image,omitempty"`
	ProgramID string `json:"programId,omitempty"`
}

type fileState struct {
	Rules    []Rule              `json:"rules"`
	Meta     map[string]meta     `json:"meta"`
	Progress map[string]Progress `json:"progress"` // by Tvheadend entry UUID
	Prefs    Prefs               `json:"prefs"`
}

// ChannelResolver maps a virtual channel to a Tvheadend channel UUID.
type ChannelResolver interface {
	ChannelUUID(ctx context.Context, number string) (string, error)
}

// Manager owns the rules.
type Manager struct {
	tvh      *tvh.Client
	channels ChannelResolver
	path     string

	mu sync.Mutex
	st fileState
}

// Open loads rules from path (created on first save).
func Open(path string, c *tvh.Client, channels ChannelResolver) (*Manager, error) {
	m := &Manager{tvh: c, channels: channels, path: path, st: fileState{Meta: map[string]meta{}, Progress: map[string]Progress{}}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m.st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.st.Meta == nil {
		m.st.Meta = map[string]meta{}
	}
	if m.st.Progress == nil {
		m.st.Progress = map[string]Progress{}
	}
	return m, nil
}

func (m *Manager) save() error {
	raw, err := json.MarshalIndent(m.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// Add creates a rule from a guide selection and schedules it.
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
	return &r, m.Reconcile(ctx, g, chans)
}

// DeleteRule removes a rule and any of its recordings that have not started.
func (m *Manager) DeleteRule(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.st.Rules, func(r Rule) bool { return r.ID == id })
	if i < 0 {
		return fmt.Errorf("no rule %s", id)
	}
	m.st.Rules = slices.Delete(m.st.Rules, i, i+1)
	if err := m.save(); err != nil {
		return err
	}
	upcoming, err := m.tvh.Upcoming(ctx)
	if err != nil {
		return err
	}
	for _, e := range upcoming {
		if rule, _, ok := parseComment(e.Comment); ok && rule == id && e.SchedStatus == "scheduled" {
			if err := m.tvh.DeleteScheduled(ctx, e.UUID); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteEntry removes one recording: a scheduled airing is skipped (and
// stays skipped for series rules), an active one is cancelled, a finished
// one is deleted with its file.
func (m *Manager) DeleteEntry(ctx context.Context, uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	upcoming, err := m.tvh.Upcoming(ctx)
	if err != nil {
		return err
	}
	for _, e := range upcoming {
		if e.UUID != uuid {
			continue
		}
		if rule, key, ok := parseComment(e.Comment); ok {
			for i := range m.st.Rules {
				if m.st.Rules[i].ID == rule {
					m.st.Rules[i].Skip = append(m.st.Rules[i].Skip, key)
				}
			}
			if err := m.save(); err != nil {
				return err
			}
		}
		if e.SchedStatus == "recording" {
			return m.tvh.Cancel(ctx, uuid)
		}
		return m.tvh.DeleteScheduled(ctx, uuid)
	}
	return m.tvh.Remove(ctx, uuid)
}

type airing struct {
	key  string
	rule *Rule
	p    guide.Program
	ch   lineup.Channel
}

// Reconcile schedules what the rules want and unschedules what they no
// longer want. Safe to call often.
func (m *Manager) Reconcile(ctx context.Context, g *guide.Guide, chans []lineup.Channel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()

	upcoming, err := m.tvh.Upcoming(ctx)
	if err != nil {
		return err
	}
	finished, err := m.tvh.Finished(ctx)
	if err != nil {
		return err
	}
	scheduled := make(map[string]tvh.Entry)
	for _, e := range upcoming {
		if _, key, ok := parseComment(e.Comment); ok {
			scheduled[key] = e
		}
	}
	recordedPrograms := make(map[string]bool)
	for _, e := range finished {
		if _, key, ok := parseComment(e.Comment); ok && e.ErrorCode == 0 {
			if id := m.st.Meta[key].ProgramID; episodeID(id) {
				recordedPrograms[id] = true
			}
		}
	}

	// Once rules whose airing is over are done; Tvheadend keeps the file.
	m.st.Rules = slices.DeleteFunc(m.st.Rules, func(r Rule) bool { return r.Kind == Once && r.End.Before(now) })

	want := m.desired(g, chans, now, recordedPrograms)
	wantKeys := make(map[string]bool, len(want))
	for _, a := range want {
		wantKeys[a.key] = true
		if _, ok := scheduled[a.key]; ok {
			continue
		}
		uuid, err := m.channels.ChannelUUID(ctx, a.ch.Number)
		if err != nil {
			continue // shown as unavailable in State
		}
		stopExtra := 3
		if slices.Contains(a.p.Genres, "sports") {
			stopExtra = 30 // live sports overrun
		}
		_, err = m.tvh.CreateEntry(ctx, tvh.NewEntry{
			Channel: uuid, Start: a.p.Start.Unix(), Stop: a.p.End.Unix(),
			Title: a.p.Title, Subtitle: a.p.EpisodeTitle, Description: a.p.Description,
			Comment: comment(a.rule.ID, a.key), StartExtra: 1, StopExtra: stopExtra,
		})
		if err != nil {
			return err
		}
		m.st.Meta[a.key] = meta{RuleID: a.rule.ID, Channel: a.ch.Number, CallSign: a.ch.CallSign, Image: a.p.Image, ProgramID: a.p.ProgramID}
	}
	for key, e := range scheduled {
		if !wantKeys[key] && e.SchedStatus == "scheduled" {
			if err := m.tvh.DeleteScheduled(ctx, e.UUID); err != nil {
				return err
			}
		}
	}
	return m.save()
}

// desired lists the airings the rules call for, earliest first.
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
			ch := lineup.Channel{Number: r.Channel, CallSign: r.CallSign, GuideID: r.GuideID}
			if key := airingKey(r.Channel, r.Start); !slices.Contains(r.Skip, key) {
				out = append(out, airing{key: key, rule: r, p: p, ch: ch})
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
	slices.SortFunc(out, func(a, b airing) int { return a.p.Start.Compare(b.p.Start) })
	return out
}

// State describes rules, schedule and library for the UI.
func (m *Manager) State(ctx context.Context, g *guide.Guide, chans []lineup.Channel) (*State, error) {
	if _, err := m.tvh.ServerInfo(ctx); err != nil {
		return &State{Reason: "Tvheadend is not reachable: " + err.Error()}, nil
	}
	upcoming, err := m.tvh.Upcoming(ctx)
	if err != nil {
		return nil, err
	}
	finished, err := m.tvh.Finished(ctx)
	if err != nil {
		return nil, err
	}
	failed, err := m.tvh.Failed(ctx)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	st := &State{Available: true, Rules: slices.Clone(m.st.Rules), Prefs: m.st.Prefs}
	scheduledKeys := make(map[string]bool)
	for _, e := range upcoming {
		it := m.item(e)
		scheduledKeys[it.Key] = true
		st.Upcoming = append(st.Upcoming, it)
	}
	// Airings the rules want but Tvheadend cannot tune yet.
	for _, a := range m.desired(g, chans, time.Now(), nil) {
		if scheduledKeys[a.key] {
			continue
		}
		st.Upcoming = append(st.Upcoming, Item{
			Key: a.key, RuleID: a.rule.ID, Title: a.p.Title, Subtitle: a.p.EpisodeTitle,
			Description: a.p.Description, Image: a.p.Image, Channel: a.ch.Number, CallSign: a.ch.CallSign,
			Start: a.p.Start, End: a.p.End, Status: "unavailable",
			Detail: "Channel " + a.ch.Number + " is not tuned on the server",
		})
	}
	for _, e := range finished {
		st.Recorded = append(st.Recorded, m.item(e))
	}
	for _, e := range failed {
		it := m.item(e)
		it.Status = "failed"
		st.Recorded = append(st.Recorded, it)
	}
	slices.SortFunc(st.Upcoming, func(a, b Item) int { return a.Start.Compare(b.Start) })
	slices.SortFunc(st.Recorded, func(a, b Item) int { return b.Start.Compare(a.Start) })
	return st, nil
}

func (m *Manager) item(e tvh.Entry) Item {
	it := Item{
		ID: e.UUID, Title: e.Title, Subtitle: e.Subtitle, Description: e.Description,
		Channel: e.ChannelName, Start: time.Unix(e.Start, 0), End: time.Unix(e.Stop, 0),
		Status: e.SchedStatus, Detail: e.Status, SizeBytes: e.FileSize, Duration: fileDuration(e),
	}
	if p, ok := m.st.Progress[e.UUID]; ok {
		it.Position, it.Watched = p.Position, p.Watched
	}
	if strings.HasPrefix(it.Status, "completed") {
		it.Status = "completed"
	}
	if rule, key, ok := parseComment(e.Comment); ok {
		md := m.st.Meta[key]
		it.Key, it.RuleID, it.Image = key, rule, md.Image
		if md.Channel != "" {
			it.Channel, it.CallSign = md.Channel, md.CallSign
		}
	}
	return it
}

// Prune drops metadata for recordings that no longer exist.
func (m *Manager) Prune(ctx context.Context) error {
	upcoming, err := m.tvh.Upcoming(ctx)
	if err != nil {
		return err
	}
	finished, err := m.tvh.Finished(ctx)
	if err != nil {
		return err
	}
	failed, err := m.tvh.Failed(ctx)
	if err != nil {
		return err
	}
	live := make(map[string]bool)
	liveIDs := make(map[string]bool)
	for _, e := range slices.Concat(upcoming, finished, failed) {
		liveIDs[e.UUID] = true
		if _, key, ok := parseComment(e.Comment); ok {
			live[key] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.st.Meta {
		if !live[key] {
			delete(m.st.Meta, key)
		}
	}
	for id := range m.st.Progress {
		if !liveIDs[id] {
			delete(m.st.Progress, id)
		}
	}
	return m.save()
}

// fileDuration is how long the recorded file runs, padding included.
func fileDuration(e tvh.Entry) float64 {
	if e.StopReal > e.StartReal && e.StartReal > 0 {
		return float64(e.StopReal - e.StartReal)
	}
	return float64(e.Stop - e.Start)
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
func (m *Manager) Retain(ctx context.Context) error {
	finished, err := m.tvh.Finished(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	keep := make(map[string]int)
	for _, r := range m.st.Rules {
		if r.Kind == Series && r.Keep > 0 {
			keep[r.ID] = r.Keep
		}
	}
	days := m.st.Prefs.DeleteWatchedAfterDays
	progress := maps.Clone(m.st.Progress)
	m.mu.Unlock()

	slices.SortFunc(finished, func(a, b tvh.Entry) int { return cmp.Compare(b.Start, a.Start) })
	var remove []string
	seen := make(map[string]int)
	for _, e := range finished {
		if rule, _, ok := parseComment(e.Comment); ok && keep[rule] > 0 {
			seen[rule]++
			if seen[rule] > keep[rule] {
				remove = append(remove, e.UUID)
				continue
			}
		}
		if p := progress[e.UUID]; days > 0 && p.Watched && time.Since(time.Unix(e.Stop, 0)) > time.Duration(days)*24*time.Hour {
			remove = append(remove, e.UUID)
		}
	}
	for _, id := range remove {
		if err := m.tvh.Remove(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

const commentPrefix = "airwaves|"

func comment(ruleID, key string) string { return commentPrefix + ruleID + "|" + key }

func parseComment(c string) (rule, key string, ok bool) {
	rest, ok := strings.CutPrefix(c, commentPrefix)
	if !ok {
		return "", "", false
	}
	rule, key, ok = strings.Cut(rest, "|")
	return rule, key, ok
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
