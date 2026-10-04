package stream

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/tuner"
)

// tsWithPTS is a one-packet segment whose picture is at pts.
func tsWithPTS(pts int64) []byte {
	p := []byte{0x47, 0x41, 0x00, 0x10, 0, 0, 1, 0xe0, 0, 0, 0x80, 0x80, 5,
		byte(pts>>29&0x0e | 0x21), byte(pts >> 22), byte(pts>>14 | 1), byte(pts >> 7), byte(pts<<1 | 1)}
	for len(p) < 188 {
		p = append(p, 0xff)
	}
	// An audio packet first, as segments may start.
	audio := append([]byte{0x47, 0x41, 0x01, 0x10, 0, 0, 1, 0xc0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}, make([]byte, 170)...)
	return append(audio, p...)
}

type fakeHLS struct {
	t    *testing.T
	dir  string
	segs []int // sequence numbers listed
	pts  map[int]int64
}

// list writes stream_0.m3u8 listing segments from..to, two seconds each,
// with their files.
func (f *fakeHLS) list(from, to int) {
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", from)
	for n := from; n <= to; n++ {
		fmt.Fprintf(&b, "#EXTINF:2.000000,\nseg_0_%05d.ts\n", n)
		name := filepath.Join(f.dir, fmt.Sprintf("seg_0_%05d.ts", n))
		if _, err := os.Stat(name); err != nil {
			if err := os.WriteFile(name, tsWithPTS(f.pts[n]), 0o644); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(f.dir, "stream_0.m3u8"), []byte(b.String()), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

var timestampMap = regexp.MustCompile(`X-TIMESTAMP-MAP=MPEGTS:(\d+),LOCAL:(\d+):(\d+):(\d+)\.(\d+)`)
var cueTimes = regexp.MustCompile(`(\d+):(\d+):(\d+)\.(\d+) --> `)

func vttSeconds(m []string) float64 {
	h, _ := strconv.Atoi(m[0])
	mi, _ := strconv.Atoi(m[1])
	s, _ := strconv.Atoi(m[2])
	ms, _ := strconv.Atoi(m[3])
	return float64(h*3600+mi*60+s) + float64(ms)/1000
}

func TestSubtitleSegments(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	dir := t.TempDir()
	// The stream's first picture is two seconds short of the 90 kHz clock
	// wrapping round.
	first := int64(1<<33) - 180000
	f := &fakeHLS{t: t, dir: dir, pts: map[int]int64{}}
	for n := range 20 {
		f.pts[n] = (first + int64(n)*180000) & ptsMask
	}
	var script cc.Script
	script.Set(0, s(1e6), []cc.Cue{
		{Start: s(0.5), End: s(1.5), Text: "In the first"},
		{Start: s(3), End: s(5), Text: "Across two & more"},
		{Start: s(9), End: s(10), Text: "word by word", Words: []cc.Word{{Start: s(9), Text: "word"}, {Start: s(9.4), Text: "by"}, {Start: s(9.6), Text: "word"}}},
	})
	sub := newSubtitler(dir, &script)
	f.list(0, 2)
	if err := sub.update(); err != nil {
		t.Fatal(err)
	}
	f.list(0, 5)
	if err := sub.update(); err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile(filepath.Join(dir, subsPlaylist))
	if err != nil {
		t.Fatal(err)
	}
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n" +
		strings.Repeat("#EXTINF:2.000000,\nsub_%05d.vtt\n", 6)
	want = fmt.Sprintf(want, 0, 1, 2, 3, 4, 5)
	if string(list) != want {
		t.Errorf("subs.m3u8:\n%s\nwant\n%s", list, want)
	}
	read := func(n int) string {
		b, err := os.ReadFile(filepath.Join(dir, subName(n)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// Where a player puts a cue: its time, less the segment's local time,
	// plus how far the segment's picture is past the stream's first.
	initPTS := first
	placed := func(vtt string) []float64 {
		m := timestampMap.FindStringSubmatch(vtt)
		if m == nil {
			t.Fatalf("no timestamp map in\n%s", vtt)
		}
		mpegts, _ := strconv.ParseInt(m[1], 10, 64)
		local := vttSeconds(m[2:])
		offset := float64((mpegts-initPTS)&ptsMask) / 90000
		var out []float64
		for _, c := range cueTimes.FindAllStringSubmatch(vtt, -1) {
			out = append(out, vttSeconds(c[1:])-local+offset)
		}
		return out
	}
	// Each segment has the cues showing in it, and those starting within
	// ten seconds after, all placed where they belong.
	for n, want := range map[int][]float64{0: {0.5, 3, 9}, 1: {3, 9}, 2: {3, 9}, 3: {9}, 4: {9}, 5: {}} {
		got := placed(read(n))
		if fmt.Sprint(got) != fmt.Sprint(want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("segment %d places cues at %v, want %v:\n%s", n, got, want, read(n))
		}
	}
	// Segment 1 is past the clock's wrap: its picture time is small, and its
	// cues still land where they belong.
	if !strings.Contains(read(1), "MPEGTS:0,LOCAL:00:00:02.000") {
		t.Errorf("segment 1:\n%s", read(1))
	}
	if !strings.Contains(read(1), "Across two &amp; more") || !strings.Contains(read(4), "word <00:00:09.400>by <00:00:09.600>word") {
		t.Errorf("cue text:\n%s\n%s", read(1), read(4))
	}

	// The window slides: old segments go, after a couple more.
	f.list(4, 9)
	if err := sub.update(); err != nil {
		t.Fatal(err)
	}
	for n, there := range map[int]bool{0: false, 1: false, 2: true, 3: true, 9: true} {
		if _, err := os.Stat(filepath.Join(dir, subName(n))); (err == nil) != there {
			t.Errorf("segment %d there: %v, want %v", n, err == nil, there)
		}
	}
	if list, _ := os.ReadFile(filepath.Join(dir, subsPlaylist)); !strings.Contains(string(list), "#EXT-X-MEDIA-SEQUENCE:4\n#EXTINF:2.000000,\nsub_00004.vtt\n") {
		t.Errorf("subs.m3u8 after sliding:\n%s", list)
	}
}

func TestMasterPlaylistForSubtitles(t *testing.T) {
	in := tuner.Input{Video: "0:v:0", Audio: []tuner.AudioTrack{{Map: "0:a:0"}}, Captions: true, Subtitles: &cc.Script{}}
	m := masterPlaylist(in)
	for _, want := range []string{
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",DEFAULT=NO,AUTOSELECT=NO,FORCED=NO,URI="subs.m3u8"`,
		`SUBTITLES="subs",CLOSED-CAPTIONS=NONE`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("master lacks %s:\n%s", want, m)
		}
	}
	if strings.Contains(m, "TYPE=CLOSED-CAPTIONS") {
		t.Errorf("captions twice:\n%s", m)
	}
	if got := playlists(in); fmt.Sprint(got) != "[stream_0.m3u8 subs.m3u8]" {
		t.Errorf("playlists %v", got)
	}
	in.VOD = true
	if strings.Contains(masterPlaylist(in), "SUBTITLES") || len(playlists(in)) != 1 {
		t.Error("a recording has no subtitle rendition")
	}
}
