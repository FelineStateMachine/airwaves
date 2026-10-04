package cc

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestParseSRT(t *testing.T) {
	srt := "\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:03,500\r\n<i>Where are you</i>\r\n<font color=\"#ffff00\">going?</font>\r\n\r\n" +
		"2\r\n00:00:04,250 --> 00:00:06,000 X1:100 X2:200 Y1:10 Y2:20\r\n{\\an8}Tom &amp; Jerry\\Nare here\r\n\r\n" +
		"3\r\n00:00:07,000 --> 00:00:07,000\r\nNo time at all\r\n\r\n" +
		"4\r\n00:00:08,000 --> 00:00:09,000\r\n<b></b>\r\n\r\n" +
		"5\r\n01:02:03,4 --> 01:02:05,000\r\nCafé – naïve… “quoted” ♫\r\n" +
		"6\r\n01:02:06,000 --> 01:02:07,000\r\nNo blank line before this one\r\n"
	got, err := Parse([]byte(srt))
	if err != nil {
		t.Fatal(err)
	}
	want := []Cue{
		{Start: ms(1000), End: ms(3500), Text: "Where are you\ngoing?"},
		{Start: ms(4250), End: ms(6000), Text: "Tom & Jerry\nare here"},
		{Start: time.Hour + 2*time.Minute + ms(3400), End: time.Hour + 2*time.Minute + ms(5000), Text: "Café – naïve… “quoted” ♫"},
		{Start: time.Hour + 2*time.Minute + ms(6000), End: time.Hour + 2*time.Minute + ms(7000), Text: "No blank line before this one"},
	}
	if !equalCues(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestParseVTT(t *testing.T) {
	vtt := `WEBVTT
X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:900000

STYLE
::cue { color: yellow }

NOTE a comment

intro
00:01.000 --> 00:02.000 align:start position:10%
<v Roger>Hi &lt;there&gt;</v> <c.loud>you</c>

00:00:03.000 --> 00:00:04.000
- Line one
- Line two
`
	got, err := Parse([]byte(vtt))
	if err != nil {
		t.Fatal(err)
	}
	want := []Cue{
		{Start: ms(1000), End: ms(2000), Text: "Hi <there> you"},
		{Start: ms(3000), End: ms(4000), Text: "- Line one\n- Line two"},
	}
	if !equalCues(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// YouTube's automatic captions roll: each repeats the last one's line.
func TestParseRollingVTT(t *testing.T) {
	vtt := "WEBVTT\nKind: captions\nLanguage: en\n\n" +
		"00:00:00.320 --> 00:00:02.389 align:start position:0%\n \nhello<00:00:00.640><c> everybody</c>\n\n" +
		"00:00:02.389 --> 00:00:02.399 align:start position:0%\nhello everybody\n \n\n" +
		"00:00:02.399 --> 00:00:05.030 align:start position:0%\nhello everybody\nand<00:00:02.720><c> welcome</c>\n\n" +
		"00:00:05.030 --> 00:00:05.040 align:start position:0%\nand welcome\n \n\n" +
		"00:00:05.040 --> 00:00:07.000 align:start position:0%\nand welcome\nback\n"
	got, err := Parse([]byte(vtt))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range got {
		texts = append(texts, c.Text)
	}
	if want := []string{"hello everybody", "and welcome", "back"}; !slices.Equal(texts, want) {
		t.Errorf("captions %q, want %q", texts, want)
	}
}

// A long line stays whole: the 608 encoder and players lay it out.
func TestParseKeepsLongLines(t *testing.T) {
	long := "This line is a good deal longer than thirty-two characters, and then some"
	got, err := Parse([]byte("1\n00:00:00,000 --> 00:00:02,000\n" + long + "\n"))
	if err != nil || len(got) != 1 || got[0].Text != long {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestParseRejectsNonsense(t *testing.T) {
	if _, err := Parse([]byte("<html>not found</html>")); !errors.Is(err, ErrNoCues) {
		t.Errorf("err = %v", err)
	}
	// A real file whose captions are all empty is fine, just empty.
	got, err := Parse([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\n<i></i>\n"))
	if err != nil || len(got) != 0 {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestFormatSRTRoundTrip(t *testing.T) {
	cues := []Cue{{Start: ms(1500), End: time.Hour + ms(61001), Text: "One\nTwo"}, {Start: ms(-5), End: ms(500), Text: "Clipped"}}
	srt := string(FormatSRT(cues))
	if !strings.HasPrefix(srt, "1\n00:00:01,500 --> 01:01:01,001\nOne\nTwo\n\n2\n00:00:00,000 --> ") {
		t.Errorf("SRT:\n%s", srt)
	}
	back, err := Parse([]byte(srt))
	if err != nil || len(back) != 2 || !equalCues(back[1:], cues[:1]) { // in time order
		t.Errorf("back %q, %v", back, err)
	}
}

func TestShift(t *testing.T) {
	cues := []Cue{
		{Start: ms(0), End: ms(1000), Text: "a"},
		{Start: ms(1500), End: ms(3000), Text: "b c", Words: []Word{{ms(1500), "b"}, {ms(2500), "c"}}},
		{Start: ms(4000), End: ms(5000), Text: "d"},
	}
	got := Shift(cues, -2*time.Second)
	want := []Cue{
		{Start: 0, End: ms(1000), Text: "b c", Words: []Word{{0, "b"}, {ms(500), "c"}}},
		{Start: ms(2000), End: ms(3000), Text: "d"},
	}
	if !equalCues(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if cues[1].Words[0].Start != ms(1500) {
		t.Error("Shift changed its argument's words")
	}
}

func TestLanguages(t *testing.T) {
	for _, l := range []string{"eng", "en", "EN-us", "English", "en_GB"} {
		if !English(l) || Foreign(l) {
			t.Errorf("%q isn't English", l)
		}
	}
	for _, l := range []string{"", "und", "mul", "zxx"} {
		if English(l) || Foreign(l) {
			t.Errorf("%q: English %v, foreign %v; want neither", l, English(l), Foreign(l))
		}
	}
	for _, l := range []string{"jpn", "ja", "fre", "spa", "Korean"} {
		if !Foreign(l) {
			t.Errorf("%q isn't foreign", l)
		}
	}
}

func equalCues(a, b []Cue) bool {
	return slices.EqualFunc(a, b, func(x, y Cue) bool {
		return x.Start == y.Start && x.End == y.End && x.Text == y.Text && slices.Equal(x.Words, y.Words)
	})
}

func TestParseJSON3(t *testing.T) {
	raw, err := os.ReadFile("testdata/asr.json3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 10 {
		t.Fatalf("%d cues", len(got))
	}
	// The first lines of a Northernlion video, YouTube's speech recognition
	// timing each word.
	first := got[1]
	if first.Start != ms(640) || first.End != ms(640+5679) || !strings.HasPrefix(first.Text, "right, gamers. We are back") {
		t.Errorf("line %+v", first)
	}
	if w := first.Words; len(w) < 4 || w[0] != (Word{ms(640), "right,"}) || w[1] != (Word{ms(960), "gamers."}) || w[3] != (Word{ms(1680), "are"}) {
		t.Errorf("words %+v", first.Words)
	}
	for _, c := range got {
		if strings.Contains(c.Text, "\n") || c.Text == "" || len(c.Words) == 0 || strings.Join(wordTexts(c.Words), " ") != c.Text {
			t.Errorf("cue %+v", c)
		}
	}
	// Subtitles a person wrote have no word times.
	manual := `{"events":[{"tStartMs":1000,"dDurationMs":2000,"segs":[{"utf8":"Hello &amp; welcome\nto the show"}]},{"tStartMs":0,"dDurationMs":9000,"id":1}]}`
	got, err = Parse([]byte(manual))
	if err != nil || !equalCues(got, []Cue{{Start: ms(1000), End: ms(3000), Text: "Hello & welcome\nto the show"}}) {
		t.Errorf("manual: %+v, %v", got, err)
	}
	if _, err := Parse([]byte(`{"not": "captions"}`)); !errors.Is(err, ErrNoCues) {
		t.Errorf("not json3: %v", err)
	}
}

func wordTexts(ws []Word) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Text)
	}
	return out
}

// Subtitles made from broadcast roll-up captions repeat each caption's
// last lines atop the next; only the new lines are kept.
func TestParseUnrollsSRT(t *testing.T) {
	srt := "1\n00:00:29,120 --> 00:00:32,250\nON STAGE IN MY HEELS IS WHERE I\nBELONG DOWN AT THE ¶\n¶ PINK PONY CLUB, I'M GONNA KEEP\n\n" +
		"2\n00:00:32,250 --> 00:00:33,320\nBELONG DOWN AT THE ¶\n¶ PINK PONY CLUB, I'M GONNA KEEP\nON DANCING AT THE PINK PONY\n\n" +
		"3\n00:00:33,320 --> 00:00:35,000\n¶ PINK PONY CLUB, I'M GONNA KEEP\nON DANCING AT THE PINK PONY\nCLUB ¶\n\n" +
		"4\n00:00:40,000 --> 00:00:42,000\nCLUB ♪\nA new caption after a pause\n"
	got, err := Parse([]byte(srt))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range got {
		texts = append(texts, c.Text)
	}
	want := []string{
		"ON STAGE IN MY HEELS IS WHERE I\nBELONG DOWN AT THE ♪\n♪ PINK PONY CLUB, I'M GONNA KEEP",
		"ON DANCING AT THE PINK PONY", "CLUB ♪", "CLUB ♪\nA new caption after a pause",
	}
	if !slices.Equal(texts, want) {
		t.Errorf("got  %q\nwant %q", texts, want)
	}
}

// YouTube's speech recognition sends lines that overlap (each shows until
// the line after next begins); text players get one screen at a time,
// rolling up: the older line on top, each line once, the bottom timed.
func TestScreenRollsUp(t *testing.T) {
	raw, err := os.ReadFile("testdata/asr.json3")
	if err != nil {
		t.Fatal(err)
	}
	cues, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	overlaps := 0
	for i := 1; i < len(cues); i++ {
		if cues[i].Start < cues[i-1].End {
			overlaps++
		}
	}
	if overlaps < len(cues)/2 {
		t.Fatalf("the excerpt should overlap: %d of %d", overlaps, len(cues))
	}
	screens := Screen(cues)
	if len(screens) != len(cues) {
		t.Errorf("%d screens for %d lines", len(screens), len(cues))
	}
	for i, s := range screens {
		lines := strings.Split(s.Text, "\n")
		if len(lines) > 2 || (len(lines) == 2 && lines[0] == lines[1]) {
			t.Errorf("screen %d: %q", i, s.Text)
		}
		if i > 0 && s.Start < screens[i-1].End {
			t.Errorf("screen %d starts at %v, before %d ends at %v", i, s.Start, i-1, screens[i-1].End)
		}
		if last := lines[len(lines)-1]; strings.Join(wordTexts(s.Words), " ") != last || s.Words[0].Start != s.Start {
			t.Errorf("screen %d: words %v for its last line %q", i, s.Words, last)
		}
		if i > 0 && len(lines) == 2 {
			prev := strings.Split(screens[i-1].Text, "\n")
			if lines[0] != prev[len(prev)-1] {
				t.Errorf("screen %d's top line %q isn't the last screen's bottom %q", i, lines[0], prev[len(prev)-1])
			}
		}
	}
	vtt := string(AppendVTT(nil, screens[2]))
	lines := strings.Split(strings.TrimSpace(vtt), "\n")
	if len(lines) != 3 || strings.Contains(lines[1], "<") || !strings.Contains(lines[2], "<00:00:") {
		t.Errorf("WebVTT:\n%s", vtt)
	}
	t.Logf("screens:\n%s", strings.Join(func() (out []string) {
		for _, s := range screens[:6] {
			out = append(out, fmt.Sprintf("%v-%v %q", s.Start, s.End, s.Text))
		}
		return out
	}(), "\n"))
}

// Subtitles that overlap show one at a time too.
func TestScreenSubtitles(t *testing.T) {
	got := Screen([]Cue{
		{Start: ms(0), End: ms(3000), Text: "One"},
		{Start: ms(2000), End: ms(4000), Text: "Two"},
		{Start: ms(4000), End: ms(5000), Text: "Two"},
		{Start: ms(6000), End: ms(7000), Text: "[door slams]"},
		{Start: ms(6000), End: ms(8000), Text: "Who's there?"},
	})
	want := []Cue{
		{Start: ms(0), End: ms(2000), Text: "One"},
		{Start: ms(2000), End: ms(5000), Text: "Two"},
		{Start: ms(6000), End: ms(8000), Text: "[door slams]\nWho's there?"},
	}
	if !equalCues(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}
