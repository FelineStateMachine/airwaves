package vchan

import (
	"path/filepath"
	"sync"
	"time"
)

// YouTubeConfig is a YouTube channel's youtube.json, with the settings it
// leaves out filled in.
type YouTubeConfig = ytConfig

// ReadYouTubeConfig reads the youtube.json in a YouTube channel's folder.
// The settings come back, defaults included, even with an error.
func ReadYouTubeConfig(dir string) (YouTubeConfig, error) {
	return readYtConfig(filepath.Join(dir, youtubeConfig))
}

// YouTubeDefaults are the settings a youtube.json may leave out.
func YouTubeDefaults() YouTubeConfig {
	c, _ := readYtConfig("") // the defaults, and an error for the missing file
	return c
}

// YouTubeFiles are the files a YouTube channel keeps in its folder: its
// youtube.json first, then the hidden catalog and schedule, with their
// copies being written.
func YouTubeFiles() []string {
	return []string{youtubeConfig, ytCatalogFile, ytCatalogFile + ".tmp", ytPlayoutFile, ytPlayoutFile + ".tmp"}
}

// YouTubeStatus is how a YouTube channel is getting on.
type YouTubeStatus struct {
	Config  YouTubeConfig
	Err     error           // the config's
	Sources []YouTubeSource // as configured
	Busy    bool            // a refresh is running
}

// YouTubeSource is one of the YouTube channels a channel plays, or the
// playlist it plays.
type YouTubeSource struct {
	Channel  string // as in youtube.json
	URL      string // its uploads tab, or the playlist's page
	ID, Name string // once listed: a playlist's ID and title
	Videos   int    // in the catalog
	Playable int    // of those, the ones that fit the channel's settings now, age included; a playlist's that can play
	// Owner and OwnerID are a playlist's channel, and Image its picture,
	// its first video's thumbnail.
	Owner, OwnerID string
	Image          string
	Listed         time.Time
	// Listing is set while it's being listed, or waiting its turn to be;
	// Since is when that was first seen.
	Listing bool
	Since   time.Time
	// Failed is when the last listing failed, if none has worked since.
	Failed time.Time
}

// Status reports the channel's sources: what their catalogs hold, and
// listings under way or failed. Like the guide, it starts a refresh
// that's due.
func (y *YouTube) Status() YouTubeStatus {
	y.mu.Lock()
	defer y.mu.Unlock()
	y.tend()
	now := y.clock()
	st := YouTubeStatus{Config: y.cfg, Err: y.err, Busy: y.busy}
	if y.err != nil {
		return st
	}
	// tend has started a refresh for any source due a listing.
	add := func(src YouTubeSource) {
		if t := y.tried[src.URL]; t.After(src.Listed) {
			src.Failed = t
		}
		src.Since = listingSince(y.Dir+"\x00"+src.URL, src.Listing, now)
		st.Sources = append(st.Sources, src)
	}
	if y.isPlaylist() {
		url, _ := YouTubePlaylist(y.cfg.Playlist)
		if s := y.source(url); s != nil {
			seq, _ := y.sequence()
			src := YouTubeSource{Channel: y.cfg.Playlist, URL: s.URL, ID: s.ID, Name: s.Name, Videos: len(s.Videos),
				Playable: len(seq), Owner: s.Owner, OwnerID: s.OwnerID, Listed: s.Listed, Listing: y.listDue(s, now)}
			if len(seq) > 0 {
				src.Image = ytThumbnail(seq[0].ID)
			}
			add(src)
		}
		return st
	}
	seen := map[string]bool{}
	for _, c := range y.cfg.Channels {
		s := y.source(ytVideosURL(c))
		if s == nil || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		add(YouTubeSource{Channel: c, URL: s.URL, ID: s.ID, Name: s.Name, Videos: len(s.Videos),
			Playable: y.fits(s), Listed: s.Listed, Listing: y.listDue(s, now)})
	}
	return st
}

// listings holds when each listing under way was first seen, by folder
// and source, so it survives the channel being made again after an edit.
var listings sync.Map

func listingSince(key string, on bool, now time.Time) time.Time {
	if !on {
		listings.Delete(key)
		return time.Time{}
	}
	t, _ := listings.LoadOrStore(key, now)
	return t.(time.Time)
}
