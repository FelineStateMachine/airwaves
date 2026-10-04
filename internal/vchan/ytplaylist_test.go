package vchan

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const csList = "PL8dPuuaLjXtNlUrzyH5r6jN9ulIgZBpdo"

func TestYouTubePlaylistLinks(t *testing.T) {
	page := "https://www.youtube.com/playlist?list=" + csList
	for _, in := range []string{
		page,
		" youtube.com/playlist?list=" + csList + "&si=x1y2 ",
		"https://www.youtube.com/watch?v=O5nskjZ_GoI&list=" + csList + "&index=2",
		"https://youtu.be/O5nskjZ_GoI?list=" + csList,
		"https://m.youtube.com/playlist?list=" + csList,
		"https://music.youtube.com/playlist?list=" + csList,
		"http://www.youtube.com/embed/videoseries?list=" + csList,
		csList,
	} {
		if got, err := YouTubePlaylist(in); got != page || err != nil {
			t.Errorf("YouTubePlaylist(%q) = %q, %v", in, got, err)
		}
	}
	if got, err := YouTubePlaylist("UU3tNpTOHsTnkmbwztCs30sA"); got != "https://www.youtube.com/playlist?list=UU3tNpTOHsTnkmbwztCs30sA" || err != nil {
		t.Errorf("uploads playlist: %q, %v", got, err)
	}
	for in, want := range map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":                    "link to a video",
		"https://youtu.be/dQw4w9WgXcQ":                                   "link to a video",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RDdQw4w9WgXcQ": "mixes",
		"https://www.youtube.com/@crashcourse":                           "isn't a YouTube playlist",
		"@crashcourse":                                                   "isn't a YouTube playlist",
		"https://vimeo.com/showcase/1?list=" + csList:                    "isn't a YouTube playlist",
		"crash course": "isn't a YouTube playlist",
		"https://www.youtube.com/playlist?list=PL<script>": "isn't a YouTube playlist",
	} {
		if got, err := YouTubePlaylist(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("YouTubePlaylist(%q) = %q, %v; want an error saying %q", in, got, err, want)
		}
	}
}

func TestYtConfigPlaylist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, youtubeConfig)
	for cfg, want := range map[string]string{
		`{"playlist": "` + csList + `", "maxHeight": 480, "minMinutes": 20}`: "",
		`{"playlist": "` + csList + `", "channels": ["@crashcourse"]}`:       "both channels and a playlist",
		`{"playlist": "", "channels": []}`:                                   "no channels or playlist",
		`{"playlist": "https://youtu.be/dQw4w9WgXcQ"}`:                       "link to a video",
	} {
		if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := readYtConfig(path)
		switch {
		case want == "" && (err != nil || c.Playlist != csList || c.MaxHeight != 480):
			t.Errorf("%s: %+v %v", cfg, c, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want an error saying %q", cfg, err, want)
		}
	}
}

func TestYtPlaylistListing(t *testing.T) {
	l, err := parseYtPlaylist(readTestdata(t, "yt_playlist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != csList || l.Name != "Computer Science" || l.Owner != "CrashCourse" || l.OwnerID != "UCX6b17PVsYBQ0ip5gyeme-Q" {
		t.Errorf("playlist: %q %q by %q %q", l.ID, l.Name, l.Owner, l.OwnerID)
	}
	// In order, once each, without the private, deleted and members-only
	// videos; live and upcoming ones marked.
	var got []string
	for _, v := range l.Videos {
		got = append(got, v.ID+":"+v.Skip)
	}
	want := []string{"tpIctyqH29Q:", "O5nskjZ_GoI:", "LN0ucKNX0hc:", "gI-qXk7XojA:", "1GSjbWt0c9M:", "NoLength001:", "Premiere001:upcoming",
		"LiveNow0001:live", "StreamVod01:"}
	if !slices.Equal(got, want) {
		t.Errorf("videos %v,\nwant %v", got, want)
	}
	if v := l.Videos[1]; v.Seconds != 713 || v.Channel != "CrashCourse" || v.Title != "Early Computing: Crash Course Computer Science #1" {
		t.Errorf("a video: %+v", v)
	}
	if _, err := parseYtPlaylist(readTestdata(t, "yt_listing.json")); err == nil {
		t.Error("a channel's tab read as a playlist")
	}

	// Listed again: the playlist's new order, what's known kept, a video
	// taken out gone, and one found unplayable still so.
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	s := &ytSource{URL: ytPlaylistURL(csList)}
	if added := s.mergePlaylist(l, now); added != 0 || s.Name != "Computer Science" || s.Owner != "CrashCourse" || len(s.Videos) != 9 {
		t.Fatalf("first listing: %d added, %+v", added, s)
	}
	s.Videos[0].Description, s.Videos[0].Loudness = "The preview.", &loudness{}
	s.Videos[2].Skip = "gone"
	again := ytListing{ID: csList, Name: "Computer Science (all)", Owner: "CrashCourse", Videos: []*ytVideo{
		{ID: "O5nskjZ_GoI", Title: "Early Computing", Seconds: 713},
		{ID: "Inserted001", Title: "New", Seconds: 300},
		{ID: "tpIctyqH29Q", Title: "Preview", Seconds: 0},
		{ID: "LN0ucKNX0hc", Title: "Electronic Computing", Seconds: 644},
		{ID: "Premiere001", Title: "Coming soon: #41", Seconds: 690},
	}}
	if added := s.mergePlaylist(again, now.Add(time.Hour)); added != 1 {
		t.Errorf("added %d", added)
	}
	got = nil
	for _, v := range s.Videos {
		got = append(got, v.ID+":"+v.Skip)
	}
	if want := []string{"O5nskjZ_GoI:", "Inserted001:", "tpIctyqH29Q:", "LN0ucKNX0hc:gone", "Premiere001:"}; !slices.Equal(got, want) {
		t.Errorf("relisted %v, want %v", got, want)
	}
	if v := s.Videos[2]; v.Seconds != 165 || v.Description != "The preview." || v.Loudness == nil || v.Title != "Preview" {
		t.Errorf("a known video: %+v", v)
	}
	if v := s.Videos[1]; !v.Found.Equal(now.Add(time.Hour)) || v.New {
		t.Errorf("a new video: %+v", v)
	}
	if s.Name != "Computer Science (all)" {
		t.Errorf("title %q", s.Name)
	}
}

// testPlaylist makes a channel playing a playlist of five videos, 10, 20,
// 30, 40 and 50 minutes long, with an upcoming one among them, listed at
// clock; with a schedule, as channel.json would have it.
func testPlaylist(t *testing.T, clock time.Time, schedule string) *YouTube {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AIRWAVES_FAKE_YTDLP", filepath.Join(t.TempDir(), "yt-dlp.log"))
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(youtubeConfig, `{"playlist": "PLtestplaylist01"}`)
	if schedule != "" {
		write(DetailsFile, `{"schedule": `+schedule+`}`)
	}
	s := &ytSource{URL: ytPlaylistURL("PLtestplaylist01"), ID: "PLtestplaylist01", Name: "A course", Owner: "Teacher",
		Listed: clock, Checked: clock, Dated: true}
	for i := range 5 {
		s.Videos = append(s.Videos, &ytVideo{ID: fmt.Sprintf("part%07d", i+1), Title: fmt.Sprintf("Part %d", i+1),
			Seconds: (i + 1) * 600, Description: "About part " + fmt.Sprint(i+1), Looked: clock})
		if i == 2 {
			s.Videos = append(s.Videos, &ytVideo{ID: "Premiere001", Title: "Bonus", Skip: "upcoming", Looked: clock})
		}
	}
	s.Videos[1].Channel = "Guest"
	y := &YouTube{Num: "1.9", Title: "Course", Dir: dir, now: func() time.Time { return clock },
		YtDlp: &YtDlp{Path: os.Args[0], HTTP: &http.Client{Transport: &feedStub{down: true}}}}
	y.save(ytCatalogFile, ytCatalog{Sources: []*ytSource{s}})
	return y
}

// TestYtPlaylistAroundTheClock: without a schedule, the playlist loops on
// the clock from the epoch, as folder channels do.
func TestYtPlaylistAroundTheClock(t *testing.T) {
	const loop = 9000 * time.Second                     // the five videos
	at := scheduleEpoch.Add(100*loop + 700*time.Second) // 100 s into part 2
	y := testPlaylist(t, at, "")
	if y.Empty() {
		t.Fatal("empty")
	}
	progs := y.Programs(at, at.Add(3*time.Hour))
	var titles []string
	for i, p := range progs {
		titles = append(titles, p.Title)
		if i > 0 && !p.Start.Equal(progs[i-1].End) {
			t.Errorf("program %d starts %v, after %v", i, p.Start, progs[i-1].End)
		}
	}
	if want := []string{"Part 2", "Part 3", "Part 4", "Part 5", "Part 1", "Part 2", "Part 3"}; !slices.Equal(titles, want) {
		t.Errorf("programs %v, want %v", titles, want)
	}
	if p := progs[0]; !p.Start.Equal(scheduleEpoch.Add(100*loop+600*time.Second)) || p.Subtitle != "Guest" ||
		p.Description != "About part 2" || p.Image != ytThumbnail("part0000002") {
		t.Errorf("part 2: %+v", p)
	}
	if p := progs[1]; p.Subtitle != "Teacher" {
		t.Errorf("a video without a channel of its own: %+v", p)
	}
	it, skip, err := y.cue()(at, true)
	if err != nil || it.Path != "part0000002" || it.Slate || it.Open == nil || it.Frames != 1200*loopFPS || skip != 100*loopFPS {
		t.Errorf("cue: %+v from %d, %v", it, skip, err)
	}
	items, ok := y.Sequence()
	if !ok || len(items) != 5 || items[2].Title != "Part 3" || items[2].Length != 30*time.Minute || items[1].Subtitle != "Guest" {
		t.Errorf("sequence: %+v %v", items, ok)
	}
	if m, n := y.Leveled(); m != 0 || n != 5 {
		t.Errorf("leveled %d of %d", m, n)
	}
	if _, err := os.Stat(filepath.Join(y.Dir, ytPlayoutFile)); !os.IsNotExist(err) {
		t.Errorf("a playlist made a schedule file: %v", err)
	}

	// Uploads play in no order.
	up := &YouTube{Num: "1.8", Dir: t.TempDir(), loaded: true, cfg: ytConfig{Channels: []string{"@a"}}}
	if items, ok := up.Sequence(); ok || items != nil {
		t.Errorf("uploads' sequence: %v %v", items, ok)
	}
}

// TestYtPlaylistOnASchedule: two videos a night at 8 PM from today, off
// the air between, as the guide and the stream both have it.
func TestYtPlaylistOnASchedule(t *testing.T) {
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, 4+d, h, m, 0, 0, time.Local) } // a Sunday
	y := testPlaylist(t, day(0, 15, 0), `{"start": "2026-10-04", "blocks": [{"at": "20:00", "count": 2}]}`)
	if y.Empty() {
		t.Fatal("empty while off the air")
	}
	progs := y.Programs(day(0, 15, 0), day(1, 15, 0))
	type entry struct {
		start, end  time.Time
		title, desc string
	}
	want := []entry{
		{day(0, 15, 0), day(0, 20, 0), "Off air", "Starts at 8:00 PM with Part 1."},
		{day(0, 20, 0), day(0, 20, 10), "Part 1", "About part 1"},
		{day(0, 20, 10), day(0, 20, 30), "Part 2", "About part 2"},
		{day(0, 20, 30), day(1, 20, 0), "Off air", "Back Mon, Oct 5 at 8:00 PM with Part 3."},
	}
	if len(progs) != len(want) {
		t.Fatalf("programs: %+v", progs)
	}
	for i, w := range want {
		if p := progs[i]; !p.Start.Equal(w.start) || !p.End.Equal(w.end) || p.Title != w.title || p.Description != w.desc {
			t.Errorf("program %d: %v-%v %q %q, want %+v", i, p.Start, p.End, p.Title, p.Description, w)
		}
	}
	// The next night picks up at part 3; the night after loops round.
	progs = y.Programs(day(2, 0, 0), day(3, 0, 0))
	var titles []string
	for _, p := range progs {
		titles = append(titles, p.Title)
	}
	if !slices.Equal(titles, []string{"Off air", "Part 5", "Part 1", "Off air"}) {
		t.Errorf("Tuesday: %v", titles)
	}

	cue := y.cue()
	for _, c := range []struct {
		at    time.Time
		path  string
		slate string
		frames,
		skip int64
	}{
		{day(0, 15, 0), "", "Starts at 8:00 PM with Part 1", 60 * loopFPS, 0},
		{day(0, 20, 5), "part0000001", "", 600 * loopFPS, 300 * loopFPS},
		{day(0, 20, 10), "part0000002", "", 1200 * loopFPS, 0},
		{day(0, 20, 31), "", "Back Mon, Oct 5 at 8:00 PM with Part 3", 60 * loopFPS, 0},
		{day(1, 19, 1).Add(-30 * time.Second), "", "Back at 8:00 PM with Part 3", 60 * loopFPS, 0},
	} {
		it, skip, err := cue(c.at, true)
		if err != nil || it.Path != c.path || it.Slate != (c.slate != "") || c.slate != "" && it.Title != c.slate || it.Frames != c.frames || skip != c.skip {
			t.Errorf("at %v: %q slate=%v %q, %d frames from %d; want %q %q, %d from %d", c.at, it.Path, it.Slate, it.Title, it.Frames, skip,
				c.path, c.slate, c.frames, c.skip)
		}
	}
	// Found ahead: what airs after the night's last video is the next
	// night's first.
	y.mu.Lock()
	next := y.after(ytAiring{ID: "part0000002", Start: day(0, 20, 10), End: day(0, 20, 30)})
	y.mu.Unlock()
	if next.ID != "part0000003" || !next.Start.Equal(day(1, 20, 0)) {
		t.Errorf("after the night: %+v", next)
	}
}

// TestYtPlaylistListedWithYtDlp lists a playlist with the fake yt-dlp: the
// catalog keeps its videos in order, a video without a length is looked
// up and joins, and editing youtube.json lists it again, taking in a video
// added to the playlist.
func TestYtPlaylistListedWithYtDlp(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "yt-dlp.log")
	t.Setenv("AIRWAVES_FAKE_YTDLP", calls)
	gap := ytLookupGap
	ytLookupGap = 0
	defer func() { ytLookupGap = gap }()
	config := filepath.Join(dir, youtubeConfig)
	given := "https://www.youtube.com/watch?v=O5nskjZ_GoI&list=" + csList + "&index=2"
	if err := os.WriteFile(config, []byte(`{"playlist": "`+given+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	y := &YouTube{Num: "1.9", Title: "CS", Dir: dir, now: func() time.Time { return clock },
		YtDlp: &YtDlp{Path: os.Args[0], HTTP: &http.Client{Transport: &feedStub{down: true}}}}
	refresh := func() {
		y.mu.Lock()
		y.tend()
		for y.busy { // tend's own
			y.mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			y.mu.Lock()
		}
		y.busy = true
		y.mu.Unlock()
		y.refresh()
	}

	if !y.Empty() {
		t.Error("on the air before it's listed")
	}
	refresh()
	raw, _ := os.ReadFile(calls)
	if !strings.Contains(string(raw), "--flat-playlist -J https://www.youtube.com/playlist?list="+csList+"\n") {
		t.Errorf("yt-dlp ran as:\n%s", raw)
	}
	cat, err := readYtCatalog(filepath.Join(dir, ytCatalogFile))
	if err != nil || len(cat.Sources) != 1 {
		t.Fatalf("catalog: %+v %v", cat, err)
	}
	if s := cat.Sources[0]; s.ID != csList || s.Name != "Computer Science" || s.Owner != "CrashCourse" || len(s.Videos) != 9 || s.Videos[0].ID != "tpIctyqH29Q" {
		t.Errorf("catalog's source: %+v", s)
	}
	ids := func() []string {
		y.mu.Lock()
		defer y.mu.Unlock()
		seq, _ := y.sequence()
		var ids []string
		for _, v := range seq {
			ids = append(ids, v.ID)
		}
		return ids
	}
	// The video without a length joins once looked up; the premiere waits.
	if got, want := ids(), []string{"tpIctyqH29Q", "O5nskjZ_GoI", "LN0ucKNX0hc", "gI-qXk7XojA", "1GSjbWt0c9M", "NoLength001", "StreamVod01"}; !slices.Equal(got, want) {
		t.Errorf("sequence %v, want %v", got, want)
	}
	items, ok := y.Sequence()
	if !ok || len(items) != 7 || items[5].Title != "Looked up NoLength001" || items[5].Length != 15*time.Minute || items[5].Subtitle != "Some Other Channel" {
		t.Errorf("sequence: %+v %v", items, ok)
	}
	st := y.Status()
	if len(st.Sources) != 1 || st.Config.Playlist != given {
		t.Fatalf("status: %+v", st)
	}
	if s := st.Sources[0]; s.Channel != given || s.URL != ytPlaylistURL(csList) || s.Name != "Computer Science" || s.Owner != "CrashCourse" ||
		s.Videos != 9 || s.Playable != 7 || s.Listing || !s.Listed.Equal(clock) || s.Image != ytThumbnail("tpIctyqH29Q") {
		t.Errorf("status: %+v", s)
	}
	if y.Empty() {
		t.Error("empty once listed")
	}

	// Nothing's due until the quarter hour's lookups; the listing is a day
	// off.
	y.mu.Lock()
	due, soon := y.due(clock.Add(time.Minute)), y.due(clock.Add(16*time.Minute))
	y.mu.Unlock()
	if due || !soon {
		t.Errorf("due a minute on %v, a quarter hour on %v", due, soon)
	}

	// Edited: listed again at once, with what was added to the playlist.
	t.Setenv("AIRWAVES_FAKE_PLAYLIST", "Added000001")
	clock = clock.Add(5 * time.Minute)
	if err := os.WriteFile(config, []byte(`{"playlist": "`+csList+`", "maxHeight": 1080}`), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(config, clock, clock)
	if st := y.Status(); !st.Sources[0].Listing {
		t.Errorf("not listing after the edit: %+v", st.Sources[0])
	}
	refresh()
	if got := ids(); len(got) != 8 || got[7] != "Added000001" {
		t.Errorf("after the edit: %v", got)
	}
	if items, _ = y.Sequence(); items[5].Length != 15*time.Minute {
		t.Errorf("the looked-up length was lost: %+v", items[5])
	}
	if n := strings.Count(func() string { raw, _ := os.ReadFile(calls); return string(raw) }(), "--flat-playlist -J https://www.youtube.com/playlist"); n != 2 {
		t.Errorf("listed %d times", n)
	}
	var saved ytCatalog
	raw, _ = os.ReadFile(filepath.Join(dir, ytCatalogFile))
	if err := json.Unmarshal(raw, &saved); err != nil || len(saved.Sources[0].Videos) != 10 {
		t.Errorf("saved catalog: %v", err)
	}
}
