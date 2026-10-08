package dvr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/hdhr"
	"airwaves/internal/hdhr/hdhrtest"
	"airwaves/internal/lineup"
	"airwaves/internal/ts"
	"airwaves/internal/ts/tstest"
	"airwaves/internal/tuner"
)

// Where the test's channels are: 9.1 and 9.2 on one RF channel, 4.1 and
// 2.1 on others; 7.1 isn't in the tuner's lineup.
var where = map[string][2]int64{
	"9.1": {189_000_000, 3},
	"9.2": {189_000_000, 4},
	"4.1": {557_000_000, 1},
	"2.1": {605_000_000, 6},
}

func mux(prog ...int) []byte {
	var ps []tstest.Program
	for i, n := range prog {
		pid := 0x30 + i*0x10
		ps = append(ps, tstest.Program{Number: n, PMTPID: pid, VideoPID: pid + 1, Audio: []tstest.Audio{{PID: pid + 4, Lang: "eng"}}})
	}
	return tstest.Mux{TSID: prog[0], Programs: ps}.Packets(400)
}

func locate(_ context.Context, number string) (int64, int, error) {
	if w, ok := where[number]; ok {
		return w[0], int(w[1]), nil
	}
	return 0, 0, fmt.Errorf("channel %s isn't in the tuner's lineup", number)
}

type env struct {
	m     *Manager
	srv   *hdhrtest.Server
	g     *guide.Guide
	chans []lineup.Channel
	base  time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	muxes := map[int64][]byte{189_000_000: mux(3, 4), 557_000_000: mux(1), 605_000_000: mux(6)}
	srv := hdhrtest.New(t, hdhrtest.Device{Mux: func(f int64) []byte { return muxes[f] }})
	dev, err := hdhr.Open(context.Background(), srv.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tuners := tuner.NewManager()
	tuners.Linger = 0
	tuners.SetDevice(dev, dev.Tuners)
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "dvr.json"), filepath.Join(dir, "recordings"), tuners, locate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	base := time.Now().Truncate(time.Hour).Add(2 * time.Hour)
	ep := func(h int, id string, flags ...string) guide.Program {
		s := base.Add(time.Duration(h) * time.Hour)
		return guide.Program{Start: s, End: s.Add(time.Hour), Title: "Nature", SeriesID: "SH1", ProgramID: id, Flags: flags}
	}
	g := &guide.Guide{Programs: map[string][]guide.Program{
		"g9":  {ep(0, "EP1", "New"), ep(1, "EP1"), ep(2, "EP2"), {Start: base.Add(3 * time.Hour), End: base.Add(4 * time.Hour), Title: "News", SeriesID: "SH2", ProgramID: "EP9"}},
		"g92": {{Start: base, End: base.Add(time.Hour), Title: "Western", SeriesID: "SH4", ProgramID: "MV1"}},
		"g4":  {{Start: base, End: base.Add(time.Hour), Title: "Golf", SeriesID: "SH3", ProgramID: "SP1", Genres: []string{"sports"}}},
		"g2":  {{Start: base, End: base.Add(time.Hour), Title: "Cartoons", SeriesID: "SH5", ProgramID: "EP5"}},
		"g7":  {{Start: base, End: base.Add(time.Hour), Title: "Quiz", SeriesID: "SH6", ProgramID: "EP6"}},
	}}
	chans := []lineup.Channel{
		{Number: "9.1", CallSign: "KUSADT", GuideID: "g9"}, {Number: "9.2", CallSign: "KUSADT2", GuideID: "g92"},
		{Number: "4.1", CallSign: "KCNCDT", GuideID: "g4"}, {Number: "2.1", CallSign: "KWGNDT", GuideID: "g2"},
		{Number: "7.1", CallSign: "KMGHDT", GuideID: "g7"},
	}
	return &env{m: m, srv: srv, g: g, chans: chans, base: base}
}

func (e *env) add(t *testing.T, kind Kind, channel string, start time.Time) *Rule {
	t.Helper()
	call := ""
	for _, c := range e.chans {
		if c.Number == channel {
			call = c.CallSign
		}
	}
	r, err := e.m.Add(context.Background(), Request{Kind: kind, Channel: channel, CallSign: call, Start: start}, e.g, e.chans)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) upcoming(t *testing.T) []Item {
	t.Helper()
	st, err := e.m.State(context.Background(), e.g, e.chans)
	if err != nil {
		t.Fatal(err)
	}
	return st.Upcoming
}

func titles(items []Item) string {
	var out []string
	for _, it := range items {
		out = append(out, fmt.Sprintf("%s@%s:%s", it.Title, it.Start.Sub(it.Start.Truncate(24*time.Hour)), it.Status))
	}
	return strings.Join(out, " ")
}

func TestSeriesSkipsRepeatsAndOtherShows(t *testing.T) {
	e := setup(t)
	e.add(t, Series, "9.1", e.base)
	up := e.upcoming(t)
	// EP1 airs twice; only the first airing is recorded. News is not part of the series.
	if len(up) != 2 || up[0].Start != e.base || up[1].Start != e.base.Add(2*time.Hour) || strings.Contains(titles(up), "News") {
		t.Fatalf("upcoming %s, want the first EP1 airing and EP2", titles(up))
	}
}

func TestNewOnly(t *testing.T) {
	e := setup(t)
	if _, err := e.m.Add(context.Background(), Request{Kind: Series, Channel: "9.1", CallSign: "KUSADT", Start: e.base, NewOnly: true}, e.g, e.chans); err != nil {
		t.Fatal(err)
	}
	if up := e.upcoming(t); len(up) != 1 {
		t.Fatalf("upcoming %s, want only the new one", titles(up))
	}
}

// TestSkipAndDeleteRule: an airing removed from a series stays removed
// (its episode is recorded at a repeat), and a deleted rule records
// nothing more.
func TestSkipAndDeleteRule(t *testing.T) {
	e := setup(t)
	rule := e.add(t, Series, "9.1", e.base)
	up := e.upcoming(t)
	if up[0].ID != up[0].Key || up[0].Status != Scheduled {
		t.Fatalf("an airing to come is %+v, want its key as its ID", up[0])
	}
	if err := e.m.DeleteEntry(context.Background(), up[0].ID); err != nil {
		t.Fatal(err)
	}
	// The episode is recorded at its repeat instead.
	if up := e.upcoming(t); len(up) != 2 || !up[0].Start.Equal(e.base.Add(time.Hour)) {
		t.Fatalf("after skipping the first: %s", titles(up))
	}
	if err := e.m.DeleteRule(context.Background(), rule.ID); err != nil {
		t.Fatal(err)
	}
	if up := e.upcoming(t); len(up) != 0 {
		t.Fatalf("deleting the rule left %s", titles(up))
	}
}

func TestUntunedChannelIsUnavailable(t *testing.T) {
	e := setup(t)
	e.add(t, Once, "7.1", e.base)
	up := e.upcoming(t)
	if len(up) != 1 || up[0].Status != Unavailable || up[0].Title != "Quiz" || !strings.Contains(up[0].Detail, "lineup") {
		t.Fatalf("upcoming = %+v, want Quiz marked unavailable", up)
	}
}

// TestConflicts: two tuners record two RF channels at once; a third RF
// channel then conflicts, while another program of an RF channel being
// recorded shares its tuner.
func TestConflicts(t *testing.T) {
	e := setup(t)
	e.add(t, Once, "9.1", e.base)
	e.add(t, Once, "4.1", e.base)
	e.add(t, Once, "9.2", e.base)
	e.add(t, Once, "2.1", e.base)
	got := map[string]Item{}
	for _, it := range e.upcoming(t) {
		got[it.Title] = it
	}
	for title, want := range map[string]string{"Nature": Scheduled, "Golf": Scheduled, "Western": Scheduled, "Cartoons": Conflict} {
		if got[title].Status != want {
			t.Errorf("%s is %s (%s), want %s", title, got[title].Status, got[title].Detail, want)
		}
	}
	if d := got["Cartoons"].Detail; !strings.Contains(d, "Golf") || !strings.Contains(d, "Nature") {
		t.Errorf("conflict detail %q doesn't say what records then", d)
	}
}

// recordNow records an airing on now: 9.2's, which began 10 minutes ago.
func recordNow(t *testing.T, e *env) Item {
	t.Helper()
	now := time.Now()
	e.g.Programs["g92"] = []guide.Program{{Start: now.Add(-10 * time.Minute), End: now.Add(50 * time.Minute), Title: "Western", ProgramID: "MV1"}}
	e.add(t, Once, "9.2", now.Add(-10*time.Minute))
	var it Item
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		up := e.upcoming(t)
		if len(up) == 1 && up[0].Status == Recording && up[0].SizeBytes > 0 {
			it = up[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not recording: %s", titles(up))
		}
	}
	return it
}

// TestRecordStopKeeps: an airing on now records at once, into its own
// file holding its program alone; stopping it keeps what it recorded,
// and says what's missing.
func TestRecordStopKeeps(t *testing.T) {
	e := setup(t)
	it := recordNow(t, e)
	if until := it.Until; until.Before(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("records until %s, want the end with padding", until)
	}
	if err := e.m.Stop(it.ID); err != nil {
		t.Fatal(err)
	}
	st, _ := e.m.State(context.Background(), e.g, e.chans)
	if len(st.Recorded) != 1 || len(st.Upcoming) != 0 {
		t.Fatalf("after stopping: recorded %s, upcoming %s", titles(st.Recorded), titles(st.Upcoming))
	}
	done := st.Recorded[0]
	if done.Status != Completed || !strings.Contains(done.Detail, "Began 10 minutes into the program") || !strings.Contains(done.Detail, "Stopped 50 minutes before the end") {
		t.Fatalf("recorded = %+v", done)
	}
	r, path, ok := e.m.Get(done.ID)
	if !ok || !strings.HasPrefix(r.File, "Western/") {
		t.Fatalf("file %q", r.File)
	}
	raw, err := os.ReadFile(path)
	if err != nil || int64(len(raw)) != done.SizeBytes {
		t.Fatalf("file has %d bytes (%v), the recording says %d", len(raw), err, done.SizeBytes)
	}
	d := ts.NewDemux()
	var split ts.Splitter
	split.Write(raw, d.Packet)
	if ps := d.Programs(); len(ps) != 1 || ps[0].Number != 4 {
		t.Fatalf("the file carries %+v, want program 4 alone", ps)
	}
}

// TestExtend moves a recording's end while it records.
func TestExtend(t *testing.T) {
	e := setup(t)
	it := recordNow(t, e)
	until, err := e.m.Extend(it.ID, 30*time.Minute)
	if err != nil || !until.Equal(it.Until.Add(30*time.Minute)) {
		t.Fatalf("extended to %s (%v), want %s", until, err, it.Until.Add(30*time.Minute))
	}
	if up := e.upcoming(t); !up[0].Until.Equal(until) {
		t.Fatalf("shown until %s", up[0].Until)
	}
}

// TestDeleteWhileRecording discards the recording, and never records
// that airing again.
func TestDeleteWhileRecording(t *testing.T) {
	e := setup(t)
	it := recordNow(t, e)
	_, path, _ := e.m.Get(it.ID)
	if err := e.m.DeleteEntry(context.Background(), it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
	e.m.Start(e.g, e.chans, time.Now())
	st, _ := e.m.State(context.Background(), e.g, e.chans)
	if len(st.Recorded)+len(st.Upcoming) != 0 {
		t.Fatalf("after deleting: recorded %s, upcoming %s", titles(st.Recorded), titles(st.Upcoming))
	}
}

// TestResume: a recording the server was making when it stopped carries
// on after a restart if its time isn't up, and is finished otherwise.
func TestResume(t *testing.T) {
	e := setup(t)
	it := recordNow(t, e)
	e.m.Close()
	path := e.m.path
	r, file, _ := e.m.Get(it.ID)
	if r.Status != Recording {
		t.Fatalf("after a shutdown the recording is %s", r.Status)
	}
	size := r.Size

	again, err := Open(path, e.m.dir, e.m.tuners, locate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	again.resume(time.Now())
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if fi, err := os.Stat(file); err == nil && fi.Size() > size {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the recording didn't carry on")
		}
	}
	again.Close()

	later, err := Open(path, e.m.dir, e.m.tuners, locate)
	if err != nil {
		t.Fatal(err)
	}
	later.resume(time.Now().Add(2 * time.Hour))
	if r, _, _ := later.Get(it.ID); r.Status != Completed || !strings.Contains(r.Detail, "the server stopped") {
		t.Fatalf("a recording whose time ran out while the server was down is %s: %q", r.Status, r.Detail)
	}
}

func TestPersistence(t *testing.T) {
	e := setup(t)
	e.add(t, Series, "9.1", e.base)
	again, err := Open(e.m.path, e.m.dir, e.m.tuners, locate)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.st.Rules) != 1 || again.st.Rules[0].SeriesID != "SH1" {
		t.Fatalf("reloaded rules %+v", again.st.Rules)
	}
}

// TestCorruptFile: a state file that can't be read is set aside, and the
// DVR starts empty instead of not at all.
func TestCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dvr.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, dir, tuner.NewManager(), locate)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if len(m.st.Rules) != 0 {
		t.Fatal("rules from a corrupt file")
	}
	if raw, err := os.ReadFile(path + ".bad"); err != nil || string(raw) != "{not json" {
		t.Fatalf("the corrupt file wasn't kept aside: %v", err)
	}
}

func TestRetainKeepsNewestPerSeries(t *testing.T) {
	e := setup(t)
	rule := e.add(t, Series, "9.1", e.base)
	old := time.Now().Add(-30 * 24 * time.Hour)
	e.m.mu.Lock()
	for i, id := range []string{"f1", "f2", "f3"} {
		s := old.Add(time.Duration(i) * 24 * time.Hour)
		e.m.st.Recordings = append(e.m.st.Recordings, Rec{ID: id, RuleID: rule.ID, Title: "Nature", Start: s, End: s.Add(time.Hour), Status: Completed, File: "Nature/" + id + ".ts"})
	}
	e.m.st.Recordings = append(e.m.st.Recordings, Rec{ID: "other", Title: "Movie", Start: old, End: old.Add(2 * time.Hour), Status: Completed, File: "Movie/other.ts"})
	e.m.mu.Unlock()
	for _, f := range []string{"Nature/f1.ts", "Nature/f2.ts", "Nature/f3.ts", "Movie/other.ts"} {
		p := filepath.Join(e.m.dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}

	if _, err := e.m.UpdateRule(rule.ID, RuleUpdate{Keep: 2}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Retain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.m.Get("f1"); ok || len(e.m.st.Recordings) != 3 {
		t.Fatalf("after keep=2: %d left, want f1 (oldest) removed and the movie kept", len(e.m.st.Recordings))
	}
	if _, err := os.Stat(filepath.Join(e.m.dir, "Nature/f1.ts")); !os.IsNotExist(err) {
		t.Fatal("f1's file is still there")
	}

	// Watched recordings go after the configured number of days.
	if err := e.m.MarkWatched("other", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetPrefs(Prefs{DeleteWatchedAfterDays: 7}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Retain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.m.Get("other"); ok {
		t.Fatal("watched recording older than 7 days was kept")
	}
	if _, err := os.Stat(filepath.Join(e.m.dir, "Movie")); !os.IsNotExist(err) {
		t.Fatal("the show's emptied folder is still there")
	}
}

func TestProgressMarksWatchedNearTheEnd(t *testing.T) {
	e := setup(t)
	m := e.m
	if err := m.SaveProgress("e1", 600, 3600); err != nil {
		t.Fatal(err)
	}
	if p := m.st.Progress["e1"]; p.Watched || p.Position != 600 {
		t.Fatalf("mid-way progress = %+v", p)
	}
	if err := m.SaveProgress("e1", 3500, 3600); err != nil {
		t.Fatal(err)
	}
	if !m.st.Progress["e1"].Watched {
		t.Fatal("within the last two minutes should count as watched")
	}
	if err := m.MarkWatched("e1", false); err != nil {
		t.Fatal(err)
	}
	if p := m.st.Progress["e1"]; p.Watched || p.Position != 0 {
		t.Fatalf("unwatched = %+v, want reset", p)
	}
}

func TestFileNames(t *testing.T) {
	for in, want := range map[string]string{
		"AC/DC: Live":           "AC-DC: Live",
		"..hidden":              "hidden",
		"tab\there":             "tabhere",
		strings.Repeat("é", 60): strings.Repeat("é", 40),
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}
