package youtube

import (
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VideosURL turns a channel, as its link, "@handle" or "UC…" ID, into its
// uploads tab.
func VideosURL(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "@"):
		s = "https://www.youtube.com/" + s
	case strings.HasPrefix(s, "UC") && !strings.Contains(s, "/"):
		s = "https://www.youtube.com/channel/" + s
	case !strings.Contains(s, "://"):
		s = "https://" + s
	}
	s = strings.TrimRight(s, "/")
	for _, tab := range []string{"/videos", "/featured", "/streams", "/shorts", "/playlists", "/about"} {
		s = strings.TrimSuffix(s, tab)
	}
	return s + "/videos"
}

// FeedURL is a channel's feed of its newest 15 uploads.
func FeedURL(channelID string) string {
	return "https://www.youtube.com/feeds/videos.xml?channel_id=" + channelID
}

// WatchURL is a video's page.
func WatchURL(id string) string { return "https://www.youtube.com/watch?v=" + id }

// Thumbnail is a video's thumbnail, 480x360, which every video has.
func Thumbnail(id string) string { return "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg" }

// Listing is a channel's tab or a playlist, from yt-dlp --flat-playlist
// -J with ListingArgs or PlaylistArgs.
type Listing struct {
	ID, Name string
	Entries  []Entry // a channel's newest first; a playlist's in its order
	// Owner and OwnerID are a playlist's channel (see ParsePlaylist).
	Owner, OwnerID string
}

// Entry is a video in a listing.
type Entry struct {
	ID, Title string
	Seconds   int // 0 when the listing doesn't say
	// Published is the upload date, roughly, from YouTube's "3 years
	// ago"; zero when the listing doesn't say.
	Published time.Time
	// Skip is "upcoming" for a premiere or a stream to come, "live" for
	// one on now or not yet processed (and in a channel's tab, one that
	// was live), else "".
	Skip string
	// Channel is a playlist's video's channel, which may not be the
	// playlist's.
	Channel string
}

// ListingArgs list a channel's tab with approximate upload dates (from
// YouTube's "3 years ago"; no extra requests).
var ListingArgs = []string{"--flat-playlist", "-J", "--extractor-args", "youtubetab:approximate_date"}

// PlaylistArgs list a playlist: its videos in order, with their lengths.
var PlaylistArgs = []string{"--flat-playlist", "-J"}

// ParseListing reads a channel tab's listing.
func ParseListing(raw []byte) (Listing, error) {
	var p struct {
		Channel   string `json:"channel"`
		ChannelID string `json:"channel_id"`
		Entries   []struct {
			ID         string  `json:"id"`
			Title      string  `json:"title"`
			Duration   float64 `json:"duration"`
			Timestamp  float64 `json:"timestamp"`
			LiveStatus string  `json:"live_status"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Listing{}, err
	}
	if p.ChannelID == "" {
		return Listing{}, errors.New("not a YouTube channel")
	}
	l := Listing{ID: p.ChannelID, Name: p.Channel}
	for _, e := range p.Entries {
		if e.ID == "" {
			continue
		}
		v := Entry{ID: e.ID, Title: e.Title, Seconds: int(math.Round(e.Duration))}
		if e.Timestamp > 0 {
			v.Published = time.Unix(int64(e.Timestamp), 0).UTC()
		}
		switch e.LiveStatus {
		case "is_upcoming":
			v.Skip = "upcoming"
		case "is_live", "post_live", "was_live":
			v.Skip = "live"
		}
		l.Entries = append(l.Entries, v)
	}
	return l, nil
}

var (
	// hidden are the titles YouTube lists videos that can't play under.
	hidden = map[string]bool{"[Private video]": true, "[Deleted video]": true, "[Unavailable video]": true}
	// locked are the availabilities of videos that only some can watch.
	locked = map[string]bool{"private": true, "premium_only": true, "subscriber_only": true, "needs_auth": true}
)

// ParsePlaylist reads a playlist's listing: its videos in its order, each
// once. Videos that can't play here (private, deleted, members only) are
// left out; live and upcoming ones are kept, marked, to join once they
// can play.
func ParsePlaylist(raw []byte) (Listing, error) {
	var p struct {
		Type      string `json:"_type"`
		ID        string `json:"id"`
		Title     string `json:"title"`
		Channel   string `json:"channel"`
		ChannelID string `json:"channel_id"`
		Uploader  string `json:"uploader"`
		Entries   []struct {
			ID           string  `json:"id"`
			Title        string  `json:"title"`
			Duration     float64 `json:"duration"`
			Timestamp    float64 `json:"timestamp"`
			Channel      string  `json:"channel"`
			Uploader     string  `json:"uploader"`
			LiveStatus   string  `json:"live_status"`
			Availability string  `json:"availability"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Listing{}, err
	}
	if p.Type != "playlist" || p.ID == "" || p.ID == p.ChannelID { // not a channel's tab
		return Listing{}, errors.New("not a YouTube playlist")
	}
	l := Listing{ID: p.ID, Name: p.Title, Owner: cmp.Or(p.Channel, p.Uploader), OwnerID: p.ChannelID}
	seen := map[string]bool{}
	for _, e := range p.Entries {
		if e.ID == "" || seen[e.ID] || hidden[e.Title] || locked[e.Availability] {
			continue
		}
		seen[e.ID] = true
		v := Entry{ID: e.ID, Title: e.Title, Seconds: int(math.Round(e.Duration)), Channel: cmp.Or(e.Channel, e.Uploader)}
		if e.Timestamp > 0 {
			v.Published = time.Unix(int64(e.Timestamp), 0).UTC()
		}
		switch e.LiveStatus {
		case "is_upcoming":
			v.Skip = "upcoming"
		case "is_live", "post_live":
			v.Skip = "live"
		}
		l.Entries = append(l.Entries, v)
	}
	return l, nil
}

var (
	errNotPlaylist      = errors.New("that isn't a YouTube playlist: use its link (youtube.com/playlist?list=...) or its ID")
	errVideoNotPlaylist = errors.New("that's a link to a video: use the playlist's link, with list= in it")
	errMix              = errors.New("that's one of YouTube's mixes, which change as they play: use a playlist's link")
	// listID is a playlist's ID in a link, and bareList one given on its
	// own: "PL…", a channel's uploads ("UU…") or an album ("OLAK5uy_…").
	listID   = regexp.MustCompile(`^[A-Za-z0-9_-]{2,64}$`)
	bareList = regexp.MustCompile(`^(PL|UU|UL|FL|OLAK5uy_)[A-Za-z0-9_-]{10,62}$`)
)

// Playlist checks a playlist given as its link (or the link of a video in
// it) or its ID, and returns its page: https://www.youtube.com/playlist?list=
// and the ID.
func Playlist(s string) (string, error) {
	s = strings.TrimSpace(s)
	id := s
	if !bareList.MatchString(s) {
		raw := s
		if !strings.Contains(s, "://") {
			raw = "https://" + s
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "", errNotPlaylist
		}
		host := strings.ToLower(u.Hostname())
		for _, p := range []string{"www.", "m.", "music."} {
			host = strings.TrimPrefix(host, p)
		}
		if host != "youtube.com" && host != "youtu.be" {
			return "", errNotPlaylist
		}
		id = u.Query().Get("list")
		switch {
		case id != "":
		case host == "youtu.be", u.Path == "/watch", strings.HasPrefix(u.Path, "/shorts/"), strings.HasPrefix(u.Path, "/live/"):
			return "", errVideoNotPlaylist
		default:
			return "", errNotPlaylist
		}
	}
	switch {
	case !listID.MatchString(id):
		return "", errNotPlaylist
	case strings.HasPrefix(id, "RD"):
		return "", errMix
	}
	return PlaylistURL(id), nil
}

// PlaylistURL is a playlist's page.
func PlaylistURL(id string) string { return "https://www.youtube.com/playlist?list=" + id }

// PlaylistInfo is a playlist as a lookup finds it.
type PlaylistInfo struct {
	ID, URL        string
	Title          string
	Owner, OwnerID string // the channel that made it
	// Videos is how many it has, as YouTube counts them, ones that can't
	// play included.
	Videos int
	Image  string // its first video's thumbnail
}

// LookupPlaylist finds the playlist at page (as Playlist gives it): its
// title, whose it is, and how many videos it has.
func (t *YtDlp) LookupPlaylist(ctx context.Context, page string) (PlaylistInfo, error) {
	raw, err := t.Run(ctx, "--flat-playlist", "-J", "--playlist-end", "1", page)
	if err != nil {
		return PlaylistInfo{}, err
	}
	l, err := ParsePlaylist(raw)
	if err != nil {
		return PlaylistInfo{}, err
	}
	var p struct {
		Count int `json:"playlist_count"`
	}
	_ = json.Unmarshal(raw, &p)
	info := PlaylistInfo{ID: l.ID, URL: PlaylistURL(l.ID), Title: l.Name, Owner: l.Owner, OwnerID: l.OwnerID, Videos: p.Count}
	if len(l.Entries) > 0 {
		info.Image = Thumbnail(l.Entries[0].ID)
	}
	return info, nil
}

// FeedEntry is an upload in a channel's feed: the newest 15, Shorts and
// streams included, without lengths.
type FeedEntry struct {
	ID          string    `xml:"videoId"`
	Title       string    `xml:"title"`
	Published   time.Time `xml:"published"`
	Description string    `xml:"group>description"`
	Link        struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

// Short reports whether the entry is a Short, by its link.
func (e FeedEntry) Short() bool { return strings.Contains(e.Link.Href, "/shorts/") }

// ParseFeed reads a channel's feed.
func ParseFeed(raw []byte) ([]FeedEntry, error) {
	var f struct {
		Entries []FeedEntry `xml:"entry"`
	}
	err := xml.Unmarshal(raw, &f)
	return f.Entries, err
}

// Channel is a YouTube channel as a search or a lookup finds it.
type Channel struct {
	ID          string // "UC…"
	URL         string // its page: by handle, when it has one
	Handle      string // "@name"; some channels have none
	Name        string
	Subscribers int64
	Description string
	Image       string // its avatar
	// Videos is roughly how many videos it has uploaded, Shorts and
	// streams included; 0 when unknown. Only lookups count them.
	Videos int
}

// channelsOnly narrows YouTube's search results to channels.
const channelsOnly = "EgIQAg%3D%3D"

// SearchChannels asks YouTube for the channels matching query, n at most.
// YouTube's Topic channels, which it makes for artists' music, are left
// out: they have no uploads to play.
func (t *YtDlp) SearchChannels(ctx context.Context, query string, n int) ([]Channel, error) {
	u := "https://www.youtube.com/results?search_query=" + url.QueryEscape(query) + "&sp=" + channelsOnly
	raw, err := t.Run(ctx, "--flat-playlist", "-J", "--playlist-end", strconv.Itoa(n), u)
	if err != nil {
		return nil, err
	}
	var p struct {
		Entries []channelInfo `json:"entries"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	out := []Channel{}
	for _, e := range p.Entries {
		c := e.channel()
		if !strings.HasPrefix(c.ID, "UC") || c.Handle == "" && strings.HasSuffix(c.Name, " - Topic") {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// LookupChannel finds the channel at page (its link, "@handle" or "UC…"
// ID), and roughly how many videos it has.
func (t *YtDlp) LookupChannel(ctx context.Context, page string) (Channel, error) {
	raw, err := t.Run(ctx, "--flat-playlist", "-J", "--playlist-end", "1", VideosURL(page))
	if err != nil {
		return Channel{}, err
	}
	var e channelInfo
	if err := json.Unmarshal(raw, &e); err != nil {
		return Channel{}, err
	}
	c := e.channel()
	if !strings.HasPrefix(c.ID, "UC") {
		return Channel{}, errors.New("not a YouTube channel")
	}
	// The channel's uploads playlist says how many there are; its tab
	// doesn't.
	if raw, err := t.Run(ctx, "--flat-playlist", "-J", "--playlist-end", "1",
		"https://www.youtube.com/playlist?list=UU"+c.ID[2:]); err == nil {
		var p struct {
			Count int `json:"playlist_count"`
		}
		if json.Unmarshal(raw, &p) == nil {
			c.Videos = p.Count
		}
	}
	return c, nil
}

// channelInfo is a channel in yt-dlp's output: a search result, or the
// top of a channel's tab.
type channelInfo struct {
	ChannelID   string  `json:"channel_id"`
	Channel     string  `json:"channel"`
	Title       string  `json:"title"`
	UploaderID  string  `json:"uploader_id"`
	Followers   float64 `json:"channel_follower_count"`
	Description string  `json:"description"`
	Thumbnails  []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		ID     string `json:"id"`
	} `json:"thumbnails"`
}

func (e channelInfo) channel() Channel {
	c := Channel{ID: e.ChannelID, Name: cmp.Or(e.Channel, e.Title), Subscribers: int64(e.Followers),
		Description: Blurb(e.Description)}
	switch {
	case strings.HasPrefix(e.UploaderID, "@"):
		c.Handle = e.UploaderID
		c.URL = "https://www.youtube.com/" + c.Handle
	case c.ID != "":
		c.URL = "https://www.youtube.com/channel/" + c.ID
	}
	// The avatar is the largest square picture (the others are banners),
	// asked for at a size to show.
	size := 0
	for _, th := range e.Thumbnails {
		if th.Width > size && th.Width == th.Height {
			c.Image, size = th.URL, th.Width
		}
	}
	for _, th := range e.Thumbnails {
		if c.Image == "" && th.ID == "avatar_uncropped" {
			c.Image = th.URL
		}
	}
	if strings.HasPrefix(c.Image, "//") {
		c.Image = "https:" + c.Image
	}
	if i := strings.LastIndex(c.Image, "="); i > 0 {
		c.Image = c.Image[:i] + imageSize.ReplaceAllString(c.Image[i:], "=s176")
	}
	return c
}

// imageSize is the size option of a YouTube image address ("=s900-c-k").
var imageSize = regexp.MustCompile(`^=s\d+`)

// Blurb makes a channel's description one short paragraph. Search
// results come cut short with "...".
func Blurb(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if t, ok := strings.CutSuffix(s, " ..."); ok {
		s = t + "…"
	}
	if r := []rune(s); len(r) > 300 {
		s = strings.TrimSpace(string(r[:299])) + "…"
	}
	return s
}
