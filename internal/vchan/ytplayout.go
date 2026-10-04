package vchan

import (
	"cmp"
	"encoding/json"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ytAiring is one video on a YouTube channel's schedule.
type ytAiring struct {
	ID      string    `json:"id"`
	Channel string    `json:"channel"` // the YouTube channel's ID
	Title   string    `json:"title"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	First   bool      `json:"first,omitempty"` // a new upload's first run
}

// ytPlayout is a YouTube channel's schedule, kept in the channel's folder
// so the guide holds across restarts: planned two days ahead and only
// ever added to, except that new uploads may move airings beyond the lock
// window (a couple of hours) to go first, and that changing the channel's
// settings plans it again from now. Past airings are the history the
// repeat guard reads.
type ytPlayout struct {
	// Basis sums up the settings the schedule was planned with.
	Basis   string     `json:"basis,omitempty"`
	Airings []ytAiring `json:"airings"` // back to back, by start
}

const (
	ytPlanAhead = 48 * time.Hour
	// ytKeepPast is the history kept even without a repeat guard, for the
	// guide's look back.
	ytKeepPast = 2 * 24 * time.Hour
	// ytResume is how long after the schedule runs out it carries on
	// from its end rather than from now.
	ytResume = 5 * time.Minute
	// Rerun mixes count ages up to these as recent, and as fairly recent.
	ytHalfYear = 182 * 24 * time.Hour
	ytTwoYears = 730 * 24 * time.Hour
)

// ytPool is what the planner picks from.
type ytPool struct {
	Sources []ytPoolSource // in config order
	// Firsts are new uploads due a first run, newest first.
	Firsts []ytPick
	Repeat time.Duration
	// Lock is how far ahead new uploads leave the schedule as it is.
	Lock time.Duration
	// Mix is the shares of reruns picked from the last six months, the
	// last two years and any time, as in ytRerunMixes.
	Mix [3]float64
	// DeadAir leaves gaps rather than repeat a video within Repeat.
	DeadAir bool
	Seed    uint64
	Basis   string
	// ids are the videos that may stay on the schedule: every playable
	// one, including those too old to be picked now.
	ids map[string]bool
}

type ytPoolSource struct {
	ID     string     // the YouTube channel's ID
	Videos []*ytVideo // newest first
	// HalfYear and TwoYears are the videos uploaded in the last six
	// months and the last two years (see ytAges).
	HalfYear, TwoYears []*ytVideo
}

// ytByAge picks out a channel's videos (newest first) uploaded in the last
// six months and in the last two years, by their ytAges.
func ytByAge(videos []*ytVideo, now time.Time) (halfYear, twoYears []*ytVideo) {
	for i, age := range ytAges(videos, now) {
		if age <= ytHalfYear {
			halfYear = append(halfYear, videos[i])
		}
		if age <= ytTwoYears {
			twoYears = append(twoYears, videos[i])
		}
	}
	return halfYear, twoYears
}

// ytAges are how long ago a channel's videos (newest first) were
// uploaded. A video without a date takes the date of its nearest dated
// neighbour in the list, the newer of two as near. In a list without
// dates, rank stands in: the newest 3% count as uploaded now, the next 9%
// as just over six months ago, and the rest as long ago.
func ytAges(videos []*ytVideo, now time.Time) []time.Duration {
	var dated []int
	for i, v := range videos {
		if !v.Published.IsZero() {
			dated = append(dated, i)
		}
	}
	ages := make([]time.Duration, len(videos))
	for i := range videos {
		age := time.Duration(math.MaxInt64)
		switch {
		case len(dated) > 0:
			j, _ := slices.BinarySearch(dated, i)
			near := -1
			if j < len(dated) {
				near = dated[j]
			}
			if j > 0 && (near < 0 || i-dated[j-1] <= near-i) {
				near = dated[j-1]
			}
			age = now.Sub(videos[near].Published)
		case i < len(videos)*3/100:
			age = 0
		case i < len(videos)*12/100:
			age = ytHalfYear + 1
		}
		ages[i] = age
	}
	return ages
}

type ytPick struct {
	Video   *ytVideo
	Channel string
}

func (p *ytPool) has(id string) bool {
	if p.ids == nil {
		p.ids = map[string]bool{}
		for _, s := range p.Sources {
			for _, v := range s.Videos {
				p.ids[v.ID] = true
			}
		}
	}
	return p.ids[id]
}

func readYtPlayout(path string) (ytPlayout, error) {
	var p ytPlayout
	raw, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p)
	return p, err
}

// end is when the next airing starts: after the last, or now when the
// schedule ran out a while ago.
func (p *ytPlayout) end(now time.Time) time.Time {
	if n := len(p.Airings); n > 0 && p.Airings[n-1].End.After(now.Add(-ytResume)) {
		return p.Airings[n-1].End
	}
	return now.UTC().Truncate(time.Minute)
}

// at returns the airing on at t and the one after it.
func (p *ytPlayout) at(t time.Time) (on, next ytAiring, ok bool) {
	i, _ := slices.BinarySearchFunc(p.Airings, t, func(a ytAiring, t time.Time) int {
		if !a.End.After(t) {
			return -1
		}
		if a.Start.After(t) {
			return 1
		}
		return 0
	})
	if i >= len(p.Airings) || p.Airings[i].Start.After(t) {
		return ytAiring{}, ytAiring{}, false
	}
	if i+1 < len(p.Airings) {
		next = p.Airings[i+1]
	}
	return p.Airings[i], next, true
}

// update brings the schedule up to date at now and reports whether it
// changed. History older than the repeat guard needs is dropped. When the
// pool's basis (the channel's settings) changed, everything from now is
// planned again, what's on included. Otherwise the part beyond the lock
// is replanned when first runs are due, which go first, newest first,
// with the reruns planned there after them, or when it holds videos that
// can no longer play. Then reruns are added until the schedule reaches
// two days ahead.
func (p *ytPlayout) update(pool *ytPool, now time.Time) bool {
	changed := false
	i := slices.IndexFunc(p.Airings, func(a ytAiring) bool {
		return a.End.After(now.Add(-max(pool.Repeat, ytKeepPast)))
	})
	if i < 0 {
		i = len(p.Airings)
	}
	if i > 0 {
		p.Airings, changed = slices.Clone(p.Airings[i:]), true
	}

	fresh := p.Basis != pool.Basis
	var head, tail []ytAiring
	if fresh {
		// What's on is cut short, and stays with what aired.
		cut := now.UTC().Truncate(time.Second)
		k := slices.IndexFunc(p.Airings, func(a ytAiring) bool { return a.End.After(cut) })
		if k < 0 {
			k = len(p.Airings)
		}
		head = slices.Clone(p.Airings[:k])
		if k < len(p.Airings) && p.Airings[k].Start.Before(cut) {
			a := p.Airings[k]
			a.End = cut
			head = append(head, a)
		}
	} else {
		lock := now.Add(pool.Lock)
		k := slices.IndexFunc(p.Airings, func(a ytAiring) bool { return !a.Start.Before(lock) })
		if k < 0 {
			k = len(p.Airings)
		}
		head, tail = p.Airings[:k], slices.Clone(p.Airings[k:])
	}
	locked := map[string]bool{}
	for _, a := range head {
		locked[a.ID] = true
	}
	var firsts []ytPick
	for _, f := range pool.Firsts {
		if !locked[f.Video.ID] {
			firsts = append(firsts, f)
		}
	}
	replan := fresh || slices.ContainsFunc(tail, func(a ytAiring) bool { return !pool.has(a.ID) })
	for i, f := range firsts {
		// Due first runs lead the unlocked part, newest first.
		if i >= len(tail) || tail[i].ID != f.Video.ID || !tail[i].First {
			replan = true
			break
		}
	}
	if replan {
		k := len(head)
		p.Airings, p.Basis = slices.Clone(head), pool.Basis
		for _, f := range firsts {
			p.add(f.Video, f.Channel, true, now)
		}
		for _, a := range tail {
			if !a.First && pool.has(a.ID) {
				p.add(&ytVideo{ID: a.ID, Title: a.Title, Seconds: int(a.End.Sub(a.Start) / time.Second)}, a.Channel, false, now)
			}
		}
		changed = changed || fresh || !sameAirings(p.Airings[k:], tail)
	}

	if p.end(now).Before(now.Add(ytPlanAhead - time.Hour)) {
		g := newYtGuard(p.Airings, pool)
		for p.end(now).Before(now.Add(ytPlanAhead)) {
			f, ok := g.pick(p.end(now))
			if !ok || f.Video.Seconds <= 0 {
				break
			}
			g.note(p.add(f.Video, f.Channel, false, now))
			changed = true
		}
	}
	return changed
}

func (p *ytPlayout) add(v *ytVideo, channel string, first bool, now time.Time) ytAiring {
	start := p.end(now)
	a := ytAiring{ID: v.ID, Channel: channel, Title: v.Title, Start: start,
		End: start.Add(time.Duration(v.Seconds) * time.Second), First: first}
	p.Airings = append(p.Airings, a)
	return a
}

func sameAirings(a, b []ytAiring) bool {
	return slices.EqualFunc(a, b, func(x, y ytAiring) bool {
		return x.ID == y.ID && x.Start.Equal(y.Start) && x.End.Equal(y.End) && x.First == y.First
	})
}

// ytGuard picks reruns: the channels take turns, and each picks a video at
// random that hasn't aired within the repeat window, or failing that the
// one that aired longest ago (with dead air, a channel without a rested
// video sits out instead), preferring one of a different series than
// the last few airings. The pool's mix says how often to pick from the
// last six months, the last two years, or any time; when nothing there
// is rested, the pick looks further back.
type ytGuard struct {
	pool    *ytPool
	aired   map[string]time.Time // last start by video
	turn    map[string]time.Time // last start by channel
	recent  []string             // series of the latest airings
	sources []ytPoolSource
}

func newYtGuard(history []ytAiring, pool *ytPool) *ytGuard {
	g := &ytGuard{pool: pool, aired: map[string]time.Time{}, turn: map[string]time.Time{}}
	for _, s := range pool.Sources {
		if len(s.Videos) > 0 {
			g.sources = append(g.sources, s)
		}
	}
	for _, a := range history {
		g.note(a)
	}
	return g
}

func (g *ytGuard) note(a ytAiring) {
	g.aired[a.ID], g.turn[a.Channel] = a.Start, a.Start
	g.recent = append(g.recent, series(a.Title))
	if len(g.recent) > 3 {
		g.recent = g.recent[1:]
	}
}

func (g *ytGuard) pick(at time.Time) (ytPick, bool) {
	rested := func(v *ytVideo) bool {
		t, ok := g.aired[v.ID]
		return !ok || at.Sub(t) >= g.pool.Repeat
	}
	// The channel that aired least lately goes next; config order breaks
	// ties. With dead air, only channels with a rested video take turns,
	// and with none, nothing airs.
	var src *ytPoolSource
	for i := range g.sources {
		s := &g.sources[i]
		if g.pool.DeadAir && !slices.ContainsFunc(s.Videos, rested) {
			continue
		}
		if src == nil || g.turn[s.ID].Before(g.turn[src.ID]) {
			src = s
		}
	}
	if src == nil {
		return ytPick{}, false
	}
	rng := rand.New(rand.NewPCG(g.pool.Seed, uint64(at.Unix())))
	different := func(v *ytVideo) bool {
		s := series(v.Title)
		return s == "" || !slices.Contains(g.recent, s)
	}
	ages := [][]*ytVideo{src.HalfYear, src.TwoYears, src.Videos}
	from, r := 0, rng.Float64()
	for from < 2 && r >= g.pool.Mix[from] {
		r -= g.pool.Mix[from]
		from++
	}
	for _, vs := range ages[from:] {
		if v := pickRested(vs, rng, rested, different); v != nil {
			return ytPick{v, src.ID}, true
		}
	}
	oldest := slices.MinFunc(src.Videos, func(a, b *ytVideo) int { return g.aired[a.ID].Compare(g.aired[b.ID]) })
	return ytPick{oldest, src.ID}, true
}

// pickRested picks a rested video from vs at random, of a different series
// if it can, or nil.
func pickRested(vs []*ytVideo, rng *rand.Rand, rested, different func(*ytVideo) bool) *ytVideo {
	if len(vs) == 0 {
		return nil
	}
	for range 40 {
		if v := vs[rng.IntN(len(vs))]; rested(v) && different(v) {
			return v
		}
	}
	var ok []*ytVideo
	for _, v := range vs {
		if rested(v) {
			ok = append(ok, v)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	return ok[rng.IntN(len(ok))]
}

var (
	// seriesNoise is what changes between a series' titles: "[Episode 4]",
	// "(2/2)", "(Episode 15)", "#23".
	seriesNoise = regexp.MustCompile(`(?i)\s*(\[[^\]]*\]|\((\d+/\d+|(episode|ep\.?|part)\s*\d+)\)|#\d+)`)
	episodePart = regexp.MustCompile(`(?i)^(episode|ep\.?|part)\s*\d+$`)
	genericPart = regexp.MustCompile(`(?i)^(season\s*\d+|let'?s play|northernlion plays)$`)
)

// series guesses a video's series or game from its title, so reruns of
// one don't run back to back: "(Slay the Spire 2)" ending a title, what
// follows " | ", the part before "Episode 12" in "Game - Let's Play -
// Episode 12", or the last part of "Topic - Bits and Banter". Titles
// without such parts are their own series.
func series(title string) string {
	t := strings.TrimSpace(seriesNoise.ReplaceAllString(title, ""))
	if strings.HasSuffix(t, ")") {
		if i := strings.LastIndex(t, "("); i > 0 {
			return strings.ToLower(strings.TrimSpace(t[i+1 : len(t)-1]))
		}
	}
	if i := strings.LastIndex(t, " | "); i >= 0 {
		t = t[i+3:]
	}
	parts := strings.Split(t, " - ")
	i := len(parts) - 1
	if e := slices.IndexFunc(parts, func(s string) bool { return episodePart.MatchString(strings.TrimSpace(s)) }); e >= 0 {
		i = e - 1
	}
	for i > 0 && genericPart.MatchString(strings.TrimSpace(parts[i])) {
		i--
	}
	return strings.ToLower(strings.TrimSpace(parts[max(i, 0)]))
}

// newestFirst orders first runs.
func newestFirst(a, b ytPick) int {
	return cmp.Or(
		b.Video.Published.Compare(a.Video.Published),
		b.Video.Found.Compare(a.Video.Found),
		strings.Compare(a.Video.ID, b.Video.ID))
}
