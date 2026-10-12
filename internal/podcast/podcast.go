// Package podcast is the Podcasts app's library: video podcasts from
// YouTube, watched on demand. A show is a YouTube channel's tab (its
// videos, or its past live streams) or a playlist; the library keeps its
// newest episodes, what's known of each, and how far each has been
// watched, in one file. Nothing is downloaded: an episode streams from
// YouTube while it's watched, as the YouTube channels' videos do.
package podcast

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/store"
	"airwaves/internal/tuner"
	"airwaves/internal/youtube"
)

const (
	// keepEpisodes is how many of a show's newest episodes are kept, and
	// listLimit how far down its listing they're looked for.
	keepEpisodes = 30
	listLimit    = 40
	// DefaultMinMinutes leaves out clips: shorter videos aren't episodes.
	DefaultMinMinutes = 10
	// maxHeight is the picture streamed, at most.
	maxHeight = 1080
)

var (
	// ErrBadShow is wrapped by errors about what was given as a show.
	ErrBadShow = errors.New("not a show")
	// ErrNotFound is the error for a show or an episode the library
	// doesn't have.
	ErrNotFound = errors.New("no such show or episode")
)

// Library is the shows and their episodes, kept in Path.
type Library struct {
	Path string
	// YtDlp runs yt-dlp; yt-dlp from PATH when nil.
	YtDlp *youtube.YtDlp
	// FFmpeg is the ffmpeg that plays episodes, asked whether it fetches
	// in ranges.
	FFmpeg string

	mu       sync.Mutex
	loaded   bool
	st       file
	resolved map[string]resolved // where episodes stream from, by ID
	captions map[string][]cc.Cue // episodes' English captions, nil for none
	updating bool                // listing a show
	kick     chan struct{}
	now      func() time.Time
}

// file is the library as Path keeps it.
type file struct {
	Shows []*show `json:"shows"`
}

// show is a show as the file keeps it.
type show struct {
	ID         string     `json:"id"`
	URL        string     `json:"url"` // its tab or playlist, as listed
	Title      string     `json:"title"`
	Thumbnail  string     `json:"thumbnail,omitempty"`
	MinMinutes float64    `json:"minMinutes"`
	Error      string     `json:"error,omitempty"` // the last listing's
	ChannelID  string     `json:"channelId,omitempty"`
	Listed     time.Time  `json:"listed,omitzero"`
	Checked    time.Time  `json:"checked,omitzero"` // its channel's feed
	Episodes   []*episode `json:"episodes"`         // newest first
	// Skipped are videos found not to be episodes (clips, Shorts), so
	// the feed doesn't bring them back.
	Skipped []string `json:"skipped,omitempty"`
}

// episode is an episode as the file keeps it.
type episode struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Published   time.Time `json:"published,omitzero"`
	Approx      bool      `json:"approx,omitempty"` // Published is a listing's rough date
	Seconds     float64   `json:"seconds,omitempty"`
	Thumbnail   string    `json:"thumbnail,omitempty"`
	Looked      time.Time `json:"looked,omitzero"` // its details, looked up
	Position    float64   `json:"position,omitempty"`
	Watched     bool      `json:"watched,omitempty"`
}

// State is the library for the app: its shows, as they were added, and
// whether a show is being listed.
type State struct {
	Shows    []Show `json:"shows"`
	Updating bool   `json:"updating"`
}

// Show is a show for the app.
type Show struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Thumbnail  string    `json:"thumbnail"`
	MinMinutes float64   `json:"minMinutes"`
	Error      string    `json:"error,omitempty"`
	Episodes   []Episode `json:"episodes"` // newest first
}

// Episode is an episode for the app: Duration and Position in seconds.
type Episode struct {
	ID          string    `json:"id"`
	ShowID      string    `json:"showId"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Published   time.Time `json:"published,omitzero"`
	Duration    float64   `json:"duration"`
	Thumbnail   string    `json:"thumbnail"`
	Position    float64   `json:"position"`
	Watched     bool      `json:"watched"`
}

// Found is a YouTube channel a search found, to add as a show.
type Found struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Thumbnail   string `json:"thumbnail"`
	Description string `json:"description"`
	Subscribers int64  `json:"subscribers"`
}

func (l *Library) tool() *youtube.YtDlp {
	if l.YtDlp != nil {
		return l.YtDlp
	}
	return pathYtDlp
}

// pathYtDlp runs yt-dlp from PATH for a library given none.
var pathYtDlp = &youtube.YtDlp{}

func (l *Library) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// load reads the file once. l.mu is held.
func (l *Library) load() {
	if l.loaded {
		return
	}
	l.loaded = true
	l.resolved = map[string]resolved{}
	l.kick = make(chan struct{}, 1)
	err := store.ReadFile(l.Path, func(raw []byte) error { return json.Unmarshal(raw, &l.st) })
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("podcasts: %v; starting over", err)
		l.st = file{}
	}
}

// save writes the file. l.mu is held.
func (l *Library) save() {
	raw, err := json.MarshalIndent(l.st, "", " ")
	if err == nil {
		err = store.WriteFile(l.Path, raw, 0o644)
	}
	if err != nil {
		log.Printf("podcasts: save: %v", err)
	}
}

// State lists the shows and their episodes.
func (l *Library) State() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	out := State{Shows: make([]Show, 0, len(l.st.Shows)), Updating: l.updating}
	for _, s := range l.st.Shows {
		out.Shows = append(out.Shows, s.view())
	}
	return out
}

func (s *show) view() Show {
	v := Show{ID: s.ID, Title: s.Title, URL: s.URL, Thumbnail: s.Thumbnail, MinMinutes: s.MinMinutes, Error: s.Error,
		Episodes: make([]Episode, 0, len(s.Episodes))}
	for _, e := range s.Episodes {
		v.Episodes = append(v.Episodes, Episode{ID: e.ID, ShowID: s.ID, Title: e.Title, Description: e.Description,
			Published: e.Published, Duration: e.Seconds, Thumbnail: e.thumbnail(), Position: e.Position, Watched: e.Watched})
	}
	return v
}

func (e *episode) thumbnail() string {
	if e.Thumbnail != "" {
		return e.Thumbnail
	}
	return youtube.Thumbnail(e.ID)
}

// Search finds YouTube channels to add as shows.
func (l *Library) Search(ctx context.Context, query string) ([]Found, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: search for something", ErrBadShow)
	}
	cs, err := l.tool().SearchChannels(ctx, query, 12)
	if err != nil {
		return nil, err
	}
	out := make([]Found, 0, len(cs))
	for _, c := range cs {
		out = append(out, Found{ID: c.ID, Title: c.Name, URL: c.URL, Thumbnail: c.Image, Description: c.Description, Subscribers: c.Subscribers})
	}
	return out, nil
}

// Add adds the show at raw (a channel, one of its tabs, or a playlist; see
// source), listing it first, with minMinutes the shortest video that's an
// episode (DefaultMinMinutes when 0). A show already there is returned
// as it is.
func (l *Library) Add(ctx context.Context, raw string, minMinutes float64) (Show, error) {
	src, err := source(raw)
	if err != nil {
		return Show{}, err
	}
	if minMinutes <= 0 {
		minMinutes = DefaultMinMinutes
	}
	id := showID(src.url)
	l.mu.Lock()
	l.load()
	if s := l.find(id); s != nil {
		v := s.view()
		l.mu.Unlock()
		return v, nil
	}
	l.mu.Unlock()
	s := &show{ID: id, URL: src.url, MinMinutes: minMinutes}
	lst, err := l.list(ctx, src)
	if err != nil {
		return Show{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if old := l.find(id); old != nil { // added meanwhile
		return old.view(), nil
	}
	s.merge(lst, src, l.clock())
	l.st.Shows = append(l.st.Shows, s)
	l.save()
	l.poke()
	log.Printf("podcasts: added %s (%s), %d episodes", s.Title, s.URL, len(s.Episodes))
	return s.view(), nil
}

// Remove takes a show out of the library.
func (l *Library) Remove(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	i := slices.IndexFunc(l.st.Shows, func(s *show) bool { return s.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	for _, e := range l.st.Shows[i].Episodes {
		delete(l.resolved, e.ID)
	}
	l.st.Shows = slices.Delete(l.st.Shows, i, i+1)
	l.save()
	return nil
}

// Progress records how far an episode has been watched: Position
// seconds in, of duration (which fills in a length the library lacks).
// It's watched when the app says so, or within a minute of its end.
func (l *Library) Progress(id string, position, duration float64, watched bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.load()
	_, e := l.episode(id)
	if e == nil {
		return ErrNotFound
	}
	if e.Seconds <= 0 && duration > 0 {
		e.Seconds = duration
	}
	e.Position = max(position, 0)
	e.Watched = watched || e.Seconds > 0 && e.Position >= e.Seconds-60
	l.save()
	return nil
}

// find is the show with id. l.mu is held.
func (l *Library) find(id string) *show {
	for _, s := range l.st.Shows {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// episode is the episode with id, and its show. l.mu is held.
func (l *Library) episode(id string) (*show, *episode) {
	for _, s := range l.st.Shows {
		for _, e := range s.Episodes {
			if e.ID == id {
				return s, e
			}
		}
	}
	return nil, nil
}

// src is what a show lists: a channel's tab ("videos" or "streams"), or
// a playlist.
type src struct {
	url      string
	playlist bool
	tab      string
}

// source reads a show as given: a playlist's link or ID; or a channel, as
// its link, "@handle" or "UC…" ID, its videos unless the link is to its
// live streams' tab (/streams).
func source(raw string) (src, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return src{}, fmt.Errorf("%w: give a YouTube channel or playlist", ErrBadShow)
	}
	if page, err := youtube.Playlist(raw); err == nil {
		return src{url: page, playlist: true}, nil
	}
	u := raw
	if !strings.HasPrefix(u, "@") && !(strings.HasPrefix(u, "UC") && !strings.Contains(u, "/")) {
		if !strings.Contains(u, "://") {
			u = "https://" + u
		}
		p, err := url.Parse(u)
		host := ""
		if err == nil {
			host = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(p.Hostname()), "www."), "m.")
		}
		switch {
		case host == "youtu.be", host == "youtube.com" && (p.Path == "/watch" || strings.HasPrefix(p.Path, "/shorts/") || strings.HasPrefix(p.Path, "/live/")):
			return src{}, fmt.Errorf("%w: that's a link to a video; give its channel or a playlist", ErrBadShow)
		case host != "youtube.com":
			return src{}, fmt.Errorf("%w: give a YouTube channel or playlist", ErrBadShow)
		case strings.HasSuffix(strings.TrimRight(p.Path, "/"), "/podcasts"):
			return src{}, fmt.Errorf("%w: that's the channel's list of podcasts; give one of them, a playlist", ErrBadShow)
		}
		u = "https://www.youtube.com" + p.Path // one address for a show
	}
	tab := "videos"
	if strings.HasSuffix(strings.TrimRight(u, "/"), "/streams") {
		tab = "streams"
	}
	base := strings.TrimSuffix(youtube.VideosURL(u), "/videos")
	return src{url: base + "/" + tab, tab: tab}, nil
}

func (s *show) source() src {
	if strings.Contains(s.URL, "playlist?list=") {
		return src{url: s.URL, playlist: true}
	}
	tab := "videos"
	if strings.HasSuffix(s.URL, "/streams") {
		tab = "streams"
	}
	return src{url: s.URL, tab: tab}
}

// showID names a show by its address: short, and the same each time.
func showID(u string) string {
	sum := sha1.Sum([]byte(u))
	return hex.EncodeToString(sum[:6])
}

// Input is how to play an episode from from seconds in: where it streams
// from (found ahead, or now), for the stream server to copy its H.264
// picture and make its sound AAC, keeping every segment to seek in.
func (l *Library) Input(ctx context.Context, id string, from float64) (tuner.Input, error) {
	l.mu.Lock()
	l.load()
	_, e := l.episode(id)
	var length float64
	if e != nil {
		length = e.Seconds
	}
	l.mu.Unlock()
	if e == nil {
		return tuner.Input{}, ErrNotFound
	}
	info, err := l.resolve(ctx, id)
	if err != nil {
		return tuner.Input{}, err
	}
	in := info.Inputs()
	if from <= 5 || length > 0 && from >= length-5 {
		from = 0
	}
	start := time.Duration(from * float64(time.Second))
	args, audio := youtube.InputArgs(in, start, youtube.HTTPChunks(l.FFmpeg))
	out := tuner.Input{Args: args, Video: "0:v:0", VOD: true, Offset: from, Copy: strings.HasPrefix(in[0].VCodec, "avc1")}
	if audio != "" {
		out.Audio = []tuner.AudioTrack{{Map: audio}}
	}
	// English captions, as text for the app to lay out, timed from where
	// the stream starts.
	cctx, cancel := context.WithTimeout(ctx, playCaptionsTime)
	defer cancel()
	if cues := l.captionsFor(cctx, id, info); len(cues) > 0 {
		out.Subtitles = &cc.Script{}
		out.Subtitles.Set(0, 1000*time.Hour, cc.Shift(cues, -start))
	}
	return out, nil
}

const (
	// playCaptionsTime bounds waiting for an episode's captions at Play
	// (they're usually fetched ahead); captionsTime fetching them ahead.
	playCaptionsTime = 8 * time.Second
	captionsTime     = 30 * time.Second
	// keptCaptions bounds how many episodes' captions are kept.
	keptCaptions = 64
)

// captionsFor are an episode's English captions, the uploader's or
// YouTube's automatic ones, fetched once; nil when it has none. A failed
// fetch is tried again next time.
func (l *Library) captionsFor(ctx context.Context, id string, info youtube.Info) []cc.Cue {
	l.mu.Lock()
	cues, ok := l.captions[id]
	l.mu.Unlock()
	if ok {
		return cues
	}
	if sub, ok := info.Captions(); ok {
		raw, err := youtube.FetchCaptions(ctx, l.tool().Client(), sub)
		if err == nil {
			cues, err = cc.Parse(raw)
		}
		if err != nil {
			log.Printf("podcasts: captions for %s: %v", id, err)
			return nil
		}
	}
	l.mu.Lock()
	if l.captions == nil || len(l.captions) >= keptCaptions {
		l.captions = map[string][]cc.Cue{}
	}
	l.captions[id] = cues
	l.mu.Unlock()
	return cues
}
