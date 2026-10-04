package cc

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Captions on a CEA-608 screen hold two lines of 32 characters, so a long
// cue is split into several shown in turn. They split where a reader would
// pause, at the end of a sentence or clause before anywhere else, with
// lines of about equal length, and each shows long enough to read.

const (
	// readingRate is how many characters a second a caption is shown for,
	// and minShow the least time any is.
	readingRate = 18
	minShow     = time.Second
)

// showFor is how long a caption of n characters needs to be read.
func showFor(n int) time.Duration {
	return max(minShow, time.Duration(n)*time.Second/readingRate)
}

// captions608 lays cues out for CEA-608's screen, as captions of at most
// two lines of 32 characters each. A cue too short to read runs on into
// the gap after it, never past the next cue's start.
func captions608(cues []Cue) []Cue {
	cues = merge(cues)
	var out []Cue
	for i, c := range cues {
		limit := time.Duration(math.MaxInt64)
		if i+1 < len(cues) {
			limit = cues[i+1].Start
		}
		out = append(out, pace(c.Start, c.End, limit, chunk(c.Text))...)
	}
	return out
}

// merge sorts cues and joins those starting together, which a single
// pop-on caption must show at once.
func merge(cues []Cue) []Cue {
	cues = slices.Clone(cues)
	slices.SortStableFunc(cues, func(a, b Cue) int { return cmp.Compare(a.Start, b.Start) })
	var out []Cue
	for _, c := range cues {
		if n := len(out); n > 0 && c.Start-out[n-1].Start < 20*time.Millisecond {
			out[n-1].Text += "\n" + c.Text
			out[n-1].End = max(out[n-1].End, c.End)
			continue
		}
		out = append(out, c)
	}
	return out
}

// pace times a cue's captions: from start, sharing its time out by
// length, running past end to give each long enough to read, but not past
// limit.
func pace(start, end, limit time.Duration, chunks []string) []Cue {
	var need time.Duration
	weights, total := make([]int, len(chunks)), 0
	for i, ch := range chunks {
		n := len([]rune(ch))
		need += showFor(n)
		weights[i] = n + 8 // so short captions get a fair share
		total += weights[i]
	}
	stop := min(max(end, start+need), limit)
	if len(chunks) == 0 || stop <= start {
		return nil
	}
	out := make([]Cue, 0, len(chunks))
	at, done := start, 0
	for i, ch := range chunks {
		done += weights[i]
		next := start + (stop-start)*time.Duration(done)/time.Duration(total)
		out = append(out, Cue{Start: at, End: next, Text: ch})
		at = next
	}
	return out
}

// token is a word of a caption on its way to 608.
type token struct {
	text     string
	n        int  // length in characters
	dialogue bool // begins a line of dialogue ("- Who's there?")
}

// chunk splits a cue's text into captions of at most Rows lines of
// Columns characters, in characters 608 has. Lines that already fit are
// kept as the source broke them.
func chunk(text string) []string {
	var lines []string
	for l := range strings.SplitSeq(text, "\n") {
		if l = strings.Join(strings.Fields(fold(l)), " "); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) <= Rows && !slices.ContainsFunc(lines, func(l string) bool { return len([]rune(l)) > Columns }) {
		return []string{strings.Join(lines, "\n")}
	}
	var toks []token
	for _, l := range lines {
		for i, w := range strings.Fields(l) {
			r := []rune(w)
			for first := true; len(r) > 0; first = false { // a word longer than a line goes in pieces
				piece := r[:min(len(r), Columns)]
				r = r[len(piece):]
				toks = append(toks, token{text: string(piece), n: len(piece), dialogue: first && i == 0 && speaker(w)})
			}
		}
	}
	// The cheapest way to split the words into captions that fit, where
	// each caption costs, and more so if it ends mid-phrase or is short.
	n := len(toks)
	cost, from := make([]int, n+1), make([]int, n+1)
	for i := 1; i <= n; i++ {
		cost[i] = math.MaxInt
		chars := -1
		for j := i - 1; j >= 0; j-- {
			chars += toks[j].n + 1
			if chars > Rows*Columns+1 {
				break
			}
			if cost[j] == math.MaxInt || layout(toks[j:i]) == nil {
				continue
			}
			c := cost[j] + 10
			if i < n {
				c += breakCost(toks[i-1], toks[i])
			}
			if chars < 24 && (j > 0 || i < n) {
				c += 4
			}
			if c < cost[i] {
				cost[i], from[i] = c, j
			}
		}
	}
	var out []string
	for i := n; i > 0; i = from[i] {
		out = append(out, strings.Join(layout(toks[from[i]:i]), "\n"))
	}
	slices.Reverse(out)
	return out
}

// layout puts words on one line, or two of about equal length broken
// where a reader would pause, or at a change of speaker. It returns nil
// when they don't fit.
func layout(toks []token) []string {
	join := func(ts []token) string {
		var b strings.Builder
		for i, t := range ts {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(t.text)
		}
		return b.String()
	}
	width := func(ts []token) int {
		n := len(ts) - 1
		for _, t := range ts {
			n += t.n
		}
		return n
	}
	speakers := 0
	for _, t := range toks[1:] {
		if t.dialogue {
			speakers++
		}
	}
	best, at := math.MaxInt, -1
	for k := 1; k < len(toks); k++ {
		a, b := width(toks[:k]), width(toks[k:])
		if a > Columns || b > Columns || (speakers > 0 && !toks[k].dialogue) {
			continue
		}
		c := abs(a-b) + breakCost(toks[k-1], toks[k])/2
		if a > b {
			c += 2 // the longer line below reads better
		}
		if c < best {
			best, at = c, k
		}
	}
	switch {
	case speakers > 1:
		return nil
	case speakers == 0 && width(toks) <= Columns:
		return []string{join(toks)}
	case at < 0:
		return nil
	}
	return []string{join(toks[:at]), join(toks[at:])}
}

func abs(n int) int { return max(n, -n) }

// conjunctions begin clauses, so are good places to break before.
var conjunctions = map[string]bool{
	"and": true, "but": true, "or": true, "so": true, "because": true, "if": true, "when": true,
	"while": true, "that": true, "which": true, "who": true, "where": true, "then": true,
	"though": true, "although": true, "until": true, "unless": true, "since": true,
	"after": true, "before": true, "as": true, "than": true,
}

// breakCost is how badly breaking between two words interrupts a reader:
// not at all at the end of a sentence or a change of speaker, a little
// after a comma or before a conjunction, and most mid-phrase.
func breakCost(a, b token) int {
	end := strings.TrimRightFunc(a.text, func(r rune) bool { return strings.ContainsRune("\"')]}»", r) })
	last, _ := lastRune(end)
	switch {
	case b.dialogue || strings.ContainsRune(".!?♪", last):
		return 0
	case strings.ContainsRune(",;:—", last) || isDash(b.text):
		return 3
	case conjunctions[strings.ToLower(strings.TrimFunc(b.text, func(r rune) bool { return !unicode.IsLetter(r) }))]:
		return 6
	}
	return 12
}

func lastRune(s string) (rune, bool) {
	r := []rune(s)
	if len(r) == 0 {
		return 0, false
	}
	return r[len(r)-1], true
}

// isDash reports whether a word is a dash: "-", "--" or "—".
func isDash(w string) bool { return w == "-" || w == "--" || w == "—" }

// speaker reports whether a line's first word marks a change of speaker:
// "- Hi" or "-Hi".
func speaker(w string) bool {
	r := []rune(w)
	return isDash(w) || len(r) > 1 && r[0] == '-' && unicode.IsLetter(r[1])
}

// fold maps text to characters CEA-608 can show: lookalikes stand in for
// the rest, such as "..." for an ellipsis and plain letters for accented
// ones it lacks, and anything else is dropped.
func fold(s string) string {
	var b strings.Builder
	for _, r := range s {
		_, ok := charset[r]
		switch {
		case ok:
			b.WriteRune(r)
		case substitutes[r] != "":
			b.WriteString(substitutes[r])
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// substitutes stand in for characters CEA-608 lacks.
var substitutes = map[rune]string{
	'`': "'", '‘': "'", '’': "'", '‚': "'", '‛': "'", '′': "'", '´': "'",
	'„': "\"", '‟': "\"", '″': "\"",
	'…': "...", '‐': "-", '‑': "-", '‒': "-", '–': "-", '―': "—", '−': "-",
	'♫': "♪", '♩': "♪", '♬': "♪",
	'‹': "<", '›': ">",
	'Æ': "AE", 'æ': "ae", 'Œ': "OE", 'œ': "oe", 'Þ': "Th", 'þ': "th",
	'Ð': "D", 'ð': "d", 'Ý': "Y", 'ý': "y", 'ÿ': "y", 'Ÿ': "Y",
	'¹': "1", '²': "2", '³': "3", '¼': "1/4", '¾': "3/4",
	'×': "x", '¬': "-", 'ª': "a", 'º': "o",
	'Ā': "A", 'ā': "a", 'Ă': "A", 'ă': "a", 'Ą': "A", 'ą': "a",
	'Ć': "C", 'ć': "c", 'Č': "C", 'č': "c", 'Ď': "D", 'ď': "d", 'Đ': "D", 'đ': "d",
	'Ē': "E", 'ē': "e", 'Ė': "E", 'ė': "e", 'Ę': "E", 'ę': "e", 'Ě': "E", 'ě': "e",
	'Ğ': "G", 'ğ': "g", 'İ': "I", 'ı': "i", 'Ī': "I", 'ī': "i",
	'Ł': "L", 'ł': "l", 'Ń': "N", 'ń': "n", 'Ň': "N", 'ň': "n",
	'Ō': "O", 'ō': "o", 'Ő': "O", 'ő': "o", 'Ř': "R", 'ř': "r",
	'Ś': "S", 'ś': "s", 'Ş': "S", 'ş': "s", 'Š': "S", 'š': "s",
	'Ţ': "T", 'ţ': "t", 'Ť': "T", 'ť': "t",
	'Ū': "U", 'ū': "u", 'Ů': "U", 'ů': "u", 'Ű': "U", 'ű': "u",
	'Ź': "Z", 'ź': "z", 'Ż': "Z", 'ż': "z", 'Ž': "Z", 'ž': "z",
}
