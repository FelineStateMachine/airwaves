package stream

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"airwaves/internal/tuner"
)

// TestDemoStream runs ffmpeg end to end: a demo channel should produce a
// playable HLS playlist and segments over loopback HTTP.
func TestDemoStream(t *testing.T) {
	if testing.Short() {
		t.Skip("starts ffmpeg")
	}
	s, err := NewServer(true)
	if err != nil {
		t.Skip(err)
	}
	defer s.Close()

	in, _ := tuner.Demo{}.Input(context.Background(), "9.1")
	pb, err := s.Start(context.Background(), "test", in)
	if err != nil {
		t.Fatal(err)
	}

	master := get(t, pb.URL)
	for _, want := range []string{`NAME="English"`, `NAME="Spanish"`, `AUDIO="aud"`, "CLOSED-CAPTIONS=NONE", "stream_0.m3u8"} {
		if !strings.Contains(master, want) {
			t.Errorf("master playlist lacks %s:\n%s", want, master)
		}
	}
	base := pb.URL[:strings.LastIndex(pb.URL, "/")+1]
	for _, variant := range []string{"stream_0.m3u8", "stream_1.m3u8", "stream_2.m3u8"} {
		body := get(t, base+variant)
		var seg string
		for _, line := range strings.Split(body, "\n") {
			if strings.HasSuffix(line, ".ts") {
				seg = line
				break
			}
		}
		if data := get(t, base+seg); len(data) < 1000 || data[0] != 0x47 {
			t.Errorf("%s segment %s is not MPEG-TS (%d bytes)", variant, seg, len(data))
		}
	}

	s.Stop("test")
	resp, err := http.Get(pb.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("stopped stream still served: HTTP %d", resp.StatusCode)
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestMasterPlaylistForBroadcast(t *testing.T) {
	in := tuner.Input{
		Video:     "0:v:0",
		Audio:     []tuner.AudioTrack{{Map: "0:a:0", Lang: "eng"}, {Map: "0:a:1", Lang: "eng", Described: true}},
		Broadcast: true,
	}
	m := masterPlaylist(in)
	for _, want := range []string{
		`NAME="English",LANGUAGE="en",DEFAULT=YES`,
		`NAME="English (Described)"`, "describes-video",
		`TYPE=CLOSED-CAPTIONS`, `CLOSED-CAPTIONS="cc"`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("master lacks %s:\n%s", want, m)
		}
	}
	args := strings.Join(ffmpegArgs(in, "/tmp/x"), " ")
	if !strings.Contains(args, "-var_stream_map v:0 a:0 a:1") || !strings.Contains(args, "-hls_list_size 900") {
		t.Errorf("ffmpeg args: %s", args)
	}
	single := strings.Join(ffmpegArgs(tuner.Input{Video: "0:v:0", Audio: in.Audio[:1]}, "/tmp/x"), " ")
	if !strings.Contains(single, "-var_stream_map v:0,a:0") {
		t.Errorf("single-track args: %s", single)
	}
}

// Custom channels' videos carry closed captions, without being broadcasts.
func TestMasterPlaylistForCaptions(t *testing.T) {
	in := tuner.Input{Video: "0:v:0", Audio: []tuner.AudioTrack{{Map: "0:a:0"}}, Captions: true}
	m := masterPlaylist(in)
	for _, want := range []string{`TYPE=CLOSED-CAPTIONS,GROUP-ID="cc"`, `INSTREAM-ID="CC1",DEFAULT=NO,AUTOSELECT=NO`, `CLOSED-CAPTIONS="cc"`} {
		if !strings.Contains(m, want) {
			t.Errorf("master lacks %s:\n%s", want, m)
		}
	}
	if args := strings.Join(ffmpegArgs(in, "/tmp/x"), " "); strings.Contains(args, "bwdif") || !strings.Contains(args, "-a53cc 1") {
		t.Errorf("ffmpeg args: %s", args)
	}
}
