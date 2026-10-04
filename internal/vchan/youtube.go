package vchan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/web"
)

// YouTube is a channel of uploads from YouTube channels, set up by a
// youtube.json in its folder, and run like TV: a schedule two days ahead
// that holds across restarts, new uploads airing as first runs ahead of
// reruns, and a guard against repeats. Nothing is downloaded: the channel
// keeps only metadata (a catalog and the schedule, hidden files in its
// folder), and streams the video on air from YouTube while someone
// watches.
type YouTube struct {
	Num    string
	Title  string
	Dir    string
	FFmpeg string
	YtDlp  *YtDlp // yt-dlp from PATH when nil
	// Loudness is the level the videos' sound is evened out to, in LUFS;
	// 0 leaves it as it is.
	Loudness float64

	mu       sync.Mutex
	loaded   bool
	cfg      ytConfig
	err      error     // the config's
	cfgMod   time.Time // youtube.json's modification time when read
	cat      ytCatalog
	plan     ytPlayout
	pool     *ytPool             // nil after the catalog changes
	poolAt   time.Time           // when pool was built
	videos   map[string]*ytVideo // the catalog's, by ID; built with pool, or seq
	names    map[string]string   // channel names by ID
	seq      []*ytVideo          // a playlist's that can play, in order; nil after the catalog changes
	lens     []time.Duration     // their lengths
	relist   bool                // a playlist is due a listing, its config edited
	catDirty bool
	catSaved time.Time
	busy     bool                  // a refresh is running
	tried    map[string]time.Time  // failed listings, by URL
	feedDown map[string]string     // failing feeds by URL: how they're stood in for
	resolved map[string]ytResolved // found ahead, by video ID
	ahead    map[string]*ytAhead   // being found ahead
	logged   string                // the last save error logged
	now      func() time.Time
	record   recordFile
}

type ytResolved struct {
	info ytInfo
	at   time.Time
}

type ytAhead struct {
	started bool          // asking yt-dlp, rather than waiting to
	done    chan struct{} // closed at the end
}

const (
	ytCatalogFile = ".catalog.json"
	ytPlayoutFile = ".playout.json"
	ytListEvery   = 24 * time.Hour   // a full listing of each channel
	ytFeedEvery   = 15 * time.Minute // a look at its feed for new uploads
	ytRetryList   = time.Hour
	// Lookups of single videos' details (new uploads' lengths, and what's
	// coming up's descriptions) are rationed per refresh.
	ytLookups       = 4
	ytDescribeAhead = 8 * time.Hour
	ytResolveTime   = 45 * time.Second
)

// ytLookupGap spaces out lookups of single videos.
var ytLookupGap = 3 * time.Second

// Number implements Channel.
func (y *YouTube) Number() string { return y.Num }

// Name implements Channel.
func (y *YouTube) Name() string { return y.Title }

// Details implements Channel, from channel.json in the folder.
func (y *YouTube) Details() Details { return folderDetails(&y.record, y.Dir, y.Num, y.Title) }

// Empty reports whether nothing is on: until the channels are first
// listed, or when none has videos that fit. A channel with dead air stays
// in the lineup through it once it has aired, and a playlist with videos
// to play stays in it while off the air.
func (y *YouTube) Empty() bool {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.tend()
	if y.isPlaylist() {
		seq, _ := y.sequence()
		return len(seq) == 0
	}
	_, _, ok := y.plan.at(y.clock())
	return !ok && !(y.cfg.DeadAir && len(y.plan.Airings) > 0)
}

// Programs implements Channel: the schedule, one entry per video, and
// with dead air, "Off air" where nothing is planned. A playlist's is as
// playlistPrograms has it.
func (y *YouTube) Programs(from, to time.Time) []Program {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.tend()
	if y.isPlaylist() {
		return y.playlistPrograms(from, to)
	}
	var out []Program
	offAir := func(start, end time.Time) {
		if y.cfg.DeadAir && end.After(start) && end.After(from) && start.Before(to) && len(out) < maxPrograms {
			out = append(out, Program{Start: start.Local(), End: end.Local(), Title: "Off air",
				Description: "Everything has aired lately. Back with the next new upload."})
		}
	}
	var gap time.Time // where the last airing ended
	for _, a := range y.plan.Airings {
		if !gap.IsZero() && a.Start.After(gap) {
			offAir(gap, a.Start)
		}
		gap = a.End
		if !a.End.After(from) || !a.Start.Before(to) {
			continue
		}
		if len(out) == maxPrograms {
			break
		}
		p := Program{Start: a.Start.Local(), End: a.End.Local(), Title: a.Title, Subtitle: y.names[a.Channel],
			Image: ytThumbnail(a.ID), New: a.First}
		if v := y.videos[a.ID]; v != nil {
			p.Description = v.Description
			p.Subtitle, p.Date = ytDated(p.Subtitle, v, y.clock())
			p.AudioLang, p.Captions = v.AudioLang, v.Captions
		}
		out = append(out, p)
	}
	if !gap.IsZero() {
		offAir(gap, to)
	}
	return out
}

// ytDated adds a video's upload date to its channel's name, for the
// guide's subtitle: "Northernlion, Mar 4, 2014" for a known date. A date
// from a listing's "3 years ago" is only that good: "Northernlion, 2014",
// or within the last year "Northernlion, March 2026". date is the same as
// XMLTV writes it.
func ytDated(name string, v *ytVideo, now time.Time) (subtitle, date string) {
	if v.Published.IsZero() {
		return name, ""
	}
	t := v.Published.UTC() // as YouTube counted back
	var shown string
	switch {
	case !v.Approx:
		t = t.Local()
		shown, date = t.Format("Jan 2, 2006"), t.Format("20060102")
	case now.Sub(t) >= 365*24*time.Hour:
		shown, date = t.Format("2006"), t.Format("2006")
	default:
		shown, date = t.Format("January 2006"), t.Format("200601")
	}
	if name == "" {
		return shown, date
	}
	return name + ", " + shown, date
}

// Stream implements Channel: the video on air from where it is now, then
// the schedule on.
func (y *YouTube) Stream(ctx context.Context, w io.Writer) error {
	if y.Empty() {
		return fmt.Errorf("channel %s: nothing scheduled yet", y.Num)
	}
	return playCues(ctx, w, y.FFmpeg, y.label(), y.cue())
}

// start loads the channel and begins its first listing, alongside the
// other channels rather than when first asked.
func (y *YouTube) start() {
	go func() {
		y.mu.Lock()
		defer y.mu.Unlock()
		y.tend()
	}()
}

func (y *YouTube) label() string { return "channel " + y.Num }

func (y *YouTube) clock() time.Time {
	if y.now != nil {
		return y.now()
	}
	return time.Now()
}

func (y *YouTube) tool() *YtDlp {
	if y.YtDlp != nil {
		return y.YtDlp
	}
	return defaultYtDlp
}

var defaultYtDlp = &YtDlp{}

// tend loads the channel's files on first use, keeps the schedule two days
// ahead, and starts a refresh of the catalog when one is due. y.mu is
// held.
func (y *YouTube) tend() {
	if !y.loaded {
		y.load()
	} else if info, err := os.Stat(filepath.Join(y.Dir, youtubeConfig)); err == nil && !info.ModTime().Equal(y.cfgMod) {
		// Edited: the new settings plan the schedule again from now, and a
		// playlist is listed again, for videos added to it.
		y.readConfig(y.cat)
		y.relist = y.isPlaylist()
		switch {
		case y.relist:
			log.Printf("%s: settings changed; listing the playlist again", y.label())
		case y.err == nil:
			log.Printf("%s: settings changed; planning again from now", y.label())
		}
	}
	if y.err != nil {
		return
	}
	now := y.clock()
	y.replan(now)
	if !y.busy && y.due(now) {
		y.busy = true
		go y.refresh()
	}
}

func (y *YouTube) load() {
	y.loaded = true
	y.tried, y.resolved, y.ahead = map[string]time.Time{}, map[string]ytResolved{}, map[string]*ytAhead{}
	y.feedDown = map[string]string{}
	cat, err := readYtCatalog(filepath.Join(y.Dir, ytCatalogFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("%s: %s: %v; listing the channels again", y.label(), ytCatalogFile, err)
	}
	y.catSaved = time.Now()
	if y.plan, err = readYtPlayout(filepath.Join(y.Dir, ytPlayoutFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("%s: %s: %v; starting a new schedule", y.label(), ytPlayoutFile, err)
	}
	y.readConfig(cat)
}

// readConfig reads youtube.json and takes the sources it names from cat,
// in order, keeping what's known of each: its channels, or its playlist.
// y.mu is held.
func (y *YouTube) readConfig(cat ytCatalog) {
	path := filepath.Join(y.Dir, youtubeConfig)
	if info, err := os.Stat(path); err == nil {
		y.cfgMod = info.ModTime()
	}
	y.pool, y.seq = nil, nil
	y.cfg, y.err = readYtConfig(path)
	if y.err != nil {
		log.Printf("%s: %v", y.label(), y.err)
		return
	}
	var sources []*ytSource
	if y.cfg.Playlist != "" {
		url, _ := YouTubePlaylist(y.cfg.Playlist) // as readYtConfig checked
		sources = append(sources, cat.source(url))
	}
	for _, c := range y.cfg.Channels {
		url := ytVideosURL(c)
		if !slices.ContainsFunc(sources, func(s *ytSource) bool { return s.URL == url }) {
			sources = append(sources, cat.source(url))
		}
	}
	y.cat.Sources = sources
}

// ready reports whether every channel has been listed, or tried, so the
// first schedule isn't made from only some of them.
func (y *YouTube) ready() bool {
	for _, s := range y.cat.Sources {
		if s.Listed.IsZero() && y.tried[s.URL].IsZero() {
			return false
		}
	}
	return true
}

// changed marks the catalog for saving and the pool, or a playlist's
// sequence, for rebuilding.
func (y *YouTube) changed() {
	y.catDirty, y.pool, y.seq = true, nil, nil
}

// replan keeps the schedule up to date and saves what changed. A
// playlist has nothing to plan: its order is its schedule. y.mu is held.
func (y *YouTube) replan(now time.Time) {
	if !y.ready() {
		return
	}
	if y.isPlaylist() {
		if y.catDirty && time.Since(y.catSaved) > 10*time.Minute {
			y.saveCatalog()
		}
		return
	}
	pool := y.currentPool()
	// A first run that has started is just a video now.
	for _, a := range y.plan.Airings {
		if v := y.videos[a.ID]; a.First && v != nil && v.New && !a.Start.After(now) {
			v.New = false
			y.changed()
		}
	}
	if y.pool == nil {
		pool = y.currentPool()
	}
	if y.plan.update(pool, now) {
		y.save(ytPlayoutFile, y.plan)
	}
	if y.catDirty && time.Since(y.catSaved) > 10*time.Minute {
		y.saveCatalog()
	}
}

func (y *YouTube) saveCatalog() {
	y.catDirty, y.catSaved = false, time.Now()
	y.save(ytCatalogFile, y.cat)
}

// save writes one of the channel's files, whole or not at all.
func (y *YouTube) save(name string, v any) {
	raw, err := json.Marshal(v)
	if err == nil {
		tmp := filepath.Join(y.Dir, name+".tmp")
		if err = os.WriteFile(tmp, raw, 0o644); err == nil {
			err = os.Rename(tmp, filepath.Join(y.Dir, name))
		}
	}
	if err != nil && err.Error() != y.logged {
		log.Printf("%s: saving %s: %v", y.label(), name, err)
	}
	if err != nil {
		y.logged = err.Error()
	}
}

// currentPool returns what the planner picks from, rebuilt after the
// catalog changes, and hourly with a maximum age, so videos age out.
func (y *YouTube) currentPool() *ytPool {
	now := y.clock()
	if y.pool != nil && (y.cfg.MaxAgeDays <= 0 || now.Sub(y.poolAt) < time.Hour) {
		return y.pool
	}
	h := fnv.New64a()
	h.Write([]byte(filepath.Base(y.Dir)))
	p := &ytPool{Repeat: y.cfg.repeat(), Lock: time.Duration(y.cfg.LockHours * float64(time.Hour)),
		Mix: ytRerunMixes[y.cfg.RerunMix], DeadAir: y.cfg.DeadAir, Seed: h.Sum64(), ids: map[string]bool{}}
	y.videos, y.names = map[string]*ytVideo{}, map[string]string{}
	maxAge := y.cfg.maxAge()
	var ids []string
	for _, s := range y.cat.Sources {
		if s.ID == "" {
			continue
		}
		ids = append(ids, s.ID)
		y.names[s.ID] = s.Name
		var playable []*ytVideo
		for _, v := range s.Videos {
			y.videos[v.ID] = v
			if y.cfg.playable(v) {
				playable = append(playable, v)
				p.ids[v.ID] = true // may stay on the schedule once too old to pick
			}
		}
		ps := ytPoolSource{ID: s.ID}
		for i, age := range ytAges(playable, now) {
			if maxAge > 0 && age > maxAge {
				continue
			}
			v := playable[i]
			ps.Videos = append(ps.Videos, v)
			if age <= ytHalfYear {
				ps.HalfYear = append(ps.HalfYear, v)
			}
			if age <= ytTwoYears {
				ps.TwoYears = append(ps.TwoYears, v)
			}
			if v.New {
				p.Firsts = append(p.Firsts, ytPick{v, s.ID})
			}
		}
		p.Sources = append(p.Sources, ps)
	}
	slices.SortFunc(p.Firsts, newestFirst)
	p.Basis = fmt.Sprintf("%s|%g|%g|%g|%s", strings.Join(ids, ","), y.cfg.RepeatDays, y.cfg.MinMinutes, y.cfg.MaxMinutes, y.cfg.RerunMix)
	if y.cfg.MaxAgeDays > 0 {
		// Only then, so schedules planned before there was a maximum age
		// aren't planned again for it.
		p.Basis += fmt.Sprintf("|%gd", y.cfg.MaxAgeDays)
	}
	if y.cfg.DeadAir {
		p.Basis += "|dead"
	}
	y.pool, y.poolAt = p, now
	return p
}

// index makes y.videos, the catalog's videos by ID, current. y.mu is
// held.
func (y *YouTube) index() {
	if y.isPlaylist() {
		y.sequence()
	} else {
		y.currentPool()
	}
}

// fits counts a source's videos that fit the channel's settings now, the
// maximum age included. y.mu is held.
func (y *YouTube) fits(s *ytSource) int {
	for _, ps := range y.currentPool().Sources {
		if s.ID != "" && ps.ID == s.ID {
			return len(ps.Videos)
		}
	}
	return 0
}

func (y *YouTube) source(url string) *ytSource {
	for _, s := range y.cat.Sources {
		if s.URL == url {
			return s
		}
	}
	return nil
}

// due reports whether a listing or a feed check is due. y.mu is held.
func (y *YouTube) due(now time.Time) bool {
	for _, s := range y.cat.Sources {
		if y.listDue(s, now) || s.ID != "" && now.Sub(s.Checked) >= ytFeedEvery {
			return true
		}
	}
	return false
}

func (y *YouTube) listDue(s *ytSource, now time.Time) bool {
	// Catalogs listed before listings had dates are listed again now.
	return y.relist || (now.Sub(s.Listed) >= ytListEvery || !s.Dated) && now.Sub(y.tried[s.URL]) >= ytRetryList
}

// refresh brings the catalog up to date: full listings when due (daily),
// the feeds for new uploads (every quarter hour), and a few lookups of
// single videos' details. Then the schedule takes in what changed. A
// playlist has no feed: every quarter hour it has only the lookups.
func (y *YouTube) refresh() {
	defer func() {
		y.mu.Lock()
		y.busy = false
		y.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	tool := y.tool()

	type job struct {
		url, id    string
		list, feed bool
	}
	y.mu.Lock()
	playlist := y.isPlaylist()
	var jobs []job
	for _, s := range y.cat.Sources {
		now := y.clock()
		jobs = append(jobs, job{s.URL, s.ID, y.listDue(s, now), !playlist && now.Sub(s.Checked) >= ytFeedEvery})
		if playlist {
			s.Checked = now
		}
	}
	y.relist = false
	y.mu.Unlock()

	for _, j := range jobs {
		if j.list && playlist {
			y.listPlaylist(ctx, j.url)
		} else if j.list {
			tool.jobs.Lock()
			raw, err := tool.run(ctx, slices.Concat(ytListingArgs, []string{j.url})...)
			tool.jobs.Unlock()
			var l ytListing
			if err == nil {
				l, err = parseYtListing(raw)
			}
			y.mu.Lock()
			if err != nil {
				y.tried[j.url] = y.clock()
				log.Printf("%s: listing %s: %v", y.label(), j.url, err)
			} else if s := y.source(j.url); s != nil { // unless no longer configured
				added := s.mergeListing(l, y.clock())
				j.id, j.feed = l.ID, true
				y.changed()
				log.Printf("%s: %s has %d videos (%d new)", y.label(), l.Name, len(l.Videos), added)
			}
			y.mu.Unlock()
		}
		if j.feed && j.id != "" {
			y.checkFeed(ctx, j.url, j.id, j.list)
		}
	}

	y.mu.Lock()
	y.replan(y.clock()) // so a first schedule's videos are looked up too
	ids := y.lookupsDue(y.clock())
	y.mu.Unlock()
	for n, id := range ids {
		if n > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(ytLookupGap):
			}
		}
		tool.jobs.Lock()
		raw, err := tool.run(ctx, "-J", "--skip-download", "--no-playlist", "--ignore-no-formats-error", ytWatchURL(id))
		tool.jobs.Unlock()
		var info ytInfo
		if err == nil {
			info, err = parseYtInfo(raw)
		}
		y.mu.Lock()
		y.learn(id, info, err)
		y.mu.Unlock()
	}

	y.mu.Lock()
	y.replan(y.clock())
	if y.catDirty {
		y.saveCatalog()
	}
	y.mu.Unlock()
}

// checkFeed looks at a source's feed for new uploads. While the feed
// fails, the newest uploads on its tab stand in, unless it was just
// listed in full. Only changes between the two are logged.
func (y *YouTube) checkFeed(ctx context.Context, url, id string, listed bool) {
	tool := y.tool()
	raw, err := web.Get(ctx, tool.client(), ytFeedURL(id))
	var entries []ytFeedEntry
	if err == nil {
		entries, err = parseYtFeed(raw)
	}
	var newest ytListing
	ferr := err
	if err != nil && !listed {
		tool.jobs.Lock()
		raw, ferr = tool.run(ctx, slices.Concat(ytListingArgs, []string{"--playlist-end", strconv.Itoa(ytNewAtTop), url})...)
		tool.jobs.Unlock()
		if ferr == nil {
			newest, ferr = parseYtListing(raw)
		}
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	s := y.source(url)
	if s == nil {
		return // no longer configured
	}
	now := y.clock()
	s.Checked = now
	var added int
	var changed bool
	was := y.feedDown[url]
	switch {
	case err == nil:
		if was != "" {
			log.Printf("%s: feed of %s works again", y.label(), s.Name)
		}
		delete(y.feedDown, url)
		added, changed = s.mergeFeed(entries, now)
	case listed:
		if was == "" {
			y.feedDown[url] = "listed"
			log.Printf("%s: feed of %s: %v", y.label(), s.Name, err)
		}
	case ferr == nil:
		// Said once, not every quarter hour.
		if was != "yt-dlp" {
			log.Printf("%s: feed of %s: %v; reading its newest uploads with yt-dlp until it's back", y.label(), s.Name, err)
		}
		y.feedDown[url] = "yt-dlp"
		added, changed = s.mergeNewest(newest, now)
	default:
		if was != "nothing" {
			log.Printf("%s: feed of %s: %v; and its newest uploads: %v", y.label(), s.Name, err, ferr)
		}
		y.feedDown[url] = "nothing"
	}
	if changed {
		y.changed()
	}
	if added > 0 {
		log.Printf("%s: %d new from %s", y.label(), added, s.Name)
	}
}

// lookupsDue picks the videos to look up next: new uploads without a
// length, premieres to check again, then what airs soon without a
// description. y.mu is held.
func (y *YouTube) lookupsDue(now time.Time) []string {
	if y.isPlaylist() {
		return y.playlistLookups(now)
	}
	var ids []string
	add := func(id string) {
		if len(ids) < ytLookups && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, s := range y.cat.Sources {
		for _, v := range s.Videos {
			if v.Found.IsZero() || now.Sub(v.Found) > 14*24*time.Hour {
				continue
			}
			switch {
			case v.Skip == "" && v.Seconds == 0 && now.Sub(v.Looked) >= 10*time.Minute,
				v.Skip == "upcoming" && now.Sub(v.Looked) >= time.Hour:
				add(v.ID)
			}
		}
	}
	y.currentPool()
	for _, a := range y.plan.Airings {
		if a.Start.After(now.Add(ytDescribeAhead)) {
			break
		}
		if v := y.videos[a.ID]; v != nil && a.End.After(now) && v.Looked.IsZero() {
			add(a.ID)
		}
	}
	return ids
}

// learn takes in a lookup of one video. y.mu is held.
func (y *YouTube) learn(id string, info ytInfo, err error) {
	y.index()
	v := y.videos[id]
	if v == nil {
		return
	}
	now := y.clock()
	switch {
	case err == nil:
		y.learnVideo(v, info)
	case gone(err):
		v.Skip, v.New, v.Looked = "gone", false, now
		log.Printf("%s: %s can't play: %v", y.label(), id, err)
	default:
		v.Looked = now
		log.Printf("%s: looking up %s: %v", y.label(), id, err)
	}
	y.changed()
}

// cue plays the schedule: the airing on now, from where it is. When a
// video ends early, because it was shorter than listed or its stream
// failed, it is opened again where it should be, twice at most in a row,
// and otherwise a slate fills its time. A playlist off the air shows a
// slate saying when it's back, a minute at a time.
func (y *YouTube) cue() cueFunc {
	var last ytAiring
	var played time.Time // when last was last opened
	tries := 0
	return func(now time.Time, _ bool) (item, int64, error) {
		y.mu.Lock()
		y.tend()
		on, next, wait, ok := y.airingAt(now)
		y.mu.Unlock()
		if !ok {
			return item{Slate: true, Title: standBy, Frames: 10 * loopFPS}, 0, nil
		}
		if wait != "" {
			left := on.Start.Sub(now)
			if left <= 2*time.Minute {
				// The video is found ahead, as open does for the next.
				ctx, cancel := context.WithDeadline(context.Background(), on.Start.Add(time.Minute))
				go func() {
					defer cancel()
					y.prefetch(ctx, on)
				}()
			}
			return item{Slate: true, Title: wait, Frames: max(framesIn(min(left, time.Minute)), 1)}, 0, nil
		}
		if on.ID == last.ID && on.Start.Equal(last.Start) {
			left := on.End.Sub(now)
			if now.Sub(played) > time.Minute {
				tries = 0 // it played a good while: a new failure
			}
			switch {
			case left <= time.Second && next.ID != "":
				// Done: ffmpeg reads the first half second of input
				// at once, so videos end a little early.
				on, tries = next, 0
			case left < 15*time.Second || tries >= 2:
				return item{Slate: true, Frames: max(framesIn(left), 1)}, 0, nil
			default:
				tries++
				log.Printf("%s: reopening %s %s in", y.label(), on.ID, now.Sub(on.Start).Round(time.Second))
			}
		} else {
			tries = 0
		}
		last, played = on, now
		return y.item(on), max(framesIn(now.Sub(on.Start)), 0), nil
	}
}

// airingAt is what airs at t: the airing on and the one after it, or for
// a playlist off the air, the next airing and the slate's words until
// then (see playlistAt). y.mu is held.
func (y *YouTube) airingAt(t time.Time) (on, next ytAiring, wait string, ok bool) {
	if y.isPlaylist() {
		return y.playlistAt(t)
	}
	on, next, ok = y.plan.at(t)
	return on, next, "", ok
}

// after is the airing after a, to find ahead. y.mu is held.
func (y *YouTube) after(a ytAiring) ytAiring {
	if y.isPlaylist() {
		return y.playlistAfter(a)
	}
	_, next, _ := y.plan.at(a.Start)
	return next
}

func (y *YouTube) item(a ytAiring) item {
	return item{
		Path: a.ID, Frames: framesIn(a.End.Sub(a.Start)), Audio: true, Title: a.Title,
		Open: func(ctx context.Context, offset time.Duration) (opened, error) {
			return y.open(ctx, a, offset)
		},
		Level: y.level(a.ID),
	}
}

// open finds where to stream an airing's video from, and its English
// captions, and starts finding the next one's shortly before it's due.
func (y *YouTube) open(ctx context.Context, a ytAiring, offset time.Duration) (opened, error) {
	began := time.Now()
	info, err := y.resolve(ctx, a.ID)
	y.mu.Lock()
	if err != nil {
		if v := y.videos[a.ID]; v != nil && gone(err) {
			v.Skip, v.New, v.Looked = "gone", false, y.clock()
			y.changed() // and so off the unlocked schedule
		}
		y.mu.Unlock()
		return opened{}, err
	}
	if v := y.videos[a.ID]; v != nil && v.Looked.IsZero() {
		y.learnVideo(v, info)
		y.changed()
	}
	next := y.after(a)
	y.mu.Unlock()
	if next.ID != "" {
		go y.prefetch(ctx, next)
	}
	in := info.inputs()
	o := opened{AudioLang: info.audioLang()}
	o.Args, o.Audio = ytInputArgs(in, offset, httpChunks(y.FFmpeg))
	if sub, ok := info.captions(); ok {
		client := y.tool().client()
		o.Captions = newCaptions(a.ID, func(ctx context.Context) ([]byte, error) { return ytCaptions(ctx, client, sub) })
	}
	log.Printf("%s: on air %s %s in (format %s, %dp, found in %s)", y.label(), a.ID, offset.Round(time.Second),
		info.FormatID, in[0].Height, time.Since(began).Round(100*time.Millisecond))
	return o, nil
}

// ytCaptions fetches a caption track: a json3, WebVTT or SRT file, or a
// playlist of WebVTT pieces, joined.
func ytCaptions(ctx context.Context, client *http.Client, sub ytSubtitle) ([]byte, error) {
	raw, err := ytGet(ctx, client, sub.URL)
	if err != nil || !strings.HasPrefix(sub.Protocol, "m3u8") {
		return raw, err
	}
	base, err := url.Parse(sub.URL)
	if err != nil {
		return nil, errors.New("captions: bad address")
	}
	var all []byte
	pieces := 0
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if pieces++; pieces > 500 {
			break
		}
		u, err := base.Parse(line)
		if err != nil {
			return nil, errors.New("captions: bad address")
		}
		piece, err := ytGet(ctx, client, u.String())
		if err != nil {
			return nil, err
		}
		all = append(append(all, piece...), "\n\n"...)
	}
	return all, nil
}

// ytGet fetches captions, leaving their address, which is signed, out of
// errors.
func ytGet(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("captions: bad address")
	}
	resp, err := client.Do(req)
	if uerr := (*url.Error)(nil); errors.As(err, &uerr) {
		return nil, fmt.Errorf("captions: %w", uerr.Err)
	}
	if err != nil {
		return nil, fmt.Errorf("captions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("captions: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// resolve finds where to stream a video from, using what was found ahead
// of time, or waiting for it when that's under way. The addresses expire
// after some hours.
func (y *YouTube) resolve(ctx context.Context, id string) (ytInfo, error) {
	y.mu.Lock()
	f := y.ahead[id]
	busy := f != nil && f.started
	y.mu.Unlock()
	if busy {
		select {
		case <-f.done:
		case <-ctx.Done():
			return ytInfo{}, ctx.Err()
		}
	}
	y.mu.Lock()
	r, ok := y.resolved[id]
	delete(y.resolved, id)
	height := y.cfg.MaxHeight
	y.mu.Unlock()
	if ok && time.Since(r.at) < time.Hour {
		return r.info, nil
	}
	return y.fetch(ctx, id, height)
}

// fetch asks yt-dlp where to stream a video from.
func (y *YouTube) fetch(ctx context.Context, id string, height int) (ytInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, ytResolveTime)
	defer cancel()
	raw, err := y.tool().run(ctx, "-J", "--no-playlist", "-f", ytFormatSpec(height), ytWatchURL(id))
	if err != nil {
		return ytInfo{}, err
	}
	info, err := parseYtInfo(raw)
	if err == nil && len(info.inputs()) == 0 {
		err = errors.New("yt-dlp found no stream")
	}
	return info, err
}

// prefetch resolves an airing's video a minute before it starts, so the
// change from one video to the next is quick.
func (y *YouTube) prefetch(ctx context.Context, a ytAiring) {
	y.mu.Lock()
	if y.ahead[a.ID] != nil {
		y.mu.Unlock()
		return
	}
	f := &ytAhead{done: make(chan struct{})}
	y.ahead[a.ID] = f
	height := y.cfg.MaxHeight
	y.mu.Unlock()
	defer func() {
		y.mu.Lock()
		delete(y.ahead, a.ID)
		y.mu.Unlock()
		close(f.done)
	}()
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Until(a.Start.Add(-time.Minute))):
	}
	y.mu.Lock()
	f.started = true
	y.mu.Unlock()
	info, err := y.fetch(ctx, a.ID, height)
	if err != nil {
		return // open tries again, and says why
	}
	y.mu.Lock()
	for id, r := range y.resolved {
		if time.Since(r.at) > time.Hour {
			delete(y.resolved, id)
		}
	}
	y.resolved[a.ID] = ytResolved{info, time.Now()}
	y.mu.Unlock()
}

// ytFormatSpec picks H.264 picture up to height and AAC sound, English
// sound of a video dubbed in several languages, else the best single file
// up to height.
func ytFormatSpec(height int) string {
	return fmt.Sprintf("bv*[height<=%[1]d][vcodec^=avc1]+ba[ext=m4a][language^=en]/bv*[height<=%[1]d][vcodec^=avc1]+ba[ext=m4a]/b[height<=%[1]d]", height)
}

// ytInputArgs are ffmpeg's inputs for a resolved video, played in real
// time from offset in: picture first, with yt-dlp's headers, reconnecting
// when a connection drops. A stream that fails for good stops ffmpeg
// (-xerror) rather than ending the video early, so it can be reopened.
// chunked fetches in 10 MB ranges as yt-dlp does, which YouTube serves
// without throttling.
func ytInputArgs(fs []ytFormat, offset time.Duration, chunked bool) (args []string, audio string) {
	args = []string{"-xerror"}
	for i, f := range fs {
		args = append(args, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_on_network_error", "1",
			"-reconnect_delay_max", "4", "-rw_timeout", "15000000")
		if chunked {
			args = append(args, "-request_size", "10485760", "-multiple_requests", "1")
		}
		if len(f.HTTPHeaders) > 0 {
			var h strings.Builder
			for _, k := range sortedKeys(f.HTTPHeaders) {
				h.WriteString(k + ": " + f.HTTPHeaders[k] + "\r\n")
			}
			args = append(args, "-headers", h.String())
		}
		if offset > 0 {
			args = append(args, "-ss", strconv.FormatFloat(offset.Seconds(), 'f', 3, 64))
		}
		args = append(args, "-re", "-i", f.URL)
		if audio == "" && f.ACodec != "none" && f.ACodec != "" {
			audio = fmt.Sprintf("%d:a:0", i)
		}
	}
	return args, audio
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var chunks sync.Map // ffmpeg path: bool

// httpChunks reports whether ffmpeg can fetch HTTP in ranges
// (-request_size, FFmpeg 8).
func httpChunks(ffmpeg string) bool {
	if ok, found := chunks.Load(ffmpeg); found {
		return ok.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-h", "protocol=http").Output()
	ok := strings.Contains(string(out), "-request_size")
	chunks.Store(ffmpeg, ok)
	return ok
}
