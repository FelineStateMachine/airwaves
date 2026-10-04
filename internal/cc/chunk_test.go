package cc

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestChunkAtPhrases(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		// Sentences and clauses end captions; lines are about even.
		{"I told you before, we can't go back there. The bridge is out and the road is flooded all the way to town.",
			[]string{"I told you before,\nwe can't go back there.", "The bridge is out and the road\nis flooded all the way to town."}},
		{"This line is a good deal longer than thirty-two characters",
			[]string{"This line is a good deal longer\nthan thirty-two characters"}},
		// Lines that fit stay as written, one speaker to a line.
		{"- Who's there?\n- It's me, open the door.", []string{"- Who's there?\n- It's me, open the door."}},
		{"- Who's there?\n- It's me.\n- Who is me?", []string{"- Who's there?\n- It's me.", "- Who is me?"}},
		{"[thunder rumbling]", []string{"[thunder rumbling]"}},
		// What 608 lacks is folded, and a word too long for a line is cut.
		{"Wait… “what”?", []string{"Wait... “what”?"}},
		{"Supercalifragilisticexpialidocious-and-then-some words", []string{"Supercalifragilisticexpialidocio\nus-and-then-some words"}},
		{"  \n ", nil},
	} {
		got := chunk(c.text)
		if !slices.Equal(got, c.want) {
			t.Errorf("chunk(%q)\n got %q\nwant %q", c.text, got, c.want)
		}
		for _, ch := range got {
			lines := strings.Split(ch, "\n")
			if len(lines) > Rows || slices.ContainsFunc(lines, func(l string) bool { return len([]rune(l)) > Columns }) {
				t.Errorf("%q doesn't fit", ch)
			}
		}
	}
}

func TestPace(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	// Too short to read: runs on into the gap, but not over the next cue.
	got := captions608([]Cue{
		{Start: s(1), End: s(1.4), Text: "Hi there"},
		{Start: s(5), End: s(5.2), Text: "Wait"},
		{Start: s(5.5), End: s(8), Text: "What?"},
	})
	want := []Cue{
		{Start: s(1), End: s(2), Text: "Hi there"},
		{Start: s(5), End: s(5.5), Text: "Wait"},
		{Start: s(5.5), End: s(8), Text: "What?"},
	}
	if !equalCues(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	// A long cue's captions follow each other, sharing out its time by
	// length, and longer when there's room to.
	long := "Well, I suppose if you really want to know what happened that night, you should ask your mother, because she was there and I wasn't."
	got = captions608([]Cue{{Start: s(10), End: s(14), Text: long}})
	if len(got) != 3 || got[0].Start != s(10) || got[0].End != got[1].Start || got[1].End != got[2].Start {
		t.Fatalf("captions %v", got)
	}
	if end := got[2].End; end < s(10)+showFor(len(long))-time.Second || end > s(18) {
		t.Errorf("ends at %v", end)
	}
	for _, c := range got {
		if c.End-c.Start < showFor(len([]rune(c.Text)))*9/10 {
			t.Errorf("%q shows for %v", c.Text, c.End-c.Start)
		}
	}
	// Squeezed by the next cue, they share what time there is.
	got = captions608([]Cue{{Start: s(10), End: s(14), Text: long}, {Start: s(13), End: s(14), Text: "Oh."}})
	if len(got) != 4 || got[2].End != s(13) || got[3].Start != s(13) {
		t.Errorf("squeezed %v", got)
	}
	// Cues starting together show together.
	got = captions608([]Cue{{Start: s(1), End: s(3), Text: "[door slams]"}, {Start: s(1), End: s(2), Text: "Who's there?"}})
	if len(got) != 1 || got[0].Text != "[door slams]\nWho's there?" || got[0].End != s(3) {
		t.Errorf("together %v", got)
	}
}

func TestScript(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	var sc Script
	sc.Set(0, s(100), []Cue{
		{Start: s(1), End: s(3), Text: "one"},
		{Start: s(8), End: s(12), Text: "two"},
		{Start: s(20), End: s(22), Text: "three"},
	})
	// The next video starts at 10: what was due after then goes.
	sc.Set(s(10), s(1e6), nil)
	sc.Set(s(10), s(1e6), []Cue{{Start: s(11), End: s(13), Text: "four"}, {Start: s(9), End: s(10), Text: "too early"}})
	texts := func(cs []Cue) (out []string) {
		for _, c := range cs {
			out = append(out, c.Text)
		}
		return out
	}
	if got := texts(sc.Between(0, s(100))); !slices.Equal(got, []string{"one", "two", "four"}) {
		t.Errorf("script %q", got)
	}
	if got := sc.Between(s(9), s(10)); len(got) != 1 || got[0].End != s(10) {
		t.Errorf("cut short: %v", got)
	}
	if got := texts(sc.Between(s(3), s(8))); got != nil {
		t.Errorf("between captions: %q", got)
	}
	if got := texts(sc.Between(s(12), s(12.5))); !slices.Equal(got, []string{"four"}) {
		t.Errorf("at 12: %q", got)
	}
	sc.Forget(s(5))
	if got := texts(sc.Between(0, s(100))); !slices.Equal(got, []string{"two", "four"}) {
		t.Errorf("after forgetting: %q", got)
	}
}

func TestAppendVTT(t *testing.T) {
	s := func(sec float64) time.Duration { return time.Duration(sec * float64(time.Second)) }
	got := string(AppendVTT(nil, Cue{Start: s(3661.5), End: s(3663), Text: "Tom & Jerry\n<3 cats>"}))
	if want := "01:01:01.500 --> 01:01:03.000\nTom &amp; Jerry\n&lt;3 cats&gt;\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = string(AppendVTT(nil, Cue{Start: s(1), End: s(3), Text: "all right gamers",
		Words: []Word{{s(1), "all"}, {s(1.25), "right"}, {s(1.25), "gamers"}, {s(5), "late"}}}))
	if want := "00:00:01.000 --> 00:00:03.000\nall <00:00:01.250>right gamers late\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
