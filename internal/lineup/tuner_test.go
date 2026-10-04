package lineup_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"airwaves/internal/lineup"
	"airwaves/internal/lineup/lineuptest"
)

// found is part of what the tuner's scan found around Denver.
var found = []lineup.Scanned{
	{Number: "20.1", Name: "KTVD-HD", RF: 31},
	{Number: "20.2", Name: "H & I", RF: 31},
	{Number: "9.4", Name: "KUSA-HD", RF: 31},
	{Number: "9.7", Name: "TBD", RF: 31},
	{Number: "31.1", Name: "KDVR-DT", RF: 36},
	{Number: "2.1", Name: "KWGN-DT", RF: 36},
	{Number: "6.1", Name: "KRMADT1", RF: 33},
	{Number: "14.1", Name: "Univision Denver", RF: 32},
	{Number: "88.1", Name: "Mystery", RF: 0},
}

// TestMatch describes the tuner's channels from the records, and never
// adds one: KMGH 7.1 and KUSA 9.1 are listed and licensed, but the tuner
// didn't find them.
func TestMatch(t *testing.T) {
	chans := lineup.Match(lineuptest.Report(), lineuptest.Guide(), found)
	var numbers []string
	by := map[string]lineup.TunerChannel{}
	for _, c := range chans {
		numbers = append(numbers, c.Number)
		by[c.Number] = c
	}
	if want := []string{"2.1", "6.1", "9.4", "9.7", "14.1", "20.1", "20.2", "31.1", "88.1"}; !slices.Equal(numbers, want) {
		t.Fatalf("channels %v, want %v", numbers, want)
	}
	check := func(number, call, base, tx, via, guideID string, rf int) {
		t.Helper()
		c := by[number]
		if c.CallSign != call || c.BaseCall != base || c.Transmitter != tx || c.Via != via || c.GuideID != guideID || c.RF != rf {
			t.Errorf("%s = %+v", number, c)
		}
	}
	// From the listings, on its own transmitter (not the nearer
	// low-power one on the same RF channel).
	check("20.1", "KTVDDT", "KTVD", "KTVD", "", "g20", 31)
	// Not listed: the transmitter's own virtual channel.
	check("20.2", "KTVD", "KTVD", "KTVD", "", "", 31)
	// KUSA's subchannels ride KTVD's transmitter.
	check("9.4", "KUSADT4", "KUSA", "KTVD", "KTVD", "g94", 31)
	check("9.7", "KUSA", "KUSA", "KTVD", "KTVD", "", 31)
	// KWGN's ATSC 1.0 signal is on KDVR's transmitter; it broadcasts in
	// ATSC 3.0 itself.
	check("2.1", "KWGNDT", "KWGN", "KDVR", "KDVR", "g2", 36)
	check("6.1", "KRMADT", "KRMA", "KRMA-TV", "", "g6", 33)
	check("14.1", "KCECDT", "KCEC", "KCEC", "", "g14", 32)
	// Nothing on record: only what the broadcast says.
	check("88.1", "Mystery", "MYSTERY", "", "", "", 0)
	if c := by["2.1"]; c.NextGen == nil || c.NextGen.HostCall != "KWGN-TV" || c.NextGen.RF != "34" || c.Network != "CW" || c.Name != "KWGN-DT" {
		t.Errorf("2.1 = %+v, %+v", c, c.NextGen)
	}
	if c := by["31.1"]; c.NextGen == nil || c.NextGen.HostCall != "KWGN-TV" || c.FacilityID != 126 {
		t.Errorf("31.1 = %+v", c)
	}
	if c := by["20.1"]; c.NextGen != nil {
		t.Errorf("20.1 in ATSC 3.0: %+v", c.NextGen)
	}

	// Without listings or records: every channel still, as broadcast.
	bare := lineup.Match(&lineup.Report{}, nil, found)
	if len(bare) != len(found) || bare[0].Number != "2.1" || bare[0].CallSign != "KWGN-DT" || bare[0].Transmitter != "" {
		t.Errorf("bare = %+v", bare)
	}
}

// TestForTuner: the app's report is the records without estimates, its
// stations carrying what the tuner receives from them.
func TestForTuner(t *testing.T) {
	rep := lineuptest.Report()
	rep.Presets = nil
	chans := lineup.Match(rep, lineuptest.Guide(), found)
	out := lineup.ForTuner(rep, chans)
	carries := map[string][]string{}
	for _, s := range out.Stations {
		carries[s.CallSign] = s.Carries
		if s.Signal == nil || len(s.Signal) != 0 {
			t.Errorf("%s signal %v", s.CallSign, s.Signal)
		}
	}
	if got := carries["KTVD"]; !slices.Equal(got, []string{"9.4", "9.7", "20.1", "20.2"}) {
		t.Errorf("KTVD carries %v", got)
	}
	if got := carries["KWGN-TV"]; !slices.Equal(got, []string{"2.1 (3.0)", "31.1 (3.0)", "4.1 (3.0)"}) {
		t.Errorf("KWGN-TV carries %v", got)
	}
	if got := carries["KMGH-TV"]; got == nil || len(got) != 0 {
		t.Errorf("KMGH-TV carries %v", got)
	}
	if out.Stations[0].CallSign != "K31AB-D" || len(out.Channels) != 0 || len(rep.Stations[5].Carries) != 0 {
		t.Errorf("stations not nearest first, or the report changed: %+v", out.Stations[0])
	}
	raw, _ := json.Marshal(out)
	for _, bad := range []string{"noiseMarginDb", "tier", `"presets"`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("report has %s", bad)
		}
	}
}
