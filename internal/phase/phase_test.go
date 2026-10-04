package phase

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTimerMarksEachStepOnce(t *testing.T) {
	tm := New("tune 9.1 (antenna)", "antenna")
	tm.Mark("first data")
	time.Sleep(20 * time.Millisecond)
	tm.Mark("ready")
	tm.Mark("first data") // again: the first time counts
	ms := tm.Marks()
	if len(ms) != 2 || ms[0].Name != "first data" || ms[1].Name != "ready" || ms[1].At < 20*time.Millisecond || ms[1].MS != ms[1].At.Milliseconds() {
		t.Fatalf("marks %+v", ms)
	}
	if !tm.Has("ready") || tm.Has("first frame") {
		t.Error("Has")
	}
	if s := tm.String(); !strings.HasPrefix(s, "first data 0.0s, ready 0.") {
		t.Errorf("String %q", s)
	}

	// A context without a timer marks nothing, and doesn't fail.
	Step(context.Background(), "x")
	var none *Timer
	none.Mark("x")
	if none.Has("x") || none.Marks() != nil {
		t.Error("a nil timer recorded a step")
	}
	ctx := NewContext(context.Background(), tm)
	Step(ctx, "opened")
	if !tm.Has("opened") || FromContext(ctx) != tm {
		t.Error("Step didn't reach the context's timer")
	}
}

func TestStatsSummaries(t *testing.T) {
	var s Stats
	for i := 1; i <= 10; i++ {
		s.Add("folder", Ready, time.Duration(i)*100*time.Millisecond)
	}
	s.Add("folder", FirstFrame, 2*time.Second)
	s.Add("nonsense", Ready, time.Second)   // not a kind
	s.Add("folder", "other", time.Second)   // not a measure
	s.Add("antenna", Ready, -time.Second)   // can't be
	s.Add("antenna", Ready, 10*time.Minute) // can't be either
	got := s.Summaries()
	if len(got) != 2 {
		t.Fatalf("summaries %+v", got)
	}
	r := got[0]
	if r.Kind != "folder" || r.Measure != Ready || r.Count != 10 || r.P50 != 0.5 || r.P90 != 0.9 || r.Max != 1 {
		t.Errorf("ready %+v", r)
	}
	if f := got[1]; f.Measure != FirstFrame || f.Count != 1 || f.P50 != 2 {
		t.Errorf("first frame %+v", f)
	}

	// Only the last ones are kept.
	for range 2 * keep {
		s.Add("youtube", Ready, time.Second)
	}
	for _, x := range s.Summaries() {
		if x.Kind == "youtube" && x.Count != keep {
			t.Errorf("kept %d, want %d", x.Count, keep)
		}
	}
}
