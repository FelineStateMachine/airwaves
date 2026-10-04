package cc

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Script is a stream's captions as text, timed from the stream's start,
// for players that show WebVTT themselves rather than decoding CEA-608.
// The stream fills it in as each video starts; a player's packager reads
// it as the stream's segments are made. Its zero value is empty and ready.
type Script struct {
	mu   sync.Mutex
	cues []Cue // by start
}

// Set puts cues in the script for the time from from until until, in
// place of what was there. Cues are clipped to that time; those that were
// showing at from end there.
func (s *Script) Set(from, until time.Duration, cues []Cue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.cues[:0]
	for _, c := range s.cues {
		switch {
		case c.Start >= from && c.Start < until:
			continue
		case c.Start < from && c.End > from:
			c.End = from
		}
		kept = append(kept, c)
	}
	s.cues = kept
	for _, c := range cues {
		if c.Start < from || c.Start >= until {
			continue
		}
		c.End = min(c.End, until)
		s.cues = append(s.cues, c)
	}
	slices.SortStableFunc(s.cues, func(a, b Cue) int { return cmp.Compare(a.Start, b.Start) })
}

// Between returns the cues showing at some time from from until until.
func (s *Script) Between(from, until time.Duration) []Cue {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Cue
	for _, c := range s.cues {
		if c.Start >= until {
			break
		}
		if c.End > from {
			out = append(out, c)
		}
	}
	return out
}

// Forget drops cues that ended before t, which nothing will show again.
func (s *Script) Forget(t time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cues = slices.DeleteFunc(s.cues, func(c Cue) bool { return c.End < t })
}

// VTTTime formats a time as WebVTT does: "01:02:03.456".
func VTTTime(d time.Duration) string {
	d = max(d, 0)
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

var vttEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Screen turns a video's cues into ones for players that show text: one
// at a time, never overlapping. Cues that start together join, a cue
// ends when the next starts, and a cue the same as the one before runs
// on. Captions timed word by word come as lines, each showing until the
// line after next begins (YouTube's speech recognition): they roll up as
// on TV, two lines a screen, the last line moving up as the next begins,
// each line once, and only the bottom one's words timed.
func Screen(cues []Cue) []Cue {
	if slices.ContainsFunc(cues, func(c Cue) bool { return len(c.Words) > 0 }) {
		return rollUpScreens(cues)
	}
	var out []Cue
	cues = merge(cues)
	for i, c := range cues {
		if i+1 < len(cues) {
			c.End = min(c.End, cues[i+1].Start)
		}
		if c.End <= c.Start {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Text == c.Text && out[n-1].End >= c.Start-time.Millisecond {
			out[n-1].End = c.End
			continue
		}
		out = append(out, c)
	}
	return out
}

// rollUpScreens composes the screens of lines timed word by word.
func rollUpScreens(cues []Cue) []Cue {
	cues = slices.Clone(cues)
	slices.SortStableFunc(cues, func(a, b Cue) int { return cmp.Compare(a.Start, b.Start) })
	var out []Cue
	var above Cue // the line before, while it shows
	for i, c := range cues {
		line := strings.Join(strings.Fields(strings.ReplaceAll(c.Text, "\n", " ")), " ")
		end := c.End
		if i+1 < len(cues) {
			end = min(end, cues[i+1].Start)
		}
		if line == "" || end <= c.Start {
			continue
		}
		s := Cue{Start: c.Start, End: end, Text: line, Words: c.Words}
		if above.End > c.Start && above.Text != line {
			s.Text = above.Text + "\n" + line
		}
		out = append(out, s)
		above = Cue{End: c.End, Text: line}
	}
	return out
}

// AppendVTT appends a cue in WebVTT to b. A cue timed word by word has
// each word of its last line after the first marked with when it's said
// ("<00:00:01.500>word"), for players that reveal words as they come.
func AppendVTT(b []byte, c Cue) []byte {
	b = fmt.Appendf(b, "%s --> %s\n", VTTTime(c.Start), VTTTime(c.End))
	if len(c.Words) > 0 {
		lines := strings.Split(c.Text, "\n")
		for _, l := range lines[:len(lines)-1] {
			if l = strings.TrimSpace(l); l != "" {
				b = append(append(b, vttEscape.Replace(l)...), '\n')
			}
		}
		last := c.Start
		for i, w := range c.Words {
			if i > 0 {
				b = append(b, ' ')
				if w.Start > last && w.Start < c.End {
					b = fmt.Appendf(b, "<%s>", VTTTime(w.Start))
					last = w.Start
				}
			}
			b = append(b, vttEscape.Replace(w.Text)...)
		}
		return append(b, "\n\n"...)
	}
	for l := range strings.SplitSeq(c.Text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			b = append(append(b, vttEscape.Replace(l)...), '\n')
		}
	}
	return append(b, '\n')
}
