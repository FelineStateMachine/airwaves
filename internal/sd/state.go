package sd

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"airwaves/internal/guide"
)

// state is what an update leaves for the next: the token, the lineup, and
// the listings by station and day with the checksums Schedules Direct
// gave them, so only what changed is fetched again.
type state struct {
	Token        string    `json:"token,omitempty"`
	TokenExpires time.Time `json:"tokenExpires,omitzero"`
	// Stopped says why updating stopped until the account changes.
	Stopped string    `json:"stopped,omitempty"`
	Expires time.Time `json:"expires,omitzero"` // the account
	Updated time.Time `json:"updated,omitzero"`

	Lineup         string    `json:"lineup,omitempty"`
	LineupModified string    `json:"lineupModified,omitempty"`
	Stations       []station `json:"stations,omitempty"`
	// Days holds each station's schedule by date ("2026-10-12", UTC).
	Days map[string]map[string]day `json:"days,omitempty"`
	// Programs are the programs aired, by program ID.
	Programs map[string]program `json:"programs,omitempty"`
	// Art is the picture kept for each series or program, by artKey: its
	// file in the images folder. ArtWanted are those picked and not yet
	// downloaded (their URIs), ArtChecked when each key was last looked
	// up, BadArt URIs Schedules Direct hasn't got, and ImagesPaused when
	// downloads may start again after the daily cap.
	Art          map[string]string    `json:"art,omitempty"`
	ArtWanted    map[string]string    `json:"artWanted,omitempty"`
	ArtChecked   map[string]time.Time `json:"artChecked,omitempty"`
	BadArt       map[string]bool      `json:"badArt,omitempty"`
	ImagesPaused time.Time            `json:"imagesPaused,omitzero"`
}

type station struct {
	ID        string `json:"id"`
	Number    string `json:"number"` // "2.1"
	CallSign  string `json:"callSign"`
	Name      string `json:"name,omitempty"`
	Affiliate string `json:"affiliate,omitempty"`
	// Logos are the station's logo by the background it's drawn for:
	// "light" (the colored one), "gray", "dark" and "white" (white ones);
	// Logo is the one Schedules Direct gives without saying.
	Logos map[string]string `json:"logos,omitempty"`
	Logo  string            `json:"logo,omitempty"`
}

// logoDark is the station's logo drawn for a dark background: white.
func (s station) logoDark() string { return cmp.Or(s.Logos["dark"], s.Logos["white"]) }

// logo is the station's logo for a light background: the colored one,
// else gray, else any.
func (s station) logo() string {
	for _, c := range []string{"light", "gray"} {
		if u := s.Logos[c]; u != "" {
			return u
		}
	}
	if s.Logo != "" {
		return s.Logo
	}
	for _, c := range []string{"white", "dark"} {
		if u := s.Logos[c]; u != "" {
			return u
		}
	}
	return ""
}

// oldLogos reports whether the stations were kept before their logos'
// variants were, so the lineup should be read again for them.
func (st *state) oldLogos() bool {
	for _, s := range st.Stations {
		if len(s.Logos) > 0 {
			return false
		}
	}
	return len(st.Stations) > 0
}

type day struct {
	MD5     string   `json:"md5"`
	Airings []airing `json:"airings"`
}

type airing struct {
	ProgramID string    `json:"programId"`
	Start     time.Time `json:"start"`
	Seconds   int       `json:"seconds"`
	MD5       string    `json:"md5"`
	New       bool      `json:"new,omitempty"`
	Live      bool      `json:"live,omitempty"`
	Premiere  bool      `json:"premiere,omitempty"`
	Finale    bool      `json:"finale,omitempty"`
	Tags      []string  `json:"tags,omitempty"` // CC, Stereo, DVS...
	Rating    string    `json:"rating,omitempty"`
}

type program struct {
	MD5             string   `json:"md5"`
	Title           string   `json:"title"`
	EpisodeTitle    string   `json:"episodeTitle,omitempty"`
	Description     string   `json:"description,omitempty"`
	OriginalAirDate string   `json:"originalAirDate,omitempty"`
	Season          int      `json:"season,omitempty"`
	Episode         int      `json:"episode,omitempty"`
	Genres          []string `json:"genres,omitempty"`
	Kind            string   `json:"kind,omitempty"` // Show, Episode, Sports, Movie
	Cast            []string `json:"cast,omitempty"`
	Year            int      `json:"year,omitempty"`
	Runtime         int      `json:"runtime,omitempty"` // minutes
	Stars           float64  `json:"stars,omitempty"`
	Rating          string   `json:"rating,omitempty"`
	HomeTeam        string   `json:"homeTeam,omitempty"`
	AwayTeam        string   `json:"awayTeam,omitempty"`
	Art             bool     `json:"art,omitempty"` // Schedules Direct has pictures of it
}

const stateVersion = 1

type stateFile struct {
	Version int `json:"version"`
	*state
}

// loadState reads the state kept at path; a missing or unreadable file is
// a fresh start (an unreadable one is set aside as path.bad).
func loadState(path string) (*state, error) {
	f := stateFile{state: &state{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f.state, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &f)
	}
	if err != nil || f.Version != stateVersion {
		_ = os.Rename(path, path+".bad")
		if err == nil {
			err = fmt.Errorf("listings saved by another version")
		}
		return &state{}, fmt.Errorf("%s: %w", path, err)
	}
	return f.state, nil
}

func (st *state) save(path string) error {
	raw, err := json.Marshal(stateFile{Version: stateVersion, state: st})
	if err != nil {
		return err
	}
	return writeFile(path, raw, 0o600)
}

// clone copies st for an update to change, so a failed update leaves the
// listings as they were.
func (st *state) clone() *state {
	c := *st
	c.Stations = slices.Clone(st.Stations)
	c.Days = make(map[string]map[string]day, len(st.Days))
	for id, days := range st.Days {
		c.Days[id] = maps.Clone(days)
	}
	c.Programs = maps.Clone(st.Programs)
	c.Art, c.ArtWanted, c.ArtChecked, c.BadArt = maps.Clone(st.Art), maps.Clone(st.ArtWanted), maps.Clone(st.ArtChecked), maps.Clone(st.BadArt)
	if c.Programs == nil {
		c.Programs = map[string]program{}
	}
	if c.Art == nil {
		c.Art = map[string]string{}
	}
	if c.ArtWanted == nil {
		c.ArtWanted = map[string]string{}
	}
	return &c
}

// prune drops days before yesterday, and programs and pictures nothing
// airs any more.
func (st *state) prune(now time.Time) {
	oldest := now.UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	used := map[string]bool{}
	for id, days := range st.Days {
		for date, d := range days {
			if date < oldest {
				delete(days, date)
				continue
			}
			for _, a := range d.Airings {
				used[a.ProgramID] = true
			}
		}
		if len(days) == 0 {
			delete(st.Days, id)
		}
	}
	for id := range st.Programs {
		if !used[id] {
			delete(st.Programs, id)
		}
	}
	// Pictures are kept (they're files, and a series comes back); what
	// was only being looked up for programs gone is dropped.
	keys := map[string]bool{}
	for id := range used {
		keys[artKey(id)] = true
	}
	for k := range st.ArtWanted {
		if !keys[k] {
			delete(st.ArtWanted, k)
		}
	}
	for k, t := range st.ArtChecked {
		if !keys[k] && now.Sub(t) > artRecheck {
			delete(st.ArtChecked, k)
		}
	}
}

// SeriesID is a program's series as the guide's grid gives it, so series
// recording rules carry over: "SH" and the series number, for episodes and
// shows; "" for movies and the like.
func SeriesID(programID string) string {
	if len(programID) < 10 || !strings.HasPrefix(programID, "EP") && !strings.HasPrefix(programID, "SH") {
		return ""
	}
	return "SH" + programID[2:10]
}

// guide is the listings as the guide has them, from a day before now on;
// nil while there are none.
func (st *state) guide(zip string, now time.Time) *guide.Guide {
	if len(st.Stations) == 0 || len(st.Days) == 0 {
		return nil
	}
	g := &guide.Guide{Lineup: st.Lineup, Fetched: st.Updated, Programs: map[string][]guide.Program{}}
	from := now.Add(-24 * time.Hour)
	for _, sta := range st.Stations {
		g.Channels = append(g.Channels, guide.Channel{
			ID: sta.ID, Number: sta.Number, CallSign: sta.CallSign, Network: guide.NetworkName(sta.Affiliate), Logo: sta.logo(), LogoDark: sta.logoDark(),
		})
		for _, d := range st.Days[sta.ID] {
			for _, a := range d.Airings {
				end := a.Start.Add(time.Duration(a.Seconds) * time.Second)
				if !end.After(from) {
					continue
				}
				g.Programs[sta.ID] = append(g.Programs[sta.ID], st.program(a, end))
			}
		}
	}
	slices.SortFunc(g.Channels, func(a, b guide.Channel) int { return guide.CompareNumbers(a.Number, b.Number) })
	for id, ps := range g.Programs {
		slices.SortFunc(ps, func(a, b guide.Program) int { return a.Start.Compare(b.Start) })
		// Days overlap at midnight: an airing listed on both stays once.
		g.Programs[id] = slices.CompactFunc(ps, func(a, b guide.Program) bool { return a.Start.Equal(b.Start) })
		if n := len(ps); n > 0 {
			g.Start = cmp.Or(minTime(g.Start, ps[0].Start), ps[0].Start)
			g.End = maxTime(g.End, ps[n-1].End)
		}
	}
	return g
}

func (st *state) program(a airing, end time.Time) guide.Program {
	p := st.Programs[a.ProgramID]
	out := guide.Program{
		Start: a.Start, End: end, Title: cmp.Or(p.Title, "To be announced"), EpisodeTitle: p.EpisodeTitle,
		Description: p.Description, Rating: cmp.Or(a.Rating, p.Rating), Tags: a.Tags,
		ProgramID: a.ProgramID, SeriesID: SeriesID(a.ProgramID), OriginalAirDate: p.OriginalAirDate, Cast: p.Cast,
		Runtime: p.Runtime, Stars: p.Stars, HomeTeam: p.HomeTeam, AwayTeam: p.AwayTeam, Genres: genres(a.ProgramID, p),
	}
	if p.Season > 0 && p.Episode > 0 {
		out.Season, out.Episode = strconv.Itoa(p.Season), strconv.Itoa(p.Episode)
	}
	if p.Year > 0 {
		out.Year = strconv.Itoa(p.Year)
	}
	for flag, on := range map[string]bool{"New": a.New, "Live": a.Live, "Premiere": a.Premiere, "Finale": a.Finale} {
		if on {
			out.Flags = append(out.Flags, flag)
		}
	}
	slices.Sort(out.Flags)
	if img := st.Art[artKey(a.ProgramID)]; img != "" {
		out.Image = ArtPath + img
	}
	return out
}

// filterGenres maps Schedules Direct's genres to the guide's filters.
var filterGenres = map[string][]string{
	"news": {"news"}, "sports event": {"sports"}, "sports non-event": {"sports"}, "sports talk": {"sports"},
	"children": {"kids", "family"}, "family": {"family"}, "animated": {"family"}, "talk": {"talk"},
	"documentary": {"documentary"}, "music": {"music"}, "musical": {"music"},
}

// genres are a program's genres: the guide's filter keys ("sports",
// "movie", "news"...), then the rest as Schedules Direct names them.
func genres(id string, p program) []string {
	var out []string
	seen := map[string]bool{}
	add := func(g string) {
		if k := strings.ToLower(g); g != "" && !seen[k] {
			seen[k] = true
			out = append(out, g)
		}
	}
	if strings.HasPrefix(id, "MV") || p.Kind == "Movie" {
		add("movie")
	}
	if strings.HasPrefix(id, "SP") || p.Kind == "Sports" {
		add("sports")
	}
	for _, g := range p.Genres {
		for _, k := range filterGenres[strings.ToLower(g)] {
			add(k)
		}
	}
	for _, g := range p.Genres {
		add(g)
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
