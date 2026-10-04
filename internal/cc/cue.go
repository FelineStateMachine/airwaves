// Package cc makes closed captions: it reads SRT, WebVTT and YouTube's
// json3 subtitles, encodes them as CEA-608 captions (what a TV's CC button
// turns on) and adds those to the H.264 picture of an MPEG-TS stream as it
// is written, and keeps them as text for players that show WebVTT.
package cc

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Cue is a caption and when it shows.
type Cue struct {
	Start time.Duration
	End   time.Duration
	// Text is the caption's lines, separated by "\n", as its source broke
	// them.
	Text string
	// Words, when the source times each word (YouTube's automatic
	// captions), are the words of the caption's last line and when each is
	// spoken; that line of Text is them with spaces between.
	Words []Word
}

// Word is a word of a caption and when it's spoken.
type Word struct {
	Start time.Duration
	Text  string
}

const (
	// Columns and Rows bound a CEA-608 caption: its screen is 32 columns,
	// and pop-on captions rarely run to more than two rows.
	Columns = 32
	Rows    = 2
)

var (
	timing = regexp.MustCompile(`^\s*((?:\d+:)?\d{1,2}:\d{1,2}(?:[,.]\d+)?)\s*-->\s*((?:\d+:)?\d{1,2}:\d{1,2}(?:[,.]\d+)?)`)
	number = regexp.MustCompile(`^\s*\d+\s*$`)
	// markup is HTML-style tags (<i>, <font color=...>, WebVTT's <c.x>,
	// <v Name> and <00:00:01.000>) and ASS overrides ({\an8}).
	markup    = regexp.MustCompile(`<[^>\n]*>|\{\\[^}\n]*\}`)
	assBreaks = strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ",
		"¶", "♪") // a music note, as subtitles made from broadcast captions misname it
)

// ErrNoCues is returned for a file with no timed captions in it.
var ErrNoCues = errors.New("cc: no captions found")

// Parse reads SRT, WebVTT or YouTube's json3 subtitles into cues in time
// order, without styling.
func Parse(b []byte) ([]Cue, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '{' {
		return parseJSON3(t)
	}
	s := string(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	vtt := strings.HasPrefix(s, "WEBVTT")
	lines := strings.Split(s, "\n")
	var cues []Cue
	found := false
	for i := 0; i < len(lines); i++ {
		m := timing.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		found = true
		start, err1 := parseTime(m[1])
		end, err2 := parseTime(m[2])
		var text []string
		// WebVTT ends a caption at an empty line; YouTube's have lines of a
		// space within them.
		for i+1 < len(lines) && !blank(lines[i+1], vtt) && !timing.MatchString(lines[i+1]) {
			// An SRT missing the blank line before the next cue's number.
			if number.MatchString(lines[i+1]) && i+2 < len(lines) && timing.MatchString(lines[i+2]) {
				break
			}
			i++
			text = append(text, lines[i])
		}
		if err1 != nil || err2 != nil || end <= start {
			continue
		}
		if t := clean(text); len(t) > 0 {
			cues = append(cues, Cue{Start: start, End: end, Text: strings.Join(t, "\n")})
		}
	}
	if !found {
		return nil, ErrNoCues
	}
	slices.SortStableFunc(cues, func(a, b Cue) int { return cmp.Compare(a.Start, b.Start) })
	return unroll(cues), nil
}

// parseJSON3 reads YouTube's json3 subtitles. Those YouTube makes by
// speech recognition time each word, and come as lines that show two at a
// time, each until the line after next has begun.
func parseJSON3(b []byte) ([]Cue, error) {
	var doc struct {
		Events []struct {
			Start    int64 `json:"tStartMs"`
			Duration int64 `json:"dDurationMs"`
			Append   int   `json:"aAppend"`
			Segs     []struct {
				Text   string `json:"utf8"`
				Offset *int64 `json:"tOffsetMs"`
			} `json:"segs"`
		} `json:"events"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("cc: json3: %w", err)
	}
	if doc.Events == nil {
		return nil, ErrNoCues
	}
	timed := false
	for _, e := range doc.Events {
		for _, s := range e.Segs {
			timed = timed || s.Offset != nil
		}
	}
	ms := func(n int64) time.Duration { return time.Duration(n) * time.Millisecond }
	var cues []Cue
	for _, e := range doc.Events {
		if len(e.Segs) == 0 || e.Append != 0 || e.Duration <= 0 {
			continue // windows, and the line breaks of rolling captions
		}
		c := Cue{Start: ms(e.Start), End: ms(e.Start + e.Duration)}
		if timed {
			for _, s := range e.Segs {
				at := c.Start
				if s.Offset != nil {
					at += ms(*s.Offset)
				}
				for w := range strings.FieldsSeq(html.UnescapeString(s.Text)) {
					c.Words = append(c.Words, Word{Start: at, Text: w})
				}
			}
			var words []string
			for _, w := range c.Words {
				words = append(words, w.Text)
			}
			c.Text = strings.Join(words, " ")
		} else {
			var text strings.Builder
			for _, s := range e.Segs {
				text.WriteString(s.Text)
			}
			c.Text = strings.Join(clean(strings.Split(text.String(), "\n")), "\n")
		}
		if c.Text != "" {
			cues = append(cues, c)
		}
	}
	slices.SortStableFunc(cues, func(a, b Cue) int { return cmp.Compare(a.Start, b.Start) })
	return cues, nil
}

func blank(line string, vtt bool) bool {
	if vtt {
		return line == ""
	}
	return strings.TrimSpace(line) == ""
}

// parseTime reads "01:02:03,456", "01:02:03.456" or "02:03.456".
func parseTime(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	secs, frac, _ := strings.Cut(strings.ReplaceAll(parts[len(parts)-1], ",", "."), ".")
	var d time.Duration
	for _, p := range append(parts[:len(parts)-1], secs) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, fmt.Errorf("cc: bad time %q", s)
		}
		d = d*60 + time.Duration(n)
	}
	d *= time.Second
	if frac != "" {
		frac = (frac + "000000000")[:9]
		ns, err := strconv.Atoi(frac)
		if err != nil {
			return 0, fmt.Errorf("cc: bad time %q", s)
		}
		d += time.Duration(ns)
	}
	return d, nil
}

// clean strips a caption's styling and returns its lines.
func clean(lines []string) []string {
	s := assBreaks.Replace(strings.Join(lines, "\n"))
	s = html.UnescapeString(markup.ReplaceAllString(s, ""))
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// unroll turns rolling captions, where each caption repeats the last
// one's bottom lines above new ones (YouTube's automatic captions, and
// subtitles made from broadcast roll-up captions), into captions of new
// lines only. Blips between captions go too.
func unroll(cues []Cue) []Cue {
	var out []Cue
	var prev []string // the last caption's lines, as it came
	var prevEnd time.Duration
	for _, c := range cues {
		if c.End-c.Start < 50*time.Millisecond {
			continue
		}
		lines := strings.Split(c.Text, "\n")
		kept := lines
		if len(out) > 0 && c.Start-prevEnd < 100*time.Millisecond {
			for k := min(len(lines)-1, len(prev)); k > 0; k-- {
				if slices.Equal(lines[:k], prev[len(prev)-k:]) {
					kept = lines[k:]
					break
				}
			}
		}
		prev, prevEnd = lines, c.End
		c.Text = strings.Join(kept, "\n")
		out = append(out, c)
	}
	return out
}

// FormatSRT writes cues as SRT.
func FormatSRT(cues []Cue) []byte {
	var b bytes.Buffer
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(c.Start), srtTime(c.End), c.Text)
	}
	return b.Bytes()
}

func srtTime(d time.Duration) string {
	d = max(d, 0)
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// Shift moves cues by d, dropping those that end up wholly before zero and
// clipping those that straddle it.
func Shift(cues []Cue, d time.Duration) []Cue {
	var out []Cue
	for _, c := range cues {
		c.Start += d
		c.End += d
		if c.End <= 0 {
			continue
		}
		c.Start = max(c.Start, 0)
		if c.Words != nil {
			words := make([]Word, len(c.Words))
			for i, w := range c.Words {
				words[i] = Word{Start: max(w.Start+d, 0), Text: w.Text}
			}
			c.Words = words
		}
		out = append(out, c)
	}
	return out
}

// English reports whether lang, an ISO 639 code or a name as files and
// servers label tracks ("eng", "en-US", "English"), is English.
func English(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	return l == "en" || l == "eng" || l == "english"
}

// Foreign reports whether lang names a language other than English. An
// unlabeled or undetermined language isn't foreign: most such tracks are
// English.
func Foreign(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "", "und", "unknown", "mul", "mis", "zxx", "qaa":
		return false
	}
	return !English(lang)
}
