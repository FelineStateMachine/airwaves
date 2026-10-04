package vchan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestYtCatalogFromListingAndFeed(t *testing.T) {
	l, err := parseYtListing(readTestdata(t, "yt_listing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "UC_O58Rr2DOskJvs9bArpLkQ" || l.Name != "The Library of Letourneau" || len(l.Videos) != 7 {
		t.Fatalf("listing: %q %q, %d videos", l.ID, l.Name, len(l.Videos))
	}
	if v := l.Videos[0]; v.ID != "9KaMhx-S0jo" || v.Seconds != 594 || v.Title != "Streamers go to Jupiter to get more STUPIDER" {
		t.Errorf("first video: %+v", v)
	}
	if v := l.Videos[2]; v.ID != "PremiereXyz" || v.Skip != "upcoming" || v.Seconds != 0 {
		t.Errorf("premiere: %+v", v)
	}

	cfg := ytConfig{MinMinutes: 3, MaxMinutes: 240}
	var playable []string
	for _, v := range l.Videos {
		if cfg.playable(v) {
			playable = append(playable, v.ID)
		}
	}
	// The premiere has no length yet, and uUNKsYaCyXg is under three
	// minutes.
	if want := []string{"9KaMhx-S0jo", "D0PHjmJUG0w", "aNauO_0Y9RA", "velAP60VACY", "8JE8devZIZQ"}; !slices.Equal(playable, want) {
		t.Errorf("playable %v, want %v", playable, want)
	}

	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	s := &ytSource{URL: ytVideosURL("@TheLibraryofLetourneau")}
	if added := s.mergeListing(l, now); added != 0 {
		t.Errorf("the first listing found %d new videos; it should take them all as the back catalog", added)
	}
	for _, v := range s.Videos {
		if v.New || !v.Found.IsZero() {
			t.Errorf("back catalog video %s marked new", v.ID)
		}
	}

	entries, err := parseYtFeed(readTestdata(t, "yt_feed.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 || entries[0].ID != "NewUpload01" || entries[0].Published.IsZero() ||
		!strings.Contains(entries[3].Link.Href, "/shorts/") {
		t.Fatalf("feed: %+v", entries)
	}
	added, changed := s.mergeFeed(entries, now)
	if added != 1 || !changed {
		t.Errorf("feed added %d new uploads (changed %v), want 1", added, changed)
	}
	byID := map[string]*ytVideo{}
	for _, v := range s.Videos {
		byID[v.ID] = v
	}
	if v := byID["NewUpload01"]; v == nil || !v.New || v.Seconds != 0 || v.Skip != "" || v.Found != now ||
		v.Description != "BUT YOURE ALREADY REHEATING THE WEDGES SO WHY NOT?? YKNOW WHATEVER MAN" {
		t.Errorf("new upload: %+v", v)
	}
	if v := byID[entries[3].ID]; v == nil || v.Skip != "short" || v.New {
		t.Errorf("Short: %+v", v)
	}
	if v := byID["9KaMhx-S0jo"]; v.Published.IsZero() || v.Description != "if you were on youtube shorts these would be so much easier ryan" {
		t.Errorf("known video not filled in from the feed: %+v", v)
	}
	if s.Videos[0].ID != "NewUpload01" {
		t.Errorf("feed uploads should go first, got %s", s.Videos[0].ID)
	}

	// Its lookup gives the length; it's playable and still due a first run.
	info, err := parseYtInfo(readTestdata(t, "yt_video.json"))
	if err != nil {
		t.Fatal(err)
	}
	byID["NewUpload01"].learn(info, now)
	if v := byID["NewUpload01"]; v.Seconds != 593 || !v.New || !cfg.playable(v) || v.Looked != now {
		t.Errorf("after lookup: %+v", v)
	}

	// A later listing with an upload at the top marks it new; one the
	// listing lacks that was found lately stays.
	l.Videos = append([]*ytVideo{{ID: "Fresh000001", Title: "Fresh", Seconds: 900}}, l.Videos...)
	if added := s.mergeListing(l, now.Add(time.Hour)); added != 1 {
		t.Errorf("second listing added %d", added)
	}
	if !slices.ContainsFunc(s.Videos, func(v *ytVideo) bool { return v.ID == "Fresh000001" && v.New }) ||
		!slices.ContainsFunc(s.Videos, func(v *ytVideo) bool { return v.ID == "NewUpload01" && v.Seconds == 593 }) {
		t.Errorf("second listing lost videos: %d", len(s.Videos))
	}
}

func TestYtInfoInputs(t *testing.T) {
	info, err := parseYtInfo(readTestdata(t, "yt_video.json"))
	if err != nil {
		t.Fatal(err)
	}
	in := info.inputs()
	if len(in) != 2 || in[0].VCodec == "none" || in[1].ACodec == "none" || in[0].Height != 720 {
		t.Fatalf("inputs: %+v", in)
	}
	args, audio := ytInputArgs(in, 90*time.Second, true)
	if audio != "1:a:0" || countInputs(args) != 2 || args[0] != "-xerror" {
		t.Errorf("audio %q, %d inputs: %q", audio, countInputs(args), args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-ss 90.000 -re -i https://rr1---sn-test.googlevideo.com/videoplayback?itag=298",
		"-request_size 10485760", "User-Agent: Mozilla/5.0", "\r\n"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q:\n%s", want, joined)
		}
	}
	// One file with picture and sound.
	single := ytInfo{ID: "x", ytFormat: ytFormat{URL: "https://example.com/v.mp4", VCodec: "avc1", ACodec: "mp4a.40.2"}}
	if args, audio := ytInputArgs(single.inputs(), 0, false); audio != "0:a:0" || countInputs(args) != 1 ||
		slices.Contains(args, "-ss") || slices.Contains(args, "-request_size") {
		t.Errorf("single file: %q %q", args, audio)
	}
	if got := ytFormatSpec(480); got != "bv*[height<=480][vcodec^=avc1]+ba[ext=m4a][language^=en]/bv*[height<=480][vcodec^=avc1]+ba[ext=m4a]/b[height<=480]" {
		t.Errorf("format %s", got)
	}
}

func TestYtDescription(t *testing.T) {
	for in, want := range map[string]string{
		"Batomon Showdown on Steam: https://store.steampowered.com/app/1/\n\nThis video came from my stream on September 29th, 2026. Catch me live here: http://twitch.tv/Northernlion\n\nIf you enjoyed the video...\n-----------------\nSubscribe: http://bit.ly/x": "This video came from my stream on September 29th, 2026. Catch me live here",
		"if you were on youtube shorts\n=============\nGames: fermi.gg":                                                     "if you were on youtube shorts",
		"Lego Party on Steam: https://x.com\n\nApollo: http://twitch.tv/a\nJustin: http://twitch.tv/b\n\nFun with friends.": "Fun with friends.",
		"https://only.links/here": "",
	} {
		if got := ytDescription(in); got != want {
			t.Errorf("ytDescription(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSeries(t *testing.T) {
	for title, want := range map[string]string{
		"Do you do anything or what (Slay the Spire 2)":                               "slay the spire 2",
		"The Sounds Of Revelry | Spelunky 2 (Episode 15)":                             "spelunky 2",
		"The Binding of Isaac: Rebirth - Let's Play - Episode 458 [Crystal]":          "the binding of isaac: rebirth",
		"Northernlion Plays - Hitman - Episode 21 [Twitch VOD]":                       "hitman",
		"El Dorado - Europa Universalis IV Multiplayer - Episode 53":                  "europa universalis iv multiplayer",
		"XCOM: Long War - Season 2 - Episode 9 [Bridge]":                              "xcom: long war",
		"modern day seneca - Bits and Banter [09/30/2026]":                            "bits and banter",
		"The Northernlion Live Super Show! [February 19th, 2014] (1/2)":               "the northernlion live super show!",
		"When Rolling Out Goes Wrong | Fall Guys Season 2 #23":                        "fall guys season 2",
		"Playing online chess for the first time in 15 years":                         "playing online chess for the first time in 15 years",
		"Cities: Skylines [Re-Do] - Northernlion Plays - Episode 3":                   "cities: skylines",
		"Ultimate Chicken Horse with Friends - Episode 13 - Crypt":                    "ultimate chicken horse with friends",
		"Giving The Doubters Some Hope | Repentance on Stream (Episode 226)":          "repentance on stream",
		"Let's Play - The Binding of Isaac - Episode 404 [Dentist's Appointment]":     "the binding of isaac",
		"Darkest Dungeon: The Color of Madness - Northernlion Plays - Episode 32 [x]": "darkest dungeon: the color of madness",
	} {
		if got := series(title); got != want {
			t.Errorf("series(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestYtVideosURL(t *testing.T) {
	for in, want := range map[string]string{
		"@Northernlion":                                "https://www.youtube.com/@Northernlion/videos",
		"https://www.youtube.com/@Northernlion":        "https://www.youtube.com/@Northernlion/videos",
		"https://www.youtube.com/@Northernlion/":       "https://www.youtube.com/@Northernlion/videos",
		"youtube.com/@Northernlion/streams":            "https://youtube.com/@Northernlion/videos",
		"UC3tNpTOHsTnkmbwztCs30sA":                     "https://www.youtube.com/channel/UC3tNpTOHsTnkmbwztCs30sA/videos",
		"https://www.youtube.com/@Northernlion/videos": "https://www.youtube.com/@Northernlion/videos",
	} {
		if got := ytVideosURL(in); got != want {
			t.Errorf("ytVideosURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// testPool makes two channels of videos n minutes long, cycling through
// lengths.
func testPool(per int, repeat time.Duration, minutes ...int) *ytPool {
	p := &ytPool{Repeat: repeat, Lock: 2 * time.Hour, Seed: 7, Basis: "a,b"}
	for c, ch := range []string{"UCa", "UCb"} {
		s := ytPoolSource{ID: ch}
		for i := range per {
			s.Videos = append(s.Videos, &ytVideo{
				ID: fmt.Sprintf("%s-%03d", ch, i), Title: fmt.Sprintf("Video %d (Game %d)", i, (i+c)%7),
				Seconds: minutes[i%len(minutes)] * 60,
			})
		}
		p.Sources = append(p.Sources, s)
	}
	return p
}

// checkSchedule checks the airings are back to back and that no video
// repeats within repeat.
func checkSchedule(t *testing.T, as []ytAiring, repeat time.Duration) {
	t.Helper()
	last := map[string]time.Time{}
	for i, a := range as {
		if i > 0 && !a.Start.Equal(as[i-1].End) {
			t.Fatalf("airing %d starts %v, after %v", i, a.Start, as[i-1].End)
		}
		if t0, ok := last[a.ID]; ok && a.Start.Sub(t0) < repeat {
			t.Fatalf("%s repeats after %v", a.ID, a.Start.Sub(t0))
		}
		last[a.ID] = a.Start
	}
}

func TestYtPlanAlternatesAndGuardsRepeats(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 7, 30, 0, time.UTC)
	pool := testPool(200, 7*24*time.Hour, 10, 25, 40)
	var p ytPlayout
	if !p.update(pool, now) {
		t.Fatal("no schedule made")
	}
	if p.Airings[0].Start != now.Truncate(time.Minute) || p.end(now).Before(now.Add(ytPlanAhead)) {
		t.Fatalf("schedule spans %v to %v", p.Airings[0].Start, p.end(now))
	}
	checkSchedule(t, p.Airings, pool.Repeat)
	for i := 1; i < len(p.Airings); i++ {
		if p.Airings[i].Channel == p.Airings[i-1].Channel {
			t.Fatalf("airings %d and %d are both from %s", i-1, i, p.Airings[i].Channel)
		}
		if series(p.Airings[i].Title) == series(p.Airings[i-1].Title) {
			t.Errorf("airings %d and %d are both %q", i-1, i, series(p.Airings[i].Title))
		}
	}
	// Nothing to do an hour later, and the same inputs plan the same.
	before := slices.Clone(p.Airings)
	if p.update(pool, now.Add(30*time.Minute)) {
		t.Error("a fresh schedule changed half an hour later")
	}
	var q ytPlayout
	q.update(testPool(200, 7*24*time.Hour, 10, 25, 40), now)
	if !sameAirings(q.Airings, before) {
		t.Error("the same inputs planned differently")
	}
}

func TestYtFirstRunsOnlyReplaceUnlockedReruns(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	pool := testPool(200, 7*24*time.Hour, 20, 30)
	var p ytPlayout
	p.update(pool, now)
	before := slices.Clone(p.Airings)

	// Two uploads arrive an hour later.
	later := now.Add(time.Hour)
	older := &ytVideo{ID: "UCb-new-1", Title: "Older upload", Seconds: 600, Published: later.Add(-2 * time.Hour), New: true}
	newer := &ytVideo{ID: "UCa-new-2", Title: "Newer upload", Seconds: 1500, Published: later.Add(-time.Hour), New: true}
	pool.Sources[0].Videos = append(pool.Sources[0].Videos, newer)
	pool.Sources[1].Videos = append(pool.Sources[1].Videos, older)
	pool.Firsts = []ytPick{{older, "UCb"}, {newer, "UCa"}}
	slices.SortFunc(pool.Firsts, newestFirst)
	pool.ids = nil
	if !p.update(pool, later) {
		t.Fatal("first runs didn't change the schedule")
	}
	lock := later.Add(pool.Lock)
	k := slices.IndexFunc(before, func(a ytAiring) bool { return !a.Start.Before(lock) })
	if !sameAirings(p.Airings[:k], before[:k]) {
		t.Fatal("airings within the lock changed")
	}
	if a := p.Airings[k]; a.ID != "UCa-new-2" || !a.First || !a.Start.Equal(before[k-1].End) {
		t.Errorf("first unlocked airing %+v, want the newer upload's first run", a)
	}
	if a := p.Airings[k+1]; a.ID != "UCb-new-1" || !a.First {
		t.Errorf("second unlocked airing %+v, want the older upload's first run", a)
	}
	// The reruns planned there follow, in the same order.
	var was, now2 []string
	for _, a := range before[k:] {
		was = append(was, a.ID)
	}
	for _, a := range p.Airings[k+2:] {
		now2 = append(now2, a.ID)
	}
	if len(now2) < len(was) || !slices.Equal(now2[:len(was)], was) {
		t.Errorf("reruns after the first runs were reshuffled or dropped")
	}
	checkSchedule(t, p.Airings, pool.Repeat)
	// Planned once: another update leaves them be.
	if p.update(pool, later.Add(time.Minute)) {
		t.Error("first runs were planned again")
	}
	// Once within the lock, a first run stays put like the rest, even
	// when more uploads arrive.
	first := p.Airings[k]
	newest := &ytVideo{ID: "UCb-new-3", Title: "Newest upload", Seconds: 900, Published: lock, New: true}
	pool.Sources[1].Videos = append(pool.Sources[1].Videos, newest)
	pool.Firsts = append([]ytPick{{newest, "UCb"}}, pool.Firsts...)
	pool.ids = nil
	p.update(pool, first.Start.Add(-time.Hour))
	if i := slices.IndexFunc(p.Airings, func(a ytAiring) bool { return a.ID == first.ID }); i < 0 || !sameAirings(p.Airings[i:i+1], []ytAiring{first}) {
		t.Error("a locked first run moved")
	}
	if !slices.ContainsFunc(p.Airings, func(a ytAiring) bool { return a.ID == "UCb-new-3" && a.First }) {
		t.Error("the newest upload wasn't planned")
	}
	checkSchedule(t, p.Airings, pool.Repeat)
}

func TestYtRepeatGuardAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	repeat := 4 * 24 * time.Hour
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	y := &YouTube{Num: "1.8", Dir: dir}
	var p ytPlayout
	// Six days of restarts every 20 hours: each loads the schedule, extends
	// it and saves it.
	for day := range 8 {
		at := now.Add(time.Duration(day) * 20 * time.Hour)
		if day > 0 {
			var err error
			if p, err = readYtPlayout(filepath.Join(dir, ytPlayoutFile)); err != nil {
				t.Fatal(err)
			}
		}
		p.update(testPool(150, repeat, 20, 30), at)
		y.save(ytPlayoutFile, p)
	}
	loaded, err := readYtPlayout(filepath.Join(dir, ytPlayoutFile))
	if err != nil {
		t.Fatal(err)
	}
	if !sameAirings(loaded.Airings, p.Airings) || loaded.Basis != p.Basis {
		t.Fatal("the schedule didn't survive a save and load")
	}
	if loaded.Airings[0].Start.After(now.Add(7*20*time.Hour - repeat)) {
		t.Errorf("history starts %v; the repeat guard needs %v back", loaded.Airings[0].Start, repeat)
	}
	checkSchedule(t, loaded.Airings, repeat)
}

func TestYtSmallPoolRepeatsLeastRecent(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	pool := testPool(5, 30*24*time.Hour, 60) // ten hours of video for two days
	var p ytPlayout
	p.update(pool, now)
	// Each channel cycles through its five videos in the same order.
	for i := 10; i < len(p.Airings); i++ {
		if p.Airings[i].ID != p.Airings[i-10].ID {
			t.Fatalf("airing %d is %s, want %s again", i, p.Airings[i].ID, p.Airings[i-10].ID)
		}
	}
	seen := map[string]bool{}
	for _, a := range p.Airings[:10] {
		seen[a.ID] = true
	}
	if len(seen) != 10 {
		t.Errorf("a video repeated before all had aired: %d distinct", len(seen))
	}
}

// TestYtCueKeepsToTheSchedule walks the stream's cues through a video that
// ends early: it's reopened where it should be, twice, then a slate fills
// its time, and the next video starts on time.
func TestYtCueKeepsToTheSchedule(t *testing.T) {
	t0 := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	y := &YouTube{Num: "1.8", Dir: t.TempDir(), loaded: true, now: func() time.Time { return t0 },
		pool: &ytPool{Basis: "test", Lock: time.Hour}, plan: ytPlayout{Basis: "test", Airings: []ytAiring{
			{ID: "aaaaaaaaaaa", Channel: "UCa", Title: "A", Start: t0, End: t0.Add(time.Minute)},
			{ID: "bbbbbbbbbbb", Channel: "UCb", Title: "B", Start: t0.Add(time.Minute), End: t0.Add(3 * time.Minute)},
		}}}
	cue := y.cue()
	for _, c := range []struct {
		at    time.Duration
		id    string
		slate bool
		frames,
		skip int64
	}{
		{10 * time.Second, "aaaaaaaaaaa", false, 60 * loopFPS, 10 * loopFPS}, // tuned in
		{20 * time.Second, "aaaaaaaaaaa", false, 60 * loopFPS, 20 * loopFPS}, // ended early: reopened
		{25 * time.Second, "aaaaaaaaaaa", false, 60 * loopFPS, 25 * loopFPS}, // and once more
		{30 * time.Second, "", true, 30 * loopFPS, 0},                        // then a slate to the end
		{59*time.Second + 800*time.Millisecond, "bbbbbbbbbbb", false, 120 * loopFPS, 0},
		{80 * time.Second, "bbbbbbbbbbb", false, 120 * loopFPS, 20 * loopFPS},  // failed, reopened
		{150 * time.Second, "bbbbbbbbbbb", false, 120 * loopFPS, 90 * loopFPS}, // played a while, failed again
		{155 * time.Second, "bbbbbbbbbbb", false, 120 * loopFPS, 95 * loopFPS},
		{3*time.Minute + 5*time.Second, "", true, 10 * loopFPS, 0}, // off the end of the schedule
	} {
		it, skip, err := cue(t0.Add(c.at), true)
		if err != nil {
			t.Fatal(err)
		}
		if it.Path != c.id || it.Slate != c.slate || it.Frames != c.frames || skip != c.skip {
			t.Errorf("at %v: got %q slate=%v %d frames from %d; want %q slate=%v %d from %d",
				c.at, it.Path, it.Slate, it.Frames, skip, c.id, c.slate, c.frames, c.skip)
		}
		if !c.slate && it.Open == nil {
			t.Errorf("at %v: no Open", c.at)
		}
	}
}

func TestYouTubeGuide(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(youtubeConfig, `{"channels": ["@Northernlion", "https://www.youtube.com/@TheLibraryofLetourneau"], "minMinutes": 5}`)
	cat := ytCatalog{}
	for i, name := range []string{"Northernlion", "The Library of Letourneau"} {
		s := &ytSource{URL: ytVideosURL([]string{"@Northernlion", "@TheLibraryofLetourneau"}[i]),
			ID: fmt.Sprintf("UC%d", i), Name: name, Listed: now, Checked: now}
		for j := range 40 {
			s.Videos = append(s.Videos, &ytVideo{ID: fmt.Sprintf("v%d-%02d", i, j), Title: fmt.Sprintf("%s video %d", name, j),
				Seconds: 60 * (2 + j%30), Description: "About video " + fmt.Sprint(j), Looked: now})
		}
		cat.Sources = append(cat.Sources, s)
	}
	y := &YouTube{Num: "1.8", Title: "Northernlion", Dir: dir}
	y.save(ytCatalogFile, cat)

	if y.Empty() {
		t.Fatal("channel is empty")
	}
	progs := y.Programs(now, now.Add(12*time.Hour))
	if len(progs) < 12 {
		t.Fatalf("%d programs in 12 hours", len(progs))
	}
	for i, p := range progs {
		if p.Subtitle != "Northernlion" && p.Subtitle != "The Library of Letourneau" ||
			!strings.HasPrefix(p.Title, p.Subtitle+" video ") || !strings.HasPrefix(p.Description, "About video") ||
			!strings.HasPrefix(p.Image, "https://i.ytimg.com/vi/v") || p.End.Sub(p.Start) < 5*time.Minute {
			t.Fatalf("program %d: %+v", i, p)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ytPlayoutFile)); err != nil {
		t.Errorf("schedule not saved: %v", err)
	}
	// A restart reads the same schedule.
	again := &YouTube{Num: "1.8", Title: "Northernlion", Dir: dir}
	if got := again.Programs(now, now.Add(12*time.Hour)); len(got) != len(progs) || got[5] != progs[5] {
		t.Error("schedule differs after a restart")
	}
}

// TestOpenFailureKeepsTheSchedule plays three two-second items whose
// sources open per play: the first fails to open after a while, the second
// opens separate picture and sound inputs, the third fails at once. Slates
// fill the failures' time, so the second starts on schedule, and the
// stream is a single H.264 and AAC stream as long as the time it ran.
func TestOpenFailureKeepsTheSchedule(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	dir := t.TempDir()
	picture, sound := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "a.m4a")
	for out, args := range map[string][]string{
		picture: {"-f", "lavfi", "-i", "testsrc2=s=640x360:r=25:d=3", "-c:v", "libx264"},
		sound:   {"-f", "lavfi", "-i", "sine=f=440:d=3", "-c:a", "aac"},
	} {
		cmd := exec.Command(ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error"}, args...), out)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("make %s: %v: %s", out, err, b)
		}
	}
	var openedAt time.Duration
	var start time.Time
	items := []item{
		{Path: "fails-slowly", Frames: 2 * loopFPS, Open: func(ctx context.Context, _ time.Duration) (opened, error) {
			time.Sleep(400 * time.Millisecond)
			return opened{}, errors.New("yt-dlp: Sign in to confirm you're not a bot")
		}},
		{Path: "plays", Frames: 2 * loopFPS, Open: func(ctx context.Context, _ time.Duration) (opened, error) {
			openedAt = time.Since(start)
			return opened{Args: []string{"-re", "-i", picture, "-re", "-i", sound}, Audio: "1:a:0"}, nil
		}},
		{Path: "fails", Frames: 2 * loopFPS, Open: func(ctx context.Context, _ time.Duration) (opened, error) {
			return opened{}, errors.New("no stream")
		}},
	}
	n := 0
	cue := func(now time.Time, _ bool) (item, int64, error) {
		if n == 0 {
			start = now
		}
		if n == len(items) {
			return item{Slate: true, Frames: 10 * loopFPS}, 0, nil
		}
		n++
		return items[n-1], 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5500*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if err := playCues(ctx, &out, ffmpeg, "test", cue); err != nil {
		t.Fatal(err)
	}
	// ffmpeg reads the first half second of each input at once (-re's
	// initial burst), so items end up to that much early.
	if openedAt < 1400*time.Millisecond || openedAt > 2400*time.Millisecond {
		t.Errorf("the second item opened %v in, want two seconds", openedAt)
	}
	ts := filepath.Join(dir, "out.ts")
	if err := os.WriteFile(ts, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,width,height:format=duration", "-of", "compact", ts).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %d bytes: %v: %s", out.Len(), err, probe)
	}
	got := string(probe)
	if i := strings.Index(got, "duration="); i < 0 {
		t.Errorf("no duration:\n%s", got)
	} else if d, _ := strconv.ParseFloat(strings.TrimSpace(got[i+len("duration="):]), 64); d < 4 || d > 6 {
		// Less the time spent opening, while nothing plays.
		t.Errorf("played %.2fs in 5.5s", d)
	}
	for _, want := range []string{"codec_name=h264|width=1280|height=720", "codec_name=aac"} {
		if !strings.Contains(got, want) {
			t.Errorf("stream lacks %q:\n%s", want, got)
		}
	}
}

// TestYouTubeLive builds a real YouTube channel in the folder
// $AIRWAVES_YOUTUBE_LIVE (listing both channels takes minutes the first
// time), prints its guide, and streams 30 seconds of it to out.ts there.
// $AIRWAVES_YTDLP names the yt-dlp to use; without it the latest release
// is installed in the folder, as airwavesd does.
func TestYouTubeLive(t *testing.T) {
	root := os.Getenv("AIRWAVES_YOUTUBE_LIVE")
	if root == "" {
		t.Skip("set AIRWAVES_YOUTUBE_LIVE to a scratch folder to run against YouTube")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := filepath.Join(root, "1.8 Northernlion")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"channels": ["https://www.youtube.com/@Northernlion", "https://www.youtube.com/@TheLibraryofLetourneau"],
 "repeatDays": 30, "maxHeight": 720, "minMinutes": 3, "maxMinutes": 240, "rerunMix": "recent"}`
	if err := os.WriteFile(filepath.Join(dir, youtubeConfig), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &YtDlp{Path: os.Getenv("AIRWAVES_YTDLP")}
	if tool.Path == "" {
		tool.Path, tool.Update = filepath.Join(root, "tools", "yt-dlp"), true
	}
	y := &YouTube{Num: "1.8", Title: "Northernlion", Dir: dir, FFmpeg: ffmpeg, YtDlp: tool}
	y.mu.Lock()
	y.load()
	y.busy = true
	y.mu.Unlock()
	began := time.Now()
	y.refresh()
	t.Logf("refresh took %v", time.Since(began).Round(time.Second))
	y.mu.Lock()
	for _, s := range y.cat.Sources {
		n := 0
		for _, v := range s.Videos {
			if y.cfg.playable(v) {
				n++
			}
		}
		t.Logf("%s (%s): %d videos, %d playable", s.Name, s.ID, len(s.Videos), n)
	}
	y.mu.Unlock()

	now := time.Now()
	progs := y.Programs(now, now.Add(24*time.Hour))
	if len(progs) < 12 {
		t.Fatalf("%d programs", len(progs))
	}
	for _, p := range progs[:12] {
		new := ""
		if p.New {
			new = " (new)"
		}
		t.Logf("%s-%s  %s | %s%s | %.60q | %s", p.Start.Format("15:04:05"), p.End.Format("15:04:05"),
			p.Title, p.Subtitle, new, p.Description, p.Image)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f, err := os.Create(filepath.Join(root, "out.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &firstWrite{w: f, start: time.Now()}
	if err := y.Stream(ctx, w); err != nil {
		t.Fatal(err)
	}
	t.Logf("first bytes after %v", w.first.Round(10*time.Millisecond))
}

type firstWrite struct {
	w     io.Writer
	start time.Time
	first time.Duration
}

func (f *firstWrite) Write(p []byte) (int, error) {
	if f.first == 0 {
		f.first = time.Since(f.start)
	}
	return f.w.Write(p)
}
