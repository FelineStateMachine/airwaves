package cc

import (
	"bytes"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCaptionsReachFFmpeg captions a real H.264 stream from libx264 (with
// B-frames, as the channel encoder makes) and reads the captions back
// with ffmpeg, before and after the re-encode the Airwaves app's streams
// go through.
func TestCaptionsReachFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(ffmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "8", "-c:v", "libx264", "-preset", "veryfast", "-g", "60", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-f", "mpegts", "in.ts")
	in, err := os.ReadFile(filepath.Join(dir, "in.ts"))
	if err != nil {
		t.Fatal(err)
	}
	cues := []Cue{
		{Start: 1 * time.Second, End: 2500 * time.Millisecond, Text: "Hello there."},
		{Start: 3 * time.Second, End: 4500 * time.Millisecond, Text: "A second caption\nover two lines"},
		{Start: 4500 * time.Millisecond, End: 6 * time.Second, Text: "♪ Straight after ♪"},
	}
	track := NewTrack(cues, 30)
	var out bytes.Buffer
	w := NewWriter(&out, 30, track.Pair)
	rng := rand.New(rand.NewPCG(1, 2))
	for p := in; len(p) > 0; {
		n := min(len(p), 1+rng.IntN(1000))
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "out.ts"), out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// The app's streams are re-encoded, carrying captions over.
	run("-i", "out.ts", "-map", "0:v", "-c:v", "libx264", "-preset", "veryfast", "-a53cc", "1", "-f", "mpegts", "again.ts")

	for _, name := range []string{"out.ts", "again.ts"} {
		cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
			"-f", "lavfi", "-i", "movie="+name+"[out0+subcc]", "-map", "0:s", "-f", "srt", "-")
		cmd.Dir = dir
		srt, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: extracting captions: %v", name, err)
		}
		got, err := Parse(srt)
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, srt)
		}
		t.Logf("%s:\n%s", name, srt)
		if len(got) != len(cues) {
			t.Fatalf("%s: %d captions, want %d:\n%s", name, len(got), len(cues), srt)
		}
		// ffmpeg times captions from the stream's first timestamp.
		offset := got[0].Start - cues[0].Start
		for i, c := range cues {
			g := got[i]
			if norm(g.Text) != norm(c.Text) {
				t.Errorf("%s: caption %d = %q, want %q", name, i, g.Text, c.Text)
			}
			if d := g.Start - offset - c.Start; d < -70*time.Millisecond || d > 70*time.Millisecond {
				t.Errorf("%s: caption %d starts at %v, want %v", name, i, g.Start-offset, c.Start)
			}
			if d := g.End - offset - c.End; d < -70*time.Millisecond || d > 70*time.Millisecond {
				t.Errorf("%s: caption %d ends at %v, want %v", name, i, g.End-offset, c.End)
			}
		}
		if offset < 0 || offset > 2*time.Second {
			t.Errorf("%s: captions offset by %v", name, offset)
		}
	}
	// The stream still decodes cleanly.
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-i", "out.ts", "-f", "null", "-")
	cmd.Dir = dir
	if msg, err := cmd.CombinedOutput(); err != nil || len(msg) > 0 {
		t.Errorf("decoding the captioned stream: %v\n%s", err, msg)
	}
}

// norm compares caption text the way ffmpeg gives it back: lines as
// spaces, and spaces collapsed.
func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestRollUpReachesFFmpeg sends YouTube's word-timed captions rolling up,
// and reads them back with ffmpeg: every word, in order.
func TestRollUpReachesFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	if testing.Short() {
		t.Skip("short")
	}
	raw, err := os.ReadFile("testdata/asr.json3")
	if err != nil {
		t.Fatal(err)
	}
	cues, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var spoken []string
	for _, c := range cues {
		if c.End > 12*time.Second {
			break
		}
		for _, w := range c.Words {
			spoken = append(spoken, w.Text)
		}
	}
	dir := t.TempDir()
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30", "-t", "16", "-c:v", "libx264", "-preset", "veryfast",
		"-pix_fmt", "yuv420p", "-f", "mpegts", "in.ts")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	in, err := os.ReadFile(filepath.Join(dir, "in.ts"))
	if err != nil {
		t.Fatal(err)
	}
	track := NewTrack(cues, 30)
	var out bytes.Buffer
	w := NewWriter(&out, 30, track.Pair)
	w.Write(in)
	w.Flush()
	if err := os.WriteFile(filepath.Join(dir, "out.ts"), out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	extract := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "movie=out.ts[out0+subcc]", "-map", "0:s", "-f", "srt", "-")
	extract.Dir = dir
	srt, err := extract.Output()
	if err != nil {
		t.Fatalf("extracting captions: %v", err)
	}
	got, err := Parse(srt)
	if err != nil {
		t.Fatalf("%v:\n%s", err, srt)
	}
	t.Logf("%d captions, from %q", len(got), got[0].Text)
	// Each roll shows what's on screen; the words come through in order.
	var seen []string
	for _, c := range got {
		// ffmpeg shows 608's apostrophe as a closing quote.
		text := strings.NewReplacer(`\h`, " ", "\u2019", "'").Replace(markup.ReplaceAllString(c.Text, ""))
		for _, line := range strings.Split(text, "\n") {
			if line = norm(line); line != "" && (len(seen) == 0 || seen[len(seen)-1] != line) && !strings.HasSuffix(strings.Join(seen, " "), line) {
				seen = append(seen, line)
			}
		}
	}
	all := " " + strings.Join(seen, " ") + " "
	pos := 0
	for _, word := range spoken {
		i := strings.Index(all[pos:], " "+fold(word))
		if i < 0 {
			t.Fatalf("%q missing after %q", word, all[max(0, pos-60):pos])
		}
		pos += i + 1
	}
}
