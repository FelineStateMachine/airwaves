package vchan

import (
	"bytes"
	"context"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pcm makes secs of 48 kHz stereo s16le from f(t) for each channel.
func pcm(secs float64, f func(t float64) (l, r float64)) []byte {
	n := int(secs * sampleRate)
	b := make([]byte, 4*n)
	for i := range n {
		l, r := f(float64(i) / sampleRate)
		putSample(b[4*i:], l)
		putSample(b[4*i+2:], r)
	}
	return b
}

// sine is a 1 kHz tone peaking at dBFS, on both channels or the left.
func sine(dbfs float64, both bool) func(float64) (float64, float64) {
	a := math.Pow(10, dbfs/20)
	return func(t float64) (float64, float64) {
		x := a * math.Sin(2*math.Pi*1000*t)
		if both {
			return x, x
		}
		return x, 0
	}
}

// noise is noise at about dbfs RMS, the same each run.
func noise(dbfs float64, seed uint64) func(float64) (float64, float64) {
	rng := rand.New(rand.NewPCG(seed, 1))
	a := math.Pow(10, dbfs/20) * math.Sqrt(3)
	return func(float64) (float64, float64) {
		return a * (2*rng.Float64() - 1), a * (2*rng.Float64() - 1)
	}
}

// measured is the gated loudness of raw sound, and its loudest sample.
func measured(b []byte) (lufs, peak float64) {
	var m meter
	for i := 0; i+4 <= len(b); i += 4 {
		l := float64(int16(uint16(b[i])|uint16(b[i+1])<<8)) / 32768
		r := float64(int16(uint16(b[i+2])|uint16(b[i+3])<<8)) / 32768
		m.add(l, r)
		peak = max(peak, math.Abs(l), math.Abs(r))
	}
	lufs, _ = m.integrated()
	return lufs, peak
}

func TestMeterReference(t *testing.T) {
	near := func(name string, got, want, tol float64) {
		t.Helper()
		if math.IsNaN(got) || math.Abs(got-want) > tol {
			t.Errorf("%s: %.2f LUFS, want %.2f", name, got, want)
		}
	}
	// BS.1770: a 1 kHz sine at 0 dBFS in one channel reads -3.01 LKFS.
	l, _ := measured(pcm(10, sine(-20, false)))
	near("-20 dBFS sine, left only", l, -23.01, 0.1)
	l, _ = measured(pcm(10, sine(-20, true)))
	near("-20 dBFS sine, both", l, -20, 0.1)
	// Silence is under the absolute gate, and sound 20 LU down is under
	// the relative one.
	l, _ = measured(append(pcm(10, sine(-20, true)), pcm(10, func(float64) (float64, float64) { return 0, 0 })...))
	near("half silence", l, -20, 0.1)
	l, _ = measured(append(pcm(10, sine(-20, true)), pcm(10, sine(-40, true))...))
	near("half 20 LU down", l, -20, 0.1)
	l, _ = measured(append(pcm(10, sine(-20, true)), pcm(10, sine(-26, true))...))
	near("half 6 LU down", l, -22.04, 0.1)
	if l, _ := measured(pcm(2, func(float64) (float64, float64) { return 0, 0 })); !math.IsNaN(l) {
		t.Errorf("silence: %v, want none", l)
	}
}

// TestMeterMatchesFFmpeg meters pink noise and compares with ffmpeg's
// ebur128.
func TestMeterMatchesFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	src := "anoisesrc=color=pink:amplitude=0.3:d=12:sample_rate=48000"
	raw, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", src,
		"-af", "pan=stereo|c0=c0|c1=c0", "-f", "s16le", "-ar", "48000", "-ac", "2", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(ffmpeg, "-hide_banner", "-nostats", "-f", "lavfi", "-i", src,
		"-af", "pan=stereo|c0=c0|c1=c0,ebur128", "-f", "null", "-").CombinedOutput()
	i := strings.LastIndex(string(out), "I:")
	if i < 0 {
		t.Fatalf("no ebur128 summary: %s", out)
	}
	want, _ := strconv.ParseFloat(strings.Fields(string(out[i+2:]))[0], 64)
	if got, _ := measured(raw); math.Abs(got-want) > 0.2 {
		t.Errorf("meter %.2f LUFS, ffmpeg %.2f", got, want)
	}
}

func TestGainFor(t *testing.T) {
	for _, c := range []struct{ lufs, target, want float64 }{
		{-14, -24, -10}, {-24, -24, 0}, {-30, -24, 6}, {-45, -24, 12}, {2, -24, -20}, {-60, -24, 0}, {-14, 0, 0},
		{math.NaN(), -24, 0},
	} {
		if got := gainFor(c.lufs, c.target); got != c.want {
			t.Errorf("gainFor(%v, %v) = %v, want %v", c.lufs, c.target, got, c.want)
		}
	}
}

// levelAll levels sound through a new leveler in 64 KB chunks, as the
// feed does, and returns it with the gains it went through, in dB, each
// 100 ms.
func levelAll(lv *itemLevel, b []byte) ([]byte, []float64) {
	l := newLeveler(lv)
	out := bytes.Clone(b)
	var gains []float64
	for i := 0; i < len(out); i += relayChunk {
		chunk := out[i:min(i+relayChunk, len(out))]
		for j := 0; j < len(chunk); j += subBlock * 4 {
			l.process(chunk[j:min(j+subBlock*4, len(chunk))])
			gains = append(gains, l.gain)
		}
	}
	l.finish()
	return out, gains
}

// TestLevelerConverges: two videos 12 dB apart come out at the target
// once a few seconds have been measured, without overshooting or
// clipping.
func TestLevelerConverges(t *testing.T) {
	for _, c := range []struct {
		name string
		src  func(float64) (float64, float64)
	}{
		{"loud", noise(-20, 1)},  // about -14 LUFS, as YouTube uploads often are
		{"quiet", noise(-32, 2)}, // about -26
	} {
		in := pcm(40, c.src)
		before, _ := measured(in)
		out, gains := levelAll(&itemLevel{Target: -24}, in)
		after, peak := measured(out[len(out)-20*sampleRate*4:]) // the last 20 s
		if math.Abs(after+24) > 1.5 {
			t.Errorf("%s (%.1f LUFS): levelled to %.1f LUFS", c.name, before, after)
		}
		want := gainFor(before, -24)
		for i := 1; i < len(gains); i++ {
			if math.Abs(gains[i]-gains[i-1]) > maxSlew+1e-9 {
				t.Fatalf("%s: gain jumped from %.2f to %.2f dB", c.name, gains[i-1], gains[i])
			}
			if (want > 0 && gains[i] > want+0.5) || (want < 0 && gains[i] < want-0.5) {
				t.Fatalf("%s: gain %.2f dB overshoots %.2f", c.name, gains[i], want)
			}
		}
		if peak > peakCeiling+1e-3 {
			t.Errorf("%s: peak %.3f over the ceiling", c.name, peak)
		}
		t.Logf("%s: %.1f LUFS in, %.1f out; gain %.1f dB after 5 s, %.1f after 10, %.1f at the end", c.name, before, after,
			gains[50], gains[100], gains[len(gains)-1])
	}
}

// TestLevelerGuardsPeaks: a quiet video with loud clicks is raised, and
// the clicks held under -1 dBFS.
func TestLevelerGuardsPeaks(t *testing.T) {
	quiet := noise(-42, 3)
	in := pcm(30, func(t float64) (float64, float64) {
		l, r := quiet(t)
		if math.Mod(t, 3) < 0.0001 { // a click every three seconds
			return 0.5, -0.5
		}
		return l, r
	})
	out, gains := levelAll(&itemLevel{Target: -24}, in)
	if _, peak := measured(out); peak > peakCeiling+1e-3 {
		t.Errorf("peak %.3f over the ceiling", peak)
	}
	if g := gains[len(gains)-1]; g < 8 {
		t.Errorf("gain %.1f dB; the quiet video should be raised", g)
	}
}

func TestLevelerRemembers(t *testing.T) {
	mem := &memoryLevels{}
	lv := &itemLevel{Target: -24, Key: "v", Memory: mem}
	in := pcm(30, sine(-14, true))
	levelAll(lv, in)
	l, ok := mem.recall("v")
	if !ok || math.Abs(l.LUFS+14) > 0.1 || l.Seconds < 29 {
		t.Fatalf("remembered %+v, %v", l, ok)
	}
	// The next airing starts at the remembered gain.
	out, gains := levelAll(lv, in[:3*sampleRate*4])
	if after, _ := measured(out); math.Abs(gains[0]+10) > 0.05 || math.Abs(after+24) > 0.3 {
		t.Errorf("repeat starts at %.1f dB, plays at %.1f LUFS", gains[0], after)
	}
	if l2, _ := mem.recall("v"); l2 != l {
		t.Errorf("three seconds changed what was remembered: %+v", l2)
	}
	levelAll(lv, pcm(30, sine(-20, true)))
	if l3, _ := mem.recall("v"); math.Abs(l3.LUFS+16.2) > 0.2 || l3.Seconds < 59 {
		t.Errorf("a second airing at -20: remembered %+v, want energies averaged", l3)
	}
	// Unmeasured YouTube videos start 10 dB down.
	if _, gains := levelAll(&itemLevel{Target: -24, Assumed: ytAssumed}, in[:sampleRate*4]); math.Abs(gains[0]+10) > 0.05 {
		t.Errorf("assumed: %.1f dB", gains[0])
	}
}

func TestLoudnessFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), loudnessFileName)
	f := &loudnessFile{}
	f.use(path, []string{"a", "b"})
	if _, ok := f.recall("a"); ok {
		t.Fatal("found loudness in a new file")
	}
	f.remember("a", loudness{-20, 30})
	f.use(path, []string{"b"})
	f.remember("b", loudness{-30, 60})
	again := &loudnessFile{}
	again.use(path, []string{"a", "b"})
	if _, ok := again.recall("a"); ok {
		t.Error("a key no longer in use was saved")
	}
	if l, ok := again.recall("b"); !ok || l != (loudness{-30, 60}) || again.count([]string{"a", "b"}) != 1 {
		t.Errorf("after a reload: %+v %v", l, ok)
	}
}

func BenchmarkLeveler(b *testing.B) {
	chunk := pcm(float64(relayChunk/4)/sampleRate, noise(-20, 4))
	l := newLeveler(&itemLevel{Target: -24})
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	for b.Loop() {
		l.process(chunk)
	}
}

// TestLevelingInTheStream plays two clips 12 dB apart through the engine,
// twice: the first time each is metered and remembered, so the second
// time both start at the target, and the stream keeps time across them.
func TestLevelingInTheStream(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	dir := t.TempDir()
	mem := &memoryLevels{}
	var items []item
	for _, c := range []struct{ name, amp string }{{"loud.mp4", "0.5"}, {"quiet.mp4", "0.125"}} {
		path := filepath.Join(dir, c.name)
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=5", "-f", "lavfi", "-i", "anoisesrc=color=pink:amplitude="+c.amp+":d=5",
			"-c:v", "libx264", "-c:a", "aac", "-b:a", "192k", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("make %s: %v: %s", c.name, err, out)
		}
		items = append(items, item{Path: path, Frames: 5 * loopFPS, Audio: true, Title: c.name,
			Level: &itemLevel{Target: -24, Key: c.name, Memory: mem}})
	}
	stream := func(name string) string {
		n := 0
		cue := func(time.Time, bool) (item, int64, error) {
			if n == len(items) {
				return item{Slate: true, Frames: 10 * loopFPS}, 0, nil
			}
			n++
			return items[n-1], 0, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10500*time.Millisecond)
		defer cancel()
		var out bytes.Buffer
		if err := playCues(ctx, &out, ffmpeg, "test", cue); err != nil {
			t.Fatal(err)
		}
		ts := filepath.Join(dir, name)
		if err := os.WriteFile(ts, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		probe, _ := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", ts).Output()
		if d, _ := strconv.ParseFloat(strings.TrimSpace(string(probe)), 64); d < 9 || d > 11.5 {
			t.Errorf("%s: played %.1fs in 10.5s", name, d)
		}
		return ts
	}
	segment := func(ts string, from float64) float64 {
		raw, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-ss", strconv.FormatFloat(from, 'f', 1, 64), "-t", "3.5",
			"-i", ts, "-vn", "-f", "s16le", "-ar", "48000", "-ac", "2", "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		l, _ := measured(raw)
		return l
	}
	first := stream("first.ts")
	for _, it := range items {
		if l, ok := mem.recall(it.Level.Key); !ok || l.Seconds < settle {
			t.Fatalf("%s not remembered: %+v", it.Title, l)
		}
	}
	loud, _ := mem.recall("loud.mp4")
	quiet, _ := mem.recall("quiet.mp4")
	if d := loud.LUFS - quiet.LUFS; d < 11 || d > 13 {
		t.Errorf("remembered %.1f and %.1f LUFS, want 12 apart", loud.LUFS, quiet.LUFS)
	}
	second := stream("second.ts")
	a, b := segment(second, 1), segment(second, 6)
	t.Logf("first airing: %.1f and %.1f LUFS; second: %.1f and %.1f", segment(first, 1), segment(first, 6), a, b)
	if math.Abs(a-b) > 1.5 || math.Abs(a+24) > 1.5 || math.Abs(b+24) > 1.5 {
		t.Errorf("second airing at %.1f and %.1f LUFS; want both about -24", a, b)
	}
}

// TestWeatherMusicLeveled: the music is leveled as it plays, starting
// from what it measured last time, and remembered when the viewer leaves.
func TestWeatherMusicLeveled(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	track := filepath.Join(dir, "a.mp3")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "anoisesrc=color=pink:amplitude=0.5:d=3", track).CombinedOutput(); err != nil {
		t.Skipf("no MP3 encoder: %v: %s", err, out)
	}
	wx := &Weather{LocalMusic: dir, FFmpeg: ffmpeg, Loudness: -24}
	tracks := wx.Tracks(context.Background())
	key := strings.Join(tracks, "\x00")
	wx.music.remember(key, loudness{LUFS: -14, Seconds: 600})
	audio, cleanup, err := wx.audioInput(tracks)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	r, err := wx.levelMusic(ctx, audio, key)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 5*sampleRate*4) // five seconds, the track looping
	if _, err := io.ReadFull(r, raw); err != nil {
		t.Fatal(err)
	}
	cancel()
	r.Close()
	in := pcmOf(t, ffmpeg, track)
	before, _ := measured(in)
	after, _ := measured(raw)
	if math.Abs(after-(before-10)) > 0.5 {
		t.Errorf("music at %.1f LUFS played at %.1f; want 10 dB down, from what was remembered", before, after)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if l, _ := wx.music.recall(key); l.Seconds > 600 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the music's loudness wasn't remembered")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// pcmOf decodes a file's sound to 48 kHz stereo s16le.
func pcmOf(t *testing.T, ffmpeg, path string) []byte {
	t.Helper()
	raw, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-f", "s16le", "-ar", "48000", "-ac", "2", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestChannelsLevelOnlyWhatStreams: with leveling on, a folder's videos
// with sound carry how to level them, and nothing is measured until they
// play: the counts only grow as airings are remembered.
func TestChannelsLevelOnlyWhatStreams(t *testing.T) {
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
		"a.mp4": {"-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=2", "-f", "lavfi", "-i", "sine=d=2", "-c:v", "libx264", "-c:a", "aac"},
		"b.mp4": {"-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=2", "-c:v", "libx264"},
	} {
		if out, err := exec.Command(ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error"}, args...), filepath.Join(dir, name))...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	f := &Folder{Num: "1.9", Dir: dir, FFmpeg: ffmpeg, FFprobe: ffprobe, Loudness: -24}
	items := f.list()
	if items[0].Level == nil || !strings.HasPrefix(items[0].Level.Key, "a.mp4|") || items[0].Level.Target != -24 || items[1].Level != nil {
		t.Fatalf("levels: %+v, %+v", items[0].Level, items[1].Level)
	}
	if m, n := f.Leveled(); m != 0 || n != 1 {
		t.Errorf("leveled %d of %d before anything played", m, n)
	}
	if _, err := os.Stat(filepath.Join(dir, loudnessFileName)); !os.IsNotExist(err) {
		t.Error("measured before anything played")
	}
	items[0].Level.Memory.remember(items[0].Level.Key, loudness{LUFS: -20, Seconds: 30})
	if m, n := f.Leveled(); m != 1 || n != 1 {
		t.Errorf("leveled %d of %d after an airing", m, n)
	}
	if _, err := os.Stat(filepath.Join(dir, loudnessFileName)); err != nil {
		t.Errorf("not saved: %v", err)
	}
	if off := (&Folder{Num: "1.9", Dir: dir, FFmpeg: ffmpeg, FFprobe: ffprobe}).list(); off[0].Level != nil {
		t.Error("leveling off still levels")
	}
	y := &YouTube{Loudness: -24}
	if lv := y.level("abc"); lv == nil || lv.Assumed != ytAssumed || lv.Key != "abc" {
		t.Errorf("YouTube level: %+v", lv)
	}
	if (&YouTube{}).level("abc") != nil {
		t.Error("YouTube leveling off still levels")
	}
}
