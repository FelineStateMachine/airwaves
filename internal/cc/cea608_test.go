package cc

import (
	"fmt"
	"math/bits"
	"strings"
	"testing"
	"time"
)

func TestTrackBytes(t *testing.T) {
	track := NewTrack([]Cue{{Start: time.Second, End: 2 * time.Second, Text: "Hi"}}, 30)
	want := map[int64][2]byte{
		0: {0x94, 0x2c}, 1: {0x94, 0x2c}, // EDM: a clean screen
		21: {0x94, 0x20}, 22: {0x94, 0x20}, // RCL: pop-on
		23: {0x94, 0xae}, 24: {0x94, 0xae}, // ENM
		25: {0x94, 0x76}, 26: {0x94, 0x76}, // PAC: row 15, indent 12
		27: {0x97, 0x23}, 28: {0x97, 0x23}, // tab offset 3: column 15
		29: {0xc8, 0xe9},                   // "Hi"
		30: {0x94, 0x2f}, 31: {0x94, 0x2f}, // EOC at one second
		60: {0x94, 0x2c}, 61: {0x94, 0x2c}, // EDM at two
	}
	for f := int64(-2); f < 100; f++ {
		b1, b2 := track.Pair(f)
		w, ok := want[f]
		if !ok {
			w = [2]byte{Padding, Padding}
		}
		if [2]byte{b1, b2} != w {
			t.Errorf("frame %d: % x, want % x", f, []byte{b1, b2}, w[:])
		}
	}
	var none *Track
	if b1, b2 := none.Pair(5); b1 != Padding || b2 != Padding {
		t.Error("a nil track sent something")
	}
}

func TestParity(t *testing.T) {
	for b := range 128 {
		if p := parity(byte(b)); bits.OnesCount8(p)%2 != 1 || p&0x7f != byte(b) {
			t.Errorf("parity(%#x) = %#x", b, p)
		}
	}
}

func TestCharset(t *testing.T) {
	for _, set := range []struct{ a, b string }{
		{"ÁÉÓÚÜü\x00¡*\x00—©℠•“”ÀÂÇÈÊËëÎÏïÔÙùÛ«»", "AEOUUu !- -cs.\"\"AACEEEeIIiOUuU\"\""},
		{"ÃãÍÌìÒòÕõ{}\\^_|~ÄäÖöß¥¤¦ÅåØø┌┐└┘", "AaIIiOoOo()/ -:-AaOosY$:AaOo++++"},
	} {
		if n, m := len([]rune(set.a)), len(set.b); n != 32 || m != 32 {
			t.Errorf("extended set of %d with %d stand-ins", n, m)
		}
	}
	for r, c := range charset {
		if c.alt != 0 {
			if alt, ok := charset[rune(c.alt)]; !ok || alt.code[1] != 0 {
				t.Errorf("%q stands in for %q but isn't basic", c.alt, r)
			}
		}
	}
	for _, r := range "*\\^_{|}~" {
		if c := charset[r]; c.code[1] == 0 {
			t.Errorf("%q is sent as basic %#x, which 608 shows as something else", r, c.code[0])
		}
	}
	for r := range substitutes {
		if _, ok := charset[r]; ok {
			t.Errorf("%q is substituted though 608 has it", r)
		}
	}
}

// screen608 is a CEA-608 pop-on decoder, enough to check what a track
// shows and when.
type screen608 struct {
	shown, hidden [16][Columns]rune
	row, col      int
	prev          [2]byte
	rollUp        bool
	text          string // on screen
	since         int64
	events        []shownCaption
}

type shownCaption struct {
	start, end int64
	text       string
}

func decodeTrack(t *testing.T, track *Track, frames int64) []shownCaption {
	t.Helper()
	back := map[[2]byte]rune{}
	for r, c := range charset {
		back[c.code] = r
	}
	var s screen608
	var update func(int64)
	var f int64
	put := func(r rune) {
		if s.col >= Columns {
			return
		}
		if s.rollUp {
			s.shown[s.row][s.col] = r
			s.col++
			update(f)
			return
		}
		s.hidden[s.row][s.col] = r
		s.col++
	}
	update = func(f int64) {
		var lines []string
		for _, row := range s.shown {
			if l := strings.TrimSpace(strings.ReplaceAll(string(row[:]), "\x00", " ")); l != "" {
				lines = append(lines, l)
			}
		}
		text := strings.Join(lines, "\n")
		if text == s.text {
			return
		}
		if s.text != "" {
			s.events = append(s.events, shownCaption{s.since, f, s.text})
		}
		s.text, s.since = text, f
	}
	rows := map[[2]byte]int{}
	for r := 1; r <= 15; r++ {
		p := pac(r, 0)
		rows[[2]byte{p[0], p[1] &^ 0x1f}] = r
	}
	for f = range frames {
		b1, b2 := track.Pair(f)
		if bits.OnesCount8(b1)%2 != 1 || bits.OnesCount8(b2)%2 != 1 {
			t.Fatalf("frame %d: % x lacks odd parity", f, []byte{b1, b2})
		}
		c := [2]byte{b1 & 0x7f, b2 & 0x7f}
		if c == [2]byte{} {
			continue
		}
		if c[0] >= 0x20 {
			s.prev = [2]byte{}
			put(back[[2]byte{c[0]}])
			if c[1] != 0 {
				put(back[[2]byte{c[1]}])
			}
			continue
		}
		if c == s.prev {
			continue // the second sending
		}
		s.prev = c
		switch {
		case c == [2]byte{0x14, rcl}:
			s.rollUp = false
		case c == [2]byte{0x14, ru2}:
			if !s.rollUp {
				s.shown, s.hidden = [16][Columns]rune{}, [16][Columns]rune{}
				update(f)
			}
			s.rollUp = true
		case c == [2]byte{0x14, cr}:
			for r := 1; r < 16; r++ {
				s.shown[r-1] = s.shown[r]
				if r-1 < s.row-1 {
					s.shown[r-1] = [Columns]rune{} // two rows only
				}
			}
			s.shown[15] = [Columns]rune{}
			s.col = 0
			update(f)
		case c == [2]byte{0x14, enm}:
			s.hidden = [16][Columns]rune{}
		case c == [2]byte{0x14, eoc}:
			s.shown, s.hidden = s.hidden, s.shown
			update(f)
		case c == [2]byte{0x14, edm}:
			s.shown = [16][Columns]rune{}
			update(f)
		case c[0] == 0x17 && c[1] >= 0x21 && c[1] <= 0x23:
			s.col += int(c[1] - 0x20)
		case c[1] >= 0x40:
			r, ok := rows[[2]byte{c[0], c[1] &^ 0x1f}]
			if !ok {
				t.Fatalf("frame %d: bad PAC % x", f, c)
			}
			s.row, s.col = r, int(c[1]&0x0e)*2
		case c[0] == 0x11:
			put(back[c])
		case c[0] == 0x12 || c[0] == 0x13:
			s.col = max(s.col-1, 0)
			put(back[c])
		default:
			t.Fatalf("frame %d: unexpected % x", f, c)
		}
	}
	update(frames)
	return s.events
}

func TestTrackSchedule(t *testing.T) {
	long := "A caption long enough to need\nmost of a second to load in"
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	cues := []Cue{
		{Start: s(0), End: s(1), Text: "From the start"},          // can't load before it, so late
		{Start: s(2), End: s(3), Text: "Two to three"},            // ordinary
		{Start: s(3), End: s(5), Text: "Straight on, no blank"},   // loaded while the last shows
		{Start: s(5.1), End: s(6), Text: "A small gap"},           // the last stays on through it, and it runs on to be read
		{Start: s(10), End: s(10.5), Text: "Short"},               //
		{Start: s(11), End: s(12), Text: long},                    // loaded around the blank before it, late
		{Start: s(13), End: s(17), Text: "Long"},                  //
		{Start: s(17.6), End: s(19), Text: long},                  // loaded before the blank, and runs on to the next
		{Start: s(20), End: s(20.3), Text: "¡Olé! ♪ ½ — «ça»"},    // special and extended characters
		{Start: s(20.3), End: s(20.6), Text: "Too fast: one"},     //
		{Start: s(20.6), End: s(20.9), Text: "Too fast: and two"}, //
	}
	got := decodeTrack(t, NewTrack(cues, 30), 30*30)
	frame := func(d time.Duration) int64 { return int64(d * 30 / time.Second) }
	var lines []string
	for _, e := range got {
		lines = append(lines, fmt.Sprintf("%d-%d %q", e.start, e.end, e.text))
	}
	t.Logf("shown:\n%s", strings.Join(lines, "\n"))
	if len(got) != len(cues) {
		t.Fatalf("%d captions shown, want %d", len(got), len(cues))
	}
	for i, c := range cues {
		g := got[i]
		if g.text != c.Text {
			t.Errorf("caption %d shows %q, want %q", i, g.text, c.Text)
		}
		if g.start < frame(c.Start) {
			t.Errorf("caption %d early: frame %d, want %d", i, g.start, frame(c.Start))
		}
	}
	exact := map[int][2]int64{1: {60, 90}, 2: {90, 153}, 3: {153, 183}, 6: {390, 510}, 7: {528, 600}}
	for i, w := range exact {
		if g := got[i]; g.start != w[0] || g.end != w[1] {
			t.Errorf("caption %d shown %d-%d, want %d-%d", i, g.start, g.end, w[0], w[1])
		}
	}
	// Late ones are late by their loading time, no more.
	if g := got[0]; g.start > 2+int64(len(loadCaption(cues[0].Text))) {
		t.Errorf("first caption at frame %d", g.start)
	}
	if g := got[5]; g.start > frame(cues[4].Start+time.Second)+2+int64(len(loadCaption(long))) {
		t.Errorf("caption 5 at frame %d", g.start)
	}
}

// A caption shortly after another clears it in among its own loading, so
// it still shows on time.
func TestPopOnAfterShortGap(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	cues := []Cue{
		{Start: s(0.5), End: s(3.7), Text: "but yet we still continue to wander on."},
		{Start: s(3.95), End: s(6), Text: "We always find ourselves crying out."},
	}
	got := decodeTrack(t, NewTrack(cues, 30), 300)
	if len(got) != 2 || got[0].end != 111 || got[1].start != 119 {
		t.Errorf("shown %+v, want the first cleared at frame 111 and the second on at 119", got)
	}
}

// Tuning in mid-sentence shows a line of what was just said, then words as
// they're said.
func TestRollUpTuningIn(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	before := []Word{{s(-3), "and"}, {s(-2.8), "I"}, {s(-2.5), "don't"}, {s(-2.2), "believe"}, {s(-2), "that"},
		{s(-1.5), "it's"}, {s(-1.2), "actually"}, {s(-0.8), "haunted."}, {s(-0.4), "So,"}, {s(-0.2), "I"}}
	cues := Shift([]Cue{
		{Start: s(-3), End: s(1), Words: before},
		{Start: s(0.5), End: s(3), Words: []Word{{s(0.5), "don't"}, {s(0.8), "want"}}},
	}, 0)
	got := decodeTrack(t, NewTrack(cues, 30), 120)
	if len(got) == 0 || len(got[len(got)-1].text) > 2*Columns {
		t.Fatalf("shown %+v", got)
	}
	final := got[len(got)-1]
	if final.text != "it's actually haunted. So, I\ndon't want" {
		t.Errorf("shows %q", final.text)
	}
	for _, e := range got {
		if strings.Contains(e.text, "believe") {
			t.Errorf("shows %q, from well before tuning in", e.text)
		}
	}
	if cues[0].Words[0].Text != "and" {
		t.Error("NewTrack changed its argument")
	}
}

func TestRollUpBytes(t *testing.T) {
	track := NewTrack([]Cue{{Start: time.Second, End: 2 * time.Second, Text: "Hi you",
		Words: []Word{{time.Second, "Hi"}, {1500 * time.Millisecond, "you"}}}}, 30)
	want := map[int64][2]byte{
		0: {0x94, 0x2c}, 1: {0x94, 0x2c}, // EDM
		30: {0x94, 0x25}, 31: {0x94, 0x25}, // RU2: roll-up, two rows
		32: {0x94, 0x70}, 33: {0x94, 0x70}, // PAC: row 15, column 0
		34: {0xc8, 0xe9},                   // "Hi" as it's said
		45: {0x20, 0x79}, 46: {0xef, 0x75}, // " you", half a second on
		60: {0x94, 0x2c}, 61: {0x94, 0x2c}, // EDM at the end
	}
	for f := int64(0); f < 100; f++ {
		b1, b2 := track.Pair(f)
		w, ok := want[f]
		if !ok {
			w = [2]byte{Padding, Padding}
		}
		if [2]byte{b1, b2} != w {
			t.Errorf("frame %d: % x, want % x", f, []byte{b1, b2}, w[:])
		}
	}
}

// Words timed one by one roll up as they're said, two rows at a time, and
// a pause clears the screen.
func TestRollUp(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	words := func(at ...any) (out []Word) {
		for i := 0; i < len(at); i += 2 {
			out = append(out, Word{s(at[i].(float64)), at[i+1].(string)})
		}
		return out
	}
	cues := []Cue{
		{Start: s(1), End: s(4), Words: words(1.0, "Hello", 1.3, "there", 1.6, "everybody,", 2.0, "welcome", 2.3, "back", 2.5, "to", 2.6, "the", 2.8, "channel.")},
		{Start: s(4.2), End: s(6), Words: words(4.2, "Today", 4.5, "we", 4.7, "play")},
		{Start: s(9), End: s(10), Words: words(9.0, "Bye")},
	}
	got := decodeTrack(t, NewTrack(cues, 30), 400)
	var lines []string
	at := map[string][2]int64{}
	for _, e := range got {
		lines = append(lines, fmt.Sprintf("%d-%d %q", e.start, e.end, e.text))
		at[e.text] = [2]int64{e.start, e.end}
	}
	t.Logf("shown:\n%s", strings.Join(lines, "\n"))
	for text, when := range map[string][2]int64{
		"Hello":                                {34, 36},
		"Hello there":                          {39, 41},
		"Hello there everybody, welcome\nback": {69, 73},
		"back to the channel. Today we\nplay":  {141, 145},
		"Bye":                                  {274, 276},
	} {
		got, ok := at[text]
		if !ok || got[0] < when[0] || got[0] > when[1] {
			t.Errorf("%q shown from %v, want frame %d to %d", text, got, when[0], when[1])
		}
	}
	if w := at["back to the channel. Today we\nplay"]; w[1] != 180 {
		t.Errorf("not cleared at the pause: %v", w)
	}
	if w := at["Bye"]; w[1] != 300 {
		t.Errorf("not cleared at the end: %v", w)
	}
}

func TestControlCodesSentTwice(t *testing.T) {
	cues := []Cue{
		{Start: time.Second, End: 2 * time.Second, Text: "♪♪ ♪ «Ça» ü"},
		{Start: 2 * time.Second, End: 3 * time.Second, Text: "Line one\nline two"},
		{Start: 4 * time.Second, End: 5 * time.Second, Text: "ÄÖÜ"},
	}
	track := NewTrack(cues, 30)
	var run [][2]byte
	check := func(f int64) {
		if len(run) > 0 && run[0][0]&0x70 == 0x10 && len(run) != 2 {
			t.Errorf("frame %d: % x sent %d times in a row", f, run[0], len(run))
		}
	}
	for f := range int64(200) {
		b1, b2 := track.Pair(f)
		p := [2]byte{b1, b2}
		if len(run) > 0 && run[0] == p {
			run = append(run, p)
			continue
		}
		check(f)
		run = [][2]byte{p}
	}
	check(200)
}
