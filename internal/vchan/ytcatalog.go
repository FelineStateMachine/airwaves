package vchan

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"airwaves/internal/youtube"
)

// ytConfig is a YouTube channel's youtube.json.
type ytConfig struct {
	// Channels are the YouTube channels whose uploads play: channel URLs,
	// "@handle"s or "UC…" channel IDs.
	Channels []string `json:"channels"`
	// Playlist, instead of Channels, plays a playlist in its order,
	// looping: its link or ID (see ytplaylist.go). Of the settings below,
	// only MaxHeight applies to it.
	Playlist string `json:"playlist,omitempty"`
	// RepeatDays is how long before a video may air again, when the
	// channels have enough videos.
	RepeatDays float64 `json:"repeatDays"`
	MaxHeight  int     `json:"maxHeight"` // picture height to stream, at most
	// MinMinutes and MaxMinutes leave out shorter and longer videos;
	// MaxMinutes 0 has no limit.
	MinMinutes float64 `json:"minMinutes"`
	MaxMinutes float64 `json:"maxMinutes"`
	// RerunMix is how reruns lean to recent uploads: "recent" (about 70%
	// from the last six months, 20% from the 18 months before, 10% from
	// any time), "balanced" (40/30/30) or "any" (all equally likely).
	RerunMix string `json:"rerunMix"`
	// LockHours is how far ahead the schedule is kept as it is when new
	// uploads arrive; they go in after that. Editing the config replans
	// at once regardless.
	LockHours float64 `json:"lockHours"`
	// MaxAgeDays, when set, leaves out videos uploaded longer ago, first
	// runs and reruns alike, as ages are judged for the rerun mix. Videos
	// age out as days pass; 0 has no limit.
	MaxAgeDays float64 `json:"maxAgeDays"`
	// DeadAir leaves the channel off the air rather than repeat a video
	// within RepeatDays: a YouTube channel with nothing rested sits out
	// its turn, and with none left, "Please stand by" plays until a new
	// upload arrives or a video is rested again.
	DeadAir bool `json:"deadAir"`
}

// ytRerunMixes are the rerun mixes' shares of reruns from the last six
// months, the 18 months before, and any time.
var ytRerunMixes = map[string][3]float64{
	"recent":   {0.7, 0.2, 0.1},
	"balanced": {0.4, 0.3, 0.3},
	"any":      {0, 0, 1},
}

func readYtConfig(path string) (ytConfig, error) {
	c := ytConfig{RepeatDays: 30, MaxHeight: 720, MinMinutes: 3, MaxMinutes: 240, RerunMix: "balanced", LockHours: 2}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	switch {
	case c.Playlist != "" && len(c.Channels) > 0:
		return c, fmt.Errorf("%s names both channels and a playlist; it plays one or the other", filepath.Base(path))
	case c.Playlist != "":
		if _, err := youtube.Playlist(c.Playlist); err != nil {
			return c, fmt.Errorf("%s: playlist %q: %w", filepath.Base(path), c.Playlist, err)
		}
	case len(c.Channels) == 0:
		return c, fmt.Errorf("%s names no channels or playlist", filepath.Base(path))
	}
	if _, ok := ytRerunMixes[c.RerunMix]; !ok {
		return c, fmt.Errorf("%s: rerunMix is %q; it can be \"recent\", \"balanced\" or \"any\"", filepath.Base(path), c.RerunMix)
	}
	if c.MaxHeight <= 0 {
		c.MaxHeight = 720
	}
	c.LockHours = max(c.LockHours, 0)
	c.MaxAgeDays = max(c.MaxAgeDays, 0)
	return c, nil
}

func (c ytConfig) repeat() time.Duration {
	return time.Duration(c.RepeatDays * float64(24*time.Hour))
}

// maxAge is MaxAgeDays as a duration, 0 for no limit.
func (c ytConfig) maxAge() time.Duration {
	return time.Duration(c.MaxAgeDays * float64(24*time.Hour))
}

// playable reports whether v can go on the schedule.
func (c ytConfig) playable(v *ytVideo) bool {
	if v.Skip != "" || v.Seconds <= 0 {
		return false
	}
	m := float64(v.Seconds) / 60
	return m >= c.MinMinutes && (c.MaxMinutes <= 0 || m <= c.MaxMinutes)
}

// ytCatalog is what's known about the channels' uploads: metadata only,
// kept in the channel folder so restarts don't list them again.
type ytCatalog struct {
	Sources []*ytSource `json:"sources"`
}

// ytSource is one YouTube channel's uploads.
type ytSource struct {
	URL     string    `json:"url"` // as configured
	ID      string    `json:"id"`  // "UC…"
	Name    string    `json:"name"`
	Listed  time.Time `json:"listed,omitzero"`  // the last full listing
	Checked time.Time `json:"checked,omitzero"` // the last feed check
	// Dated is set once a listing has asked for upload dates; catalogs
	// listed before that are listed again straight away.
	Dated  bool       `json:"dated,omitempty"`
	Videos []*ytVideo `json:"videos"` // newest first; a playlist's in its order
	// Owner and OwnerID are a playlist's channel, the one that made it.
	Owner   string `json:"owner,omitempty"`
	OwnerID string `json:"ownerID,omitempty"`
}

// ytVideo is an upload.
type ytVideo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Seconds int    `json:"seconds,omitempty"` // 0 until known
	// Published is the upload date; Approx marks one a listing gave,
	// from YouTube's "3 years ago", rather than the feed or a lookup.
	Published   time.Time `json:"published,omitzero"`
	Approx      bool      `json:"approx,omitempty"`
	Description string    `json:"description,omitempty"` // its first paragraph
	// Found is when a video that came after the first listing was found.
	Found time.Time `json:"found,omitzero"`
	New   bool      `json:"new,omitempty"` // its first run is due
	// Skip says why the video can't play: "short", "live", "upcoming" (a
	// premiere, checked again later) or "gone".
	Skip   string    `json:"skip,omitempty"`
	Looked time.Time `json:"looked,omitzero"` // the last lookup of its details
	// Loudness is its sound's, once measured (see ytloudness.go).
	Loudness *loudness `json:"loudness,omitempty"`
	// AudioLang is its sound's language as YouTube gives it ("en-US",
	// "es"), "" until looked up or when YouTube doesn't say; Captions is
	// whether it has English captions, the uploader's or YouTube's.
	AudioLang string `json:"audioLang,omitempty"`
	Captions  bool   `json:"captions,omitempty"`
	// Channel is the name of a playlist's video's channel, which may not
	// be the playlist's.
	Channel string `json:"channel,omitempty"`
}

func readYtCatalog(path string) (ytCatalog, error) {
	var c ytCatalog
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}

// source returns the catalog of the channel configured as url, adding an
// empty one.
func (c *ytCatalog) source(url string) *ytSource {
	for _, s := range c.Sources {
		if s.URL == url {
			return s
		}
	}
	s := &ytSource{URL: url}
	c.Sources = append(c.Sources, s)
	return s
}

// ytListing is a channel's uploads tab, from yt-dlp --flat-playlist -J
// with youtube.ListingArgs.
type ytListing struct {
	ID, Name string
	Videos   []*ytVideo // newest first; a playlist's in its order
	// Owner and OwnerID are a playlist's channel (see parseYtPlaylist).
	Owner, OwnerID string
}

func parseYtListing(raw []byte) (ytListing, error) {
	l, err := youtube.ParseListing(raw)
	return listingOf(l, true), err
}

// listingOf is a listing as the catalog keeps it: dated, its upload
// dates, rough as they are.
func listingOf(l youtube.Listing, dated bool) ytListing {
	out := ytListing{ID: l.ID, Name: l.Name, Owner: l.Owner, OwnerID: l.OwnerID}
	for _, e := range l.Entries {
		v := &ytVideo{ID: e.ID, Title: e.Title, Seconds: e.Seconds, Skip: e.Skip, Channel: e.Channel}
		if dated && !e.Published.IsZero() {
			v.Published, v.Approx = e.Published, true
		}
		out.Videos = append(out.Videos, v)
	}
	return out
}

// ytNewAtTop is how far down a listing a video it didn't have before
// counts as a new upload, due a first run, rather than an old one that
// came back.
const ytNewAtTop = 15

// mergeListing replaces the source's videos with a full listing, keeping
// what's known about each. Videos found lately that the listing lacks
// stay a while: Shorts and streams from the feed, and uploads the listing
// doesn't show yet.
func (s *ytSource) mergeListing(l ytListing, now time.Time) (added int) {
	old := map[string]*ytVideo{}
	for _, v := range s.Videos {
		old[v.ID] = v
	}
	first := s.Listed.IsZero()
	seen := map[string]bool{}
	var out []*ytVideo
	for i, v := range l.Videos {
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		if o := old[v.ID]; o != nil {
			o.Title = v.Title
			if v.Seconds > 0 {
				o.Seconds = v.Seconds
			}
			if o.Skip == "upcoming" && v.Skip == "" && v.Seconds > 0 {
				o.Skip = ""
			}
			if !v.Published.IsZero() && (o.Published.IsZero() || o.Approx) {
				o.Published, o.Approx = v.Published, true
			}
			out = append(out, o)
			continue
		}
		if !first {
			v.Found = now
			v.New = i < ytNewAtTop && v.Skip == ""
			added++
		}
		out = append(out, v)
	}
	var recent []*ytVideo // ahead of the listing, keeping newest first
	for _, o := range s.Videos {
		if !seen[o.ID] && !o.Found.IsZero() && now.Sub(o.Found) < 3*24*time.Hour {
			recent = append(recent, o)
		}
	}
	s.ID, s.Name, s.Listed, s.Dated, s.Videos = l.ID, l.Name, now, true, append(recent, out...)
	return added
}

// mergeNewest takes in the top of a listing in the feed's place: uploads
// the source doesn't have yet are new, due a first run, as from the feed,
// and ones it has get a length or rough date they lack. The rest of the
// catalog is left as it is.
func (s *ytSource) mergeNewest(l ytListing, now time.Time) (added int, changed bool) {
	known := map[string]*ytVideo{}
	for _, v := range s.Videos {
		known[v.ID] = v
	}
	var fresh []*ytVideo
	for _, v := range l.Videos {
		if o := known[v.ID]; o != nil {
			if o.Seconds == 0 && v.Seconds > 0 {
				o.Seconds, changed = v.Seconds, true
			}
			if o.Published.IsZero() && !v.Published.IsZero() {
				o.Published, o.Approx, changed = v.Published, true, true
			}
			continue
		}
		v.Found, v.New = now, v.Skip == ""
		if v.New {
			added++
		}
		known[v.ID] = v
		fresh = append(fresh, v)
	}
	s.Videos = append(fresh, s.Videos...)
	return added, changed || len(fresh) > 0
}

// mergeFeed adds the feed's uploads the source doesn't have yet, as new
// videos due a first run once their details are looked up, and fills in
// the dates and descriptions of ones it has. Shorts are recognized by
// their links, and kept only so they aren't looked at again. added counts
// the new uploads.
func (s *ytSource) mergeFeed(entries []youtube.FeedEntry, now time.Time) (added int, changed bool) {
	known := map[string]*ytVideo{}
	for _, v := range s.Videos {
		known[v.ID] = v
	}
	var fresh []*ytVideo
	for _, e := range entries {
		if e.ID == "" {
			continue
		}
		if v := known[e.ID]; v != nil {
			if (v.Published.IsZero() || v.Approx) && !e.Published.IsZero() {
				v.Published, v.Approx, changed = e.Published, false, true
			}
			if d := youtube.Description(e.Description); v.Description == "" && d != "" {
				v.Description, changed = d, true
			}
			continue
		}
		v := &ytVideo{ID: e.ID, Title: e.Title, Published: e.Published,
			Description: youtube.Description(e.Description), Found: now, New: true}
		if e.Short() {
			v.Skip, v.New = "short", false
		} else {
			added++
		}
		known[e.ID] = v
		fresh = append(fresh, v)
	}
	s.Videos = append(fresh, s.Videos...)
	s.Checked = now
	return added, changed || len(fresh) > 0
}

// learn takes in a video's looked-up details.
func (v *ytVideo) learn(i youtube.Info, now time.Time) {
	v.Looked = now
	if i.Title != "" {
		v.Title = i.Title
	}
	if i.Duration > 0 {
		v.Seconds = int(math.Round(i.Duration))
	}
	if i.Timestamp > 0 {
		v.Published, v.Approx = time.Unix(int64(i.Timestamp), 0).UTC(), false
	}
	if d := youtube.Description(i.Description); d != "" {
		v.Description = d
	}
	if l := i.AudioLang(); l != "" {
		v.AudioLang = l
	}
	_, v.Captions = i.Captions()
	switch {
	case i.MediaType == "short":
		v.Skip = "short"
	case i.LiveStatus == "is_upcoming":
		v.Skip = "upcoming"
	case i.LiveStatus == "is_live" || i.LiveStatus == "post_live" || i.LiveStatus == "was_live" || i.MediaType == "livestream":
		v.Skip = "live"
	case v.Skip == "upcoming" || v.Skip == "gone":
		v.Skip = ""
	}
	if v.Skip != "" {
		v.New = false
	}
}
