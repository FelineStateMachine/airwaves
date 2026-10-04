package vchan

import (
	"strings"
	"testing"
	"time"
)

func mustSched(t *testing.T, s Schedule, loc *time.Location) *sched {
	t.Helper()
	r, err := s.read(loc)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// hotd is ten episodes, 60 to 69 minutes long.
func hotd() []time.Duration {
	var ls []time.Duration
	for i := range 10 {
		ls = append(ls, time.Duration(60+i)*time.Minute)
	}
	return ls
}

// TestScheduleBlockCount: four episodes a night from 8 PM, from the first
// on the start date, picking up the next night where the last left off,
// and looping at the end.
func TestScheduleBlockCount(t *testing.T) {
	loc, _ := time.LoadLocation("America/Denver")
	s := mustSched(t, Schedule{Start: "2026-10-04", Blocks: []Block{{At: "20:00", Count: 4}}}, loc)
	ls := hotd()
	day := func(d int, h, m int) time.Time { return time.Date(2026, 10, 4+d, h, m, 0, 0, loc) }

	got := s.slots(ls, day(0, 0, 0), day(3, 0, 0), 100)
	if len(got) != 12 {
		t.Fatalf("%d airings in three nights", len(got))
	}
	for n, a := range got {
		if a.I != n%10 {
			t.Fatalf("airing %d is item %d", n, a.I)
		}
	}
	if !got[0].Start.Equal(day(0, 20, 0)) || !got[4].Start.Equal(day(1, 20, 0)) || !got[1].Start.Equal(got[0].End) {
		t.Errorf("nights start %v and %v", got[0].Start, got[4].Start)
	}
	// The third night runs episodes 9 and 10, then 1 and 2 again.
	if got[8].I != 8 || got[10].I != 0 {
		t.Errorf("third night: %d, %d", got[8].I, got[10].I)
	}

	// Mid-afternoon: off the air, with tonight's first episode next.
	on, onAir, ok := s.at(ls, day(1, 15, 0))
	if !ok || onAir || on.I != 4 || !on.Start.Equal(day(1, 20, 0)) {
		t.Errorf("afternoon: %+v on air %v", on, onAir)
	}
	// Half an hour into the second episode tonight.
	on, onAir, _ = s.at(ls, day(1, 21, 34))
	if !onAir || on.I != 5 {
		t.Errorf("evening: %+v on air %v", on, onAir)
	}
	// Before the start: the first episode is next.
	if on, onAir, ok := s.at(ls, day(-3, 12, 0)); !ok || onAir || on.I != 0 || !on.Start.Equal(day(0, 20, 0)) {
		t.Errorf("before the start: %+v", on)
	}
}

// TestScheduleUntil: a block that stops at a time lets the episode on
// finish, and one stopping after midnight runs into the next day.
func TestScheduleUntil(t *testing.T) {
	loc, _ := time.LoadLocation("America/Denver")
	s := mustSched(t, Schedule{Start: "2026-10-04T21:00", First: 3, Blocks: []Block{{At: "21:00", Until: "00:30"}}}, loc)
	ls := hotd()
	got := s.slots(ls, time.Date(2026, 10, 4, 0, 0, 0, 0, loc), time.Date(2026, 10, 5, 12, 0, 0, 0, loc), 100)
	// Episodes 3 to 6 (62 to 65 minutes) start at 21:00, 22:02, 23:05 and
	// 0:09; 0:09 is before 0:30, so the fourth airs and ends at 1:14.
	if len(got) != 4 || got[0].I != 2 || got[3].I != 5 {
		t.Fatalf("night: %+v", got)
	}
	if end := got[3].End; !end.Equal(time.Date(2026, 10, 5, 1, 14, 0, 0, loc)) {
		t.Errorf("ends %v", end)
	}
}

// TestScheduleDaysAndBlocks: weekend matinees and weeknight blocks, a block
// run into by the one before starting when it ends.
func TestScheduleDaysAndBlocks(t *testing.T) {
	loc := time.UTC
	s := mustSched(t, Schedule{Start: "2026-10-03", Blocks: []Block{ // a Saturday
		{Days: []string{"sat", "sun"}, At: "13:00", Count: 3},
		{Days: []string{"sat"}, At: "15:00", Count: 1},
	}}, loc)
	ls := []time.Duration{time.Hour, time.Hour, time.Hour, time.Hour}
	got := s.slots(ls, time.Date(2026, 10, 3, 0, 0, 0, 0, loc), time.Date(2026, 10, 6, 0, 0, 0, 0, loc), 100)
	if len(got) != 7 {
		t.Fatalf("%d airings", len(got))
	}
	// Saturday: 13:00, 14:00, 15:00, then the 15:00 block at 16:00.
	if !got[3].Start.Equal(time.Date(2026, 10, 3, 16, 0, 0, 0, loc)) || got[3].I != 3 {
		t.Errorf("saturday's second block: %+v", got[3])
	}
	// Sunday: three more, picking up at the first item; nothing Monday.
	if !got[4].Start.Equal(time.Date(2026, 10, 4, 13, 0, 0, 0, loc)) || got[4].I != 0 {
		t.Errorf("sunday: %+v", got[4])
	}
}

// TestScheduleAroundTheClock: without blocks, the items loop from the
// start, and a window years on is found without walking there.
func TestScheduleAroundTheClock(t *testing.T) {
	loc := time.UTC
	s := mustSched(t, Schedule{Start: "2026-10-04T20:00", First: 2}, loc)
	ls := hotd()
	on, onAir, _ := s.at(ls, time.Date(2026, 10, 4, 21, 30, 0, 0, loc))
	if !onAir || on.I != 2 { // item 2 ran 20:00 to 21:01
		t.Errorf("on: %+v", on)
	}
	far := time.Date(2036, 1, 1, 0, 0, 0, 0, loc)
	got := s.slots(ls, far, far.Add(3*time.Hour), 10)
	if len(got) < 3 || got[0].Start.After(far) || !got[0].End.After(far) {
		t.Errorf("years on: %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if !got[i].Start.Equal(got[i-1].End) || got[i].I != (got[i-1].I+1)%10 {
			t.Errorf("not back to back: %+v", got)
		}
	}
}

// TestScheduleGuide: gaps are off the air until the next airing, past the
// window's end if need be, and say what's next.
func TestScheduleGuide(t *testing.T) {
	loc := time.UTC
	s := mustSched(t, Schedule{Start: "2026-10-04", Blocks: []Block{{At: "20:00", Count: 2}}}, loc)
	ls := hotd()
	from, to := time.Date(2026, 10, 4, 12, 0, 0, 0, loc), time.Date(2026, 10, 5, 12, 0, 0, 0, loc)
	g := s.guide(ls, from, to, 100)
	if len(g) != 4 || g[0].I != -1 || !g[0].Start.Equal(from) || !g[0].End.Equal(g[1].Start) ||
		g[1].I != 0 || g[2].I != 1 || g[3].I != -1 {
		t.Fatalf("guide: %+v", g)
	}
	if want := time.Date(2026, 10, 5, 20, 0, 0, 0, loc); !g[3].End.Equal(want) {
		t.Errorf("the gap after ends %v, want %v", g[3].End, want)
	}
	p := offAirProgram(g[3], "Episode 3", g[3].End, false)
	if p.Title != "Off air" || p.Description != "Back Mon, Oct 5 at 8:00 PM with Episode 3." {
		t.Errorf("off air: %+v", p)
	}
	p = offAirProgram(g[0], "Episode 1", g[0].End, true)
	if p.Description != "Starts at 8:00 PM with Episode 1." {
		t.Errorf("before the start: %q", p.Description)
	}
}

func TestScheduleCheck(t *testing.T) {
	for _, c := range []struct {
		s    Schedule
		want string
	}{
		{Schedule{Start: "Oct 4"}, "isn't a date"},
		{Schedule{Start: "2026-10-04", Blocks: []Block{{At: "8pm", Count: 4}}}, "like 20:00"},
		{Schedule{Start: "2026-10-04", Blocks: []Block{{At: "20:00"}}}, "needs a count or a time"},
		{Schedule{Start: "2026-10-04", Blocks: []Block{{At: "20:00", Count: 2, Until: "23:00"}}}, "give one"},
		{Schedule{Start: "2026-10-04", Blocks: []Block{{At: "20:00", Count: 2, Days: []string{"funday"}}}}, "isn't a day"},
		{Schedule{Start: "2026-10-04", First: -1}, "counts from 1"},
	} {
		if err := c.s.Check(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.s, err, c.want)
		}
	}
	if err := (Schedule{Start: "2026-10-04T20:00", Blocks: []Block{{Days: []string{"Fri", "sat"}, At: "20:00", Until: "02:00"}}}).Check(); err != nil {
		t.Error(err)
	}
}
