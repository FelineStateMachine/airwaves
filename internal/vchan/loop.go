package vchan

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"airwaves/internal/cc"
	"airwaves/internal/phase"
)

// item is one video in a looping channel.
type item struct {
	Path        string // file or URL
	Frames      int64  // length in output frames
	Audio       bool
	Title       string
	Subtitle    string
	Description string
	Category    string
	Season      string
	Episode     string
	Image       string
	// Input, when set, returns ffmpeg input options (ending "-i <url>") that
	// play the video from offset in, for sources that seek on their side.
	// Otherwise Path is read with a local seek. The stream's pace is set
	// as it goes to the encoder (see pacer), not by the input.
	Input func(offset time.Duration) []string
	// AudioStream is the sound's stream specifier in Path's or Input's
	// streams; "0:a:0" when empty.
	AudioStream string
	// AudioLang is the sound's language as tagged ("eng", "jpn"), "" when
	// unknown, which is taken for English.
	AudioLang string
	// Captions are the video's English captions, nil when it has none.
	Captions *captions
	// Level, when set, evens the item's sound out as it streams (see
	// loudness.go).
	Level *itemLevel
	// Open, when set, takes precedence over Input. It is called right before
	// the video plays, for sources whose addresses must be looked up then
	// and may fail. When it fails, a slate fills the video's time so the
	// stream keeps to the schedule.
	Open func(ctx context.Context, offset time.Duration) (opened, error)
	// Slate makes the item a quiet dark picture, captioned with Title when
	// ffmpeg can draw text.
	Slate bool
}

// opened is how to play an item found when it opens.
type opened struct {
	// Args are ffmpeg input options for one or more inputs (picture first,
	// each ending "-i <url>") that play from the offset asked for.
	Args []string
	// Audio is the sound's stream specifier ("1:a:0"), empty for silence.
	Audio string
	// AudioLang and Captions take the place of the item's.
	AudioLang string
	Captions  *captions
}

const (
	loopWidth  = 1280
	loopHeight = 720
	loopFPS    = 30
	sampleRate = 48000
	// lead is how far ahead of real time a stream starts: its first
	// seconds are decoded and encoded as fast as they can be, so a viewer
	// tuning in waits for none of them, and after that the stream keeps
	// that far ahead of the clock (see pacer).
	lead = 6 * time.Second
	// maxPrograms bounds a guide request on a channel of very short videos.
	maxPrograms = 2000
)

// scheduleEpoch anchors every looping channel. A schedule only changes when
// the channel's videos do.
var scheduleEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// framesSince is how many output frames have passed between the epoch and t.
func framesSince(t time.Time) int64 { return framesIn(t.Sub(scheduleEpoch)) }

// framesIn is how many output frames d lasts.
func framesIn(d time.Duration) int64 {
	return int64(d/time.Second)*loopFPS + int64(d%time.Second)*loopFPS/int64(time.Second)
}

func frameDuration(frames int64) time.Duration {
	return time.Duration(frames) * time.Second / loopFPS
}

// playing returns the index of the video on at t and how many frames into
// it, or -1 for an empty folder.
func playing(items []item, t time.Time) (int, int64) {
	var total int64
	for _, it := range items {
		total += it.Frames
	}
	if total == 0 {
		return -1, 0
	}
	n := framesSince(t) % total
	if n < 0 {
		n += total
	}
	for i, it := range items {
		if n < it.Frames {
			return i, n
		}
		n -= it.Frames
	}
	return 0, 0
}

// programs is the guide for items looping on the clock: one entry per
// video, in loop order.
func programs(items []item, from, to time.Time) []Program {
	i, into := playing(items, from)
	if i < 0 {
		return nil
	}
	start := from.Add(-frameDuration(into))
	var out []Program
	for start.Before(to) && len(out) < maxPrograms {
		it := items[i]
		end := start.Add(frameDuration(it.Frames))
		out = append(out, programOf(it, start, end))
		start, i = end, (i+1)%len(items)
	}
	return out
}

// programOf is an item's guide entry, airing from start to end.
func programOf(it item, start, end time.Time) Program {
	return Program{
		Start: start, End: end, Title: it.Title, Subtitle: it.Subtitle, Description: it.Description,
		Category: it.Category, Season: it.Season, Episode: it.Episode, Image: it.Image,
		AudioLang: it.AudioLang, Captions: it.Captions != nil,
	}
}

// lineup gives a looping channel's items, and its schedule (nil for none),
// as they are now.
type lineup func() ([]item, *sched)

// guideFor is the guide for a looping channel: on its schedule when it has
// one, else looping on the clock.
func guideFor(get lineup, from, to time.Time) []Program {
	items, s := get()
	if s == nil {
		return programs(items, from, to)
	}
	return scheduledPrograms(items, s, from, to)
}

// scheduledPrograms is the guide for items airing on a schedule: one entry
// per airing, and "Off air" between, saying what's next and when.
func scheduledPrograms(items []item, s *sched, from, to time.Time) []Program {
	ls := lengths(items)
	g := s.guide(ls, from, to, maxPrograms)
	var out []Program
	for k, a := range g {
		if a.I >= 0 {
			out = append(out, programOf(items[a.I], a.Start, a.End))
			continue
		}
		// A gap ends with the next entry, or past the window, the next airing.
		next, ok := slot{}, false
		if k+1 < len(g) {
			next, ok = g[k+1], true
		} else {
			next, _, ok = s.at(ls, a.End)
		}
		if !ok {
			out = append(out, offAirProgram(a, "", time.Time{}, false))
			continue
		}
		out = append(out, offAirProgram(a, airingName(items[next.I]), next.Start, isPremiere(s, ls, next)))
	}
	return out
}

// isPremiere reports whether a is the schedule's very first airing.
func isPremiere(s *sched, lengths []time.Duration, a slot) bool {
	first, ok := s.premiere(lengths)
	return ok && first.Start.Equal(a.Start)
}

// lengths are the items' lengths, as a schedule takes them.
func lengths(items []item) []time.Duration {
	out := make([]time.Duration, len(items))
	for i, it := range items {
		out[i] = frameDuration(it.Frames)
	}
	return out
}

// airingName names an item as an off-air note gives what's next: an
// episode by its series and number, "House of the Dragon, S1 E4".
func airingName(it item) string {
	if it.Season != "" && it.Episode != "" {
		return fmt.Sprintf("%s, S%s E%s", it.Title, it.Season, it.Episode)
	}
	return it.Title
}

// sequence lists items for choosing where a schedule begins.
func sequence(items []item) []SequenceItem {
	out := make([]SequenceItem, len(items))
	for i, it := range items {
		out[i] = SequenceItem{Title: it.Title, Subtitle: it.Subtitle, Season: it.Season, Episode: it.Episode, Length: frameDuration(it.Frames)}
	}
	return out
}

// play streams a looping channel to w until ctx ends: its items on the
// clock as they were when it started, or on its schedule, from when it has
// one (see scheduled). label names the channel in logs.
func play(ctx context.Context, w io.Writer, ffmpeg, label string, get lineup) error {
	items, s := get()
	i, skip := playing(items, time.Now())
	if i < 0 {
		return fmt.Errorf("%s: nothing to play", label)
	}
	var onSchedule cueFunc
	if s != nil {
		onSchedule = scheduled(label, get)
	}
	started, failures := false, 0
	return playCues(ctx, w, ffmpeg, label, func(now time.Time, ok bool) (item, int64, error) {
		// A schedule set meanwhile takes over as the video on ends.
		if onSchedule == nil && started {
			if _, s := get(); s != nil {
				onSchedule = scheduled(label, get)
			}
		}
		if onSchedule != nil {
			return onSchedule(now, ok)
		}
		if started {
			if ok {
				failures = 0
			} else if failures++; failures >= len(items) {
				return item{}, 0, fmt.Errorf("%s: no video would play", label)
			}
			i, skip = (i+1)%len(items), 0
		}
		started = true
		// The next video's captions load while this one plays.
		if next := items[(i+1)%len(items)].Captions; next != nil {
			next.start()
		}
		return items[i], skip, nil
	})
}

const (
	// cueSlack is how far a scheduled stream may fall behind the clock
	// before it skips ahead to catch up.
	cueSlack = 5 * time.Second
	// slateChunk is the longest a slate plays before the schedule is
	// looked at again, so changes to it show.
	slateChunk = time.Minute
)

// scheduled cues a channel's items on its schedule: as each ends, the one
// on air then, from where it is, and while the channel is off the air, a
// slate saying when it's back, a minute at a time. The lineup is asked for
// each time, so changes to the items or the schedule apply as the stream
// goes on; with the schedule gone, the items loop on the clock. An airing
// that fails stands by for the rest of its time, and the stream ends once
// as many fail in a row as there are items.
func scheduled(label string, get lineup) cueFunc {
	// The airing cued last (I -1 for a slate), and the last that failed.
	last, failed := slot{I: -1}, slot{I: -1}
	var end time.Time // when what was cued last ends
	failures := 0
	return func(now time.Time, ok bool) (item, int64, error) {
		items, s := get()
		if len(items) == 0 {
			return item{}, 0, fmt.Errorf("%s: nothing to play", label)
		}
		if !end.IsZero() {
			switch {
			case !ok:
				if failures++; failures >= len(items) {
					return item{}, 0, fmt.Errorf("%s: no video would play", label)
				}
				if last.I >= 0 {
					failed = last
				}
			case last.I >= 0:
				failures = 0
			}
		}
		// What plays next picks up where the last left off, however early
		// its decoder finished, unless it failed or fell well behind.
		t := now
		if !end.IsZero() && ok && now.Sub(end) < cueSlack {
			t = end
		}
		var on slot
		onAir := true
		if s == nil {
			i, into := playing(items, t)
			start := t.Add(-frameDuration(into))
			on = slot{i, start, start.Add(frameDuration(items[i].Frames))}
		} else {
			ls := lengths(items)
			var found bool
			if on, onAir, found = s.at(ls, t); !found {
				return item{}, 0, fmt.Errorf("%s: nothing will air", label)
			}
			if !onAir {
				stop := minTime(on.Start, t.Add(slateChunk))
				last, end = slot{-1, t, stop}, stop
				note := offAirProgram(slot{-1, t, on.Start}, airingName(items[on.I]), on.Start, isPremiere(s, ls, on))
				return item{Slate: true, Title: strings.TrimSuffix(note.Description, "."), Frames: max(framesIn(stop.Sub(t)), 1)}, 0, nil
			}
		}
		if on.I == failed.I && on.Start.Equal(failed.Start) {
			stop := minTime(on.End, t.Add(slateChunk))
			last, end = slot{-1, t, stop}, stop
			return item{Slate: true, Title: standBy, Frames: max(framesIn(stop.Sub(t)), 1)}, 0, nil
		}
		// The next video's captions load while this one plays.
		if next := items[(on.I+1)%len(items)].Captions; next != nil {
			next.start()
		}
		last, end = on, on.End
		return items[on.I], max(framesIn(t.Sub(on.Start)), 0), nil
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// cueFunc says what to play at now, and how many frames into it to start.
// It is asked first when the stream starts, then each time an item ends;
// ok reports whether that item played without error. An error ends the
// stream.
type cueFunc func(now time.Time, ok bool) (item, int64, error)

// playCues streams the items cue gives to w until ctx ends. One encoder
// runs for the whole viewing, fed raw picture and sound by a decoder per
// video in turn, so clients see a single unbroken stream across videos of
// any format. The videos' captions go into the picture as it leaves.
func playCues(ctx context.Context, w io.Writer, ffmpeg, label string, cue cueFunc) error {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Decoders write picture and sound to pipes of their own; feeds carry
	// them to the encoder's pair through memory. A raw frame is far bigger
	// than a pipe, and ffmpeg holds back whichever input runs ahead, so
	// direct pipes deadlock: the decoder stuck writing picture while the
	// encoder waits for sound.
	p, err := openPipes(2)
	if err != nil {
		return err
	}
	encVideo, encAudio := p[0], p[1]
	app, script := forApp(ctx)
	out := &output{captions: &timeline{script: script}, app: app}
	captioned := cc.NewWriter(w, loopFPS, out.captions.pair)
	enc := exec.CommandContext(ctx, ffmpeg, encodeArgs()...)
	enc.ExtraFiles = []*os.File{encVideo.r, encAudio.r} // fds 3 and 4
	enc.Stdout = captioned
	var encErr bytes.Buffer
	enc.Stderr = &encErr
	err = enc.Start()
	encVideo.r.Close()
	encAudio.r.Close()
	if err != nil {
		encVideo.w.Close()
		encAudio.w.Close()
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	encDone := make(chan struct{})
	go func() {
		_ = enc.Wait()
		close(encDone)
		cancel() // the viewer left, or the encoder failed
	}()
	// The picture sets the pace: the decoders run as fast as it lets them,
	// sound and all.
	out.video = newFeed(encVideo.w, 512, frameBytes, &pacer{per: loopFPS, lead: lead, quit: ctx.Done()}) // up to 32 MB, under a second
	out.audio = newFeed(encAudio.w, 128, 4, nil)
	out.video.first = func() { phase.Step(ctx, "decoding") }
	finish := func() {
		// Ending the feeds ends the encoder.
		out.video.close()
		out.audio.close()
		<-encDone
	}
	defer finish()

	// What comes next is cued at the stream's own clock: where it was last
	// cued, plus the frames since. It runs ahead of the clock on the wall
	// by up to lead, which so holds from one video to the next; a stream
	// that fell behind is cued at the wall's.
	var at time.Time
	var cued int64 // frames when last cued
	ok := true
	for ctx.Err() == nil {
		now := time.Now()
		if s := at.Add(frameDuration(out.video.units - cued)); !at.IsZero() && s.After(now) {
			now = s
		}
		it, skip, err := cue(now, ok)
		if err != nil {
			return err
		}
		at, cued = now, out.video.units
		ok = decode(ctx, ffmpeg, label, it, skip, out)
	}
	finish()
	_ = captioned.Flush()
	if parent.Err() == nil && encErr.Len() > 0 {
		return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(encErr.String()))
	}
	return nil
}

// output is where decoders send a stream's picture, sound and captions.
type output struct {
	video, audio *feed
	captions     *timeline
	app          bool // for the Airwaves app: no subtitles burned in
}

type osPipe struct{ r, w *os.File }

func openPipes(n int) ([]osPipe, error) {
	var out []osPipe
	for range n {
		r, w, err := os.Pipe()
		if err != nil {
			for _, p := range out {
				p.r.Close()
				p.w.Close()
			}
			return nil, err
		}
		out = append(out, osPipe{r, w})
	}
	return out, nil
}

const relayChunk = 64 << 10

// frameBytes is the size of a raw frame.
const frameBytes = loopWidth * loopHeight * 3 / 2

// feed carries raw picture or sound to one of the encoder's pipes through
// up to chunks*64 KB of memory, from one decoder after another, counting
// it in units (frames, or sound samples). If the encoder fails, the feed
// keeps reading, so decoders never block.
type feed struct {
	buf    chan []byte
	unit   int
	units  int64        // from the decoders that have finished
	bytes  atomic.Int64 // fed so far
	closed sync.Once
	first  func() // called as the first unit arrives, when set
}

// newFeed starts a feed to dst. With pace set, units go to dst no faster
// than pace allows; otherwise as soon as they arrive.
func newFeed(dst *os.File, chunks, unit int, pace *pacer) *feed {
	f := &feed{buf: make(chan []byte, chunks), unit: unit}
	go func() {
		defer dst.Close()
		failed := false
		var sent int64
		for b := range f.buf {
			if !failed && pace != nil {
				failed = !pace.wait(sent / int64(unit))
			}
			if !failed {
				_, err := dst.Write(b)
				failed = err != nil
			}
			sent += int64(len(b))
		}
	}()
	return f
}

// pacer holds a feed to real time, but for a lead: unit n goes out lead
// before n/per seconds after the first did. Units that come late go out
// at once, so a stream that falls behind (a slow start, a network
// stall) catches up as fast as its decoder can.
type pacer struct {
	per   int64 // units a second
	lead  time.Duration
	quit  <-chan struct{}
	start time.Time
	timer *time.Timer
}

// wait waits until unit n is due, and reports false if quit came first.
func (p *pacer) wait(n int64) bool {
	now := time.Now()
	if p.start.IsZero() {
		p.start = now
	}
	due := p.start.Add(time.Duration(n/p.per)*time.Second + time.Duration(n%p.per)*time.Second/time.Duration(p.per) - p.lead)
	d := due.Sub(now)
	if d <= 0 {
		return true
	}
	if p.timer == nil {
		p.timer = time.NewTimer(d)
	} else {
		p.timer.Reset(d)
	}
	select {
	case <-p.timer.C:
		return true
	case <-p.quit:
		return false
	}
}

// source returns a pipe for a decoder to write to, and done, which waits
// for what the decoder wrote to pass and returns how many units it was. A
// decoder cut off mid-frame has its frame made up, so the next decoder's
// frames start whole. Close the pipe once the decoder has it. process, when
// set, sees what passes, whole units at a time, and may change it in place.
func (f *feed) source(process func([]byte)) (w *os.File, done func() int64, err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	result := make(chan int64, 1)
	go func() {
		defer r.Close()
		var n int64
		for {
			b := make([]byte, relayChunk)
			k, err := io.ReadFull(r, b)
			if k > 0 {
				if n == 0 && f.first != nil {
					f.first()
				}
				if process != nil {
					process(b[:k-k%f.unit])
				}
				f.buf <- b[:k]
				n += int64(k)
				f.bytes.Add(int64(k))
			}
			if err != nil {
				break
			}
		}
		if rest := int(n % int64(f.unit)); rest > 0 {
			pad := filler(f.unit, rest)
			f.buf <- pad
			n += int64(len(pad))
			f.bytes.Add(int64(len(pad)))
		}
		result <- n / int64(f.unit)
	}()
	var units int64
	once := sync.Once{}
	return w, func() int64 {
		once.Do(func() {
			units = <-result
			f.units += units
		})
		return units
	}, nil
}

// filler completes a unit begun with have bytes: black for a frame (luma
// then chroma), silence for sound.
func filler(unit, have int) []byte {
	pad := make([]byte, unit-have)
	if unit == frameBytes {
		for i := range pad {
			if have+i < loopWidth*loopHeight {
				pad[i] = 0x10
			} else {
				pad[i] = 0x80
			}
		}
	}
	return pad
}

// fed is how many units have gone into the feed.
func (f *feed) fed() int64 { return f.bytes.Load() / int64(f.unit) }

// close ends the feed, once no decoder is writing.
func (f *feed) close() { f.closed.Do(func() { close(f.buf) }) }

// encodeArgs reads raw 720p30 picture on fd 3 and 48 kHz stereo on fd 4
// and writes H.264 and AAC in MPEG-TS to stdout.
func encodeArgs() []string {
	return []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "rawvideo", "-pix_fmt", "yuv420p",
		"-video_size", fmt.Sprintf("%dx%d", loopWidth, loopHeight), "-framerate", strconv.Itoa(loopFPS), "-i", "pipe:3",
		"-f", "s16le", "-ar", strconv.Itoa(sampleRate), "-ac", "2", "-i", "pipe:4",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-preset", "veryfast", "-g", "60",
		"-b:v", "3M", "-maxrate", "4M", "-bufsize", "6M",
		"-c:a", "aac", "-b:a", "160k",
		"-f", "mpegts", "pipe:1",
	}
}

// decodeArgs plays one video from skip frames in, as raw
// picture (fd 3) and sound (fd 4): letterboxed to 720p at 30 fps, and padded
// or trimmed to exactly the frames the schedule gives it, so picture and
// sound stay in step from one video to the next.
func decodeArgs(it item, skip int64) []string {
	input, audio := it.inputs(skip)
	return decodeInputs(input, audio, it.Frames-skip, "")
}

// inputs are ffmpeg's inputs for an item without Open, from skip frames
// in, and the sound's stream specifier, empty for silence.
func (it item) inputs(skip int64) (input []string, audio string) {
	if it.Input != nil {
		input = it.Input(frameDuration(skip))
	} else {
		if skip > 0 {
			input = append(input, "-ss", strconv.FormatFloat(float64(skip)/loopFPS, 'f', 3, 64))
		}
		input = append(input, "-i", it.Path)
	}
	if !it.Audio {
		return input, ""
	}
	return input, cmp.Or(it.AudioStream, "0:a:0")
}

// decodeInputs plays ffmpeg inputs (picture from the first) as decodeArgs
// does, for frames. audio is the sound's stream specifier, empty for
// silence. subtitles, when set, is an SRT file to burn into the picture,
// timed from where the inputs start.
func decodeInputs(input []string, audio string, frames int64, subtitles string) []string {
	args := append([]string{"-hide_banner", "-loglevel", "error", "-nostdin"}, input...)
	if audio == "" {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("anullsrc=r=%d:cl=stereo", sampleRate))
		audio = fmt.Sprintf("%d:a", countInputs(input))
	}
	burn := ""
	if subtitles != "" {
		burn = "subtitles=filename=" + subtitles + ":force_style='FontName=DejaVu Sans,Outline=1.5,Shadow=0.5,MarginV=20',"
	}
	graph := fmt.Sprintf("[0:v:0]scale=%[1]d:%[2]d:force_original_aspect_ratio=decrease:force_divisible_by=2,"+
		"pad=%[1]d:%[2]d:-1:-1,setsar=1,%[8]sfps=%[3]d,format=yuv420p,tpad=stop=-1:stop_mode=clone,trim=end_frame=%[4]d[v];"+
		"[%[5]s]aresample=%[6]d,aformat=sample_fmts=s16:channel_layouts=stereo,apad,atrim=end_sample=%[7]d[a]",
		loopWidth, loopHeight, loopFPS, frames, audio, sampleRate, frames*sampleRate/loopFPS, burn)
	return append(args, "-filter_complex", graph,
		"-map", "[v]", "-f", "rawvideo", "pipe:3",
		"-map", "[a]", "-f", "s16le", "pipe:4")
}

// countInputs counts the "-i" in ffmpeg options.
func countInputs(args []string) int {
	n := 0
	for i := 0; i < len(args); i++ {
		if args[i] == "-i" {
			n++
			i++
		}
	}
	return n
}

// standBy captions the slate shown when a video can't be opened.
const standBy = "Please stand by"

// decode plays one item into the stream and reports whether it played
// without error. An item whose Open fails is replaced by a slate for the
// rest of its time.
func decode(ctx context.Context, ffmpeg, label string, it item, skip int64, out *output) bool {
	name := it.Title
	var input []string
	var sound string
	switch {
	case it.Slate:
	case it.Open != nil:
		// Opened sources are logged by Path, an ID: their addresses are
		// long and may carry credentials.
		name = it.Path
		o, err := it.Open(ctx, frameDuration(skip))
		if err != nil {
			if ctx.Err() != nil {
				return true
			}
			log.Printf("%s: %s: %v", label, name, err)
			phase.Step(ctx, "open failed")
			// All of it: the time spent opening played nothing, so the
			// stream's clock didn't move (see playCues).
			if left := it.Frames - skip; left > 0 {
				decode(ctx, ffmpeg, label, item{Slate: true, Title: standBy, Frames: left}, 0, out)
			}
			return false
		}
		phase.Step(ctx, "opened")
		input, sound = o.Args, o.Audio
		it.AudioLang, it.Captions = o.AudioLang, o.Captions
	default:
		input, sound = it.inputs(skip)
	}
	video, videoDone, err := out.video.source(nil)
	if err != nil {
		log.Printf("%s: %v", label, err)
		return false
	}
	// The sound is leveled on its way to the encoder: metered and turned
	// up or down, adding no delay.
	var level func([]byte)
	var lev *leveler
	if it.Level != nil && sound != "" && !it.Slate {
		lev = newLeveler(it.Level)
		level = lev.process
	}
	audio, audioDone, err := out.audio.source(level)
	if err != nil {
		video.Close()
		videoDone()
		log.Printf("%s: %v", label, err)
		return false
	}
	span := out.captions.begin(out.video.units)
	var args []string
	if it.Slate {
		args = slateArgs(ffmpeg, it.Title, it.Frames-skip)
	} else {
		subs := subtitle(ctx, ffmpeg, it, skip, span, out)
		if subs != "" {
			defer os.Remove(subs)
		}
		args = decodeInputs(input, sound, it.Frames-skip, subs)
	}
	dec := exec.CommandContext(ctx, ffmpeg, args...)
	dec.ExtraFiles = []*os.File{video, audio}
	var decErr bytes.Buffer
	dec.Stderr = &decErr
	err = dec.Run()
	// The decoder has its own copies of the pipes; with these closed too,
	// the feeds see the end of its output.
	video.Close()
	audio.Close()
	videoDone()
	audioDone()
	if lev != nil {
		lev.finish() // what it measured is remembered for its next airing
	}
	if err != nil && ctx.Err() == nil {
		msg := strings.TrimSpace(decErr.String())
		if it.Open != nil {
			msg = urls.ReplaceAllString(msg, "<url>")
		}
		log.Printf("%s: %s: %v: %s", label, name, err, msg)
		return false
	}
	return true
}

var urls = regexp.MustCompile(`https?://\S+`)

// slateArgs plays a quiet dark picture for frames, with caption when
// ffmpeg can draw text.
func slateArgs(ffmpeg, caption string, frames int64) []string {
	src := fmt.Sprintf("color=c=0x101318:s=%dx%d:r=%d", loopWidth, loopHeight, loopFPS)
	if text := slateText(caption); text != "" && canDrawText(ffmpeg) {
		// Longer captions, like "Back at 8:00 PM with ...", get smaller to fit.
		size := 40
		if n := utf8.RuneCountInString(caption); n > 50 {
			size = max(24, 40*50/n)
		}
		src += fmt.Sprintf(",drawtext=text='%s':fontcolor=0x8c939f:fontsize=%d:x=(w-tw)/2:y=(h-th)/2", text, size)
	}
	return decodeInputs([]string{"-f", "lavfi", "-i", src}, "", frames, "")
}

// slateText is a caption as drawtext's quoted text: letters, digits and
// a little punctuation, its colons escaped.
func slateText(caption string) string {
	var b strings.Builder
	for _, r := range caption {
		switch {
		case r == ':':
			b.WriteString(`\:`)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" .!,", r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

var drawText sync.Map // ffmpeg path: bool

// canDrawText reports whether ffmpeg has drawtext and a font for it.
func canDrawText(ffmpeg string) bool {
	if ok, found := drawText.Load(ffmpeg); found {
		return ok.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "color=s=64x64:d=0.1,drawtext=text=A", "-frames:v", "1", "-f", "null", "-").Run()
	drawText.Store(ffmpeg, err == nil)
	return err == nil
}
