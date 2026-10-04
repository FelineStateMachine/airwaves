package vchan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for yt-dlp: run with
// AIRWAVES_FAKE_YTDLP set, it answers like yt-dlp would.
func TestMain(m *testing.M) {
	if os.Getenv("AIRWAVES_FAKE_YTDLP") != "" {
		os.Exit(fakeYtDlp(os.Args[1:]))
	}
	// Channels find videos ahead only in the tests of that, so the rest
	// see only the yt-dlp runs they cause.
	ytWarm = false
	os.Exit(m.Run())
}

// fakeYtDlp answers yt-dlp's command line, noting each run's arguments in
// the file AIRWAVES_FAKE_YTDLP names. A channel tab's newest uploads are a
// new 15-minute upload, "fresh000001", then the channel's v000 to v013;
// any video's details give it 15 minutes, and with a format asked for, an
// address that expires in six hours and English captions, but a
// "Premiere…" is upcoming.
// A playlist is testdata/yt_playlist.json, with AIRWAVES_FAKE_PLAYLIST's
// videos added to the end when set.
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
	var out any
	switch {
	case strings.HasSuffix(u, "/videos") && slices.Contains(args, "--playlist-end"):
		entries := []map[string]any{{"id": "fresh000001", "title": "A fresh upload (New Game)", "duration": 900, "timestamp": now - 3600}}
		for i := range 14 {
			entries = append(entries, map[string]any{"id": fmt.Sprintf("v%03d", i), "title": fmt.Sprintf("Video %d", i),
				"duration": 1200, "timestamp": now - int64(i+1)*86400})
		}
		out = map[string]any{"channel": "A", "channel_id": "UCa", "entries": entries}
	case strings.Contains(u, "watch?v="):
		id := u[strings.Index(u, "v=")+2:]
		out = map[string]any{"id": id, "title": "Looked up " + id, "duration": 900, "timestamp": now - 3600,
			"description": "Details of " + id, "live_status": "not_live", "media_type": "video"}
		if strings.HasPrefix(id, "Premiere") {
			out = map[string]any{"id": id, "title": "Looked up " + id, "live_status": "is_upcoming", "media_type": "video"}
		} else if slices.Contains(args, "-f") {
			// Where to stream it from, for six hours, and its captions.
			out.(map[string]any)["url"] = fmt.Sprintf("https://example.com/%s.mp4?expire=%d", id, now+6*3600)
			out.(map[string]any)["vcodec"], out.(map[string]any)["acodec"] = "avc1.4d401f", "mp4a.40.2"
			out.(map[string]any)["automatic_captions"] = map[string]any{"en": []map[string]any{{"ext": "json3", "url": "https://example.com/captions/" + id}}}
		}
	case strings.Contains(u, "playlist?list="):
		var p map[string]any
		raw, _ := os.ReadFile(filepath.Join("testdata", "yt_playlist.json"))
		if err := json.Unmarshal(raw, &p); err != nil {
			fmt.Fprintln(os.Stderr, "ERROR: "+err.Error())
			return 1
		}
		for id := range strings.FieldsSeq(os.Getenv("AIRWAVES_FAKE_PLAYLIST")) {
			p["entries"] = append(p["entries"].([]any), map[string]any{"id": id, "title": "Added " + id, "duration": 600, "channel": "CrashCourse"})
		}
		out = p
	default:
		fmt.Fprintln(os.Stderr, "ERROR: fake yt-dlp doesn't know "+u)
		return 1
	}
	json.NewEncoder(os.Stdout).Encode(out)
	return 0
}

// feedStub serves a channel's feed, or fails with HTTP 404 while down,
// and captions, counting them.
type feedStub struct {
	mu       sync.Mutex
	down     bool
	feed     []byte
	captions int
}

func (f *feedStub) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.String(), "https://example.com/captions/") {
		f.captions++
		caps := `{"events":[{"tStartMs":1000,"dDurationMs":2000,"segs":[{"utf8":"Hello"}]}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(caps)), Request: r}, nil
	}
	if f.down || !strings.HasPrefix(r.URL.String(), "https://www.youtube.com/feeds/videos.xml?channel_id=UCa") {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(f.feed)), Request: r}, nil
}

// TestYtFeedFallback: while a channel's feed fails, its newest uploads
// are listed with yt-dlp instead, every quarter hour like the feed, and a
// new upload found that way gets a first run. Switching to the stand-in
// and back are each logged once.
func TestYtFeedFallback(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "yt-dlp.log")
	t.Setenv("AIRWAVES_FAKE_YTDLP", calls)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	gap := ytLookupGap
	ytLookupGap = 0
	defer func() { ytLookupGap = gap }()

	clock := time.Now().UTC()
	if err := os.WriteFile(filepath.Join(dir, youtubeConfig), []byte(`{"channels": ["@a"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &ytSource{URL: ytVideosURL("@a"), ID: "UCa", Name: "A", Listed: clock, Dated: true}
	for i := range 100 {
		s.Videos = append(s.Videos, &ytVideo{ID: fmt.Sprintf("v%03d", i), Title: fmt.Sprintf("Video %d", i),
			Seconds: 1200, Published: clock.AddDate(0, 0, -i-1), Approx: true, Looked: clock})
	}
	feed := &feedStub{down: true, feed: []byte(strings.ReplaceAll(string(readTestdata(t, "yt_feed.xml")), "NewUpload01", "fromfeed001"))}
	y := &YouTube{Num: "1.8", Title: "A", Dir: dir, now: func() time.Time { return clock },
		YtDlp: &YtDlp{Path: os.Args[0], HTTP: &http.Client{Transport: feed}}}
	y.save(ytCatalogFile, ytCatalog{Sources: []*ytSource{s}})
	refresh := func() {
		y.mu.Lock()
		if !y.loaded {
			y.load()
		}
		y.replan(clock)
		y.busy = true
		y.mu.Unlock()
		y.refresh()
	}
	fallbacks := func() int {
		raw, _ := os.ReadFile(calls)
		return strings.Count(string(raw), "--playlist-end 15")
	}

	refresh()
	if n := fallbacks(); n != 1 {
		t.Fatalf("%d runs listing the newest uploads, want 1", n)
	}
	y.mu.Lock()
	fresh := y.videos["fresh000001"]
	var first ytAiring
	for _, a := range y.plan.Airings {
		if a.First {
			first = a
		}
	}
	lock := clock.Add(time.Duration(y.cfg.LockHours * float64(time.Hour)))
	y.mu.Unlock()
	if fresh == nil || fresh.Seconds != 900 || fresh.Found != clock {
		t.Fatalf("the new upload: %+v", fresh)
	}
	if first.ID != "fresh000001" || first.Start.Before(lock) || first.Start.After(lock.Add(time.Hour)) {
		t.Errorf("first run %+v, want the new upload's right after %v", first, lock)
	}

	// Within the quarter hour: nothing more. After it: the stand-in again,
	// without saying so again.
	clock = clock.Add(10 * time.Minute)
	refresh()
	if n := fallbacks(); n != 1 {
		t.Errorf("listed the newest uploads %d times within a quarter hour", n)
	}
	clock = clock.Add(6 * time.Minute)
	refresh()
	if n := fallbacks(); n != 2 {
		t.Errorf("%d runs listing the newest uploads after a quarter hour, want 2", n)
	}
	if n := strings.Count(logged.String(), "until it's back"); n != 1 {
		t.Errorf("the switch to yt-dlp was logged %d times:\n%s", n, logged.String())
	}

	// The feed is back.
	feed.mu.Lock()
	feed.down = false
	feed.mu.Unlock()
	clock = clock.Add(16 * time.Minute)
	refresh()
	if n := fallbacks(); n != 2 {
		t.Errorf("listed the newest uploads with the feed working")
	}
	y.mu.Lock()
	fromFeed := y.videos["fromfeed001"]
	y.mu.Unlock()
	if fromFeed == nil || !fromFeed.New {
		t.Errorf("the feed's new upload: %+v", fromFeed)
	}
	if n := strings.Count(logged.String(), "works again"); n != 1 {
		t.Errorf("the feed's return was logged %d times:\n%s", n, logged.String())
	}
}
