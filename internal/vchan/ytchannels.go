package vchan

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// YouTubeChannel is a YouTube channel as a search or a lookup finds it.
type YouTubeChannel struct {
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

// ytChannelsOnly narrows YouTube's search results to channels.
const ytChannelsOnly = "EgIQAg%3D%3D"

// SearchChannels asks YouTube for the channels matching query, n at most.
// YouTube's Topic channels, which it makes for artists' music, are left
// out: they have no uploads to play.
func (t *YtDlp) SearchChannels(ctx context.Context, query string, n int) ([]YouTubeChannel, error) {
	u := "https://www.youtube.com/results?search_query=" + url.QueryEscape(query) + "&sp=" + ytChannelsOnly
	raw, err := t.run(ctx, "--flat-playlist", "-J", "--playlist-end", strconv.Itoa(n), u)
	if err != nil {
		return nil, err
	}
	var p struct {
		Entries []ytChannelInfo `json:"entries"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	out := []YouTubeChannel{}
	for _, e := range p.Entries {
		c := e.channel()
		if !strings.HasPrefix(c.ID, "UC") || c.Handle == "" && strings.HasSuffix(c.Name, " - Topic") {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// LookupChannel finds the channel at page (its link, "@handle" or "UC…" ID,
// as youtube.json names channels), and roughly how many videos it has.
func (t *YtDlp) LookupChannel(ctx context.Context, page string) (YouTubeChannel, error) {
	raw, err := t.run(ctx, "--flat-playlist", "-J", "--playlist-end", "1", ytVideosURL(page))
	if err != nil {
		return YouTubeChannel{}, err
	}
	var e ytChannelInfo
	if err := json.Unmarshal(raw, &e); err != nil {
		return YouTubeChannel{}, err
	}
	c := e.channel()
	if !strings.HasPrefix(c.ID, "UC") {
		return YouTubeChannel{}, errors.New("not a YouTube channel")
	}
	// The channel's uploads playlist says how many there are; its tab
	// doesn't.
	if raw, err := t.run(ctx, "--flat-playlist", "-J", "--playlist-end", "1",
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

// ytChannelInfo is a channel in yt-dlp's output: a search result, or the
// top of a channel's tab.
type ytChannelInfo struct {
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

func (e ytChannelInfo) channel() YouTubeChannel {
	c := YouTubeChannel{ID: e.ChannelID, Name: cmp.Or(e.Channel, e.Title), Subscribers: int64(e.Followers),
		Description: ytBlurb(e.Description)}
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

// ytBlurb makes a channel's description one short paragraph. Search
// results come cut short with "...".
func ytBlurb(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if t, ok := strings.CutSuffix(s, " ..."); ok {
		s = t + "…"
	}
	if r := []rune(s); len(r) > 300 {
		s = strings.TrimSpace(string(r[:299])) + "…"
	}
	return s
}

// ErrYtDlpInstalling is Ready's answer while yt-dlp is being installed.
var ErrYtDlpInstalling = errors.New("yt-dlp is being installed; try again in a minute")

// ytdlpInstall is Ready's install of a managed yt-dlp.
type ytdlpInstall struct {
	mu      sync.Mutex
	running bool
	err     error // the last try's
}

var ytdlpInstalls sync.Map // *YtDlp: *ytdlpInstall

// Ready reports whether yt-dlp is there to run. When t keeps its own copy
// and hasn't installed it yet, Ready starts installing it and reports
// ErrYtDlpInstalling, or why the last try failed.
func (t *YtDlp) Ready() error {
	if !t.Update {
		if _, err := exec.LookPath(cmp.Or(t.Path, "yt-dlp")); err != nil {
			return fmt.Errorf("yt-dlp isn't installed on the server: %w", err)
		}
		return nil
	}
	if _, err := os.Stat(t.Path); err == nil {
		return nil
	}
	v, _ := ytdlpInstalls.LoadOrStore(t, &ytdlpInstall{})
	in := v.(*ytdlpInstall)
	in.mu.Lock()
	defer in.mu.Unlock()
	failed := in.err
	if !in.running {
		in.running = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			_, err := t.command(ctx)
			in.mu.Lock()
			in.running, in.err = false, err
			in.mu.Unlock()
		}()
	}
	if failed != nil {
		return fmt.Errorf("couldn't install yt-dlp (%v); trying again", failed)
	}
	return ErrYtDlpInstalling
}
