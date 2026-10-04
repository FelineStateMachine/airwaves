package jellyfin

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Item is a series, movie, collection, playlist or library on the server.
type Item struct {
	ID       string
	Type     string // "Series", "Movie", "BoxSet", "Playlist" or "Library"
	Name     string
	Year     int
	Genres   []string
	Overview string
	Count    int           // episodes for a series, items for a collection or playlist
	Runtime  time.Duration // movies
	HasImage bool
}

// Key is how a channel config names the item: "Name (Year)", or "Name"
// without a year.
func (it Item) Key() string {
	if it.Year > 0 {
		return fmt.Sprintf("%s (%d)", it.Name, it.Year)
	}
	return it.Name
}

// Matches reports whether name, from a channel config, is this item: its
// name, or its Key to pick between same-named items. Case doesn't matter.
func (it Item) Matches(name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(name, it.Key()) || strings.EqualFold(name, it.Name)
}

// Video is an episode or movie, with what playing it and listing it in a
// guide take.
type Video struct {
	ID       string
	Source   string // the media source to play
	Type     string // "Episode" or "Movie"
	Name     string
	Series   string // the series' name, for an episode
	SeriesID string
	Season   int    // 0 when unknown, and for specials
	Episode  int    // 0 when unknown
	Premiere string // first aired, "2006-01-02", or "" when unknown
	Year     int
	Overview string
	Runtime  time.Duration
	Audio    bool
	// Image is artwork on the server, which loads without signing in: an
	// episode's still or its series' backdrop, a movie's backdrop or poster.
	Image string
	// AudioStreams and Subtitles are the video's sound and subtitle
	// streams, in the server's order. Files the server hasn't probed list
	// none.
	AudioStreams []Stream
	Subtitles    []Stream
}

// Stream is one of a video's sound or subtitle streams.
type Stream struct {
	// Index is the server's number for the stream, which subtitle URLs
	// and transcodes take.
	Index int
	// Position counts the streams of its kind in the file before it, as
	// ffmpeg's "0:a:1" does; -1 for a file beside the video.
	Position int
	Language string // as tagged, usually ISO 639-2 ("eng"); "" when untagged
	Codec    string // "aac", "subrip", "ass", "pgssub"...
	Title    string
	Default  bool
	Forced   bool
	// HearingImpaired marks subtitles for the deaf and hard of hearing
	// (SDH), which describe sounds as well as speech.
	HearingImpaired bool
	External        bool // a file beside the video
	// Text marks a text subtitle, which the server converts to SRT, as it
	// can't picture ones (PGS, VobSub).
	Text bool
}

// dto is the part of Jellyfin's BaseItemDto the client reads.
type dto struct {
	ID                      string `json:"Id"`
	Name                    string
	Type                    string
	MediaType               string
	ProductionYear          int
	PremiereDate            string
	Overview                string
	Genres                  []string
	ChildCount              int
	RecursiveItemCount      int
	RunTimeTicks            int64
	SeriesName              string
	SeriesID                string `json:"SeriesId"`
	SeriesPrimaryImageTag   string
	ParentIndexNumber       int
	IndexNumber             int
	ImageTags               map[string]string
	BackdropImageTags       []string
	ParentBackdropItemID    string `json:"ParentBackdropItemId"`
	ParentBackdropImageTags []string
	MediaSources            []struct {
		ID           string `json:"Id"`
		RunTimeTicks int64
		MediaStreams []mediaStream
	}
}

// mediaStream is the part of Jellyfin's MediaStream the client reads.
type mediaStream struct {
	Type                 string // "Video", "Audio", "Subtitle"...
	Index                int
	Language             string
	Codec                string
	Title                string
	IsDefault            bool
	IsForced             bool
	IsHearingImpaired    bool
	IsExternal           bool
	IsTextSubtitleStream bool
}

// ticks converts Jellyfin's 100 ns ticks.
func ticks(t int64) time.Duration { return time.Duration(t) * 100 }

// Series lists the account's series by name.
func (c *Client) Series(ctx context.Context) ([]Item, error) {
	return c.list(ctx, "Series", "Overview,Genres,RecursiveItemCount")
}

// Movies lists the account's movies by name.
func (c *Client) Movies(ctx context.Context) ([]Item, error) {
	return c.list(ctx, "Movie", "Overview,Genres")
}

// Collections lists the account's collections by name.
func (c *Client) Collections(ctx context.Context) ([]Item, error) {
	return c.list(ctx, "BoxSet", "Overview,Genres,ChildCount")
}

// Playlists lists the account's playlists by name.
func (c *Client) Playlists(ctx context.Context) ([]Item, error) {
	return c.list(ctx, "Playlist", "Overview,Genres,ChildCount")
}

// list returns every item of one type; without fields, only names and
// years. Music playlists are left out.
func (c *Client) list(ctx context.Context, kind, fields string) ([]Item, error) {
	q := url.Values{"recursive": {"true"}, "includeItemTypes": {kind}, "sortBy": {"SortName"}}
	if fields == "" {
		q.Set("enableImages", "false")
		q.Set("enableUserData", "false")
	} else {
		// User data stays on: series' episode counts come with it.
		q.Set("fields", fields)
		q.Set("enableImageTypes", "Primary")
		q.Set("imageTypeLimit", "1")
	}
	list, err := c.items(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(list))
	for _, d := range list {
		if d.Type == "Playlist" && d.MediaType == "Audio" {
			continue
		}
		it := Item{
			ID: d.ID, Type: d.Type, Name: d.Name, Year: d.ProductionYear, Genres: d.Genres,
			Overview: d.Overview, HasImage: d.ImageTags["Primary"] != "",
		}
		switch d.Type {
		case "Series":
			it.Count = d.RecursiveItemCount
		case "BoxSet", "Playlist":
			it.Count = d.ChildCount
		case "Movie":
			it.Runtime = ticks(d.RunTimeTicks)
		}
		out = append(out, it)
	}
	return out, nil
}

// Libraries lists the account's libraries (Jellyfin's user views).
func (c *Client) Libraries(ctx context.Context) ([]Item, error) {
	_, userID, err := c.signIn(ctx)
	if err != nil {
		return nil, err
	}
	path := "/UserViews"
	if userID == "" {
		path = "/Library/MediaFolders" // an API key acting as no user
	}
	var page struct{ Items []dto }
	if err := c.get(ctx, path, url.Values{}, &page); err != nil {
		return nil, err
	}
	var out []Item
	for _, d := range page.Items {
		out = append(out, Item{ID: d.ID, Type: "Library", Name: d.Name, HasImage: d.ImageTags["Primary"] != ""})
	}
	return out, nil
}

// Find looks up names from a channel config among the account's items of
// one kind ("Series", "Movie", "BoxSet", "Playlist" or "Library"), as
// Matches does. It returns the items found, without overviews or images,
// and the names that matched nothing.
func (c *Client) Find(ctx context.Context, kind string, names []string) (found []Item, unmatched []string, err error) {
	if len(names) == 0 {
		return nil, nil, nil
	}
	var all []Item
	if kind == "Library" {
		all, err = c.Libraries(ctx)
	} else {
		all, err = c.list(ctx, kind, "")
	}
	if err != nil {
		return nil, nil, err
	}
	for _, n := range names {
		hit := false
		for _, it := range all {
			if it.Matches(n) {
				found = append(found, it)
				hit = true
			}
		}
		if !hit {
			unmatched = append(unmatched, n)
		}
	}
	return found, unmatched, nil
}

// Videos returns the episodes and movies in items, each once: series,
// seasons, collections, playlists and libraries are expanded. Episodes the
// server knows of but has no file for are left out.
func (c *Client) Videos(ctx context.Context, items []Item) ([]Video, error) {
	var out []Video
	seen := map[string]bool{}
	add := func(list []dto) {
		for _, d := range list {
			if (d.Type == "Episode" || d.Type == "Movie") && !seen[d.ID] {
				seen[d.ID] = true
				out = append(out, c.video(d))
			}
		}
	}
	var ids []string
	for _, it := range items {
		if it.Type == "Episode" || it.Type == "Movie" {
			ids = append(ids, it.ID)
			continue
		}
		list, err := c.contents(ctx, it.ID, it.Type)
		if err != nil {
			return nil, err
		}
		add(list)
	}
	for len(ids) > 0 {
		n := min(len(ids), 50)
		list, err := c.items(ctx, videoQuery(url.Values{"ids": {strings.Join(ids[:n], ",")}}))
		if err != nil {
			return nil, err
		}
		add(list)
		ids = ids[n:]
	}
	return out, nil
}

// contents lists what's in a series, season, collection, playlist or
// library, with the fields a Video needs.
func (c *Client) contents(ctx context.Context, id, kind string) ([]dto, error) {
	q := videoQuery(url.Values{"parentId": {id}})
	switch kind {
	case "BoxSet", "Playlist":
		// Entries can be whole series or seasons as well as videos.
		list, err := c.items(ctx, q)
		if err != nil {
			return nil, err
		}
		var out []dto
		for _, d := range list {
			if d.Type != "Series" && d.Type != "Season" {
				out = append(out, d)
				continue
			}
			eps, err := c.contents(ctx, d.ID, d.Type)
			if err != nil {
				return nil, err
			}
			out = append(out, eps...)
		}
		return out, nil
	case "Series", "Season":
		q.Set("includeItemTypes", "Episode")
	default:
		q.Set("includeItemTypes", "Episode,Movie")
	}
	q.Set("recursive", "true")
	return c.items(ctx, q)
}

func videoQuery(q url.Values) url.Values {
	q.Set("fields", "Overview,MediaSources")
	q.Set("excludeLocationTypes", "Virtual")
	q.Set("enableUserData", "false")
	return q
}

const pageSize = 200

// items pages through /Items.
func (c *Client) items(ctx context.Context, q url.Values) ([]dto, error) {
	var out []dto
	for {
		q.Set("startIndex", strconv.Itoa(len(out)))
		q.Set("limit", strconv.Itoa(pageSize))
		var page struct {
			Items            []dto
			TotalRecordCount int
		}
		if err := c.get(ctx, "/Items", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if len(page.Items) == 0 || len(out) >= page.TotalRecordCount {
			return out, nil
		}
	}
}

func (c *Client) video(d dto) Video {
	v := Video{
		ID: d.ID, Source: d.ID, Type: d.Type, Name: d.Name, Series: d.SeriesName, SeriesID: d.SeriesID,
		Season: d.ParentIndexNumber, Episode: d.IndexNumber, Year: d.ProductionYear, Overview: d.Overview,
		Runtime: ticks(d.RunTimeTicks), Audio: true, Image: c.artwork(d),
	}
	if len(d.PremiereDate) >= len("2006-01-02") {
		v.Premiere = d.PremiereDate[:len("2006-01-02")]
	}
	if len(d.MediaSources) > 0 {
		src := d.MediaSources[0]
		v.Source = src.ID
		if src.RunTimeTicks > 0 {
			v.Runtime = ticks(src.RunTimeTicks)
		}
		// Unprobed files list no streams; assume those have sound.
		if len(src.MediaStreams) > 0 {
			v.Audio = false
			for _, s := range src.MediaStreams {
				v.Audio = v.Audio || s.Type == "Audio"
			}
		}
		v.AudioStreams, v.Subtitles = streams(src.MediaStreams)
	}
	return v
}

// streams sorts a media source's sound and subtitle streams out.
func streams(list []mediaStream) (audio, subs []Stream) {
	list = slices.Clone(list)
	slices.SortStableFunc(list, func(a, b mediaStream) int { return cmp.Compare(a.Index, b.Index) })
	pos := map[string]int{}
	for _, m := range list {
		s := Stream{
			Index: m.Index, Position: -1, Language: m.Language, Codec: m.Codec, Title: m.Title,
			Default: m.IsDefault, Forced: m.IsForced, HearingImpaired: m.IsHearingImpaired, External: m.IsExternal,
		}
		if !m.IsExternal {
			s.Position = pos[m.Type]
			pos[m.Type]++
		}
		switch m.Type {
		case "Audio":
			audio = append(audio, s)
		case "Subtitle":
			s.Text = m.IsTextSubtitleStream
			subs = append(subs, s)
		}
	}
	return audio, subs
}

// english reports whether a stream's language tag is English.
func english(lang string) bool {
	l, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(lang)), "-")
	return l == "en" || l == "eng" || l == "english"
}

// untagged reports whether a stream's language is unknown.
func untagged(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "und", "unknown":
		return true
	}
	return false
}

var (
	extraAudio  = regexp.MustCompile(`(?i)comment|descri`)
	sdhTitle    = regexp.MustCompile(`(?i)\b(sdh|cc|hearing)`)
	forcedTitle = regexp.MustCompile(`(?i)\b(forced|signs)\b`)
)

// PreferredAudio is the sound to play: English when there is some, else
// the default stream, else the first. Commentaries and described video
// come last, and the default goes first among equals. ok is false when
// the server lists no sound.
func (v Video) PreferredAudio() (s Stream, ok bool) {
	best := -1
	for _, a := range v.AudioStreams {
		score := 0
		if !extraAudio.MatchString(a.Title) {
			score += 4
		}
		if english(a.Language) {
			score += 2
		}
		if a.Default {
			score++
		}
		if score > best {
			s, best = a, score
		}
	}
	return s, best >= 0
}

// EnglishSubtitle picks the subtitles to caption v with: full English
// subtitles, else English SDH, else untagged ones, which are usually
// English. Forced subtitles, which only translate the odd sign or line in
// another language, are taken last and only when forced is set: when the
// sound isn't English they're better than nothing. Picture subtitles
// can't become captions, so they're passed over.
func (v Video) EnglishSubtitle(forced bool) (s Stream, ok bool) {
	best := -1
	for _, sub := range v.Subtitles {
		if !sub.Text {
			continue
		}
		isForced := sub.Forced || forcedTitle.MatchString(sub.Title)
		sdh := sub.HearingImpaired || sdhTitle.MatchString(sub.Title)
		var score int
		switch {
		case isForced && (!forced || !english(sub.Language)):
			continue
		case isForced:
			score = 1
		case english(sub.Language) && !sdh:
			score = 4
		case english(sub.Language):
			score = 3
		case untagged(sub.Language):
			score = 2
		default:
			continue
		}
		score = score * 2
		if sub.Default {
			score++
		}
		if score > best {
			s, best = sub, score
		}
	}
	return s, best >= 0
}

// Subtitle fetches one of v's text subtitle streams as SRT.
func (c *Client) Subtitle(ctx context.Context, v Video, s Stream) ([]byte, error) {
	if !s.Text {
		return nil, fmt.Errorf("jellyfin: subtitle %d of %s isn't text", s.Index, v.ID)
	}
	path := fmt.Sprintf("/Videos/%s/%s/Subtitles/%d/0/Stream.srt", url.PathEscape(v.ID), url.PathEscape(v.Source), s.Index)
	return c.fetch(ctx, path, 16<<20)
}

// artwork picks a landscape image where there is one, as the guide shows
// them: an episode's still, else its series' backdrop or poster; a movie's
// backdrop, else its poster.
func (c *Client) artwork(d dto) string {
	img := func(id, kind, tag string) string {
		return c.s.server + "/Items/" + url.PathEscape(id) + "/Images/" + kind + "?" + url.Values{"tag": {tag}, "maxWidth": {"640"}}.Encode()
	}
	switch {
	case d.Type == "Episode" && d.ImageTags["Primary"] != "":
		return img(d.ID, "Primary", d.ImageTags["Primary"])
	case d.Type == "Episode" && d.ParentBackdropItemID != "" && len(d.ParentBackdropImageTags) > 0:
		return img(d.ParentBackdropItemID, "Backdrop", d.ParentBackdropImageTags[0])
	case d.Type == "Episode" && d.SeriesID != "" && d.SeriesPrimaryImageTag != "":
		return img(d.SeriesID, "Primary", d.SeriesPrimaryImageTag)
	case d.Type == "Movie" && len(d.BackdropImageTags) > 0:
		return img(d.ID, "Backdrop", d.BackdropImageTags[0])
	case d.Type == "Movie" && d.ImageTags["Primary"] != "":
		return img(d.ID, "Primary", d.ImageTags["Primary"])
	}
	return ""
}

// FileURL streams v's original file, which ffmpeg can seek in with range
// requests. Send Authorization with it.
func (c *Client) FileURL(v Video) string {
	return c.s.server + "/Videos/" + url.PathEscape(v.ID) + "/stream?" + url.Values{"static": {"true"}, "mediaSourceId": {v.Source}}.Encode()
}

// Transcode is what to ask the server to convert a stream to.
type Transcode struct {
	Bitrate   int // bits per second, picture and sound together
	MaxWidth  int
	MaxHeight int
}

// transcodeAudio is the sound's share of a transcode's bitrate.
const transcodeAudio = 128_000

// TranscodeURL streams v from start as MPEG-TS, converted by the server to
// H.264 and stereo AAC within t. The server seeks, so the stream begins at
// start. Of several audio streams it plays the one PreferredAudio picks.
// Send Authorization with it.
func (c *Client) TranscodeURL(v Video, start time.Duration, t Transcode) string {
	q := url.Values{
		"mediaSourceId": {v.Source},
		"videoCodec":    {"h264"}, "videoBitRate": {strconv.Itoa(max(t.Bitrate-transcodeAudio, t.Bitrate/2))},
		"audioCodec": {"aac"}, "audioBitRate": {strconv.Itoa(transcodeAudio)}, "maxAudioChannels": {"2"},
	}
	if a, ok := v.PreferredAudio(); ok && len(v.AudioStreams) > 1 {
		q.Set("audioStreamIndex", strconv.Itoa(a.Index))
	}
	if t.MaxWidth > 0 {
		q.Set("maxWidth", strconv.Itoa(t.MaxWidth))
	}
	if t.MaxHeight > 0 {
		q.Set("maxHeight", strconv.Itoa(t.MaxHeight))
	}
	if start > 0 {
		q.Set("startTimeTicks", strconv.FormatInt(int64(start/100), 10))
	}
	return c.s.server + "/Videos/" + url.PathEscape(v.ID) + "/stream.ts?" + q.Encode()
}

// Image fetches an item's primary image (poster), scaled to maxWidth, and
// its content type.
func (c *Client) Image(ctx context.Context, id string, maxWidth int) ([]byte, string, error) {
	token, _, err := c.signIn(ctx)
	if err != nil {
		return nil, "", err
	}
	q := url.Values{}
	if maxWidth > 0 {
		q.Set("maxWidth", strconv.Itoa(maxWidth))
	}
	resp, err := c.request(ctx, http.MethodGet, "/Items/"+url.PathEscape(id)+"/Images/Primary", q, nil, token)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("jellyfin: image for %s: %s", id, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", err
	}
	return b, resp.Header.Get("Content-Type"), nil
}
