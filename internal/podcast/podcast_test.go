package podcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"airwaves/internal/youtube"
)

// TestMain lets the test binary stand in for yt-dlp: run with
// AIRWAVES_FAKE_YTDLP set, it answers like yt-dlp would.
func TestMain(m *testing.M) {
	if os.Getenv("AIRWAVES_FAKE_YTDLP") != "" {
		os.Exit(fakeYtDlp(os.Args[1:]))
	}
	lookupGap = 0
	os.Exit(m.Run())
}

// fakeYtDlp answers yt-dlp's command line, noting each run's arguments in
// the file AIRWAVES_FAKE_YTDLP names. Show A (@showa) has three episodes,
// a clip and a premiere to come in its videos, and a past and a current
// stream in its streams; Show B is a playlist with a private video. Any
// video's details give it an hour, but "feedclip" lasts 200 seconds; with
// a format asked for, an H.264 picture and AAC sound six hours long.
func fakeYtDlp(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("2026.08.19")
		return 0
	}
	if f, err := os.OpenFile(os.Getenv("AIRWAVES_FAKE_YTDLP"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	u := args[len(args)-1]
	now := time.Now().Unix()
	day := int64(86400)
	var out any
	switch {
	case u == "https://www.youtube.com/@showa/videos":
		out = map[string]any{"channel": "Show A", "channel_id": "UCa", "uploader_id": "@showa",
			"thumbnails": []map[string]any{{"url": "https://yt3.example/a=s900-c-k", "width": 900, "height": 900}},
			"entries": []map[string]any{
				{"id": "ep3", "title": "Episode 3", "duration": 3600, "timestamp": now - day},
				{"id": "clip1", "title": "A clip", "duration": 300, "timestamp": now - day},
				{"id": "ep2", "title": "Episode 2", "duration": 3000, "timestamp": now - 2*day},
				{"id": "up1", "title": "Premiere", "live_status": "is_upcoming"},
				{"id": "ep1", "title": "Episode 1", "duration": 4000, "timestamp": now - 3*day},
			}}
	case u == "https://www.youtube.com/@showa/streams":
		out = map[string]any{"channel": "Show A", "channel_id": "UCa", "entries": []map[string]any{
			{"id": "now1", "title": "Live now", "live_status": "is_live"},
			{"id": "live1", "title": "Last night's show", "duration": 7200, "timestamp": now - day, "live_status": "was_live"},
		}}
	case strings.Contains(u, "playlist?list=PLtest00000001"):
		out = map[string]any{"_type": "playlist", "id": "PLtest00000001", "title": "Show B", "channel": "B", "channel_id": "UCb",
			"entries": []map[string]any{
				{"id": "pb1", "title": "B 1", "duration": 3600, "timestamp": now - 5*day},
				{"id": "pbx", "title": "[Private video]"},
				{"id": "pb2", "title": "B 2", "duration": 3600, "timestamp": now - day},
			}}
	case strings.Contains(u, "playlist?list=PLtest00000002"):
		out = map[string]any{"_type": "playlist", "id": "PLtest00000002", "title": "Every Episode", "channel": "TheShowC", "channel_id": "UCc",
			"entries": []map[string]any{
				{"id": "pc0"},
				{"id": "pc1", "title": "C 1", "duration": 3600, "timestamp": now - day},
			}}
	case strings.Contains(u, "results?search_query="):
		out = map[string]any{"entries": []map[string]any{{"channel_id": "UCa", "channel": "Show A", "uploader_id": "@showa",
			"channel_follower_count": 1000, "description": "A show."}}}
	case strings.Contains(u, "watch?v="):
		id := u[strings.Index(u, "v=")+2:]
		info := map[string]any{"id": id, "title": "Looked up " + id, "duration": 3600, "timestamp": now - 3600,
			"description": "About " + id + "\n\nhttps://example.com/sponsor", "live_status": "not_live", "media_type": "video"}
		if id == "feedclip" {
			info["duration"] = 200
		}
		if id == "ep2" {
			info["automatic_captions"] = map[string]any{"en": []map[string]any{{"ext": "vtt", "url": "https://example.com/ep2.en.vtt"}}}
		}
		if slices.Contains(args, "-f") {
			exp := now + 6*3600
			info["requested_formats"] = []map[string]any{
				{"url": fmt.Sprintf("https://example.com/%s.mp4?expire=%d", id, exp), "vcodec": "avc1.640028", "acodec": "none", "height": 1080},
				{"url": fmt.Sprintf("https://example.com/%s.m4a?expire=%d", id, exp), "vcodec": "none", "acodec": "mp4a.40.2"},
			}
		}
		out = info
	default:
		fmt.Fprintln(os.Stderr, "ERROR: fake yt-dlp doesn't know "+u)
		return 1
	}
	json.NewEncoder(os.Stdout).Encode(out)
	return 0
}

// youtubeStub stands in for YouTube's feeds and pictures: Show A's feed has a new
// upload, a Short and a clip, and an episode it has; only ep3 has a
// maxresdefault picture.
type youtubeStub struct{}

func (youtubeStub) RoundTrip(r *http.Request) (*http.Response, error) {
	body, code := "", http.StatusNotFound
	switch u := r.URL.String(); {
	case u == youtube.FeedURL("UCa"):
		at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		body, code = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/" xmlns="http://www.w3.org/2005/Atom">
<entry><yt:videoId>feednew</yt:videoId><title>New one</title><link href="https://www.youtube.com/watch?v=feednew"/><published>`+at+`</published><media:group><media:description>Fresh</media:description></media:group></entry>
<entry><yt:videoId>feedshort</yt:videoId><title>A Short</title><link href="https://www.youtube.com/shorts/feedshort"/><published>`+at+`</published></entry>
<entry><yt:videoId>feedclip</yt:videoId><title>A clip</title><link href="https://www.youtube.com/watch?v=feedclip"/><published>`+at+`</published></entry>
<entry><yt:videoId>ep3</yt:videoId><title>Episode 3</title><link href="https://www.youtube.com/watch?v=ep3"/><published>`+at+`</published><media:group><media:description>From the feed</media:description></media:group></entry>
</feed>`, http.StatusOK
	case u == "https://i.ytimg.com/vi/ep3/maxresdefault.jpg":
		code = http.StatusOK
	case u == "https://example.com/ep2.en.vtt":
		body, code = "WEBVTT\n\n00:09:58.000 --> 00:10:03.000\nbefore and after\n\n00:10:30.000 --> 00:10:32.000\nlater on\n", http.StatusOK
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// library is a library in a test's folder, running the fake yt-dlp, whose
// runs are logged to the file it returns.
func library(t *testing.T) (*Library, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "yt-dlp.log")
	t.Setenv("AIRWAVES_FAKE_YTDLP", calls)
	return &Library{Path: filepath.Join(dir, "podcasts.json"),
		YtDlp: &youtube.YtDlp{Path: os.Args[0], HTTP: &http.Client{Transport: youtubeStub{}}}}, calls
}

func ids(eps []Episode) []string {
	out := []string{}
	for _, e := range eps {
		out = append(out, e.ID)
	}
	return out
}

func TestAddChannel(t *testing.T) {
	l, calls := library(t)
	s, err := l.Add(context.Background(), "https://www.youtube.com/@showa", 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Show A" || s.URL != "https://www.youtube.com/@showa/videos" || s.MinMinutes != DefaultMinMinutes ||
		s.Thumbnail != "https://yt3.example/a=s176-c-k" {
		t.Errorf("show: %+v", s)
	}
	// The clip is too short, the premiere not yet on.
	if got := ids(s.Episodes); !slices.Equal(got, []string{"ep3", "ep2", "ep1"}) {
		t.Errorf("episodes: %v", got)
	}
	if e := s.Episodes[1]; e.ShowID != s.ID || e.Duration != 3000 || e.Thumbnail != youtube.Thumbnail("ep2") || e.Published.IsZero() {
		t.Errorf("episode: %+v", e)
	}
	raw, _ := os.ReadFile(calls)
	if !strings.Contains(string(raw), "youtubetab:approximate_date -I 1:40 https://www.youtube.com/@showa/videos") {
		t.Errorf("listing: %s", raw)
	}
	// Again, by handle: the same show.
	again, err := l.Add(context.Background(), "@showa", 20)
	if err != nil || again.ID != s.ID || len(l.State().Shows) != 1 {
		t.Errorf("again: %+v %v", again, err)
	}
}

func TestAddStreamsAndPlaylist(t *testing.T) {
	l, _ := library(t)
	s, err := l.Add(context.Background(), "youtube.com/@showa/streams/", 30)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(s.Episodes); s.URL != "https://www.youtube.com/@showa/streams" || !slices.Equal(got, []string{"live1"}) {
		t.Errorf("streams: %s %v", s.URL, got)
	}
	p, err := l.Add(context.Background(), "https://www.youtube.com/playlist?list=PLtest00000001", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first, without the private video.
	if got := ids(p.Episodes); p.Title != "Show B" || !slices.Equal(got, []string{"pb2", "pb1"}) || p.Thumbnail != youtube.Thumbnail("pb1") {
		t.Errorf("playlist: %+v %v", p, got)
	}
	if st := l.State(); len(st.Shows) != 2 || st.Shows[0].ID != s.ID {
		t.Errorf("state: %+v", st)
	}
	// A playlist titled for what it holds is named for its channel; a video
	// without a title yet isn't an episode.
	c, err := l.Add(context.Background(), "https://www.youtube.com/playlist?list=PLtest00000002", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(c.Episodes); c.Title != "The Show C" || !slices.Equal(got, []string{"pc1"}) {
		t.Errorf("generic playlist: %q %v", c.Title, got)
	}
}

func TestBadShows(t *testing.T) {
	l, _ := library(t)
	for _, raw := range []string{"", "https://www.youtube.com/watch?v=abc", "https://youtu.be/abc", "https://www.youtube.com/@showa/podcasts", "https://example.com/feed"} {
		if _, err := l.Add(context.Background(), raw, 0); !errors.Is(err, ErrBadShow) {
			t.Errorf("%q: %v", raw, err)
		}
	}
}

// An update reads the channel's feed: its new upload joins, its Short and
// clip don't, and the episode it has gets the feed's date. New videos are
// looked up, and the newest unwatched episode found ahead.
func TestUpdate(t *testing.T) {
	l, calls := library(t)
	s, err := l.Add(context.Background(), "@showa", 0)
	if err != nil {
		t.Fatal(err)
	}
	l.update(context.Background())
	got := l.State().Shows[0]
	if ids := ids(got.Episodes); !slices.Equal(ids, []string{"feednew", "ep3", "ep2", "ep1"}) {
		t.Fatalf("episodes: %v", ids)
	}
	fresh, ep3 := got.Episodes[0], got.Episodes[1]
	if fresh.Title != "Looked up feednew" || fresh.Duration != 3600 || fresh.Description != "About feednew" || fresh.Thumbnail != youtube.Thumbnail("feednew") {
		t.Errorf("new upload: %+v", fresh)
	}
	if ep3.Thumbnail != "https://i.ytimg.com/vi/ep3/maxresdefault.jpg" {
		t.Errorf("ep3: %+v", ep3)
	}
	raw, _ := os.ReadFile(calls)
	if strings.Contains(string(raw), "watch?v=feedshort") {
		t.Errorf("the Short was looked up: %s", raw)
	}
	if !strings.Contains(string(raw), "-f "+youtube.FormatSpec(maxHeight)+" https://www.youtube.com/watch?v=feednew") {
		t.Errorf("not found ahead: %s", raw)
	}
	// The clip was looked up and left out for good.
	l.mu.Lock()
	skipped := slices.Clone(l.find(s.ID).Skipped)
	l.mu.Unlock()
	if !slices.Contains(skipped, "feedclip") || !slices.Contains(skipped, "feedshort") || !slices.Contains(skipped, "clip1") {
		t.Errorf("skipped: %v", skipped)
	}
}

func TestProgressPersists(t *testing.T) {
	l, _ := library(t)
	if _, err := l.Add(context.Background(), "@showa", 0); err != nil {
		t.Fatal(err)
	}
	if err := l.Progress("ep2", 120, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := l.Progress("nope", 1, 1, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown episode: %v", err)
	}
	again := &Library{Path: l.Path, YtDlp: l.YtDlp}
	e := again.State().Shows[0].Episodes[1]
	if e.ID != "ep2" || e.Position != 120 || e.Watched {
		t.Errorf("after reopening: %+v", e)
	}
	// Within a minute of the end is watched.
	if err := again.Progress("ep2", 2950, 3000, false); err != nil {
		t.Fatal(err)
	}
	if e := again.State().Shows[0].Episodes[1]; !e.Watched {
		t.Errorf("near the end: %+v", e)
	}
	if err := again.Progress("ep2", 0, 0, false); err != nil || again.State().Shows[0].Episodes[1].Watched {
		t.Errorf("marked unwatched: %v", err)
	}
}

// Playing an episode streams its H.264 picture and AAC sound from YouTube
// from where it's asked to, copied.
func TestInput(t *testing.T) {
	l, _ := library(t)
	if _, err := l.Add(context.Background(), "@showa", 0); err != nil {
		t.Fatal(err)
	}
	in, err := l.Input(context.Background(), "ep2", 600)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(in.Args, " ")
	if !strings.Contains(args, "-ss 600.000 -i https://example.com/ep2.mp4") || !strings.Contains(args, "-ss 600.000 -i https://example.com/ep2.m4a") {
		t.Errorf("args: %s", args)
	}
	if !in.VOD || !in.Copy || in.Offset != 600 || in.Video != "0:v:0" || len(in.Audio) != 1 || in.Audio[0].Map != "1:a:0" {
		t.Errorf("input: %+v", in)
	}
	// Its captions, timed from where it starts: the one showing then is
	// clipped there.
	if in.Subtitles == nil {
		t.Fatal("no captions")
	}
	cues := in.Subtitles.Between(0, time.Hour)
	if len(cues) != 2 || cues[0].Start != 0 || cues[0].End != 3*time.Second || cues[1].Start != 30*time.Second || cues[1].Text != "later on" {
		t.Errorf("captions: %+v", cues)
	}
	if in, err := l.Input(context.Background(), "ep2", 3); err != nil || in.Offset != 0 || strings.Contains(strings.Join(in.Args, " "), "-ss") {
		t.Errorf("from the start: %+v %v", in, err)
	}
	if _, err := l.Input(context.Background(), "nope", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown episode: %v", err)
	}
}

func TestSearchAndRemove(t *testing.T) {
	l, _ := library(t)
	found, err := l.Search(context.Background(), "show a")
	if err != nil || len(found) != 1 || found[0].ID != "UCa" || found[0].URL != "https://www.youtube.com/@showa" || found[0].Subscribers != 1000 {
		t.Errorf("search: %+v %v", found, err)
	}
	if _, err := l.Search(context.Background(), " "); !errors.Is(err, ErrBadShow) {
		t.Errorf("empty search: %v", err)
	}
	s, err := l.Add(context.Background(), found[0].URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Remove(s.ID); err != nil || len(l.State().Shows) != 0 {
		t.Errorf("remove: %v", err)
	}
	if err := l.Remove(s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove again: %v", err)
	}
}

func TestSpaced(t *testing.T) {
	for in, want := range map[string]string{"TheStandupPod": "The Standup Pod", "Sad Boyz": "Sad Boyz", "mkbhd": "mkbhd", "nerd-snipe": "nerd-snipe", "SMN": "SMN"} {
		if got := spaced(in); got != want {
			t.Errorf("spaced(%q) = %q, want %q", in, got, want)
		}
	}
}
