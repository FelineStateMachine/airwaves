package stream

import (
	"context"
	"io"
	"os/exec"
	"slices"
	"testing"
	"time"

	"airwaves/internal/phase"
	"airwaves/internal/tuner"
)

// TestLiveStartTimesItsSteps feeds a live MPEG-TS source in real time, as
// a custom channel does: the stream's steps are timed in order, and it's
// ready once two segments' worth (four seconds) has come in, not after
// ffmpeg's default five seconds of looking at its input first.
func TestLiveStartTimesItsSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("starts ffmpeg")
	}
	s, err := NewServer(true)
	if err != nil {
		t.Skip(err)
	}
	defer s.Close()
	in := tuner.Input{
		Args: []string{"-f", "mpegts", "-i", "pipe:0"}, Video: "0:v:0", Audio: []tuner.AudioTrack{{Map: "0:a:0"}},
		Source: func(ctx context.Context, w io.Writer) error {
			cmd := exec.CommandContext(ctx, s.FFmpeg(), "-hide_banner", "-loglevel", "error", "-nostdin",
				"-re", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30", "-re", "-f", "lavfi", "-i", "sine=f=440:r=48000",
				"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "60", "-c:a", "aac", "-f", "mpegts", "pipe:1")
			cmd.Stdout = w
			return cmd.Run()
		},
	}
	timer := phase.New("test", "folder")
	pb, err := s.Start(phase.NewContext(context.Background(), timer), "test", in)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop("test")
	var names []string
	var ready time.Duration
	for _, m := range pb.Timing {
		names = append(names, m.Name)
		if m.Name == "ready" {
			ready = m.At
		}
	}
	want := []string{"ffmpeg started", "first data", "first frame", "first segment", "ready"}
	if !slices.Equal(names, want) {
		t.Errorf("steps %q, want %q", names, want)
	}
	t.Logf("steps: %s", timer)
	if ready < 3*time.Second || ready > 5*time.Second {
		t.Errorf("ready after %v, want about four seconds", ready)
	}
}
