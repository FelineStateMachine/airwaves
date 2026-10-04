package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"airwaves/internal/jellyfin/jellyfintest"
	"airwaves/internal/vchan"
)

// TestMain lets the test binary stand in for yt-dlp: run with
// AIRWAVES_FAKE_YTDLP set, it answers like yt-dlp would, from testdata.
func TestMain(m *testing.M) {
	if os.Getenv("AIRWAVES_FAKE_YTDLP") != "" {
		os.Exit(fakeYtDlp(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeYtDlp answers yt-dlp's command line, noting each address asked for
// in the file AIRWAVES_FAKE_YTDLP names. Channel listings hold 30
// one-minute videos, too short for the default settings, so nothing gets
// scheduled and looked up.
func fakeYtDlp(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("2026.08.19")
		return 0
	}
	u := args[len(args)-1]
	if f, err := os.OpenFile(os.Getenv("AIRWAVES_FAKE_YTDLP"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintln(f, u)
		f.Close()
	}
	fail := func(msg string) int {
		fmt.Fprintln(os.Stderr, "ERROR: "+msg)
		return 1
	}
	file := func(name string) int {
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			return fail(err.Error())
		}
		os.Stdout.Write(raw)
		return 0
	}
	end := ""
	if i := slices.Index(args, "--playlist-end"); i >= 0 && i+1 < len(args) {
		end = args[i+1]
	}
	switch {
	case strings.Contains(u, "search_query="):
		p, _ := url.Parse(u)
		if p.Query().Get("search_query") == "broken" {
			return fail("[youtube:search_url] broken: Sign in to confirm you're not a bot. Use --cookies-from-browser")
		}
		return file("ytdlp-search.json")
	case strings.Contains(u, "list=UU"):
		fmt.Printf(`{"id": "UU%s", "playlist_count": 20000, "entries": []}`, strings.TrimPrefix(u, "https://www.youtube.com/playlist?list=UU"))
		return 0
	case strings.Contains(u, "list=PLnosuch"):
		return fail("[youtube:tab] PLnosuch000000: The playlist does not exist.")
	case strings.Contains(u, "list=PL"):
		return fakePlaylist(u[strings.Index(u, "list=")+len("list="):], end)
	case strings.Contains(u, "@nosuch"):
		return fail("[youtube:tab] @nosuch/videos: Unable to download API page: HTTP Error 404: Not Found (caused by <HTTPError 404: Not Found>)")
	case strings.Contains(u, "@Northernlion/videos") && end == "1":
		return file("ytdlp-channel.json")
	case strings.HasSuffix(u, "/videos"):
		page := strings.TrimSuffix(u, "/videos")
		name := strings.TrimPrefix(page[strings.LastIndex(page, "/")+1:], "@")
		var entries []map[string]any
		for i := range 30 {
			entries = append(entries, map[string]any{"id": fmt.Sprintf("%s-%02d", name, i), "title": fmt.Sprintf("%s video %d", name, i), "duration": 60})
		}
		raw, _ := json.Marshal(map[string]any{"channel": name, "channel_id": fmt.Sprintf("UC%022d", crc32.ChecksumIEEE([]byte(name))), "entries": entries})
		os.Stdout.Write(raw)
		return 0
	}
	return fail("fake yt-dlp doesn't know " + u)
}

// fakePlaylist lists a playlist: twelve hour-long episodes by Showrunner,
// with a private video among them, up to end when given.
func fakePlaylist(id, end string) int {
	entries := []map[string]any{}
	for i := range 12 {
		entries = append(entries, map[string]any{"id": fmt.Sprintf("episode%04d", i+1), "title": fmt.Sprintf("Episode %d", i+1),
			"duration": 3600, "channel": "Showrunner"})
		if i == 3 {
			entries = append(entries, map[string]any{"id": "private0001", "title": "[Private video]"})
		}
	}
	if n, err := strconv.Atoi(end); err == nil {
		entries = entries[:n]
	}
	raw, _ := json.Marshal(map[string]any{"_type": "playlist", "id": id, "title": "The Show", "channel": "Showrunner",
		"channel_id": "UCshowsowner000000000001", "playlist_count": 13, "entries": entries})
	os.Stdout.Write(raw)
	return 0
}

// noNetwork fails every request: the tests' YouTube channels read no
// feeds.
type noNetwork struct{}

func (noNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in tests")
}

func library() []jellyfintest.Item {
	return []jellyfintest.Item{
		{ID: "lib-shows", Type: "CollectionFolder", Name: "Shows"},
		{ID: "fut", Type: "Series", Name: "Futurama", Parent: "lib-shows", Year: 1999, Image: true, Genres: []string{"Animation"}},
		{ID: "fut-1-1", Type: "Episode", Name: "Space Pilot 3000", Parent: "fut", Season: 1, Episode: 1, Runtime: 22 * time.Minute},
		{ID: "fut-1-2", Type: "Episode", Name: "The Series Has Landed", Parent: "fut", Season: 1, Episode: 2, Runtime: 22 * time.Minute},
		{ID: "feud", Type: "Series", Name: "Family Feud", Parent: "lib-shows", Year: 1976, Genres: []string{"Game Show"}},
		{ID: "feud-1-1", Type: "Episode", Name: "Episode 1", Parent: "feud", Season: 1, Episode: 1, Runtime: 22 * time.Minute},
		{ID: "giant", Type: "Movie", Name: "The Iron Giant", Year: 1999, Runtime: 86 * time.Minute, Image: true},
	}
}

type harness struct {
	t     *testing.T
	root  string
	jf    *jellyfintest.Server
	url   string
	lib   *vchan.Library
	admin *Server
	ytlog string // what the fake yt-dlp was asked for
}

// newHarness starts the admin page on a channels folder holding a folder
// channel, 1.2 Cat Calming; opts set the server up further first.
func newHarness(t *testing.T, opts ...func(*Server)) *harness {
	jf := jellyfintest.New(t, "viewer", "s3cret-pw", library()...)
	jf.QuickConnect = true
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "1.2 Cat Calming"), 0o755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ytlog := filepath.Join(t.TempDir(), "yt-dlp.log")
	t.Setenv("AIRWAVES_FAKE_YTDLP", ytlog)
	lib := &vchan.Library{Root: root, FFmpeg: "ffmpeg", FFprobe: "ffprobe", Reserved: func() []string { return []string{"1.1"} },
		YtDlp: &vchan.YtDlp{Path: exe, HTTP: &http.Client{Transport: noNetwork{}}}}
	a := &Server{Library: lib, HTTP: jf.Client()}
	for _, o := range opts {
		o(a)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	t.Cleanup(a.stopBackups)
	return &harness{t: t, root: root, jf: jf, url: srv.URL, lib: lib, admin: a, ytlog: ytlog}
}

// ytRuns counts the fake yt-dlp's runs.
func (h *harness) ytRuns() int {
	raw, _ := os.ReadFile(h.ytlog)
	return strings.Count(string(raw), "\n")
}

// settle waits for a YouTube channel's refresh to end, so its files stay
// put.
func (h *harness) settle(number string) {
	h.t.Helper()
	for range 400 {
		busy := false
		for _, e := range h.lib.Entries() {
			if y, ok := e.Channel.(*vchan.YouTube); ok && y.Number() == number {
				busy = y.Status().Busy
			}
		}
		if !busy {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("channel %s is still refreshing", number)
}

// channel returns a channel from the state.
func (h *harness) channel(number string) map[string]any {
	h.t.Helper()
	_, state, raw := h.do("GET", "/admin/api/state", nil)
	for _, c := range state["channels"].([]any) {
		if c := c.(map[string]any); c["number"] == number {
			return c
		}
	}
	h.t.Fatalf("no channel %s in %s", number, raw)
	return nil
}

func (h *harness) do(method, path string, body any) (int, map[string]any, string) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.url+path, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

func TestAdminChannelsAndAccount(t *testing.T) {
	h := newHarness(t)

	_, state, _ := h.do("GET", "/admin/api/state", nil)
	if acct := state["account"].(map[string]any); acct["ok"] != false || acct["error"] == "" {
		t.Errorf("no account yet: %v", acct)
	}
	if chans := state["channels"].([]any); len(chans) != 1 || chans[0].(map[string]any)["kind"] != "folder" {
		t.Errorf("channels: %v", chans)
	}

	code, acct, raw := h.do("PUT", "/admin/api/account", map[string]string{"server": h.jf.URL + "/", "user": "viewer", "password": "s3cret-pw"})
	if code != 200 || acct["ok"] != true || acct["hasPassword"] != true {
		t.Fatalf("account: %d %s", code, raw)
	}
	if strings.Contains(raw, "s3cret-pw") {
		t.Error("the password came back")
	}
	if info, err := os.Stat(filepath.Join(h.root, accountFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("account file: %v %v", info.Mode(), err)
	}

	_, lib, _ := h.do("GET", "/admin/api/library/series", nil)
	items := lib["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("series: %v", items)
	}
	fut := items[1].(map[string]any)
	if fut["key"] != "Futurama (1999)" || fut["image"] != "/admin/api/image/fut" {
		t.Errorf("futurama: %v", fut)
	}

	code, ch, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{
		"number": "1.4", "name": " Cartoons ", "series": []string{"Futurama (1999)"}, "movies": []string{"The Iron Giant (1999)"}, "order": "aired",
	})
	if code != 200 || ch["kind"] != "jellyfin" || ch["folder"] != "1.4 Cartoons" {
		t.Fatalf("create: %d %s", code, raw)
	}
	cfg, err := os.ReadFile(filepath.Join(h.root, "1.4 Cartoons", channelFile))
	if err != nil || !strings.Contains(string(cfg), `"Futurama (1999)"`) || !strings.Contains(string(cfg), `"aired"`) || strings.Contains(string(cfg), "collections") {
		t.Errorf("config: %s %v", cfg, err)
	}

	if code, _, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "1.2", "name": "Clash"}); code != http.StatusConflict {
		t.Errorf("taken number: %d %s", code, raw)
	}
	if code, _, raw := h.do("PUT", "/admin/api/channels/1.2", map[string]any{"number": "1.2", "name": "Cat Calming"}); code != http.StatusBadRequest {
		t.Errorf("editing a folder channel: %d %s", code, raw)
	}
	if code, _, _ := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "one", "name": "x"}); code != http.StatusBadRequest {
		t.Errorf("bad number: %d", code)
	}

	code, ch, raw = h.do("PUT", "/admin/api/channels/1.4", map[string]any{"number": "1.5", "name": "Toons", "series": []string{"Futurama (1999)"}})
	if code != 200 || ch["folder"] != "1.5 Toons" {
		t.Fatalf("rename: %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.4 Cartoons")); !os.IsNotExist(err) {
		t.Error("old folder is still there")
	}

	_, sched, raw := h.do("GET", "/admin/api/channels/1.5/schedule", nil)
	if progs, _ := sched["programs"].([]any); len(progs) == 0 || progs[0].(map[string]any)["title"] != "Futurama" {
		t.Errorf("schedule: %s", raw)
	}

	if code, _, _ := h.do("DELETE", "/admin/api/channels/1.5", nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.5 Toons")); !os.IsNotExist(err) {
		t.Error("deleted channel's folder is still there")
	}
	if code, _, _ := h.do("DELETE", "/admin/api/channels/1.2", nil); code != http.StatusBadRequest {
		t.Errorf("deleting a folder channel: %d", code)
	}
}

func TestAdminQuickConnect(t *testing.T) {
	h := newHarness(t)
	code, start, raw := h.do("POST", "/admin/api/account/quickconnect", map[string]string{"server": h.jf.URL})
	if code != 200 || start["code"] == "" || strings.Contains(raw, "secret") {
		t.Fatalf("start: %d %s", code, raw)
	}
	id := start["id"].(string)
	if _, st, _ := h.do("GET", "/admin/api/account/quickconnect/"+id, nil); st["state"] != "waiting" {
		t.Errorf("before approval: %v", st)
	}
	if !h.jf.Approve(start["code"].(string)) {
		t.Fatal("fake server doesn't know the code")
	}
	_, st, raw := h.do("GET", "/admin/api/account/quickconnect/"+id, nil)
	if st["state"] != "done" || st["account"].(map[string]any)["ok"] != true {
		t.Fatalf("after approval: %s", raw)
	}
	acct, err := os.ReadFile(filepath.Join(h.root, accountFile))
	if err != nil || !strings.Contains(string(acct), `"token"`) || strings.Contains(string(acct), "password") {
		t.Errorf("account file: %s %v", acct, err)
	}
	if _, st, _ := h.do("GET", "/admin/api/account/quickconnect/"+id, nil); st["state"] != "expired" {
		t.Errorf("a used code should be gone: %v", st)
	}

	// The token signs in for browsing too.
	if _, lib, raw := h.do("GET", "/admin/api/library/movies", nil); len(lib["items"].([]any)) != 1 {
		t.Errorf("movies with the token: %s", raw)
	}

	jf2 := jellyfintest.New(t, "x", "y")
	if code, _, _ := h.do("POST", "/admin/api/account/quickconnect", map[string]string{"server": jf2.URL}); code != http.StatusConflict {
		t.Errorf("quick connect off: %d", code)
	}
}

// seedYouTube makes the live 1.8 Northernlion channel: its youtube.json and
// a catalog already listed, so nothing is due from yt-dlp. Each source's
// video j was uploaded j days ago.
func seedYouTube(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"channels": ["https://www.youtube.com/@Northernlion", "https://www.youtube.com/@TheLibraryofLetourneau"], "repeatDays": 30, "maxHeight": 720, "lockHours": 3}`
	now := time.Now().UTC()
	type video struct {
		ID        string    `json:"id"`
		Title     string    `json:"title"`
		Seconds   int       `json:"seconds"`
		Looked    time.Time `json:"looked"`
		Published time.Time `json:"published"`
	}
	type source struct {
		URL     string    `json:"url"`
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Listed  time.Time `json:"listed"`
		Checked time.Time `json:"checked"`
		Dated   bool      `json:"dated"`
		Videos  []video   `json:"videos"`
	}
	var cat struct {
		Sources []source `json:"sources"`
	}
	for i, name := range []string{"Northernlion", "TheLibraryofLetourneau"} {
		s := source{URL: "https://www.youtube.com/@" + name + "/videos", ID: fmt.Sprintf("UC%022d", i), Name: name, Listed: now, Checked: now, Dated: true}
		for j := range 40 {
			// Every fourth is a Short, too short to air.
			s.Videos = append(s.Videos, video{fmt.Sprintf("%d-%02d", i, j), fmt.Sprintf("%s video %d", name, j), 60 + 540*min(j%4, 1), now,
				now.AddDate(0, 0, -j)})
		}
		cat.Sources = append(cat.Sources, s)
	}
	raw, _ := json.Marshal(cat)
	for name, data := range map[string][]byte{"youtube.json": []byte(cfg), ".catalog.json": raw} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdminYouTubeChannels(t *testing.T) {
	h := newHarness(t)
	seedYouTube(t, filepath.Join(h.root, "1.8 Northernlion"))

	nl := h.channel("1.8")
	cfg, _ := nl["youtube"].(map[string]any)
	if nl["kind"] != "youtube" || cfg == nil || cfg["minMinutes"] != 3.0 || cfg["maxMinutes"] != 240.0 ||
		cfg["repeatDays"] != 30.0 || cfg["maxHeight"] != 720.0 || cfg["rerunMix"] != "balanced" || cfg["maxAgeDays"] != 0.0 ||
		cfg["deadAir"] != false || len(cfg["channels"].([]any)) != 2 {
		t.Fatalf("1.8: %v", nl)
	}
	srcs := nl["sources"].([]any)
	first := srcs[0].(map[string]any)
	if len(srcs) != 2 || first["name"] != "Northernlion" || first["handle"] != "@Northernlion" || first["url"] != "https://www.youtube.com/@Northernlion" ||
		first["videos"] != 40.0 || first["playable"] != 30.0 || first["listing"] != false || first["listed"] == nil || first["failed"] != nil {
		t.Errorf("sources: %v", srcs)
	}
	if nl["videos"] != 80.0 || nl["items"] != 60.0 || nl["listing"] != nil || nl["now"] == nil {
		t.Errorf("1.8 totals: %v", nl)
	}
	_, sched, raw := h.do("GET", "/admin/api/channels/1.8/schedule", nil)
	if progs, _ := sched["programs"].([]any); len(progs) == 0 || !strings.Contains(progs[0].(map[string]any)["title"].(string), " video ") {
		t.Errorf("schedule: %s", raw)
	}

	// A new channel from two YouTube channels, one given twice.
	code, ch, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{
		"kind": "youtube", "number": "1.9", "name": " DJ  Sets ",
		"youtube": map[string]any{
			"channels":   []string{"@boilerroom", "https://www.youtube.com/channel/UCJEKlziKdxoos1qbptjGgLg/videos?view=0", "youtube.com/@boilerroom/"},
			"minMinutes": 20, "maxMinutes": 240, "repeatDays": 14, "maxHeight": 1080, "rerunMix": "recent", "maxAgeDays": 90,
			"deadAir": true,
		},
	})
	if code != 200 || ch["kind"] != "youtube" || ch["folder"] != "1.9 DJ Sets" || len(ch["sources"].([]any)) != 2 {
		t.Fatalf("create: %d %s", code, raw)
	}
	var file map[string]any
	data, _ := os.ReadFile(filepath.Join(h.root, "1.9 DJ Sets", "youtube.json"))
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if want := []any{"https://www.youtube.com/@boilerroom", "https://www.youtube.com/channel/UCJEKlziKdxoos1qbptjGgLg"}; !slices.Equal(file["channels"].([]any), want) ||
		file["minMinutes"] != 20.0 || file["maxMinutes"] != 240.0 || file["repeatDays"] != 14.0 || file["maxHeight"] != 1080.0 || file["rerunMix"] != "recent" ||
		file["maxAgeDays"] != 90.0 || file["deadAir"] != true {
		t.Errorf("youtube.json: %s", data)
	}
	h.settle("1.9")
	dj := h.channel("1.9")
	for _, s := range dj["sources"].([]any) {
		if s := s.(map[string]any); s["videos"] != 30.0 || s["playable"] != 0.0 || s["listing"] != false || s["id"] == "" {
			t.Errorf("listed source: %v", s)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.9 DJ Sets", ".catalog.json")); err != nil {
		t.Errorf("catalog not saved: %v", err)
	}

	// Rename 1.8, change its sources and quality; the rest stays.
	code, ch, raw = h.do("PUT", "/admin/api/channels/1.8", map[string]any{
		"kind": "youtube", "number": "1.8", "name": "NL", "youtube": map[string]any{"channels": []string{"@Northernlion"}, "maxHeight": 480},
	})
	if code != 200 || ch["folder"] != "1.8 NL" || len(ch["sources"].([]any)) != 1 || ch["sources"].([]any)[0].(map[string]any)["videos"] != 40.0 {
		t.Fatalf("rename: %d %s", code, raw)
	}
	data, _ = os.ReadFile(filepath.Join(h.root, "1.8 NL", "youtube.json"))
	if !strings.Contains(string(data), `"lockHours": 3`) || !strings.Contains(string(data), `"maxHeight": 480`) ||
		!strings.Contains(string(data), `"repeatDays": 30`) || !strings.Contains(string(data), `"rerunMix": "balanced"`) {
		t.Errorf("edited youtube.json: %s", data)
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.8 Northernlion")); !os.IsNotExist(err) {
		t.Error("old folder is still there")
	}

	// A kind stays a kind.
	if code, _, raw := h.do("PUT", "/admin/api/channels/1.8", map[string]any{"number": "1.8", "name": "NL"}); code != http.StatusBadRequest ||
		!strings.Contains(raw, "can't become a Jellyfin one") {
		t.Errorf("YouTube to Jellyfin: %d %s", code, raw)
	}
	if code, _, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "1.4", "name": "Cartoons"}); code != 200 {
		t.Fatalf("jellyfin channel: %d %s", code, raw)
	}
	if code, _, raw := h.do("PUT", "/admin/api/channels/1.4", map[string]any{"kind": "youtube", "number": "1.4", "name": "Cartoons",
		"youtube": map[string]any{"channels": []string{"@boilerroom"}}}); code != http.StatusBadRequest || !strings.Contains(raw, "can't become a YouTube one") {
		t.Errorf("Jellyfin to YouTube: %d %s", code, raw)
	}
	if code, _, _ := h.do("PUT", "/admin/api/channels/1.2", map[string]any{"kind": "youtube", "number": "1.2", "name": "Cat Calming",
		"youtube": map[string]any{"channels": []string{"@boilerroom"}}}); code != http.StatusBadRequest {
		t.Errorf("folder to YouTube: %d", code)
	}
	if code, _, _ := h.do("PUT", "/admin/api/channels/new", map[string]any{"kind": "youtube", "number": "1.9", "name": "Again",
		"youtube": map[string]any{"channels": []string{"@boilerroom"}}}); code != http.StatusConflict {
		t.Errorf("taken number: %d", code)
	}

	// Delete both, hidden files and all.
	h.do("GET", "/admin/api/channels/1.8/schedule", nil)
	os.WriteFile(filepath.Join(h.root, "1.8 NL", ".playout.json.tmp"), []byte("{}"), 0o644)
	for _, f := range []string{".catalog.json", ".playout.json"} {
		if _, err := os.Stat(filepath.Join(h.root, "1.8 NL", f)); err != nil {
			t.Errorf("before deleting: %v", err)
		}
	}
	for _, n := range []string{"1.8", "1.9"} {
		h.settle(n)
		if code, _, raw := h.do("DELETE", "/admin/api/channels/"+n, nil); code != http.StatusNoContent {
			t.Errorf("delete %s: %d %s", n, code, raw)
		}
	}
	for _, f := range []string{"1.8 NL", "1.9 DJ Sets"} {
		if left, err := os.ReadDir(filepath.Join(h.root, f)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", f, left)
		}
	}
}

// TestAdminYouTubeMaxAge: a maximum age narrows the videos that fit, as
// the sources' counts show, and with nothing new enough the channel goes
// off the air.
func TestAdminYouTubeMaxAge(t *testing.T) {
	h := newHarness(t)
	seedYouTube(t, filepath.Join(h.root, "1.8 Northernlion"))
	if first := h.channel("1.8")["sources"].([]any)[0].(map[string]any); first["playable"] != 30.0 {
		t.Fatalf("before: %v", first)
	}
	put := func(days float64) map[string]any {
		t.Helper()
		code, ch, raw := h.do("PUT", "/admin/api/channels/1.8", map[string]any{
			"kind": "youtube", "number": "1.8", "name": "Northernlion", "youtube": map[string]any{"maxAgeDays": days},
		})
		if code != 200 {
			t.Fatalf("maxAgeDays %v: %d %s", days, code, raw)
		}
		return ch
	}
	// Uploads 1 to 29 days old (the 30-day-old one is just past the limit
	// by now), less the Shorts (every fourth).
	ch := put(30)
	if cfg := ch["youtube"].(map[string]any); cfg["maxAgeDays"] != 30.0 {
		t.Errorf("settings: %v", cfg)
	}
	for _, s := range ch["sources"].([]any) {
		if s := s.(map[string]any); s["videos"] != 40.0 || s["playable"] != 22.0 {
			t.Errorf("source within 30 days: %v", s)
		}
	}
	if ch["items"] != 44.0 || ch["now"] == nil {
		t.Errorf("channel within 30 days: %v", ch)
	}
	data, _ := os.ReadFile(filepath.Join(h.root, "1.8 Northernlion", "youtube.json"))
	if !strings.Contains(string(data), `"maxAgeDays": 30`) || !strings.Contains(string(data), `"lockHours": 3`) {
		t.Errorf("youtube.json: %s", data)
	}
	// Nothing is half a day old.
	ch = put(0.5)
	if ch["items"] != 0.0 || ch["now"] != nil {
		t.Errorf("nothing new enough: items %v, now %v", ch["items"], ch["now"])
	}
	if code, sched, raw := h.do("GET", "/admin/api/channels/1.8/schedule", nil); code != 200 || len(sched["programs"].([]any)) != 0 {
		t.Errorf("schedule with nothing new enough: %d %s", code, raw)
	}
}

func TestAdminYouTubeValidation(t *testing.T) {
	h := newHarness(t)
	eleven := make([]string, 11)
	for i := range eleven {
		eleven[i] = fmt.Sprintf("@channel%d", i)
	}
	for _, c := range []struct {
		yt   map[string]any
		kind string
		want string
	}{
		{nil, "youtube", "at least one"},
		{map[string]any{"channels": []string{}}, "youtube", "at least one"},
		{map[string]any{"channels": eleven}, "youtube", "up to 10"},
		{map[string]any{"channels": []string{"https://vimeo.com/boilerroom"}}, "youtube", "isn't a YouTube channel"},
		{map[string]any{"channels": []string{"Boiler Room"}}, "youtube", "isn't a YouTube channel"},
		{map[string]any{"channels": []string{"https://youtu.be/dQw4w9WgXcQ"}}, "youtube", "link to a video"},
		{map[string]any{"channels": []string{"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}}, "youtube", "link to a video"},
		{map[string]any{"channels": []string{"@boilerroom"}, "maxHeight": 900}, "youtube", "quality"},
		{map[string]any{"channels": []string{"@boilerroom"}, "repeatDays": 400}, "youtube", "repeat"},
		{map[string]any{"channels": []string{"@boilerroom"}, "minMinutes": -1}, "youtube", "shortest"},
		{map[string]any{"channels": []string{"@boilerroom"}, "minMinutes": 20, "maxMinutes": 10}, "youtube", "longest"},
		{map[string]any{"channels": []string{"@boilerroom"}, "rerunMix": "newest"}, "youtube", "reruns"},
		{map[string]any{"channels": []string{"@boilerroom"}, "maxAgeDays": -1}, "youtube", "3650 days"},
		{map[string]any{"channels": []string{"@boilerroom"}, "maxAgeDays": 4000}, "youtube", "3650 days"},
		{map[string]any{"channels": []string{"@boilerroom"}}, "vimeo", "kind"},
	} {
		code, _, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"kind": c.kind, "number": "1.9", "name": "DJ Sets", "youtube": c.yt})
		if code != http.StatusBadRequest || !strings.Contains(raw, c.want) {
			t.Errorf("%v: %d %s", c.yt, code, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.9 DJ Sets")); !os.IsNotExist(err) {
		t.Error("a refused channel made a folder")
	}
	for in, want := range map[string]string{
		"@Boiler.Room-1":                                   "https://www.youtube.com/@Boiler.Room-1",
		"UCGBpxWJr9FNOcFYA5GkKrMg":                         "https://www.youtube.com/channel/UCGBpxWJr9FNOcFYA5GkKrMg",
		"https://m.youtube.com/@boilerroom/videos?x=1#top": "https://www.youtube.com/@boilerroom",
		"http://youtube.com/c/BoilerRoom/featured":         "https://www.youtube.com/c/BoilerRoom",
		"www.youtube.com/user/boilerroomtv":                "https://www.youtube.com/user/boilerroomtv",
	} {
		if got, err := youtubeChannel(in); got != want || err != nil {
			t.Errorf("youtubeChannel(%q) = %q, %v", in, got, err)
		}
	}
}

func TestAdminYouTubeSearchAndLookup(t *testing.T) {
	h := newHarness(t)
	code, res, raw := h.do("GET", "/admin/api/youtube/search?q=boiler+room", nil)
	chans, _ := res["channels"].([]any)
	if code != 200 || len(chans) != 2 {
		t.Fatalf("search: %d %s", code, raw)
	}
	br := chans[0].(map[string]any)
	if br["id"] != "UCGBpxWJr9FNOcFYA5GkKrMg" || br["name"] != "Boiler Room" || br["handle"] != "@boilerroom" ||
		br["url"] != "https://www.youtube.com/@boilerroom" || br["subscribers"] != 5250000.0 ||
		!strings.HasPrefix(br["image"].(string), "https://yt3.ggpht.com/") || !strings.Contains(br["image"].(string), "=s176-") ||
		br["description"] != "Connecting club culture to the wider world, on screen and irl through parties, film and video." {
		t.Errorf("Boiler Room: %v", br)
	}
	// Search results come with descriptions cut short.
	if d := chans[1].(map[string]any)["description"].(string); !strings.HasSuffix(d, "במסחר…") {
		t.Errorf("cut short: %q", d)
	}
	if strings.Contains(raw, "- Topic") {
		t.Errorf("a Topic channel came back: %s", raw)
	}
	runs := h.ytRuns()
	if _, again, _ := h.do("GET", "/admin/api/youtube/search?q=Boiler%20%20Room", nil); len(again["channels"].([]any)) != 2 || h.ytRuns() != runs {
		t.Errorf("a repeat search ran yt-dlp again (%d runs, then %d)", runs, h.ytRuns())
	}
	if code, _, _ := h.do("GET", "/admin/api/youtube/search?q=+", nil); code != http.StatusBadRequest {
		t.Errorf("empty search: %d", code)
	}
	if code, _, raw := h.do("GET", "/admin/api/youtube/search?q=broken", nil); code != http.StatusBadGateway || !strings.Contains(raw, `"Sign in to confirm you're not a bot`) {
		t.Errorf("failed search: %d %s", code, raw)
	}

	code, nl, raw := h.do("GET", "/admin/api/youtube/channel?u=%40Northernlion", nil)
	if code != 200 || nl["id"] != "UC3tNpTOHsTnkmbwztCs30sA" || nl["name"] != "Northernlion" || nl["handle"] != "@Northernlion" ||
		nl["url"] != "https://www.youtube.com/@Northernlion" || nl["subscribers"] != 1390000.0 || nl["videos"] != 20000.0 ||
		!strings.HasSuffix(nl["image"].(string), "=s176-c-k-c0x00ffffff-no-rj") || !strings.HasPrefix(nl["description"].(string), "Subscribe for new videos") {
		t.Fatalf("lookup: %d %s", code, raw)
	}
	runs = h.ytRuns()
	if _, again, _ := h.do("GET", "/admin/api/youtube/channel?u="+url.QueryEscape("https://www.youtube.com/@Northernlion/videos"), nil); again["id"] != nl["id"] || h.ytRuns() != runs {
		t.Error("a repeat lookup ran yt-dlp again")
	}
	if code, _, raw := h.do("GET", "/admin/api/youtube/channel?u=%40nosuch", nil); code != http.StatusBadGateway || !strings.Contains(raw, "YouTube has no such channel") {
		t.Errorf("no such channel: %d %s", code, raw)
	}
	for _, u := range []string{"https://youtu.be/dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "boiler room"} {
		if code, _, _ := h.do("GET", "/admin/api/youtube/channel?u="+url.QueryEscape(u), nil); code != http.StatusBadRequest {
			t.Errorf("lookup of %q: %d", u, code)
		}
	}

	// The state shows what lookups found.
	seedYouTube(t, filepath.Join(h.root, "1.8 Northernlion"))
	h.lib.Rescan()
	if s := h.channel("1.8")["sources"].([]any)[0].(map[string]any); s["image"] != nl["image"] {
		t.Errorf("source without the looked up avatar: %v", s)
	}
}

func TestAdminYouTubeWithoutYtDlp(t *testing.T) {
	lib := &vchan.Library{Root: t.TempDir(), YtDlp: &vchan.YtDlp{Path: filepath.Join(t.TempDir(), "yt-dlp")}}
	srv := httptest.NewServer((&Server{Library: lib}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/admin/api/youtube/search?q=dj+sets")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "yt-dlp isn't installed") {
		t.Errorf("%d %s", resp.StatusCode, raw)
	}
}

// TestAdminYouTubeLive searches YouTube and looks up a channel through
// the page's API with a real yt-dlp, when AIRWAVES_YTDLP names one.
func TestAdminYouTubeLive(t *testing.T) {
	tool := os.Getenv("AIRWAVES_YTDLP")
	if tool == "" {
		t.Skip("set AIRWAVES_YTDLP to a yt-dlp to search YouTube")
	}
	lib := &vchan.Library{Root: t.TempDir(), YtDlp: &vchan.YtDlp{Path: tool, Update: os.Getenv("AIRWAVES_YTDLP_UPDATE") != ""}}
	srv := httptest.NewServer((&Server{Library: lib}).Handler())
	defer srv.Close()
	get := func(path string) map[string]any {
		for {
			start := time.Now()
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusServiceUnavailable && strings.Contains(string(raw), "being installed") {
				time.Sleep(2 * time.Second)
				continue
			}
			var m map[string]any
			json.Unmarshal(raw, &m)
			t.Logf("GET %s: %d in %s", path, resp.StatusCode, time.Since(start).Round(10*time.Millisecond))
			if resp.StatusCode != 200 {
				t.Fatalf("%s", raw)
			}
			return m
		}
	}
	show := func(c map[string]any) {
		t.Logf("  %-34s %-26v %10.0f subscribers  videos %v\n      %v\n      %.90v", c["name"], c["handle"], c["subscribers"], c["videos"], c["image"], c["description"])
	}
	for _, q := range []string{"dj sets", "boiler room"} {
		res := get("/admin/api/youtube/search?q=" + url.QueryEscape(q))
		t.Logf("%q: %d channels", q, len(res["channels"].([]any)))
		for _, c := range res["channels"].([]any) {
			show(c.(map[string]any))
		}
	}
	for _, u := range []string{"@Northernlion", "https://www.youtube.com/@TheLibraryofLetourneau/videos", "UCGBpxWJr9FNOcFYA5GkKrMg"} {
		t.Logf("lookup %s:", u)
		show(get("/admin/api/youtube/channel?u=" + url.QueryEscape(u)))
	}
}
