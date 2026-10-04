package admin

// YouTube channels: a youtube.json in the channel's folder names the
// YouTube channels whose uploads play, or a playlist to play in order (see
// vchan.YouTube). The page finds them with yt-dlp, by searching YouTube's
// channels or looking up a link.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"airwaves/internal/vchan"
)

const (
	youtubeFile = "youtube.json"
	maxSources  = 10
	// How long a search's results and a lookup are trusted, and a failed
	// lookup remembered.
	searchFor    = 5 * time.Minute
	lookupFor    = 6 * time.Hour
	lookupErrFor = 10 * time.Minute
	searchTime   = 45 * time.Second
	lookupTime   = 60 * time.Second // two runs of yt-dlp
	searchCount  = 15
	// ytRuns is how many yt-dlps the page runs at once.
	ytRuns = 3
)

var (
	handleRe    = regexp.MustCompile(`^@[\p{L}\p{M}\p{N}._-]{1,100}$`)
	channelIDRe = regexp.MustCompile(`^UC[0-9A-Za-z_-]{22}$`)
	customRe    = regexp.MustCompile(`^[\p{L}\p{M}\p{N}._-]{1,100}$`) // /c/ and /user/ names
	heights     = []int{360, 480, 720, 1080}
	// rerunMixes are how strongly reruns lean toward recent uploads.
	rerunMixes = []string{"recent", "balanced", "any"}
	// ytErrPrefix is how yt-dlp starts its errors: "[youtube:tab] @name: ".
	ytErrPrefix = regexp.MustCompile(`^yt-dlp: \[[^\]]+\] (\S+: )?`)
	// pathYtDlp runs yt-dlp from PATH when the library has none of its own.
	pathYtDlp = &vchan.YtDlp{}
)

var (
	errNotChannel   = errors.New("that isn't a YouTube channel: use its link (youtube.com/@handle), @handle or channel ID")
	errVideoLink    = errors.New("that's a link to a video: use the channel's link instead, like youtube.com/@handle")
	errPlaylistLink = errors.New("that's a playlist: play it as the channel's playlist, or use a channel's link, like youtube.com/@handle")
	errBoth         = errors.New("a channel plays YouTube channels' uploads or a playlist, not both")
)

// ytConfigJSON is a YouTube channel's settings, as the page shows them.
type ytConfigJSON struct {
	Channels   []string `json:"channels"`
	Playlist   string   `json:"playlist,omitempty"` // its page; played in order, in place of channels
	RepeatDays float64  `json:"repeatDays"`
	MaxHeight  int      `json:"maxHeight"`
	MinMinutes float64  `json:"minMinutes"`
	MaxMinutes float64  `json:"maxMinutes"` // 0: no limit
	RerunMix   string   `json:"rerunMix"`
	MaxAgeDays float64  `json:"maxAgeDays"` // 0: any age
	DeadAir    bool     `json:"deadAir"`    // stand by rather than repeat within RepeatDays
}

// ytReq is a YouTube channel's settings as the page sends them. Settings
// left out stay as they are (for a new channel, the defaults).
type ytReq struct {
	Channels   []string `json:"channels"`
	Playlist   *string  `json:"playlist"` // a link or ID; "" for none
	RepeatDays *float64 `json:"repeatDays"`
	MaxHeight  *int     `json:"maxHeight"`
	MinMinutes *float64 `json:"minMinutes"`
	MaxMinutes *float64 `json:"maxMinutes"`
	RerunMix   *string  `json:"rerunMix"`
	MaxAgeDays *float64 `json:"maxAgeDays"`
	DeadAir    *bool    `json:"deadAir"`
}

// ytSourceJSON is one of the YouTube channels a channel plays, or its
// playlist, and how its listing is going.
type ytSourceJSON struct {
	Channel  string     `json:"channel"` // as in youtube.json
	URL      string     `json:"url"`     // its page
	ID       string     `json:"id,omitempty"`
	Name     string     `json:"name,omitempty"` // a playlist's title
	Handle   string     `json:"handle,omitempty"`
	Image    string     `json:"image,omitempty"` // a playlist's first video's thumbnail
	Owner    string     `json:"owner,omitempty"` // a playlist's channel
	OwnerURL string     `json:"ownerUrl,omitempty"`
	Videos   int        `json:"videos"`   // in the catalog
	Playable int        `json:"playable"` // of those, the ones that fit the settings, age included
	Listed   *time.Time `json:"listed,omitempty"`
	Listing  bool       `json:"listing"`
	Since    *time.Time `json:"listingSince,omitempty"`
	Failed   *time.Time `json:"failed,omitempty"`
	Error    string     `json:"error,omitempty"` // why it can't be listed, when known
}

// ytChannelJSON is a channel a search or lookup found.
type ytChannelJSON struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Subscribers int64  `json:"subscribers"`
	Description string `json:"description"`
	Image       string `json:"image"`
	Videos      int    `json:"videos,omitempty"` // roughly, from a lookup
}

func channelOut(c vchan.YouTubeChannel) ytChannelJSON {
	return ytChannelJSON{ID: c.ID, URL: c.URL, Handle: c.Handle, Name: c.Name, Subscribers: c.Subscribers,
		Description: c.Description, Image: c.Image, Videos: c.Videos}
}

type searched struct {
	found []ytChannelJSON
	at    time.Time
}

// ytPlaylistJSON is a playlist a lookup found.
type ytPlaylistJSON struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Owner    string `json:"owner"`
	OwnerURL string `json:"ownerUrl,omitempty"`
	Videos   int    `json:"videos"` // as YouTube counts them, ones that can't play included
	Image    string `json:"image,omitempty"`
}

func playlistOut(p vchan.YouTubePlaylistInfo) ytPlaylistJSON {
	return ytPlaylistJSON{ID: p.ID, URL: p.URL, Title: p.Title, Owner: p.Owner, OwnerURL: ownerURL(p.OwnerID), Videos: p.Videos, Image: p.Image}
}

func ownerURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://www.youtube.com/channel/" + id
}

// lookedUp is a lookup of a channel, or of a playlist.
type lookedUp struct {
	ch  ytChannelJSON
	pl  ytPlaylistJSON
	err error
	at  time.Time
}

func (l lookedUp) fresh() bool {
	if l.err != nil {
		return time.Since(l.at) < lookupErrFor
	}
	return time.Since(l.at) < lookupFor
}

// ytConfigOf is the settings the page edits, from a youtube.json read
// with the defaults filled in.
func ytConfigOf(cfg vchan.YouTubeConfig) ytConfigJSON {
	c := ytConfigJSON{Channels: cfg.Channels, Playlist: cfg.Playlist, RepeatDays: cfg.RepeatDays, MaxHeight: cfg.MaxHeight,
		MinMinutes: cfg.MinMinutes, MaxMinutes: cfg.MaxMinutes, RerunMix: cfg.RerunMix, MaxAgeDays: cfg.MaxAgeDays, DeadAir: cfg.DeadAir}
	if c.Channels == nil {
		c.Channels = []string{}
	}
	return c
}

// describeYouTube fills in a YouTube channel's settings and how its
// sources are doing: Videos counts the catalogs, Items the videos that
// fit the settings, or a playlist's that can play.
func (s *Server) describeYouTube(c *channelJSON, y *vchan.YouTube) {
	st := y.Status()
	cfg := ytConfigOf(st.Config)
	c.YouTube = &cfg
	if st.Err != nil {
		c.Error = st.Err.Error()
	}
	videos, playable := 0, 0
	c.Sources = []ytSourceJSON{}
	for _, src := range st.Sources {
		j := ytSourceJSON{Channel: src.Channel, URL: src.URL, ID: src.ID, Name: src.Name, Videos: src.Videos,
			Playable: src.Playable, Listing: src.Listing, Listed: timeOrNil(src.Listed),
			Since: timeOrNil(src.Since), Failed: timeOrNil(src.Failed)}
		if cfg.Playlist != "" {
			s.describePlaylist(&j, src)
		} else {
			s.describeSource(&j, src)
		}
		c.Listing = c.Listing || j.Listing
		videos += src.Videos
		playable += src.Playable
		c.Sources = append(c.Sources, j)
	}
	c.Videos, c.Items = &videos, &playable
	c.Now = nowOn(y)
}

// describeSource fills in one of the YouTube channels a channel plays:
// its page, and what a lookup found of it.
func (s *Server) describeSource(j *ytSourceJSON, src vchan.YouTubeSource) {
	page, err := youtubeChannel(src.Channel)
	if err != nil {
		page = strings.TrimSuffix(src.URL, "/videos")
	}
	j.URL = page
	if i := strings.LastIndex(page, "/@"); i >= 0 {
		j.Handle = page[i+1:]
	}
	if l, ok := s.lookedUp(page, src.ID); ok {
		if l.err == nil {
			j.Image, j.Handle = l.ch.Image, cmp(j.Handle, l.ch.Handle)
			j.Name = cmp(j.Name, l.ch.Name)
		} else if j.Failed != nil {
			j.Error = ytMessage(l.err)
		}
	}
}

// describePlaylist fills in the playlist a channel plays: whose it is, its
// picture, and why it can't be listed, when a lookup found out.
func (s *Server) describePlaylist(j *ytSourceJSON, src vchan.YouTubeSource) {
	j.Owner, j.OwnerURL, j.Image = src.Owner, ownerURL(src.OwnerID), src.Image
	if l, ok := s.lookedUpPlaylist(src.URL); ok {
		if l.err == nil {
			j.Name, j.Owner, j.OwnerURL = cmp(j.Name, l.pl.Title), cmp(j.Owner, l.pl.Owner), cmp(j.OwnerURL, l.pl.OwnerURL)
			j.Image = cmp(j.Image, l.pl.Image)
		} else if j.Failed != nil {
			j.Error = ytMessage(l.err)
		}
	}
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// lookedUp finds a lookup of a channel, by its page or ID.
func (s *Server) lookedUp(page, id string) (lookedUp, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.lookups[strings.ToLower(page)]; ok && l.fresh() {
		return l, true
	}
	if l, ok := s.lookups["id:"+id]; ok && id != "" && l.fresh() {
		return l, true
	}
	return lookedUp{}, false
}

// lookedUpPlaylist finds a lookup of a playlist, by its page.
func (s *Server) lookedUpPlaylist(page string) (lookedUp, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lookups["pl:"+page] // IDs differ by case
	return l, ok && l.fresh()
}

// youtubeChannel checks a YouTube channel as the page gives it, a link,
// "@handle" or "UC…" ID, and returns its page: https://www.youtube.com/
// then @handle, channel/UC…, c/name or user/name.
func youtubeChannel(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case handleRe.MatchString(s):
		return "https://www.youtube.com/" + s, nil
	case channelIDRe.MatchString(s):
		return "https://www.youtube.com/channel/" + s, nil
	}
	raw := s
	if !strings.Contains(s, "://") {
		raw = "https://" + s
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errNotChannel
	}
	host := strings.ToLower(u.Hostname())
	for _, p := range []string{"www.", "m."} {
		host = strings.TrimPrefix(host, p)
	}
	if host == "youtu.be" {
		return "", errVideoLink
	}
	if host != "youtube.com" {
		return "", errNotChannel
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case handleRe.MatchString(parts[0]):
		return "https://www.youtube.com/" + parts[0], nil
	case len(parts) > 1 && parts[0] == "channel" && channelIDRe.MatchString(parts[1]),
		len(parts) > 1 && (parts[0] == "c" || parts[0] == "user") && customRe.MatchString(parts[1]):
		return "https://www.youtube.com/" + parts[0] + "/" + parts[1], nil
	case parts[0] == "playlist":
		return "", errPlaylistLink
	case slices.Contains([]string{"watch", "shorts", "live", "embed"}, parts[0]):
		return "", errVideoLink
	}
	return "", errNotChannel
}

// ytSettings checks a YouTube channel's settings from the page, over cur:
// the channel's settings now, or the defaults. A channel plays YouTube
// channels or a playlist: given alone, either takes the other's place.
func ytSettings(r *ytReq, cur ytConfigJSON) (ytConfigJSON, error) {
	if r == nil {
		r = &ytReq{}
	}
	c := cur
	c.Channels, c.Playlist = nil, ""
	if r.RepeatDays != nil {
		c.RepeatDays = *r.RepeatDays
	}
	if r.MaxHeight != nil {
		c.MaxHeight = *r.MaxHeight
	}
	if r.MinMinutes != nil {
		c.MinMinutes = *r.MinMinutes
	}
	if r.MaxMinutes != nil {
		c.MaxMinutes = *r.MaxMinutes
	}
	if r.RerunMix != nil {
		c.RerunMix = *r.RerunMix
	}
	if r.MaxAgeDays != nil {
		c.MaxAgeDays = *r.MaxAgeDays
	}
	if r.DeadAir != nil {
		c.DeadAir = *r.DeadAir
	}
	list, playlist := cur.Channels, cur.Playlist
	switch {
	case r.Channels != nil && r.Playlist != nil:
		list, playlist = r.Channels, *r.Playlist
	case r.Playlist != nil:
		if playlist = *r.Playlist; strings.TrimSpace(playlist) != "" {
			list = nil
		}
	case r.Channels != nil:
		if list = r.Channels; len(list) > 0 {
			playlist = ""
		}
	}
	if strings.TrimSpace(playlist) != "" {
		if len(list) > 0 {
			return c, errBoth
		}
		page, err := vchan.YouTubePlaylist(playlist)
		if err != nil {
			return c, fmt.Errorf("%q: %w", strings.TrimSpace(playlist), err)
		}
		c.Playlist = page
	}
	seen := map[string]bool{}
	for _, ch := range list {
		page, err := youtubeChannel(ch)
		if err != nil {
			return c, fmt.Errorf("%q: %w", strings.TrimSpace(ch), err)
		}
		if k := strings.ToLower(page); !seen[k] {
			seen[k] = true
			c.Channels = append(c.Channels, page)
		}
	}
	switch {
	case len(c.Channels) == 0 && c.Playlist == "":
		return c, errors.New("add at least one YouTube channel, or a playlist")
	case len(c.Channels) > maxSources:
		return c, fmt.Errorf("a channel plays up to %d YouTube channels", maxSources)
	case c.RepeatDays < 0 || c.RepeatDays > 365:
		return c, errors.New("the repeat window is 0 to 365 days")
	case !slices.Contains(heights, c.MaxHeight):
		return c, errors.New("the quality is 360, 480, 720 or 1080")
	case c.MinMinutes < 0 || c.MinMinutes > 600:
		return c, errors.New("the shortest videos are 0 to 600 minutes long")
	case c.MaxMinutes < 0 || c.MaxMinutes > 1440 || c.MaxMinutes > 0 && c.MaxMinutes <= c.MinMinutes:
		return c, errors.New("the longest videos are longer than the shortest, up to 1440 minutes (0 for no limit)")
	case !slices.Contains(rerunMixes, c.RerunMix):
		return c, errors.New("reruns are recent, balanced or any")
	case c.MaxAgeDays < 0 || c.MaxAgeDays > 3650:
		return c, errors.New("the newest videos are from the last 1 to 3650 days (0 for any age)")
	}
	return c, nil
}

// writeYtConfig updates a channel's youtube.json, keeping any settings the
// page doesn't edit.
func writeYtConfig(path string, c ytConfigJSON) error {
	m := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s: %w", youtubeFile, err)
		}
	}
	if len(c.Channels) > 0 {
		m["channels"] = c.Channels
	} else {
		delete(m, "channels")
	}
	if c.Playlist != "" {
		m["playlist"] = c.Playlist
	} else {
		delete(m, "playlist")
	}
	m["repeatDays"] = c.RepeatDays
	m["maxHeight"] = c.MaxHeight
	m["minMinutes"] = c.MinMinutes
	m["maxMinutes"] = c.MaxMinutes
	m["rerunMix"] = c.RerunMix
	m["maxAgeDays"] = c.MaxAgeDays
	if c.DeadAir {
		m["deadAir"] = true
	} else {
		delete(m, "deadAir")
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(raw, '\n'), 0o644)
}

// ytdlp is the yt-dlp the library's YouTube channels use.
func (s *Server) ytdlp() *vchan.YtDlp {
	if s.Library.YtDlp != nil {
		return s.Library.YtDlp
	}
	return pathYtDlp
}

// ytRun runs f with yt-dlp once it's ready and a run is free, and answers
// for it when it can't.
func (s *Server) ytRun(w http.ResponseWriter, r *http.Request, limit time.Duration, f func(context.Context, *vchan.YtDlp) error) bool {
	tool := s.ytdlp()
	if err := tool.Ready(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), limit)
	defer cancel()
	select {
	case s.ytSlots <- struct{}{}:
		defer func() { <-s.ytSlots }()
	case <-ctx.Done():
		writeErr(w, http.StatusServiceUnavailable, errors.New("yt-dlp is busy; try again"))
		return false
	}
	if err := f(ctx, tool); err != nil {
		code := http.StatusBadGateway
		if ctx.Err() != nil {
			code, err = http.StatusGatewayTimeout, errors.New("YouTube took too long to answer; try again")
		}
		writeErr(w, code, errors.New(ytMessage(err)))
		return false
	}
	return true
}

// ytMessage makes a yt-dlp error fit to show.
func ytMessage(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "HTTP Error 404") {
		return "YouTube has no such channel"
	}
	return ytErrPrefix.ReplaceAllString(msg, "")
}

// searchYouTube finds YouTube channels by name or topic.
func (s *Server) searchYouTube(w http.ResponseWriter, r *http.Request) {
	q := strings.Join(strings.Fields(r.URL.Query().Get("q")), " ")
	if q == "" || utf8.RuneCountInString(q) > 100 {
		writeErr(w, http.StatusBadRequest, errors.New("search for a channel or a topic, in up to 100 characters"))
		return
	}
	key := strings.ToLower(q)
	s.mu.Lock()
	if c, ok := s.searches[key]; ok && time.Since(c.at) < searchFor {
		s.mu.Unlock()
		writeJSON(w, map[string]any{"channels": c.found})
		return
	}
	s.mu.Unlock()
	var found []ytChannelJSON
	if !s.ytRun(w, r, searchTime, func(ctx context.Context, tool *vchan.YtDlp) error {
		list, err := tool.SearchChannels(ctx, q, searchCount)
		for _, c := range list {
			found = append(found, channelOut(c))
		}
		return err
	}) {
		return
	}
	if found == nil {
		found = []ytChannelJSON{}
	}
	s.mu.Lock()
	if s.searches == nil {
		s.searches = map[string]searched{}
	}
	for k, c := range s.searches {
		if time.Since(c.at) >= searchFor {
			delete(s.searches, k)
		}
	}
	s.searches[key] = searched{found: found, at: time.Now()}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"channels": found})
}

// lookupYouTube finds one YouTube channel from a link, @handle or ID,
// with roughly how many videos it has.
func (s *Server) lookupYouTube(w http.ResponseWriter, r *http.Request) {
	page, err := youtubeChannel(r.URL.Query().Get("u"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if l, ok := s.lookedUp(page, ""); ok {
		if l.err != nil {
			writeErr(w, http.StatusBadGateway, errors.New(ytMessage(l.err)))
		} else {
			writeJSON(w, l.ch)
		}
		return
	}
	var found vchan.YouTubeChannel
	var failed error
	ok := s.ytRun(w, r, lookupTime, func(ctx context.Context, tool *vchan.YtDlp) error {
		found, failed = tool.LookupChannel(ctx, page)
		return failed
	})
	if !ok && (failed == nil || errors.Is(failed, context.DeadlineExceeded) || errors.Is(failed, context.Canceled)) {
		return // yt-dlp didn't run, or ran out of time: nothing to remember
	}
	l := lookedUp{ch: channelOut(found), err: failed, at: time.Now()}
	s.remember(page, l)
	if ok {
		writeJSON(w, l.ch)
	}
}

// remember keeps a lookup of the channel at page, by its page, and when
// it was found, by its own page and ID too.
func (s *Server) remember(page string, l lookedUp) {
	keys := []string{strings.ToLower(page)}
	if l.err == nil {
		keys = append(keys, strings.ToLower(l.ch.URL), "id:"+l.ch.ID)
	}
	s.keep(l, keys...)
}

// keep holds a lookup by keys, letting go of those gone stale.
func (s *Server) keep(l lookedUp, keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookups == nil {
		s.lookups = map[string]lookedUp{}
	}
	for k, c := range s.lookups {
		if !c.fresh() {
			delete(s.lookups, k)
		}
	}
	for _, k := range keys {
		s.lookups[k] = l
	}
}

// lookupPlaylist finds a YouTube playlist from its link or ID: its title,
// whose it is, and how many videos it has.
func (s *Server) lookupPlaylist(w http.ResponseWriter, r *http.Request) {
	page, err := vchan.YouTubePlaylist(r.URL.Query().Get("u"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if l, ok := s.lookedUpPlaylist(page); ok {
		if l.err != nil {
			writeErr(w, http.StatusBadGateway, errors.New(ytMessage(l.err)))
		} else {
			writeJSON(w, l.pl)
		}
		return
	}
	var found vchan.YouTubePlaylistInfo
	var failed error
	ok := s.ytRun(w, r, lookupTime, func(ctx context.Context, tool *vchan.YtDlp) error {
		found, failed = tool.LookupPlaylist(ctx, page)
		return failed
	})
	if !ok && (failed == nil || errors.Is(failed, context.DeadlineExceeded) || errors.Is(failed, context.Canceled)) {
		return // yt-dlp didn't run, or ran out of time: nothing to remember
	}
	l := lookedUp{pl: playlistOut(found), err: failed, at: time.Now()}
	s.keep(l, "pl:"+page)
	if ok {
		writeJSON(w, l.pl)
	}
}
