package cc

import (
	"cmp"
	"math/bits"
	"slices"
	"strings"
	"time"
)

// Track is captions as CEA-608 captions on CC1, the way broadcast TV sends
// them. Subtitles go as pop-on captions: each is loaded off screen ahead of
// its time, shown whole at its start and cleared at its end. Captions timed
// word by word (speech recognition's) roll up two lines at a time as live
// TV's do, each word appearing as it's said. Every video frame carries one
// byte pair, so a caption takes a frame per two letters to send.
type Track struct {
	frames []int64 // when each pair is sent, ascending
	pairs  [][2]byte
}

// Padding is the byte pair sent when there is nothing to say.
const Padding = 0x80

// CC1 control codes (the second byte; the first is 0x14), each sent twice
// in a row so a decoder that misses one still acts.
const (
	rcl = 0x20 // resume caption loading: pop-on mode
	ru2 = 0x25 // roll-up captions, two rows
	edm = 0x2c // erase displayed memory
	cr  = 0x2d // carriage return: roll up a row
	enm = 0x2e // erase non-displayed memory
	eoc = 0x2f // end of caption: show what was loaded
)

// NewTrack encodes cues for video at fps frames a second, cue times
// counting from frame 0: rolling up if they time their words, else as
// pop-on captions laid out for the screen. Frame 0 clears the screen, so a
// track can take over from another mid-stream.
func NewTrack(cues []Cue, fps int) *Track {
	t := &Track{}
	t.add(0, twice(nil, control(edm))...)
	if slices.ContainsFunc(cues, func(c Cue) bool { return len(c.Words) > 0 }) {
		t.rollUp(cues, fps)
	} else {
		t.popOn(captions608(cues), fps)
	}
	t.sort()
	return t
}

// frames converts a time to frames at fps.
func frames(d time.Duration, fps int) int64 {
	return (int64(d)*int64(fps) + int64(time.Second)/2) / int64(time.Second)
}

// popOn sends captions that fit the screen as pop-on captions.
func (t *Track) popOn(cues []Cue, fps int) {
	frame := func(d time.Duration) int64 { return frames(d, fps) }
	// Shorter gaps between captions aren't blanked, which would flicker.
	minGap := max(int64(fps)/5, 3)
	minShow := int64(fps) / 2

	cursor := int64(2)   // the first frame free for loading
	clearAt := int64(-1) // when the caption showing is due off
	for _, c := range cues {
		load := loadCaption(c.Text)
		if len(load) == 0 {
			continue
		}
		n, start := int64(len(load)), max(frame(c.Start), 0)
		blank := clearAt >= 0 && start-clearAt >= minGap
		// Load as late as fits, so a viewer tuning in mid-caption soon sees
		// the next: just before this one shows, and with the caption before
		// to clear first, around that.
		var end int64 // the frame after loading
		switch {
		case !blank:
			at := max(start-n, cursor)
			t.add(at, load...)
			end = at + n
		case start-n >= clearAt+2:
			t.add(clearAt, twice(nil, control(edm))...)
			t.add(start-n, load...)
			end = start
		default:
			// The erase goes in among the load, at its time, but not
			// between a code and its repeat where that would act twice.
			at := max(start-n-2, cursor)
			k := min(max(clearAt-at, 0), n)
			if k > 0 && k < n && load[k-1] == load[k] && load[k][0] < 0x20 && load[k][1] < 0x40 {
				k++
			}
			t.add(at, load[:k]...)
			t.add(at+k, twice(nil, control(edm))...)
			t.add(at+k+2, load[k:]...)
			end = at + n + 2
		}
		show := max(start, end)
		t.add(show, twice(nil, control(eoc))...)
		cursor = show + 2
		clearAt = max(frame(c.End), show+minShow, cursor)
	}
	if clearAt >= 0 {
		t.add(clearAt, twice(nil, control(edm))...)
	}
}

// rollUp sends captions word by word, each as it's said, rolling up two
// rows on the bottom of the screen. A pause of a second clears it.
func (t *Track) rollUp(cues []Cue, fps int) {
	frame := func(d time.Duration) int64 { return frames(d, fps) }
	cursor := int64(2) // the first frame free
	col := -1          // where the next character goes; -1 with the screen clear
	last := int64(-1)  // when the last caption ends
	// Of the words said before the track begins, a viewer tuning in
	// mid-sentence gets a line's worth, not a rush of them.
	said, dropped := Columns, map[[2]int]bool{}
	for i := len(cues) - 1; i >= 0; i-- {
		for j := len(cues[i].Words) - 1; j >= 0; j-- {
			if w := cues[i].Words[j]; w.Start <= 0 {
				if said -= len([]rune(w.Text)) + 1; said < 0 {
					dropped[[2]int{i, j}] = true
				}
			}
		}
	}
	for i, c := range cues {
		words := c.Words
		if len(words) == 0 {
			words = spread(c)
		}
		if col >= 0 && frame(c.Start) > last+int64(fps) {
			at := max(last, cursor)
			t.add(at, twice(nil, control(edm))...)
			cursor, col = at+2, -1
		}
		for j, w := range words {
			if dropped[[2]int{i, j}] {
				continue
			}
			var chars []char608
			for _, r := range strings.Join(strings.Fields(fold(w.Text)), " ") {
				if ch, ok := charset[r]; ok {
					chars = append(chars, ch)
				}
			}
			for len(chars) > 0 {
				piece := chars[:min(len(chars), Columns)]
				chars = chars[len(piece):]
				var p [][2]byte
				switch {
				case col < 0:
					p = twice(twice(nil, control(ru2)), pac(15, 0))
					col = 0
				case col+1+len(piece) > Columns:
					p = twice(nil, control(cr))
					col = 0
				default:
					piece = append([]char608{charset[' ']}, piece...)
				}
				p = appendChars(p, piece)
				col += len(piece)
				at := max(frame(w.Start), cursor)
				t.add(at, p...)
				cursor = at + int64(len(p))
			}
		}
		last = max(last, frame(c.End), cursor)
	}
	if col >= 0 {
		t.add(max(last, cursor), twice(nil, control(edm))...)
	}
}

// spread times a caption's words evenly over most of its time.
func spread(c Cue) []Word {
	fields := strings.Fields(c.Text)
	out := make([]Word, len(fields))
	for i, f := range fields {
		out[i] = Word{Start: c.Start + (c.End-c.Start)*4/5*time.Duration(i)/time.Duration(len(fields)), Text: f}
	}
	return out
}

// add sends pairs on consecutive frames from frame on.
func (t *Track) add(frame int64, pairs ...[2]byte) {
	for i, p := range pairs {
		t.frames = append(t.frames, frame+int64(i))
		t.pairs = append(t.pairs, [2]byte{parity(p[0]), parity(p[1])})
	}
}

func (t *Track) sort() {
	idx := make([]int, len(t.frames))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(t.frames[a], t.frames[b]) })
	frames, pairs := make([]int64, len(idx)), make([][2]byte, len(idx))
	for i, j := range idx {
		frames[i], pairs[i] = t.frames[j], t.pairs[j]
	}
	t.frames, t.pairs = frames, pairs
}

// Pair returns the field 1 byte pair, with parity, for a frame: Padding,
// Padding when there's nothing to send.
func (t *Track) Pair(frame int64) (byte, byte) {
	if t == nil {
		return Padding, Padding
	}
	i, ok := slices.BinarySearch(t.frames, frame)
	if !ok {
		return Padding, Padding
	}
	return t.pairs[i][0], t.pairs[i][1]
}

// control is a CC1 control code.
func control(code byte) [2]byte { return [2]byte{0x14, code} }

// loadCaption is the pairs that load a caption into non-displayed memory,
// without parity: its lines centered on the bottom rows. It is in
// characters 608 has, as chunk leaves it.
func loadCaption(text string) [][2]byte {
	lines := strings.Split(text, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	p := twice(nil, control(rcl))
	p = twice(p, control(enm))
	empty := true
	for i, line := range lines {
		var chars []char608
		for _, r := range line {
			if c, ok := charset[r]; ok {
				chars = append(chars, c)
			}
		}
		chars = chars[:min(len(chars), Columns)]
		if len(chars) == 0 {
			continue
		}
		empty = false
		col := (Columns - len(chars)) / 2
		p = twice(p, pac(15-len(lines)+1+i, col))
		if col%4 > 0 {
			p = twice(p, [2]byte{0x17, 0x20 + byte(col%4)}) // tab offset
		}
		p = appendChars(p, chars)
	}
	if empty {
		return nil
	}
	return p
}

func twice(p [][2]byte, code [2]byte) [][2]byte { return append(p, code, code) }

// pac is the preamble address code that starts a line, in white, at row
// (1 to 15) and col, rounded down to a multiple of 4 (a tab offset makes
// up the rest).
func pac(row, col int) [2]byte {
	rows := [16][2]byte{
		1: {0x11, 0x40}, 2: {0x11, 0x60}, 3: {0x12, 0x40}, 4: {0x12, 0x60}, 5: {0x15, 0x40},
		6: {0x15, 0x60}, 7: {0x16, 0x40}, 8: {0x16, 0x60}, 9: {0x17, 0x40}, 10: {0x17, 0x60},
		11: {0x10, 0x40}, 12: {0x13, 0x40}, 13: {0x13, 0x60}, 14: {0x14, 0x40}, 15: {0x14, 0x60},
	}
	c := rows[max(1, min(row, 15))]
	c[1] |= 0x10 | byte(col/4)<<1
	return c
}

// appendChars sends a line's characters: basic ones two to a pair, and
// special and extended ones as their own codes, twice like control codes.
// An extended character follows the basic one decoders without it show,
// which it replaces.
func appendChars(p [][2]byte, chars []char608) [][2]byte {
	var held byte // a basic character waiting for a partner
	basic := func(b byte) {
		if held == 0 {
			held = b
			return
		}
		p = append(p, [2]byte{held, b})
		held = 0
	}
	var last [2]byte
	for _, c := range chars {
		if c.code[1] == 0 {
			basic(c.code[0])
			last = [2]byte{}
			continue
		}
		if c.code == last && c.alt == 0 {
			// A repeat would be taken for the second sending of the last.
			basic(' ')
		}
		if c.alt != 0 {
			basic(c.alt)
		}
		if held != 0 {
			p = append(p, [2]byte{held, 0})
			held = 0
		}
		p = twice(p, c.code)
		last = c.code
	}
	if held != 0 {
		p = append(p, [2]byte{held, 0})
	}
	return p
}

// parity sets the top bit of b so it has an odd number of ones.
func parity(b byte) byte {
	b &= 0x7f
	if bits.OnesCount8(b)%2 == 0 {
		b |= 0x80
	}
	return b
}

// char608 is how a character is sent: a basic character alone in code[0],
// else a two-byte code with, for an extended character, alt, the basic
// character it replaces on screen.
type char608 struct {
	code [2]byte
	alt  byte
}

// charset is every character CEA-608 can show, in CC1.
var charset = func() map[rune]char608 {
	m := map[rune]char608{}
	for r := rune(0x20); r < 0x7f; r++ {
		m[r] = char608{code: [2]byte{byte(r)}}
	}
	// The basic set swaps some ASCII for accented letters; the extended
	// sets below have the ASCII back.
	for b, r := range map[byte]rune{
		0x2a: 'á', 0x5c: 'é', 0x5e: 'í', 0x5f: 'ó', 0x60: 'ú', 0x7b: 'ç', 0x7c: '÷', 0x7d: 'Ñ', 0x7e: 'ñ', 0x7f: '█',
	} {
		delete(m, rune(b))
		m[r] = char608{code: [2]byte{b}}
	}
	for i, r := range []rune("®°½¿™¢£♪à\x00èâêîôû") {
		if r != 0 {
			m[r] = char608{code: [2]byte{0x11, 0x30 + byte(i)}}
		}
	}
	// Single quote marks go as apostrophes, leaving out 0x1226 and 0x1229:
	// decoders differ on which is which.
	extended := []struct {
		hi        byte
		set, alts string
	}{
		{0x12, "ÁÉÓÚÜü\x00¡*\x00—©℠•“”ÀÂÇÈÊËëÎÏïÔÙùÛ«»", "AEOUUu !- -cs.\"\"AACEEEeIIiOUuU\"\""},
		{0x13, "ÃãÍÌìÒòÕõ{}\\^_|~ÄäÖöß¥¤¦ÅåØø┌┐└┘", "AaIIiOoOo()/ -:-AaOosY$:AaOo++++"},
	}
	for _, e := range extended {
		alts := []byte(e.alts)
		for i, r := range []rune(e.set) {
			if r != 0 {
				m[r] = char608{code: [2]byte{e.hi, 0x20 + byte(i)}, alt: alts[i]}
			}
		}
	}
	return m
}()
