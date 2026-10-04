package vchan

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPlayingLoopsOnTheClock(t *testing.T) {
	items := []item{{Frames: 60 * loopFPS}, {Frames: 30 * loopFPS}} // a 90 s loop
	for _, c := range []struct {
		at       time.Duration
		i        int
		intoSecs int64
	}{
		{0, 0, 0},
		{59 * time.Second, 0, 59},
		{60 * time.Second, 1, 0},
		{100 * time.Second, 0, 10},
		{-10 * time.Second, 1, 20},
	} {
		i, into := playing(items, scheduleEpoch.Add(c.at))
		if i != c.i || into != c.intoSecs*loopFPS {
			t.Errorf("at %v: got item %d, %d frames in; want %d, %d", c.at, i, into, c.i, c.intoSecs*loopFPS)
		}
	}
	if i, _ := playing(nil, time.Now()); i != -1 {
		t.Errorf("empty folder: got item %d", i)
	}
}

func TestProgramsCoverTheWindow(t *testing.T) {
	f := &Folder{scanned: time.Now(), items: []item{
		{Frames: 20 * 60 * loopFPS, Title: "Birds"},
		{Frames: 45 * 60 * loopFPS, Title: "Fish"},
	}}
	from := time.Now()
	to := from.Add(6 * time.Hour)
	progs := f.Programs(from, to)
	if len(progs) == 0 || progs[0].Start.After(from) || progs[len(progs)-1].End.Before(to) {
		t.Fatalf("programs don't cover the window: %+v", progs)
	}
	for i := 1; i < len(progs); i++ {
		if !progs[i].Start.Equal(progs[i-1].End) {
			t.Fatalf("gap between %d and %d", i-1, i)
		}
		if progs[i].Title == progs[i-1].Title {
			t.Fatalf("videos should alternate: %q twice", progs[i].Title)
		}
	}
}

func TestDescribeUsesYtDlpInfo(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "Mouse hunt for cats [dQw4w9WgXcQ].mp4")
	if title, desc, _ := describe(plain); title != "Mouse hunt for cats" || desc != "" {
		t.Errorf("without info: got %q, %q", title, desc)
	}
	withInfo := filepath.Join(dir, "birds [abcdefghijk].webm")
	info := `{"title": "Birds for Cats 8 Hours", "description": "Relaxing birds at the feeder.\nNo ads.\n\nSubscribe: https://example.com"}`
	if err := os.WriteFile(filepath.Join(dir, "birds [abcdefghijk].info.json"), []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	if title, desc, _ := describe(withInfo); title != "Birds for Cats 8 Hours" || desc != "Relaxing birds at the feeder. No ads." {
		t.Errorf("with info: got %q, %q", title, desc)
	}
}

func TestLibraryNumbersFolders(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"1.2 Cat Sensory", "Cat Calming", "1.1 Clash", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l := &Library{Root: root, Reserved: func() []string { return []string{"1.1"} }}
	var got []string
	for _, e := range l.scan() {
		got = append(got, e.Channel.Number()+" "+e.Channel.Name())
	}
	want := "1.2 Cat Sensory|1.3 1.1 Clash|1.4 Cat Calming"
	if strings.Join(got, "|") != want {
		t.Errorf("got %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestPartialDownloadsAreSkipped(t *testing.T) {
	for name, skip := range map[string]bool{
		"a [x].mp4":       false,
		"a [x].mp4.part":  true,
		"a [x].f137.mp4":  true,
		"a [x].f251.webm": true,
		"a [x].temp.mp4":  true,
		"a [x].mp4.ytdl":  true,
		"Episode 4.5.mkv": false,
		"cats.fun.mp4":    false,
	} {
		if partial.MatchString(name) != skip {
			t.Errorf("%s: skip=%v", name, !skip)
		}
	}
}

// TestFolderStream plays across a video boundary, with videos of different
// sizes, codecs and one without sound, and checks the result is a single
// H.264 and AAC stream.
func TestFolderStream(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	dir := t.TempDir()
	for name, args := range map[string][]string{
		"a.mp4": {"-f", "lavfi", "-i", "testsrc2=s=1920x1080:r=25:d=2", "-f", "lavfi", "-i", "sine=f=440:d=2", "-c:v", "libx264", "-c:a", "aac"},
		"b.mkv": {"-f", "lavfi", "-i", "testsrc=s=480x640:r=24:d=2", "-c:v", "mpeg4"},
	} {
		cmd := exec.Command(ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error"}, args...), filepath.Join(dir, name))...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("make %s: %v: %s", name, err, out)
		}
	}
	f := &Folder{Num: "1.9", Title: "Test", Dir: dir, FFmpeg: ffmpeg, FFprobe: ffprobe}
	if f.Empty() {
		t.Fatal("folder reads as empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := f.Stream(ctx, &out); err != nil {
		t.Fatal(err)
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
	// Five seconds of real time plays past the first two-second video.
	if i := strings.Index(got, "duration="); i < 0 {
		t.Errorf("no duration:\n%s", got)
	} else if d, _ := strconv.ParseFloat(strings.TrimSpace(got[i+len("duration="):]), 64); d < 3 {
		t.Errorf("played %.1fs, want past the first video", d)
	}
	for _, want := range []string{"codec_name=h264|width=1280|height=720", "codec_name=aac"} {
		if !strings.Contains(got, want) {
			t.Errorf("stream lacks %q:\n%s", want, got)
		}
	}
}

// scheduledFolder is a folder channel of three hour-long episodes whose
// channel.json says schedule.
func scheduledFolder(t *testing.T, schedule string) *Folder {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, DetailsFile), `{"callSign": "HOTD", "schedule": `+schedule+`}`)
	var items []item
	for i := range 3 {
		items = append(items, item{Frames: 60 * 60 * loopFPS, Title: "House of the Dragon", Subtitle: fmt.Sprintf("Episode %d", i+1),
			Season: "1", Episode: strconv.Itoa(i + 1)})
	}
	return &Folder{Num: "1.7", Title: "Dragons", Dir: dir, scanned: time.Now(), items: items}
}

// TestScheduledFolderGuide: two episodes nightly at 8 PM from Oct 4, with
// the guide off the air between, saying what's next and when.
func TestScheduledFolderGuide(t *testing.T) {
	f := scheduledFolder(t, `{"start": "2026-10-04", "blocks": [{"at": "20:00", "count": 2}]}`)
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, time.Local) }

	// The day before: nothing yet, starting tomorrow with the first.
	progs := f.Programs(at(3, 12, 0), at(4, 12, 0))
	if len(progs) != 1 || !progs[0].OffAir || progs[0].Title != "Off air" || !progs[0].End.Equal(at(4, 20, 0)) ||
		progs[0].Description != "Starts Sun, Oct 4 at 8:00 PM with House of the Dragon, S1 E1." {
		t.Fatalf("before the start: %+v", progs)
	}

	// The second day: episode 3, then the first again, off the air between.
	progs = f.Programs(at(5, 12, 0), at(6, 12, 0))
	var got []string
	for _, p := range progs {
		got = append(got, p.Start.Format("Jan 2 15:04")+" "+cmp.Or(p.Subtitle, p.Title))
	}
	want := []string{"Oct 5 12:00 Off air", "Oct 5 20:00 Episode 3", "Oct 5 21:00 Episode 1", "Oct 5 22:00 Off air"}
	if !slices.Equal(got, want) {
		t.Fatalf("guide:\n%q\nwant\n%q", got, want)
	}
	if p := progs[0]; p.Description != "Back at 8:00 PM with House of the Dragon, S1 E3." || p.Season != "" {
		t.Errorf("afternoon: %+v", p)
	}
	if p := progs[3]; !p.End.Equal(at(6, 20, 0)) || p.Description != "Back Tue, Oct 6 at 8:00 PM with House of the Dragon, S1 E2." {
		t.Errorf("overnight: %+v", p)
	}
	if p := progs[1]; p.OffAir || p.Season != "1" || p.Episode != "3" || p.End.Sub(p.Start) != time.Hour {
		t.Errorf("episode: %+v", p)
	}

	// Edits to channel.json apply at once: around the clock from the third.
	writeFile(t, filepath.Join(f.Dir, DetailsFile), `{"schedule": {"start": "2026-10-04T09:00", "first": 3}}`)
	if progs := f.Programs(at(4, 9, 30), at(4, 12, 0)); len(progs) != 3 || progs[0].Subtitle != "Episode 3" || progs[1].Subtitle != "Episode 1" {
		t.Errorf("around the clock: %+v", progs)
	}
	// Without a schedule, the folder loops on the clock as ever.
	writeFile(t, filepath.Join(f.Dir, DetailsFile), `{"callSign": "HOTD"}`)
	if progs := f.Programs(at(4, 9, 30), at(4, 12, 0)); !slices.Equal(progs, programs(f.items, at(4, 9, 30), at(4, 12, 0))) {
		t.Errorf("without a schedule: %+v", progs)
	}
	// One that can't be read is logged and left out.
	logs := captureLogs(t)
	writeFile(t, filepath.Join(f.Dir, DetailsFile), `{"schedule": {"start": "October"}}`)
	if progs := f.Programs(at(4, 9, 30), at(4, 12, 0)); len(progs) == 0 || progs[0].OffAir {
		t.Errorf("with a bad schedule: %+v", progs)
	}
	if !strings.Contains(logs.String(), "airing around the clock") {
		t.Errorf("logs: %s", logs)
	}

	seq, ok := f.Sequence()
	if !ok || len(seq) != 3 || seq[2].Episode != "3" || seq[0].Length != time.Hour || seq[1].Subtitle != "Episode 2" {
		t.Errorf("sequence: %+v", seq)
	}
}

// TestScheduledCue: off the air a slate says when the channel is back, a
// minute at a time; on the air the episode on plays from where it is, and
// each picks up where the last ended, however early its decoder finished.
func TestScheduledCue(t *testing.T) {
	f := scheduledFolder(t, `{"start": "2026-10-04", "blocks": [{"at": "20:00", "count": 2}]}`)
	at := func(day, h, m, s int) time.Time { return time.Date(2026, 10, day, h, m, s, 0, time.Local) }
	type cued struct {
		slate bool
		title string
		secs  int64 // how long it plays
		into  int64 // seconds in
	}
	run := func(cue cueFunc, now time.Time, ok bool) cued {
		t.Helper()
		it, skip, err := cue(now, ok)
		if err != nil {
			t.Fatalf("at %v: %v", now, err)
		}
		title := it.Title
		if !it.Slate {
			title = it.Subtitle
		}
		return cued{it.Slate, title, (it.Frames - skip) / loopFPS, skip / loopFPS}
	}

	cue := scheduled("test", f.lineup)
	if c := run(cue, at(4, 15, 0, 0), true); c != (cued{true, "Starts at 8:00 PM with House of the Dragon, S1 E1", 60, 0}) {
		t.Errorf("afternoon: %+v", c)
	}
	cue = scheduled("test", f.lineup)
	if c := run(cue, at(4, 19, 59, 30), true); c != (cued{true, "Starts at 8:00 PM with House of the Dragon, S1 E1", 30, 0}) {
		t.Errorf("just before: %+v", c)
	}
	// The slate ended a little early; the episode starts at its top.
	if c := run(cue, at(4, 19, 59, 59), true); c != (cued{false, "Episode 1", 3600, 0}) {
		t.Errorf("at eight: %+v", c)
	}
	if c := run(cue, at(4, 20, 59, 59), true); c != (cued{false, "Episode 2", 3600, 0}) {
		t.Errorf("at nine: %+v", c)
	}
	if c := run(cue, at(4, 22, 0, 1), true); c != (cued{true, "Back Mon, Oct 5 at 8:00 PM with House of the Dragon, S1 E3", 60, 0}) {
		t.Errorf("after the block: %+v", c)
	}

	// Tuning in part way through.
	cue = scheduled("test", f.lineup)
	if c := run(cue, at(5, 20, 10, 0), true); c != (cued{false, "Episode 3", 3000, 600}) {
		t.Errorf("joining: %+v", c)
	}
	// A failed episode stands by for the rest of its time, not tried again.
	if c := run(cue, at(5, 20, 12, 0), false); c != (cued{true, standBy, 60, 0}) {
		t.Errorf("after a failure: %+v", c)
	}
	if c := run(cue, at(5, 20, 13, 0), true); c != (cued{true, standBy, 60, 0}) {
		t.Errorf("still standing by: %+v", c)
	}
	// As many failures in a row as there are videos end the stream.
	if c := run(cue, at(5, 21, 0, 0), true); c.title != "Episode 1" {
		t.Errorf("the next episode: %+v", c)
	}
	if c := run(cue, at(5, 21, 1, 0), false); c.title != standBy {
		t.Errorf("a second failure: %+v", c)
	}
	if _, _, err := cue(at(5, 21, 2, 0), false); err == nil || !strings.Contains(err.Error(), "no video would play") {
		t.Errorf("after three failures in a row, the slate's too: %v", err)
	}

	// A schedule removed while the stream plays: the loop on the clock.
	writeFile(t, filepath.Join(f.Dir, DetailsFile), `{}`)
	cue = scheduled("test", f.lineup)
	it, skip, err := cue(at(5, 15, 0, 0), true)
	i, into := playing(f.items, at(5, 15, 0, 0))
	if err != nil || it.Slate || it.Subtitle != f.items[i].Subtitle || skip != into {
		t.Errorf("without a schedule: %+v %d, want %d %d", it.Subtitle, skip, i, into)
	}
}

func TestSlateText(t *testing.T) {
	if got := slateText("Back Mon, Oct 5 at 8:00 PM with Bob's Burgers, S1 E3."); got != `Back Mon, Oct 5 at 8\:00 PM with Bobs Burgers, S1 E3.` {
		t.Errorf("got %q", got)
	}
}

// TestScheduledFolderStream plays a scheduled folder off the air, then on.
func TestScheduledFolderStream(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	dir := t.TempDir()
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=25:d=4",
		"-f", "lavfi", "-i", "sine=f=440:d=4", "-c:v", "libx264", "-c:a", "aac", filepath.Join(dir, "a.mp4"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make a.mp4: %v: %s", err, out)
	}
	f := &Folder{Num: "1.9", Title: "Test", Dir: dir, FFmpeg: ffmpeg, FFprobe: ffprobe}
	for name, start := range map[string]time.Time{"off the air": time.Now().Add(48 * time.Hour), "on the air": time.Now().Add(-time.Second)} {
		writeFile(t, filepath.Join(dir, DetailsFile), fmt.Sprintf(`{"schedule": {"start": %q}}`, start.Format("2006-01-02T15:04")))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var out bytes.Buffer
		err := f.Stream(ctx, &out)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ts := filepath.Join(t.TempDir(), "out.ts")
		if err := os.WriteFile(ts, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,width,height", "-of", "compact", ts).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: ffprobe %d bytes: %v: %s", name, out.Len(), err, probe)
		}
		for _, want := range []string{"codec_name=h264|width=1280|height=720", "codec_name=aac"} {
			if !strings.Contains(string(probe), want) {
				t.Errorf("%s: stream lacks %q:\n%s", name, want, probe)
			}
		}
	}
}
