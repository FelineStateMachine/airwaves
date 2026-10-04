package vchan

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"airwaves/internal/cc"
)

func TestYtConfigDefaultsAndMix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, youtubeConfig)
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"channels": ["@Northernlion"]}`)
	c, err := readYtConfig(path)
	if err != nil || c.RerunMix != "balanced" || c.LockHours != 2 || c.RepeatDays != 30 {
		t.Errorf("defaults: %+v, %v", c, err)
	}
	write(`{"channels": ["@Northernlion"], "rerunMix": "recent", "lockHours": 0.5}`)
	if c, err := readYtConfig(path); err != nil || c.RerunMix != "recent" || c.LockHours != 0.5 {
		t.Errorf("set: %+v, %v", c, err)
	}
	write(`{"channels": ["@Northernlion"], "rerunMix": "newest"}`)
	if _, err := readYtConfig(path); err == nil || !strings.Contains(err.Error(), `"balanced"`) {
		t.Errorf("an unknown mix should be refused, got %v", err)
	}
}

func TestYtListingDates(t *testing.T) {
	l, err := parseYtListing(readTestdata(t, "yt_listing_dated.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-03", "2026-09-04", "2026-02-04", "2025-10-04", "2023-10-04", "2022-10-04"}
	for i, v := range l.Videos {
		if got := v.Published.Format(time.DateOnly); got != want[i] || !v.Approx {
			t.Errorf("video %d (%s): published %s approx %v, want %s", i, v.ID, got, v.Approx, want[i])
		}
	}

	// A listing updates approximate dates and leaves known ones be, and
	// marks the catalog as dated so it isn't listed again early.
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	exact := time.Date(2026, 10, 3, 14, 0, 20, 0, time.UTC)
	s := &ytSource{URL: "u", Listed: now.Add(-time.Hour), Videos: []*ytVideo{
		{ID: "9KaMhx-S0jo", Published: exact},
		{ID: "ZAPEKtIcxsc", Published: now.AddDate(-1, 0, 0), Approx: true},
	}}
	y := &YouTube{tried: map[string]time.Time{}}
	if !y.listDue(s, now) {
		t.Error("a catalog listed without dates isn't due a listing")
	}
	s.mergeListing(l, now)
	if v := s.Videos[0]; !v.Published.Equal(exact) || v.Approx {
		t.Errorf("exact date replaced: %+v", v)
	}
	if v := s.Videos[1]; v.Published.Format(time.DateOnly) != "2026-09-04" || !v.Approx {
		t.Errorf("approximate date not updated: %+v", v)
	}
	if !s.Dated || y.listDue(s, now.Add(time.Hour)) {
		t.Errorf("dated %v, still due a listing", s.Dated)
	}
}

func TestYtDated(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		v             ytVideo
		subtitle, day string
	}{
		{ytVideo{Published: time.Date(2014, 3, 4, 12, 0, 0, 0, time.Local)}, "Northernlion, Mar 4, 2014", "20140304"},
		{ytVideo{Published: time.Date(2023, 10, 4, 0, 0, 0, 0, time.UTC), Approx: true}, "Northernlion, 2023", "2023"},
		{ytVideo{Published: time.Date(2025, 10, 3, 0, 0, 0, 0, time.UTC), Approx: true}, "Northernlion, 2025", "2025"},
		{ytVideo{Published: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), Approx: true}, "Northernlion, March 2026", "202603"},
		{ytVideo{}, "Northernlion", ""},
	} {
		if sub, day := ytDated("Northernlion", &c.v, now); sub != c.subtitle || day != c.day {
			t.Errorf("%v (approx %v): got %q %q, want %q %q", c.v.Published, c.v.Approx, sub, day, c.subtitle, c.day)
		}
	}
}

func TestYtByAge(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	vs := []*ytVideo{
		{ID: "a", Published: ago(10)},
		{ID: "b"}, // between a and c: the newer
		{ID: "c", Published: ago(400)},
		{ID: "d"}, // nearest dated: c, then e
		{ID: "e"}, // nearest dated: f
		{ID: "f", Published: ago(1000)},
		{ID: "g"}, // f
	}
	half, two := ytByAge(vs, now)
	ids := func(vs []*ytVideo) (s string) {
		for _, v := range vs {
			s += v.ID
		}
		return s
	}
	if ids(half) != "ab" || ids(two) != "abcd" {
		t.Errorf("half a year %q, two years %q; want ab and abcd", ids(half), ids(two))
	}
	// Without any dates, rank stands in.
	var undated []*ytVideo
	for i := range 100 {
		undated = append(undated, &ytVideo{ID: fmt.Sprint(i)})
	}
	if half, two := ytByAge(undated, now); len(half) != 3 || len(two) != 12 || half[0].ID != "0" {
		t.Errorf("by rank: %d and %d", len(half), len(two))
	}
}

// agedPool is one channel of 100 videos from the last six months, 100
// from the 18 months before, and 800 older.
func agedPool(now time.Time, mix string) *ytPool {
	s := ytPoolSource{ID: "UCa"}
	for i := range 1000 {
		days := 1 + i*90/100 // ages 1 to 90 days
		switch {
		case i >= 200:
			days = 800 + i
		case i >= 100:
			days = 200 + (i-100)*5
		}
		s.Videos = append(s.Videos, &ytVideo{ID: fmt.Sprintf("v%04d", i), Title: fmt.Sprintf("Video %d", i),
			Seconds: 1200, Published: now.AddDate(0, 0, -days), Approx: true})
	}
	s.HalfYear, s.TwoYears = ytByAge(s.Videos, now)
	return &ytPool{Sources: []ytPoolSource{s}, Mix: ytRerunMixes[mix], Seed: 3, Lock: 2 * time.Hour}
}

// shares picks n reruns and counts how many come from the last six
// months, the 18 months before, and earlier.
func shares(g *ytGuard, now time.Time, n int, aired map[string]bool) (out [3]float64) {
	for i := range n {
		f, _ := g.pick(now.Add(time.Duration(i) * time.Minute))
		if aired[f.Video.ID] {
			return [3]float64{-1, -1, -1}
		}
		age := now.Sub(f.Video.Published)
		switch {
		case age <= ytHalfYear:
			out[0]++
		case age <= ytTwoYears:
			out[1]++
		default:
			out[2]++
		}
	}
	for i := range out {
		out[i] /= float64(n)
	}
	return out
}

func TestYtRerunMixWeights(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	// The last two years hold the last six months, and any time holds
	// both: with "recent", 70% + 20%*100/200 + 10%*100/1000 are from the
	// last six months.
	for mix, want := range map[string][3]float64{
		"recent":   {0.81, 0.11, 0.08},
		"balanced": {0.58, 0.18, 0.24},
		"any":      {0.10, 0.10, 0.80},
	} {
		got := shares(newYtGuard(nil, agedPool(now, mix)), now, 6000, nil)
		t.Logf("%s: %.3f", mix, got)
		for i := range got {
			if got[i] < want[i]-0.025 || got[i] > want[i]+0.025 {
				t.Errorf("%s: shares %.3f, want about %.2f", mix, got, want)
				break
			}
		}
	}
}

func TestYtRerunMixFallsBack(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	pool := agedPool(now, "recent")
	pool.Repeat = 30 * 24 * time.Hour
	// Everything from the last six months aired yesterday: picks meant
	// for them go to the last two years instead.
	var history []ytAiring
	aired := map[string]bool{}
	for i, v := range pool.Sources[0].HalfYear {
		start := now.Add(-24*time.Hour + time.Duration(i)*time.Minute)
		history = append(history, ytAiring{ID: v.ID, Channel: "UCa", Title: v.Title, Start: start, End: start.Add(time.Minute)})
		aired[v.ID] = true
	}
	got := shares(newYtGuard(history, pool), now, 300, aired)
	if got[0] != 0 || got[1] < 0.8 {
		t.Errorf("shares %.2f; want none from the last six months, most from the 18 months before", got)
	}
	// Nothing recent at all: every pick comes from the rest.
	old := agedPool(now, "recent")
	old.Sources[0].Videos = old.Sources[0].Videos[200:]
	old.Sources[0].HalfYear, old.Sources[0].TwoYears = ytByAge(old.Sources[0].Videos, now)
	if got := shares(newYtGuard(nil, old), now, 100, nil); got[2] != 1 {
		t.Errorf("shares %.2f from a channel with only old videos", got)
	}
}

// TestYtSettingsChangeReplansFromNow: changing the channel's settings
// replaces what's on at once and everything after, keeping what aired for
// the repeat guard.
func TestYtSettingsChangeReplansFromNow(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	pool := testPool(200, 7*24*time.Hour, 20, 30)
	var p ytPlayout
	p.update(pool, now)
	before := slices.Clone(p.Airings)

	edit := now.Add(50*time.Minute + 30*time.Second)
	on, _, _ := p.at(edit)
	pool.Basis = "a,b|recent"
	if !p.update(pool, edit) {
		t.Fatal("a settings change didn't change the schedule")
	}
	i := slices.IndexFunc(p.Airings, func(a ytAiring) bool { return a.ID == on.ID && a.Start.Equal(on.Start) })
	if i < 0 || !p.Airings[i].End.Equal(edit) {
		t.Fatalf("the airing on at the edit wasn't cut short there: %+v", p.Airings[i])
	}
	if !sameAirings(p.Airings[:i], before[:i]) {
		t.Error("what aired before the edit changed")
	}
	now2, _, ok := p.at(edit)
	if !ok || !now2.Start.Equal(edit) || now2.ID == on.ID {
		t.Errorf("on at the edit: %+v, want a new airing from then", now2)
	}
	if p.Basis != "a,b|recent" || p.end(edit).Before(edit.Add(ytPlanAhead)) {
		t.Errorf("basis %q, schedule to %v", p.Basis, p.end(edit))
	}
	checkSchedule(t, p.Airings, pool.Repeat)
	if p.update(pool, edit.Add(time.Minute)) {
		t.Error("planned again without a change")
	}
}

// TestYtNewUploadLeavesTheLockAlone: a new upload changes nothing within
// the lock, what's on included, and goes in right after it.
func TestYtNewUploadLeavesTheLockAlone(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	pool := testPool(200, 7*24*time.Hour, 20, 30)
	var p ytPlayout
	p.update(pool, now)
	before := slices.Clone(p.Airings)

	later := now.Add(50 * time.Minute)
	v := &ytVideo{ID: "UCa-new", Title: "New upload", Seconds: 600, Published: later, New: true}
	pool.Sources[0].Videos = append(pool.Sources[0].Videos, v)
	pool.Firsts, pool.ids = []ytPick{{v, "UCa"}}, nil
	p.update(pool, later)
	k := slices.IndexFunc(before, func(a ytAiring) bool { return !a.Start.Before(later.Add(pool.Lock)) })
	if !sameAirings(p.Airings[:k], before[:k]) {
		t.Fatal("airings within the lock changed")
	}
	if a := p.Airings[k]; a.ID != v.ID || !a.First || !a.Start.Equal(before[k-1].End) {
		t.Errorf("after the lock: %+v, want the new upload's first run", a)
	}
}

// TestYouTubeTakesInEdits: editing youtube.json replans the running
// channel from now.
func TestYouTubeTakesInEdits(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	cfg := filepath.Join(dir, youtubeConfig)
	if err := os.WriteFile(cfg, []byte(`{"channels": ["@a"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &ytSource{URL: ytVideosURL("@a"), ID: "UCa", Name: "A", Listed: now, Checked: now, Dated: true}
	for j := range 300 {
		s.Videos = append(s.Videos, &ytVideo{ID: fmt.Sprintf("v%03d", j), Title: fmt.Sprintf("Video %d", j),
			Seconds: 3600, Published: now.AddDate(0, 0, -3*j), Approx: true, Looked: now})
	}
	y := &YouTube{Num: "1.8", Title: "A", Dir: dir}
	y.save(ytCatalogFile, ytCatalog{Sources: []*ytSource{s}})
	first := y.Programs(now, now.Add(time.Hour))
	if len(first) == 0 || !strings.HasPrefix(first[0].Subtitle, "A, ") || first[0].Date == "" {
		t.Fatalf("guide: %+v", first)
	}

	if err := os.WriteFile(cfg, []byte(`{"channels": ["@a"], "rerunMix": "recent"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cfg, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	after := y.Programs(at, at.Add(time.Hour))
	if len(after) == 0 || after[0].Start.Before(at.Add(-2*time.Second)) || after[0].Start.After(at) {
		t.Fatalf("after the edit, on now: %+v, want an airing from the edit", after)
	}
	plan, err := readYtPlayout(filepath.Join(dir, ytPlayoutFile))
	if err != nil || !strings.HasSuffix(plan.Basis, "|recent") {
		t.Errorf("saved schedule's basis %q, %v", plan.Basis, err)
	}
}

// maxAgeChannel is a channel whose one source has, newest first: an
// upload dated exactly 10 days ago, an undated one beside it, one roughly
// 20 days ago, one exactly 40 days ago, an undated one beside that, and
// 50 roughly a year old or more. It reads its settings from cfg.
func maxAgeChannel(t *testing.T, now time.Time, cfg string) *YouTube {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, youtubeConfig), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &ytSource{URL: ytVideosURL("@a"), ID: "UCa", Name: "A", Listed: now, Checked: now, Dated: true}
	add := func(id string, published time.Time, approx bool) {
		s.Videos = append(s.Videos, &ytVideo{ID: id, Title: "Video " + id, Seconds: 1800,
			Published: published, Approx: approx, Looked: now})
	}
	add("exact10", now.AddDate(0, 0, -10), false)
	add("undated1", time.Time{}, false)
	add("approx20", now.AddDate(0, 0, -20), true)
	add("exact40", now.AddDate(0, 0, -40), false)
	add("undated2", time.Time{}, false)
	for i := range 50 {
		add(fmt.Sprintf("old%02d", i), now.AddDate(-1, 0, -i), true)
	}
	y := &YouTube{Num: "1.8", Title: "A", Dir: dir, now: func() time.Time { return now }}
	y.save(ytCatalogFile, ytCatalog{Sources: []*ytSource{s}})
	y.mu.Lock()
	y.load()
	y.busy = true // no refreshes: nothing to fetch in tests
	y.mu.Unlock()
	return y
}

func poolIDs(y *YouTube) []string {
	y.mu.Lock()
	defer y.mu.Unlock()
	var ids []string
	for _, v := range y.currentPool().Sources[0].Videos {
		ids = append(ids, v.ID)
	}
	return ids
}

func TestYtMaxAgeDays(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	y := maxAgeChannel(t, now, `{"channels": ["@a"], "maxAgeDays": 30, "lockHours": 2}`)
	// The undated videos take their nearest dated neighbour's age:
	// undated1 is beside exact10, undated2 beside exact40 (the newer of
	// two as near).
	if got, want := poolIDs(y), []string{"exact10", "undated1", "approx20"}; !slices.Equal(got, want) {
		t.Errorf("videos within 30 days: %v, want %v", got, want)
	}
	if st := y.Status(); st.Sources[0].Playable != 3 || st.Sources[0].Videos != 55 || st.Config.MaxAgeDays != 30 {
		t.Errorf("status: %+v", st.Sources[0])
	}
	progs := y.Programs(now, now.Add(48*time.Hour))
	if len(progs) == 0 {
		t.Fatal("nothing scheduled")
	}
	for _, p := range progs {
		if !slices.Contains([]string{"Video exact10", "Video undated1", "Video approx20"}, p.Title) {
			t.Fatalf("scheduled %q, older than 30 days", p.Title)
		}
	}

	// Twelve days on, approx20 (now 32 days old) ages out: it isn't picked
	// any more, but what's planned stays.
	y.mu.Lock()
	before := slices.Clone(y.plan.Airings)
	y.mu.Unlock()
	later := now.Add(12 * 24 * time.Hour)
	y.now = func() time.Time { return later }
	if got, want := poolIDs(y), []string{"exact10", "undated1"}; !slices.Equal(got, want) {
		t.Errorf("videos within 30 days, 12 days on: %v, want %v", got, want)
	}
	y.mu.Lock()
	has := y.currentPool().has("approx20")
	y.mu.Unlock()
	if !has {
		t.Error("an aged-out video may no longer stay on the schedule")
	}
	// The schedule then is the old one's last airings plus new ones; none
	// of the new picks is approx20.
	y.Programs(later, later.Add(time.Hour))
	y.mu.Lock()
	after := slices.Clone(y.plan.Airings)
	y.mu.Unlock()
	for _, a := range after {
		if a.Start.After(before[len(before)-1].Start) && a.ID == "approx20" {
			t.Fatalf("picked approx20 at %v after it aged out", a.Start)
		}
	}
}

func TestYtMaxAgeBasis(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	basis := func(cfg string) string {
		y := maxAgeChannel(t, now, cfg)
		y.mu.Lock()
		defer y.mu.Unlock()
		return y.currentPool().Basis
	}
	none, zero, month := basis(`{"channels": ["@a"]}`), basis(`{"channels": ["@a"], "maxAgeDays": 0}`), basis(`{"channels": ["@a"], "maxAgeDays": 30}`)
	if none != zero || month == none || !strings.HasSuffix(month, "|30d") {
		t.Errorf("bases %q, %q, %q: no limit should plan as before, and a limit differently", none, zero, month)
	}
}

// TestYtNothingNewEnough: when no video is new enough, nothing is
// scheduled and the channel leaves the lineup, like a folder without
// videos, until a new upload arrives.
func TestYtNothingNewEnough(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	y := maxAgeChannel(t, now, `{"channels": ["@a"], "maxAgeDays": 5}`)
	if !y.Empty() || len(y.Programs(now, now.Add(48*time.Hour))) != 0 || y.Status().Sources[0].Playable != 0 {
		t.Fatal("a channel with nothing new enough should be empty")
	}
	y.mu.Lock()
	src := y.cat.Sources[0]
	src.Videos = append([]*ytVideo{{ID: "brandnew", Title: "Brand new", Seconds: 900, Published: now.Add(-time.Hour),
		Found: now, New: true, Looked: now}}, src.Videos...)
	y.changed()
	y.mu.Unlock()
	if y.Empty() {
		t.Fatal("a new upload should put the channel back on the air")
	}
	progs := y.Programs(now, now.Add(48*time.Hour))
	if len(progs) == 0 || progs[0].Title != "Brand new" || !progs[0].New {
		t.Errorf("guide: %+v", progs)
	}
}

// TestYtDeadAir: with dead air, a channel whose one video has aired sits
// out its turns, and once everything has aired nothing more is planned;
// without it, the reruns cycle.
func TestYtDeadAir(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	pool := func(deadAir bool) *ytPool {
		p := testPool(10, 30*24*time.Hour, 20)
		p.Sources[0].Videos = p.Sources[0].Videos[:1]
		p.DeadAir = deadAir
		return p
	}
	var cycle ytPlayout
	cycle.update(pool(false), now)
	if n := countID(cycle.Airings, "UCa-000"); n < 5 {
		t.Errorf("cycling: UCa's one video aired %d times in two days", n)
	}

	var dead ytPlayout
	dead.update(pool(true), now)
	checkSchedule(t, dead.Airings, 30*24*time.Hour)
	if len(dead.Airings) != 11 || countID(dead.Airings, "UCa-000") != 1 {
		t.Fatalf("dead air: %d airings; want each of the 11 videos once", len(dead.Airings))
	}
	if dead.update(pool(true), now.Add(5*time.Hour)) {
		t.Error("planned more with nothing rested")
	}
}

func countID(as []ytAiring, id string) (n int) {
	for _, a := range as {
		if a.ID == id {
			n++
		}
	}
	return n
}

// TestYtDeadAirChannel: a channel with dead air stays in the lineup once
// it runs out, standing by, and its guide says it's off the air.
func TestYtDeadAirChannel(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	y := maxAgeChannel(t, now, `{"channels": ["@a"], "maxAgeDays": 15, "deadAir": true}`)
	progs := y.Programs(now, now.Add(48*time.Hour))
	if len(progs) != 3 || progs[2].Title != "Off air" || !progs[2].Start.Equal(now.Add(time.Hour)) ||
		!progs[2].End.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("guide: %+v; want the two recent videos, then off air", progs)
	}
	later := now.Add(3 * time.Hour)
	y.now = func() time.Time { return later }
	if y.Empty() {
		t.Error("a channel with dead air left the lineup")
	}
	if progs := y.Programs(later, later.Add(time.Hour)); len(progs) != 1 || progs[0].Title != "Off air" {
		t.Errorf("guide while off the air: %+v", progs)
	}

	cycling := maxAgeChannel(t, now, `{"channels": ["@a"], "maxAgeDays": 15}`)
	for _, p := range cycling.Programs(now, now.Add(48*time.Hour)) {
		if p.Title == "Off air" {
			t.Fatal("a cycling channel went off the air")
		}
	}
}

// TestYtAgingOutKeepsThePlan: a planned video that ages out still airs;
// only new picks leave it out.
func TestYtAgingOutKeepsThePlan(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	y := maxAgeChannel(t, now, `{"channels": ["@a"], "maxAgeDays": 20.25, "repeatDays": 0}`)
	y.Programs(now, now.Add(time.Hour))
	y.mu.Lock()
	before := slices.Clone(y.plan.Airings)
	y.mu.Unlock()
	if !slices.ContainsFunc(before, func(a ytAiring) bool { return a.ID == "approx20" && a.Start.After(now.Add(12*time.Hour)) }) {
		t.Fatal("approx20 should be planned later than 12 hours on")
	}
	later := now.Add(13 * time.Hour) // approx20 is 20.5 days old
	y.now = func() time.Time { return later }
	y.Programs(later, later.Add(time.Hour))
	y.mu.Lock()
	after := slices.Clone(y.plan.Airings)
	y.mu.Unlock()
	if len(after) < len(before) || !sameAirings(after[:len(before)], before) {
		t.Error("the schedule changed when a planned video aged out")
	}
	for _, a := range after[len(before):] {
		if a.ID == "approx20" {
			t.Errorf("picked approx20 at %v, after it aged out", a.Start)
		}
	}
}

// TestYtAudioLangAndCaptions: a lookup tells the sound's language and
// whether there are English captions, and the guide carries them, so the
// app turns captions on for a Spanish video.
func TestYtAudioLangAndCaptions(t *testing.T) {
	now := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	es, err := parseYtInfo(readTestdata(t, "yt_video_es.json"))
	if err != nil {
		t.Fatal(err)
	}
	en, err := parseYtInfo(readTestdata(t, "yt_video.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spanish, plain ytVideo
	spanish.learn(es, now)
	plain.learn(en, now)
	if spanish.AudioLang != "es" || !spanish.Captions || !cc.Foreign(spanish.AudioLang) {
		t.Errorf("Spanish video: %q, captions %v", spanish.AudioLang, spanish.Captions)
	}
	if plain.AudioLang != "" || plain.Captions || cc.Foreign(plain.AudioLang) {
		t.Errorf("video without either: %q, captions %v", plain.AudioLang, plain.Captions)
	}

	y := maxAgeChannel(t, now, `{"channels": ["@a"], "maxAgeDays": 15}`)
	y.mu.Lock()
	for _, v := range y.cat.Sources[0].Videos {
		v.AudioLang, v.Captions = "es-419", true
	}
	y.mu.Unlock()
	progs := y.Programs(now, now.Add(time.Hour))
	if len(progs) == 0 || progs[0].AudioLang != "es-419" || !progs[0].Captions {
		t.Errorf("guide: %+v", progs)
	}
}
