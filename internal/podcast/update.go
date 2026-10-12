package podcast

import (
	"cmp"
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"airwaves/internal/web"
	"airwaves/internal/youtube"
)

// Keeping up: each show is listed every listEvery (its newest listLimit
// videos), and a channel's feed, YouTube's cheap word of new uploads but
// down at times, is read every feedEvery in between. A video new to a
// show is looked up (its length, exact date, description and whether it's
// an episode at all), a few an update with a gap between, so yt-dlp isn't
// run hard. The newest unwatched episode of each show has where it
// streams from found ahead, so Play starts at once.
const (
	listEvery  = 6 * time.Hour
	feedEvery  = 30 * time.Minute
	retryList  = time.Hour // after a listing failed
	maxLookups = 12        // videos looked up per update
	tickEvery  = time.Minute
	// minLeft is how long addresses must still work to be used; they're
	// found again sooner.
	minLeft = 20 * time.Minute
	// resolveTime bounds finding where an episode streams from.
	resolveTime = 90 * time.Second
	keepSkipped = 300
)

// lookupGap spaces out yt-dlp runs; tests shorten it.
var lookupGap = 3 * time.Second

// resolved is where an episode streams from, until when.
type resolved struct {
	info  youtube.Info
	until time.Time
}

// Run keeps the shows up to date until ctx ends.
func (l *Library) Run(ctx context.Context) {
	l.mu.Lock()
	l.load()
	kick := l.kick
	l.mu.Unlock()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		l.update(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

// poke has Run update now. l.mu is held.
func (l *Library) poke() {
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// update lists the shows due, reads the feeds due, looks up new videos
// and finds the next episodes' streams ahead. One show failing doesn't
// stop the others.
func (l *Library) update(ctx context.Context) {
	now := l.clock()
	l.mu.Lock()
	l.load()
	var lists, feeds []*show
	for _, s := range l.st.Shows {
		switch {
		case l.listDue(s, now):
			lists = append(lists, s)
		case !s.source().playlist && s.ChannelID != "" && now.Sub(s.Checked) >= feedEvery:
			feeds = append(feeds, s)
		}
	}
	l.updating = len(lists) > 0
	l.mu.Unlock()
	for _, s := range lists {
		if ctx.Err() != nil {
			return
		}
		l.relist(ctx, s)
	}
	l.mu.Lock()
	l.updating = false
	l.mu.Unlock()
	for _, s := range feeds {
		if ctx.Err() != nil {
			return
		}
		l.readFeed(ctx, s)
	}
	l.lookups(ctx)
	l.ahead(ctx)
}

// listDue reports whether a show is due a listing. l.mu is held.
func (l *Library) listDue(s *show, now time.Time) bool {
	if s.Error != "" {
		return now.Sub(s.Listed) >= retryList
	}
	return now.Sub(s.Listed) >= listEvery
}

// list lists a show's newest videos, one listing job at a time across
// yt-dlp's users.
func (l *Library) list(ctx context.Context, s src) (youtube.Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tool := l.tool()
	args := append(slices.Clone(youtube.ListingArgs), "-I", "1:"+strconv.Itoa(listLimit), s.url)
	tool.Jobs.Lock()
	raw, err := tool.Run(ctx, args...)
	tool.Jobs.Unlock()
	if err != nil {
		return youtube.Listing{}, err
	}
	if s.playlist {
		return youtube.ParsePlaylist(raw)
	}
	return youtube.ParseListing(raw)
}

// relist lists a show again and takes in what's new.
func (l *Library) relist(ctx context.Context, s *show) {
	lst, err := l.list(ctx, s.source())
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.find(s.ID) != s { // removed meanwhile
		return
	}
	s.Listed = now
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.Error = err.Error()
		log.Printf("podcasts: %s: %v", s.Title, err)
		l.save()
		return
	}
	s.Error = ""
	s.merge(lst, s.source(), now)
	l.save()
}

// merge takes in a listing: its newest episodes, keeping what's known of
// each, plus older ones part watched. Videos too short to be episodes,
// and streams to come or under way, are left out.
func (s *show) merge(lst youtube.Listing, sr src, now time.Time) {
	if t := showTitle(lst, sr); t != "" {
		s.Title = t
	}
	if lst.Image != "" {
		s.Thumbnail = lst.Image
	}
	if !sr.playlist {
		s.ChannelID = lst.ID
	} else {
		s.ChannelID = lst.OwnerID
	}
	s.Listed = now
	known := map[string]*episode{}
	for _, e := range s.Episodes {
		known[e.ID] = e
	}
	var out []*episode
	seen := map[string]bool{}
	for _, v := range lst.Entries {
		// A video without a title yet (a premiere to come, say) is
		// listed once it has one.
		if v.Title == "" {
			continue
		}
		if seen[v.ID] || !s.fits(v.Seconds, v.LiveStatus, sr) {
			if v.Seconds > 0 && float64(v.Seconds) < s.MinMinutes*60 {
				s.skip(v.ID)
			}
			continue
		}
		seen[v.ID] = true
		e := known[v.ID]
		if e == nil {
			e = &episode{ID: v.ID}
		}
		e.Title = v.Title
		if v.Seconds > 0 {
			e.Seconds = float64(v.Seconds)
		}
		if !v.Published.IsZero() && (e.Published.IsZero() || e.Approx) {
			e.Published, e.Approx = v.Published, true
		}
		out = append(out, e)
	}
	if sr.playlist && !slices.ContainsFunc(out, func(e *episode) bool { return e.Published.IsZero() }) {
		slices.SortStableFunc(out, func(a, b *episode) int { return b.Published.Compare(a.Published) })
	}
	// New uploads the feed found that the listing doesn't show yet stay.
	var fresh []*episode
	for _, e := range s.Episodes {
		if !seen[e.ID] && !e.Published.IsZero() && now.Sub(e.Published) < 3*24*time.Hour && !e.Approx {
			fresh = append(fresh, e)
			seen[e.ID] = true
		}
	}
	out = append(fresh, out...)
	if len(out) > keepEpisodes {
		out = out[:keepEpisodes]
	}
	for _, e := range s.Episodes { // part watched, kept till finished
		if !seen[e.ID] && e.Position > 0 && !e.Watched && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	s.Episodes = out
}

// fits reports whether a video can be one of the show's episodes, by
// what's known: long enough (when its length is), and not a stream to
// come, on now or not yet processed. A show of a channel's videos takes
// no past streams, which have their own tab.
func (s *show) fits(seconds int, live string, sr src) bool {
	switch live {
	case "is_upcoming", "is_live", "post_live":
		return false
	case "was_live":
		if sr.tab == "videos" {
			return false
		}
	}
	return seconds <= 0 || float64(seconds) >= s.MinMinutes*60
}

// showTitle is what a listing calls the show: a playlist's title, unless
// it names no show ("Every Episode", "Full Episodes", "Podcast"), then its
// channel's name; a channel's name. A channel named as a handle is
// ("TheStandupPod") reads as words ("The Standup Pod").
func showTitle(lst youtube.Listing, sr src) string {
	if sr.playlist && lst.Owner != "" && strings.TrimSpace(generic.ReplaceAllString(lst.Name, "")) == "" {
		return spaced(lst.Owner)
	}
	if !sr.playlist {
		return spaced(lst.Name)
	}
	return cmp.Or(lst.Name, spaced(lst.Owner))
}

// spaced puts spaces between the words of a name written as one
// ("TheStandupPod"), leaving any other name as it is.
func spaced(name string) string {
	if strings.ContainsAny(name, " _-.") || !camel.MatchString(name) {
		return name
	}
	return camel.ReplaceAllString(name, "$1 $2")
}

var camel = regexp.MustCompile(`(\p{Ll})(\p{Lu})`)

// generic are the words of a playlist's title that say what it holds, not
// which show.
var generic = regexp.MustCompile(`(?i)\b(the|every|all|full|latest|new|episodes?|podcasts?|shows?|uploads?|videos?)\b|[^\pL\pN]+`)

// skip remembers a video that isn't an episode.
func (s *show) skip(id string) {
	if slices.Contains(s.Skipped, id) {
		return
	}
	s.Skipped = append(s.Skipped, id)
	if len(s.Skipped) > keepSkipped {
		s.Skipped = s.Skipped[len(s.Skipped)-keepSkipped:]
	}
}

// readFeed reads a channel's feed: uploads the show hasn't seen join it,
// to be looked up, and ones it has get their exact dates and
// descriptions. Shorts are left out by their links.
func (l *Library) readFeed(ctx context.Context, s *show) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l.mu.Lock()
	id := s.ChannelID
	l.mu.Unlock()
	raw, err := web.Get(ctx, l.tool().Client(), youtube.FeedURL(id))
	var entries []youtube.FeedEntry
	if err == nil {
		entries, err = youtube.ParseFeed(raw)
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.find(s.ID) != s {
		return
	}
	s.Checked = now
	if err != nil {
		return // the listings will do
	}
	known := map[string]*episode{}
	for _, e := range s.Episodes {
		known[e.ID] = e
	}
	var fresh []*episode
	for _, f := range entries {
		if f.ID == "" || slices.Contains(s.Skipped, f.ID) {
			continue
		}
		if e := known[f.ID]; e != nil {
			if !f.Published.IsZero() && (e.Published.IsZero() || e.Approx) {
				e.Published, e.Approx = f.Published, false
			}
			if d := youtube.Description(f.Description); e.Description == "" && d != "" {
				e.Description = d
			}
			continue
		}
		if f.Short() {
			s.skip(f.ID)
			continue
		}
		// Older than the episodes kept: not new, just not kept.
		if n := len(s.Episodes); n >= keepEpisodes && !f.Published.After(s.Episodes[n-1].Published) {
			continue
		}
		fresh = append(fresh, &episode{ID: f.ID, Title: f.Title, Published: f.Published, Description: youtube.Description(f.Description)})
	}
	s.Episodes = append(fresh, s.Episodes...) // the next listing trims them
	l.save()
}

// lookups looks up the details of videos new to their shows, newest
// first, a few an update: their lengths, dates and descriptions, and
// whether they're episodes at all.
func (l *Library) lookups(ctx context.Context) {
	type due struct {
		show      *show
		id        string
		published time.Time
	}
	l.mu.Lock()
	var todo []due
	for _, s := range l.st.Shows {
		for _, e := range s.Episodes {
			if e.Looked.IsZero() {
				todo = append(todo, due{s, e.ID, e.Published})
			}
		}
	}
	l.mu.Unlock()
	slices.SortStableFunc(todo, func(a, b due) int { return b.published.Compare(a.published) })
	for i, d := range todo {
		if i == maxLookups || ctx.Err() != nil {
			return
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(lookupGap):
			}
		}
		l.lookup(ctx, d.show, d.id)
	}
}

// lookup looks up a video's details, and drops it from its show when it
// isn't an episode after all.
func (l *Library) lookup(ctx context.Context, s *show, id string) {
	ctx, cancel := context.WithTimeout(ctx, resolveTime)
	defer cancel()
	tool := l.tool()
	tool.Jobs.Lock()
	raw, err := tool.Run(ctx, "-J", "--skip-download", "--no-playlist", "--ignore-no-formats-error", youtube.WatchURL(id))
	tool.Jobs.Unlock()
	var info youtube.Info
	if err == nil {
		info, err = youtube.ParseInfo(raw)
	}
	thumb := ""
	if err == nil {
		thumb = l.thumbnail(ctx, id)
	}
	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.find(s.ID) != s {
		return
	}
	i := slices.IndexFunc(s.Episodes, func(e *episode) bool { return e.ID == id })
	if i < 0 {
		return
	}
	e := s.Episodes[i]
	if err != nil {
		if youtube.Gone(err) {
			s.Episodes = slices.Delete(s.Episodes, i, i+1)
			s.skip(id)
			l.save()
		} else if ctx.Err() == nil {
			e.Looked = now // tried; the next listing tries again
			log.Printf("podcasts: %s: looking up %s: %v", s.Title, id, err)
		}
		return
	}
	e.Looked = now
	if info.Title != "" {
		e.Title = info.Title
	}
	if info.Duration > 0 {
		e.Seconds = math.Round(info.Duration)
	}
	if info.Timestamp > 0 {
		e.Published, e.Approx = time.Unix(int64(info.Timestamp), 0).UTC(), false
	}
	if d := youtube.Description(info.Description); d != "" {
		e.Description = d
	}
	if thumb != "" {
		e.Thumbnail = thumb
	}
	live := info.LiveStatus
	if info.MediaType == "short" || !s.fits(int(e.Seconds), live, s.source()) {
		s.Episodes = slices.Delete(s.Episodes, i, i+1)
		if live != "is_upcoming" && live != "is_live" && live != "post_live" {
			s.skip(id) // for good; a stream to come may be one later
		}
	}
	l.save()
}

// thumbnail is a video's largest picture: maxresdefault when YouTube has
// one, which not every video does, else "" for the one every video has.
func (l *Library) thumbnail(ctx context.Context, id string) string {
	u := "https://i.ytimg.com/vi/" + id + "/maxresdefault.jpg"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return ""
	}
	resp, err := l.tool().Client().Do(req)
	if err != nil {
		return ""
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return u
}

// ahead finds where the newest unwatched episode of each show streams
// from, so Play starts at once, and forgets addresses that expired.
func (l *Library) ahead(ctx context.Context) {
	now := l.clock()
	l.mu.Lock()
	for id, r := range l.resolved {
		if now.After(r.until) {
			delete(l.resolved, id)
		}
	}
	var ids []string
	for _, s := range l.st.Shows {
		for _, e := range s.Episodes {
			if e.Watched || e.Looked.IsZero() {
				continue
			}
			if r, ok := l.resolved[e.ID]; !ok || r.until.Sub(now) < minLeft {
				ids = append(ids, e.ID)
			}
			break
		}
	}
	l.mu.Unlock()
	tool := l.tool()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		tool.Ahead.Lock()
		_, err := l.fetch(ctx, id)
		tool.Ahead.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Printf("podcasts: finding %s ahead: %v", id, err)
		}
	}
}

// resolve finds where an episode streams from: as found ahead, else now.
func (l *Library) resolve(ctx context.Context, id string) (youtube.Info, error) {
	l.mu.Lock()
	r, ok := l.resolved[id]
	l.mu.Unlock()
	if ok && time.Until(r.until) >= minLeft {
		return r.info, nil
	}
	return l.fetch(ctx, id)
}

// fetch asks yt-dlp where an episode streams from, and keeps it until it
// expires.
func (l *Library) fetch(ctx context.Context, id string) (youtube.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTime)
	defer cancel()
	raw, err := l.tool().Run(ctx, "-J", "--no-playlist", "-f", youtube.FormatSpec(maxHeight), youtube.WatchURL(id))
	if err != nil {
		return youtube.Info{}, err
	}
	info, err := youtube.ParseInfo(raw)
	if err == nil && len(info.Inputs()) == 0 {
		err = errors.New("yt-dlp found no stream")
	}
	if err != nil {
		return youtube.Info{}, err
	}
	l.mu.Lock()
	l.resolved[id] = resolved{info: info, until: youtube.AddressesUntil(info, time.Now())}
	l.mu.Unlock()
	return info, nil
}
