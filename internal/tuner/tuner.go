// Package tuner shares the HDHomeRun's tuners among viewers, recordings
// and measuring (see Manager), and says how ffmpeg reads what they
// receive (Input).
package tuner

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"

	"airwaves/internal/cc"
	"airwaves/internal/ts"
)

// Device describes the tuner.
type Device struct {
	ID     string `json:"id"`
	Name   string `json:"name"` // "HDHomeRun FLEX DUO"
	Kind   string `json:"kind"` // "hdhomerun" or "none"
	Model  string `json:"model,omitempty"`
	Tuners int    `json:"tuners,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Standards lists the broadcast standards the tuners receive ("ATSC
	// 1.0"); none without a tuner. ATSC3 is whether that includes ATSC 3.0
	// (NextGen TV).
	Standards []string `json:"standards,omitempty"`
	ATSC3     bool     `json:"atsc3"`
}

// AudioTrack is one audio stream of an input.
type AudioTrack struct {
	Map  string `json:"-"`    // ffmpeg stream specifier, e.g. "0:a:1"
	Lang string `json:"lang"` // ISO 639-2 code from the broadcast, e.g. "spa"
	// Described marks a video description (DVS) track for blind viewers.
	Described bool `json:"described,omitempty"`
}

// Input is how ffmpeg should read a channel or recording.
type Input struct {
	Args []string // input options ending with "-i <url>"
	// Video is the ffmpeg stream specifier of the picture.
	Video string
	// Audio lists the tracks to offer, main track first. Empty means no
	// audio (for example ATSC 3.0 AC-4, which ffmpeg cannot decode).
	Audio []AudioTrack
	// Broadcast marks a real over-the-air source: deinterlace it, and
	// expect CEA-608 closed captions in the picture.
	Broadcast bool
	// Captions marks CEA-608 closed captions in the picture of a source
	// that isn't a broadcast, such as a custom channel.
	Captions bool
	// Subtitles, when set, are the source's captions as text, timed from
	// its first frame, for a live stream to offer as WebVTT.
	Subtitles *cc.Script
	// Note is shown to the viewer, e.g. why audio is missing.
	Note string
	// VOD marks a recording: transcode as fast as possible and keep every
	// segment so the player can seek.
	VOD bool
	// Offset is where in the recording this input starts, in seconds.
	Offset float64
	// Source, when set, writes the channel's MPEG-TS for ffmpeg to read on
	// stdin (Args then end with "-i pipe:0"), until ctx ends.
	Source func(ctx context.Context, w io.Writer) error
}

// AudioTracks turns a program's audio streams into tracks to offer, in
// broadcast order (main track first). AC-4 (ATSC 3.0) can't be decoded by
// ffmpeg and is left out, keeping the others' numbering, with a note when
// it leaves nothing to hear.
func AudioTracks(p ts.Program) ([]AudioTrack, string) {
	var out []AudioTrack
	skipped := false
	for n, s := range p.Audio() {
		if s.Codec() == "AC4" {
			skipped = true
			continue
		}
		out = append(out, AudioTrack{Map: fmt.Sprintf("0:a:%d", n), Lang: s.Lang, Described: s.Described})
	}
	if len(out) == 0 && skipped {
		return nil, "ATSC 3.0 AC-4 audio cannot be decoded; video only"
	}
	return out, ""
}

var patterns = []string{
	"smptehdbars=s=1280x720:r=30",
	"testsrc2=s=1280x720:r=30",
	"mandelbrot=s=1280x720:r=30",
	"life=s=640x360:r=30:mold=10:ratio=0.12:life_color=#9fd8ff:death_color=#0b1530,scale=1280:720:flags=neighbor",
	"gradients=s=1280x720:r=30:speed=0.015:n=4",
	"zoneplate=s=1280x720:r=30:kt2=6:ku=64:kv=64",
	"rgbtestsrc=s=1280x720:r=30",
	"cellauto=s=640x360:r=30:rule=110:scroll=1,scale=1280:720:flags=neighbor",
}

// TestPattern is a test pattern read straight from ffmpeg's generators,
// with an English and a Spanish tone, for testing streaming without a
// tuner.
func TestPattern(number string) Input {
	video, eng, spa := demoSources(number)
	return Input{
		Args: []string{
			"-re", "-f", "lavfi", "-i", video,
			"-re", "-f", "lavfi", "-i", eng,
			"-re", "-f", "lavfi", "-i", spa,
		},
		Video: "0:v:0",
		Audio: []AudioTrack{{Map: "1:a:0", Lang: "eng"}, {Map: "2:a:0", Lang: "spa"}},
	}
}

// demoSources picks a stable pattern and two tones (main and second
// language) for a channel number.
func demoSources(number string) (video, main, second string) {
	h := fnv.New32a()
	h.Write([]byte(number))
	n := h.Sum32()
	tone := 220 + float64(n%12)*40
	sine := "sine=frequency=%.0f:sample_rate=48000,volume=0.05"
	return patterns[n%uint32(len(patterns))], fmt.Sprintf(sine, tone), fmt.Sprintf(sine, tone*1.5)
}
