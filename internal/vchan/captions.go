package vchan

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"sync"
	"time"

	"airwaves/internal/cc"
)

// Videos' English captions reach viewers as CEA-608 closed captions in the
// picture, which players show when the viewer turns captions on, except
// that a video whose sound isn't English gets its subtitles burned into the
// picture for HDHomeRun clients such as Channels DVR, which can't be told to
// turn captions on. The Airwaves app turns them on itself for such
// programs, and gets the captions as text besides, to lay out itself, so
// its streams never have them burned in.

type appKey struct{}

// ForApp marks a stream as one for the Airwaves app, whose viewers get
// subtitles as captions, never burned in. The stream puts its captions in
// script as text too, when script isn't nil.
func ForApp(ctx context.Context, script *cc.Script) context.Context {
	return context.WithValue(ctx, appKey{}, appStream{script})
}

type appStream struct{ script *cc.Script }

func forApp(ctx context.Context) (app bool, script *cc.Script) {
	a, ok := ctx.Value(appKey{}).(appStream)
	return ok, a.script
}

// HasCaptions reports whether a channel's streams carry CEA-608 closed
// captions: the channels of videos do, when the video on has some.
func HasCaptions(c Channel) bool {
	switch c.(type) {
	case *Folder, *Jellyfin, *YouTube:
		return true
	}
	return false
}

// captions are a video's English captions, fetched when first wanted and
// kept with the video.
type captions struct {
	key   string // what they are, to keep them across refreshes of the list
	fetch func(ctx context.Context) ([]byte, error)
	once  sync.Once
	done  chan struct{}
	cues  []cc.Cue
	err   error
}

// captionsTimeout bounds fetching captions, which a server may first have
// to extract from a large file.
const captionsTimeout = 2 * time.Minute

func newCaptions(key string, fetch func(ctx context.Context) ([]byte, error)) *captions {
	return &captions{key: key, fetch: fetch, done: make(chan struct{})}
}

// start fetches the captions in the background, once.
func (c *captions) start() {
	c.once.Do(func() {
		go func() {
			defer close(c.done)
			ctx, cancel := context.WithTimeout(context.Background(), captionsTimeout)
			defer cancel()
			raw, err := c.fetch(ctx)
			if err == nil {
				c.cues, err = cc.Parse(raw)
			}
			c.err = err
		}()
	})
}

// get waits for the captions until ctx ends.
func (c *captions) get(ctx context.Context) ([]cc.Cue, error) {
	c.start()
	select {
	case <-c.done:
		return c.cues, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ready returns the captions if fetching them has ended, nil if it
// failed.
func (c *captions) ready() ([]cc.Cue, bool) {
	select {
	case <-c.done:
		return c.cues, true
	default:
		return nil, false
	}
}

// failed reports whether fetching the captions failed.
func (c *captions) failed() bool {
	select {
	case <-c.done:
		return c.err != nil
	default:
		return false
	}
}

// timeline maps the frames of a stream to the caption track of the video
// playing each, for the cc.Writer adding them to the encoder's output, and
// keeps the stream's script, when it has one, in step.
type timeline struct {
	mu     sync.Mutex
	spans  []*span
	asked  int64      // the last frame asked about
	script *cc.Script // nil without one
}

// span is a video's frames, from start until end (-1 while it plays).
type span struct {
	start, end int64
	track      *cc.Track
}

// begin starts a video at frame start with no captions yet. Its track's
// first frames clear the screen of the last video's, and the script is
// clear from then on of any of its captions due after it ended.
func (t *timeline) begin(start int64) *span {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Spans wholly before what the writer has reached are done with.
	for len(t.spans) > 1 && t.spans[1].start <= t.asked {
		t.spans = t.spans[1:]
	}
	if n := len(t.spans); n > 0 {
		t.spans[n-1].end = start
	}
	s := &span{start: start, end: -1, track: cc.NewTrack(nil, loopFPS)}
	t.spans = append(t.spans, s)
	if t.script != nil {
		t.script.Set(frameDuration(start), forever, nil)
	}
	return s
}

// forever is a time no stream reaches.
const forever = time.Duration(math.MaxInt64)

// set gives a span its captions, timed from its start.
func (t *timeline) set(s *span, cues []cc.Cue) {
	track := cc.NewTrack(cues, loopFPS)
	t.mu.Lock()
	defer t.mu.Unlock()
	s.track = track
	if t.script != nil {
		until := forever
		if s.end >= 0 {
			until = frameDuration(s.end)
		}
		at := frameDuration(s.start)
		t.script.Set(at, until, cc.Screen(cc.Shift(cues, at)))
	}
}

// pair is the CEA-608 byte pair for frame.
func (t *timeline) pair(frame int64) (byte, byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.asked = max(t.asked, frame)
	i := sort.Search(len(t.spans), func(i int) bool { return t.spans[i].start > frame }) - 1
	if i < 0 {
		return cc.Padding, cc.Padding
	}
	s := t.spans[i]
	return s.track.Pair(frame - s.start)
}

const (
	// burnWait is how long a video waits to start for subtitles to burn
	// in, beyond which it plays with closed captions instead.
	burnWait = 10 * time.Second
	// captionsWait is how long it waits for closed captions.
	captionsWait = time.Second
	// lateCaptions is how far ahead captions arriving after their video
	// started begin, so none shows without having been loaded.
	lateCaptions = 2 * time.Second
)

// subtitle sets up a video's captions as it starts at span s, skip frames
// in, and returns a subtitle file to burn into the picture, or "". The
// file is the caller's to remove.
func subtitle(ctx context.Context, ffmpeg string, it item, skip int64, s *span, out *output) string {
	if it.Captions == nil {
		return ""
	}
	offset := frameDuration(skip)
	if !out.app && cc.Foreign(it.AudioLang) && canBurn(ffmpeg) {
		wait, cancel := context.WithTimeout(ctx, burnWait)
		cues, err := it.Captions.get(wait)
		cancel()
		if cues = cc.Shift(cues, -offset); err == nil && len(cues) > 0 {
			if path, err := writeSubtitles(cc.Screen(cues)); err == nil {
				return path
			}
		}
	}
	// Captions not fetched yet get a moment before the video starts, then
	// come in late.
	wait, cancel := context.WithTimeout(ctx, captionsWait)
	_, _ = it.Captions.get(wait)
	cancel()
	if cues, ok := it.Captions.ready(); ok {
		out.captions.set(s, cc.Shift(cues, -offset))
		return ""
	}
	go func() {
		cues, err := it.Captions.get(ctx)
		if err != nil {
			return
		}
		// They're late: start from a little after now, so every caption
		// that shows has been loaded.
		in := frameDuration(out.video.fed()-s.start) + lateCaptions
		var keep []cc.Cue
		for _, c := range cc.Shift(cues, -offset) {
			if c.Start >= in {
				keep = append(keep, c)
			}
		}
		out.captions.set(s, keep)
	}()
	return ""
}

// writeSubtitles writes cues to a temporary SRT file for ffmpeg's
// subtitles filter.
func writeSubtitles(cues []cc.Cue) (string, error) {
	f, err := os.CreateTemp("", "airwaves-subtitles-*.srt")
	if err != nil {
		return "", err
	}
	_, err = f.Write(cc.FormatSRT(cues))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !plainPath.MatchString(f.Name()) {
		err = fmt.Errorf("subtitle path %q needs escaping", f.Name())
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// plainPath matches paths that go in an ffmpeg filter graph as they are.
var plainPath = regexp.MustCompile(`^[A-Za-z0-9/_.+-]+$`)

var burns sync.Map // ffmpeg path: bool

// canBurn reports whether ffmpeg can draw subtitles on the picture (its
// subtitles filter, with libass).
func canBurn(ffmpeg string) bool {
	if ok, found := burns.Load(ffmpeg); found {
		return ok.(bool)
	}
	ok := false
	if path, err := writeSubtitles([]cc.Cue{{End: time.Second, Text: "A"}}); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
			"-f", "lavfi", "-i", "color=s=64x64:d=0.1", "-vf", "subtitles="+path, "-f", "null", "-").Run()
		cancel()
		os.Remove(path)
		ok = err == nil
	}
	burns.Store(ffmpeg, ok)
	return ok
}
