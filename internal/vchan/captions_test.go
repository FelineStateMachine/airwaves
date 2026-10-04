package vchan

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/jellyfin"
	"airwaves/internal/jellyfin/jellyfintest"
	"airwaves/internal/stream"
	"airwaves/internal/tuner"
)

func srtCaptions(srt string) *captions {
	return newCaptions("test", func(context.Context) ([]byte, error) { return []byte(srt), nil })
}

// TestStreamCarriesCaptions plays a video with captions from a second in,
// as a viewer tuning in late would, and reads the captions back out of the
// stream with ffmpeg.
func TestStreamCarriesCaptions(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "v.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=25:d=8",
		"-f", "lavfi", "-i", "sine=f=440:d=8", "-c:v", "libx264", "-c:a", "aac", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make video: %v: %s", err, out)
	}
	it := item{Path: video, Frames: 8 * loopFPS, Audio: true, Title: "Captioned", Captions: srtCaptions(
		"1\n00:00:01,500 --> 00:00:02,500\nAlready going\n\n" +
			"2\n00:00:03,500 --> 00:00:05,000\nRight on time\n")}
	n := 0
	cue := func(time.Time, bool) (item, int64, error) {
		if n++; n == 1 {
			return it, loopFPS, nil // a second in
		}
		return item{Slate: true, Frames: 10 * loopFPS}, 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5500*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	if err := playCues(ctx, &out, ffmpeg, "test", cue); err != nil {
		t.Fatal(err)
	}
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
	got, err := cc.Parse(srt)
	if err != nil {
		t.Fatalf("%v:\n%s", err, srt)
	}
	t.Logf("captions:\n%s", srt)
	if len(got) != 2 || got[0].Text != "Already going" || got[1].Text != "Right on time" {
		t.Fatalf("captions %q", got)
	}
	// Times count from the stream's start, a second into the video.
	if d := got[1].Start - 2500*time.Millisecond; d < -70*time.Millisecond || d > 70*time.Millisecond {
		t.Errorf("second caption at %v, want 2.5s", got[1].Start)
	}
	if got[0].Start > time.Second {
		t.Errorf("first caption at %v, want by 1s", got[0].Start)
	}
}

func TestFeedCountsWholeFrames(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	f := newFeed(w, 4, 10, nil)
	got := make(chan []byte)
	go func() {
		b, _ := readAll(r)
		got <- b
	}()
	for _, data := range []string{"0123456789abcdefghij", "klmno", ""} {
		src, done, err := f.source(nil)
		if err != nil {
			t.Fatal(err)
		}
		src.WriteString(data)
		src.Close()
		if n, want := done(), int64((len(data)+9)/10); n != want {
			t.Errorf("%q: %d units, want %d", data, n, want)
		}
	}
	if f.units != 3 || f.fed() != 3 {
		t.Errorf("units %d, fed %d; want 3", f.units, f.fed())
	}
	f.close()
	if b := <-got; string(b) != "0123456789abcdefghijklmno\x00\x00\x00\x00\x00" {
		t.Errorf("fed %q", b)
	}
	if pad := filler(frameBytes, loopWidth*loopHeight-1); len(pad) != frameBytes-loopWidth*loopHeight+1 || pad[0] != 0x10 || pad[1] != 0x80 {
		t.Errorf("frame filler starts % x", pad[:2])
	}
}

func readAll(f *os.File) ([]byte, error) {
	defer f.Close()
	var b bytes.Buffer
	_, err := b.ReadFrom(f)
	return b.Bytes(), err
}

func TestTimeline(t *testing.T) {
	var tl timeline
	if b1, b2 := tl.pair(0); b1 != cc.Padding || b2 != cc.Padding {
		t.Error("an empty timeline sent something")
	}
	a := tl.begin(0)
	tl.set(a, []cc.Cue{{Start: time.Second, End: 2 * time.Second, Text: "Hi"}})
	b := tl.begin(100)
	track := cc.NewTrack([]cc.Cue{{Start: time.Second, End: 2 * time.Second, Text: "Hi"}}, loopFPS)
	empty := cc.NewTrack(nil, loopFPS)
	for f := range int64(200) {
		var w1, w2 byte
		if f < 100 {
			w1, w2 = track.Pair(f)
		} else {
			w1, w2 = empty.Pair(f - 100)
		}
		if g1, g2 := tl.pair(f); g1 != w1 || g2 != w2 {
			t.Fatalf("frame %d: % x, want % x", f, []byte{g1, g2}, []byte{w1, w2})
		}
	}
	// Spans the writer has passed go.
	tl.begin(300)
	if len(tl.spans) != 2 || tl.spans[0] != b {
		t.Errorf("%d spans kept", len(tl.spans))
	}
}

func TestBurnInGraph(t *testing.T) {
	args := strings.Join(decodeInputs([]string{"-re", "-i", "in.mp4"}, "0:a:1", 300, "/tmp/subs.srt"), " ")
	if !strings.Contains(args, "setsar=1,subtitles=filename=/tmp/subs.srt:force_style='") || !strings.Contains(args, "[0:a:1]aresample") {
		t.Errorf("args %s", args)
	}
	if plain := strings.Join(decodeInputs([]string{"-i", "in.mp4"}, "", 300, ""), " "); strings.Contains(plain, "subtitles") {
		t.Errorf("subtitles without a file: %s", plain)
	}
}

// TestSubtitleChoosesBurnIn checks which viewers get subtitles burned in:
// HDHomeRun clients, for sound not in English, when ffmpeg can.
func TestSubtitleChoosesBurnIn(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	srt := "1\n00:00:05,000 --> 00:00:06,000\nBonjour\n"
	for _, c := range []struct {
		lang string
		app  bool
		burn bool
	}{
		{"fre", false, true},
		{"fre", true, false},
		{"eng", false, false},
		{"", false, false},
	} {
		out := &output{captions: &timeline{}, app: c.app, video: &feed{unit: frameBytes}}
		it := item{AudioLang: c.lang, Captions: srtCaptions(srt)}
		s := out.captions.begin(0)
		path := subtitle(context.Background(), ffmpeg, it, 2*loopFPS, s, out)
		burnt := path != ""
		if burnt {
			b, _ := os.ReadFile(path)
			os.Remove(path)
			if !strings.Contains(string(b), "00:00:03,000 --> 00:00:04,000\nBonjour") {
				t.Errorf("%s: subtitles not shifted to the start:\n%s", c.lang, b)
			}
		}
		if want := c.burn && canBurn(ffmpeg); burnt != want {
			t.Errorf("%s, app %v: burnt in %v, want %v", c.lang, c.app, burnt, want)
		}
		// Captions not burnt in are closed captions.
		var sent bool
		for f := range int64(200) {
			if b1, _ := s.track.Pair(f); b1 != cc.Padding && f > 2 {
				sent = true
			}
		}
		if sent == burnt {
			t.Errorf("%s, app %v: closed captions %v with burnt in %v", c.lang, c.app, sent, burnt)
		}
	}
}

func TestFolderPrefersEnglishAndFindsSubtitles(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	dir := t.TempDir()
	mk := func(name string, args ...string) {
		cmd := exec.Command(ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error"}, args...), filepath.Join(dir, name))...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("make %s: %v: %s", name, err, out)
		}
	}
	mk("anime.mkv", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=2", "-f", "lavfi", "-i", "sine=d=2", "-f", "lavfi", "-i", "sine=d=2",
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-c:a", "aac",
		"-metadata:s:a:0", "language=jpn", "-disposition:a:0", "default", "-metadata:s:a:1", "language=eng", "-disposition:a:1", "0")
	mk("movie.mp4", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=2", "-f", "lavfi", "-i", "sine=d=2",
		"-c:v", "libx264", "-c:a", "aac", "-metadata:s:a:0", "language=fre")
	mk("quiet.mp4", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=25:d=2", "-c:v", "libx264")
	writeFile(t, filepath.Join(dir, "movie.en.srt"), "1\n00:00:00,500 --> 00:00:01,500\nHello\n")
	writeFile(t, filepath.Join(dir, "quiet.srt"), "1\n00:00:00,500 --> 00:00:01,500\n[silence]\n")

	f := &Folder{Num: "1.9", Title: "Test", Dir: dir, FFmpeg: ffmpeg, FFprobe: ffprobe}
	items := f.list()
	if len(items) != 3 {
		t.Fatalf("%d items", len(items))
	}
	byName := map[string]item{}
	for _, it := range items {
		byName[filepath.Base(it.Path)] = it
	}
	if it := byName["anime.mkv"]; it.AudioStream != "0:a:1" || it.AudioLang != "eng" || it.Captions != nil {
		t.Errorf("anime: sound %q (%q), captions %v", it.AudioStream, it.AudioLang, it.Captions != nil)
	}
	if it := byName["movie.mp4"]; it.AudioStream != "0:a:0" || it.AudioLang != "fre" || it.Captions == nil {
		t.Errorf("movie: sound %q (%q), captions %v", it.AudioStream, it.AudioLang, it.Captions != nil)
	}
	if it := byName["quiet.mp4"]; it.Audio || it.Captions == nil {
		t.Errorf("quiet: audio %v, captions %v", it.Audio, it.Captions != nil)
	}
	cues, err := byName["movie.mp4"].Captions.get(context.Background())
	if err != nil || len(cues) != 1 || cues[0].Text != "Hello" {
		t.Errorf("movie captions %q, %v", cues, err)
	}
	progs := f.Programs(time.Now(), time.Now().Add(time.Minute))
	var flagged []string
	for _, p := range progs {
		if p.Captions {
			flagged = append(flagged, p.Title+" "+p.AudioLang)
		}
	}
	if !slices.Contains(flagged, "movie fre") || slices.Contains(flagged, "anime eng") {
		t.Errorf("programs with captions: %q", flagged)
	}
	// Rescanning keeps the captions fetched.
	f.scanned = time.Time{}
	again := f.list()
	for _, it := range again {
		if filepath.Base(it.Path) == "movie.mp4" && it.Captions != byName["movie.mp4"].Captions {
			t.Error("captions fetched again after a rescan")
		}
	}
}

func TestJellyfinItemSoundAndCaptions(t *testing.T) {
	srv := jellyfintest.New(t, "subs", "pw",
		jellyfintest.Item{ID: "lib", Type: "CollectionFolder", Name: "Movies"},
		jellyfintest.Item{ID: "akira", Type: "Movie", Name: "Akira", Parent: "lib", Runtime: 124 * time.Minute, Streams: []jellyfintest.Stream{
			{Type: "Audio", Language: "jpn", Codec: "flac", Default: true},
			{Type: "Subtitle", Language: "eng", Codec: "subrip", Forced: true, SRT: "1\n00:00:01,000 --> 00:00:02,000\nSigns\n"},
		}},
		jellyfintest.Item{ID: "dub", Type: "Movie", Name: "Dubbed", Parent: "lib", Runtime: 90 * time.Minute, Streams: []jellyfintest.Stream{
			{Type: "Audio", Language: "jpn", Codec: "flac", Default: true},
			{Type: "Audio", Language: "eng", Codec: "aac"},
			{Type: "Subtitle", Language: "eng", Codec: "subrip", Forced: true, SRT: "forced"},
		}},
	)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "subs", Password: "pw"}, "dev", nil)
	vs, err := c.Videos(context.Background(), []jellyfin.Item{{ID: "akira", Type: "Movie"}, {ID: "dub", Type: "Movie"}})
	if err != nil || len(vs) != 2 {
		t.Fatalf("videos %v, %v", vs, err)
	}
	akira, _ := jellyfinItem(c, vs[0], 0)
	if akira.AudioLang != "jpn" || akira.AudioStream != "" || akira.Captions == nil {
		t.Fatalf("akira: sound %q %q, captions %v", akira.AudioStream, akira.AudioLang, akira.Captions != nil)
	}
	cues, err := akira.Captions.get(context.Background())
	if err != nil || len(cues) != 1 || cues[0].Text != "Signs" {
		t.Errorf("akira captions %q, %v", cues, err)
	}
	// With English sound, forced subtitles aren't captions.
	dub, _ := jellyfinItem(c, vs[1], 0)
	if dub.AudioLang != "eng" || dub.AudioStream != "0:a:1" || dub.Captions != nil {
		t.Errorf("dub: sound %q %q, captions %v", dub.AudioStream, dub.AudioLang, dub.Captions != nil)
	}
	// A transcode picks the sound on the server.
	if dub, _ := jellyfinItem(c, vs[1], 4); dub.AudioStream != "" || !strings.Contains(strings.Join(decodeArgs(dub, 0), " "), "audioStreamIndex=2") {
		t.Errorf("transcode: %q", decodeArgs(dub, 0))
	}
}

func TestYouTubeCaptions(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manual.vtt":
			fmt.Fprint(w, "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nBy hand\n")
		case "/manual.json3":
			fmt.Fprint(w, `{"events":[{"tStartMs":1000,"dDurationMs":1000,"segs":[{"utf8":"By hand"}]}]}`)
		case "/asr.json3":
			fmt.Fprint(w, `{"events":[{"tStartMs":1000,"dDurationMs":2000,"segs":[{"utf8":"all"},{"utf8":" right","tOffsetMs":400}]}]}`)
		case "/auto.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:600\n#EXTINF:600,\n/piece?n=1\n#EXTINF:600,\n"+srv.URL+"/piece?n=2\n#EXT-X-ENDLIST\n")
		case "/piece":
			n := r.URL.Query().Get("n")
			fmt.Fprintf(w, "WEBVTT\nKind: captions\nLanguage: en\n\n00:%s0:00.000 --> 00:%s0:02.000\nPiece %s\n", n, n, n)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	info := ytInfo{
		Subtitles: map[string][]ytSubtitle{
			"de":    {{Ext: "vtt", URL: srv.URL + "/de.vtt"}},
			"en-GB": {{Ext: "srv3", URL: srv.URL + "/x"}, {Ext: "vtt", URL: srv.URL + "/manual.vtt"}, {Ext: "json3", URL: srv.URL + "/manual.json3"}},
		},
		AutoCaptions: map[string][]ytSubtitle{"en": {{Ext: "vtt", URL: srv.URL + "/auto.m3u8", Protocol: "m3u8_native"}}},
	}
	sub, ok := info.captions()
	if !ok || sub.URL != srv.URL+"/manual.json3" {
		t.Fatalf("picked %+v", sub)
	}
	raw, err := ytCaptions(context.Background(), srv.Client(), sub)
	if cues, _ := cc.Parse(raw); err != nil || len(cues) != 1 || cues[0].Text != "By hand" {
		t.Errorf("manual captions %q, %v", raw, err)
	}
	info.Subtitles = nil
	sub, ok = info.captions()
	if !ok || sub.Protocol != "m3u8_native" {
		t.Fatalf("picked %+v", sub)
	}
	raw, err = ytCaptions(context.Background(), srv.Client(), sub)
	cues, _ := cc.Parse(raw)
	if err != nil || len(cues) != 2 || cues[0].Text != "Piece 1" || cues[1].Start != 20*time.Minute {
		t.Errorf("automatic captions %q, %v", cues, err)
	}
	// Speech recognition's, as json3, time each word.
	info.AutoCaptions["en-orig"] = []ytSubtitle{{Ext: "vtt", URL: srv.URL + "/x"}, {Ext: "json3", URL: srv.URL + "/asr.json3"}}
	sub, _ = info.captions()
	raw, err = ytCaptions(context.Background(), srv.Client(), sub)
	cues, _ = cc.Parse(raw)
	if err != nil || len(cues) != 1 || cues[0].Text != "all right" || len(cues[0].Words) != 2 || cues[0].Words[1].Start != 1400*time.Millisecond {
		t.Errorf("speech recognition's captions %+v, %v", cues, err)
	}
	if _, ok := (ytInfo{AutoCaptions: map[string][]ytSubtitle{"fr": {{Ext: "vtt", URL: "x"}}}}).captions(); ok {
		t.Error("picked French captions")
	}
	if _, err := ytCaptions(context.Background(), srv.Client(), ytSubtitle{URL: srv.URL + "/gone?signature=secret"}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("error %v", err)
	}
	dubbed := ytInfo{Language: "ja", RequestedFormats: []ytFormat{{URL: "v", VCodec: "avc1", ACodec: "none"}, {URL: "a", VCodec: "none", ACodec: "mp4a", Language: "en-US"}}}
	if l := dubbed.audioLang(); l != "en-US" {
		t.Errorf("dubbed sound %q", l)
	}
	if l := (ytInfo{Language: "ja"}).audioLang(); l != "ja" {
		t.Errorf("sound %q", l)
	}
}

// TestBurnInDraws burns subtitles into a black picture where ffmpeg has
// libass, as the server's does.
func TestBurnInDraws(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil || !canBurn(ffmpeg) {
		t.Skip("no ffmpeg with the subtitles filter")
	}
	subs, err := writeSubtitles([]cc.Cue{{Start: 0, End: 5 * time.Second, Text: "Burned in"}})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(subs)
	bright := func(subtitles string) int {
		t.Helper()
		dir := t.TempDir()
		video, _ := os.Create(filepath.Join(dir, "v"))
		audio, _ := os.Create(filepath.Join(dir, "a"))
		cmd := exec.Command(ffmpeg, decodeInputs([]string{"-f", "lavfi", "-i", "color=c=black:s=640x360:r=30:d=1"}, "", 15, subtitles)...)
		cmd.ExtraFiles = []*os.File{video, audio}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, out)
		}
		video.Close()
		audio.Close()
		raw, _ := os.ReadFile(filepath.Join(dir, "v"))
		if len(raw) != 15*frameBytes {
			t.Fatalf("%d bytes of picture, want 15 frames", len(raw))
		}
		n := 0
		for _, y := range raw[14*frameBytes : 14*frameBytes+loopWidth*loopHeight] {
			if y > 128 {
				n++
			}
		}
		return n
	}
	if n := bright(""); n != 0 {
		t.Errorf("%d bright pixels without subtitles", n)
	}
	if n := bright(subs); n < 500 {
		t.Errorf("%d bright pixels with subtitles burned in", n)
	}
}

// TestAppStreamSubtitles plays a captioned video to the app's HLS stream,
// and checks its WebVTT rendition places each caption where the closed
// captions in its picture show it.
func TestAppStreamSubtitles(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "v.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=25:d=30",
		"-f", "lavfi", "-i", "sine=f=440:d=30", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", video)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make video: %v: %s", err, out)
	}
	srt := "1\n00:00:01,000 --> 00:00:02,500\nCaption one\n\n" +
		"2\n00:00:04,000 --> 00:00:05,500\nCaption two\n\n" +
		"3\n00:00:07,000 --> 00:00:08,500\nCaption three\n\n" +
		"4\n00:00:10,000 --> 00:00:11,500\nCaption four\n"
	it := item{Path: video, Frames: 30 * loopFPS, Audio: true, Title: "Captioned", Captions: srtCaptions(srt)}
	script := &cc.Script{}
	srv, err := stream.NewServer(true)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	in := tuner.Input{
		Args: []string{"-f", "mpegts", "-i", "pipe:0"}, Video: "0:v:0", Audio: []tuner.AudioTrack{{Map: "0:a:0"}},
		Captions: true, Subtitles: script,
		Source: func(ctx context.Context, w io.Writer) error {
			return playCues(ForApp(ctx, script), w, ffmpeg, "test", func(time.Time, bool) (item, int64, error) { return it, 0, nil })
		},
	}
	pb, err := srv.Start(context.Background(), "test", in)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Second) // past the last caption, from four seconds in
	base := strings.TrimSuffix(pb.URL, "master.m3u8")
	get := func(name string) []byte {
		t.Helper()
		resp, err := http.Get(base + name)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %s", name, resp.Status)
		}
		return b
	}
	if m := string(get("master.m3u8")); !strings.Contains(m, `TYPE=SUBTITLES`) || !strings.Contains(m, `SUBTITLES="subs"`) {
		t.Fatalf("master:\n%s", m)
	}
	segments := func(list []byte) []string {
		var out []string
		for _, l := range strings.Split(string(list), "\n") {
			if l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
		return out
	}
	subs := get("subs.m3u8")
	videos := segments(get("stream_0.m3u8"))
	vtts := segments(subs)
	if len(vtts) < 5 || len(vtts) > len(videos) {
		t.Fatalf("%d subtitle segments for %d video:\n%s", len(vtts), len(videos), subs)
	}
	t.Logf("subs.m3u8:\n%s", subs)

	// The video, and where its first picture is.
	var all []byte
	for _, v := range videos {
		all = append(all, get(v)...)
	}
	if err := os.WriteFile(filepath.Join(dir, "hls.ts"), all, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "seg0.ts"), get(videos[0]), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v", "-show_entries", "packet=pts",
		"-of", "csv=p=0", filepath.Join(dir, "seg0.ts")).Output()
	if err != nil {
		t.Fatal(err)
	}
	initPTS := int64(math.MaxInt64)
	for _, l := range strings.Fields(strings.ReplaceAll(string(out), ",", " ")) {
		if n, err := strconv.ParseInt(l, 10, 64); err == nil {
			initPTS = min(initPTS, n)
		}
	}

	// Where a player puts each WebVTT cue, through its segment's map.
	mapRe := regexp.MustCompile(`X-TIMESTAMP-MAP=MPEGTS:(\d+),LOCAL:([\d:.]+)`)
	cueRe := regexp.MustCompile(`([\d:.]+) --> ([\d:.]+)\n(.+)`)
	secs := func(v string) float64 {
		parts := strings.Split(v, ":")
		h, _ := strconv.ParseFloat(parts[0], 64)
		m, _ := strconv.ParseFloat(parts[1], 64)
		s, _ := strconv.ParseFloat(parts[2], 64)
		return h*3600 + m*60 + s
	}
	placed := map[string]float64{}
	for _, v := range vtts {
		body := string(get(v))
		m := mapRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s:\n%s", v, body)
		}
		mpegts, _ := strconv.ParseInt(m[1], 10, 64)
		offset := float64(mpegts-initPTS)/90000 - secs(m[2])
		if math.Abs(offset) > 0.001 {
			t.Errorf("%s maps %s to its local %s, %.3fs from the video", v, m[1], m[2], offset)
		}
		for _, c := range cueRe.FindAllStringSubmatch(body, -1) {
			at := secs(c[1]) + offset
			if p, ok := placed[c[3]]; ok && math.Abs(p-at) > 0.001 {
				t.Errorf("%q placed at %.3f and %.3f", c[3], p, at)
			}
			placed[c[3]] = at
		}
	}
	// The closed captions in the picture, from the same start.
	extract := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "movie=hls.ts[out0+subcc]", "-map", "0:s", "-f", "srt", "-")
	extract.Dir = dir
	srtOut, err := extract.Output()
	if err != nil {
		t.Fatalf("extracting captions: %v", err)
	}
	decoded, err := cc.Parse(srtOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) < 4 {
		t.Fatalf("closed captions:\n%s", srtOut)
	}
	for _, c := range decoded {
		text := regexp.MustCompile(`<[^>]*>|\{[^}]*\}`).ReplaceAllString(c.Text, "")
		at, ok := placed[text]
		if !ok {
			t.Errorf("%q in the picture but not the WebVTT (%v)", text, placed)
			continue
		}
		t.Logf("%q: WebVTT %.3fs, closed captions %.3fs", text, at, c.Start.Seconds())
		if d := at - c.Start.Seconds(); math.Abs(d) > 0.07 {
			t.Errorf("%q: WebVTT at %.3fs, closed captions at %.3fs", text, at, c.Start.Seconds())
		}
	}
}

// The script, which the app's WebVTT comes from, gets one screen at a
// time, timed from the stream's start.
func TestTimelineScriptScreens(t *testing.T) {
	script := &cc.Script{}
	tl := timeline{script: script}
	tl.begin(0)
	s := tl.begin(30 * loopFPS) // a video from 30 s in
	word := func(sec float64, w string) cc.Word {
		return cc.Word{Start: time.Duration(sec * float64(time.Second)), Text: w}
	}
	tl.set(s, []cc.Cue{
		{Start: 1 * time.Second, End: 5 * time.Second, Text: "one two", Words: []cc.Word{word(1, "one"), word(1.5, "two")}},
		{Start: 3 * time.Second, End: 7 * time.Second, Text: "three four", Words: []cc.Word{word(3, "three"), word(3.5, "four")}},
	})
	got := script.Between(0, time.Hour)
	if len(got) != 2 || got[0].Start != 31*time.Second || got[0].End != 33*time.Second || got[1].Text != "one two\nthree four" || got[1].Start != 33*time.Second {
		t.Errorf("script %+v", got)
	}
}
