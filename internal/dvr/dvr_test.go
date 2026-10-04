package dvr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/tvh"
)

// fakeTVH is just enough of Tvheadend's DVR API.
type fakeTVH struct {
	mu       sync.Mutex
	next     int
	upcoming map[string]tvh.Entry
	finished map[string]tvh.Entry
}

func (f *fakeTVH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	list := func(m map[string]tvh.Entry) {
		var out []tvh.Entry
		for _, e := range m {
			out = append(out, e)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": out})
	}
	switch r.URL.Path {
	case "/api/serverinfo":
		fmt.Fprint(w, `{"sw_version":"test"}`)
	case "/api/dvr/entry/grid_upcoming":
		list(f.upcoming)
	case "/api/dvr/entry/grid_finished":
		list(f.finished)
	case "/api/dvr/entry/grid_failed":
		list(nil)
	case "/api/dvr/entry/create":
		var conf struct {
			Channel string            `json:"channel"`
			Start   int64             `json:"start"`
			Stop    int64             `json:"stop"`
			Title   map[string]string `json:"title"`
			Comment string            `json:"comment"`
		}
		_ = json.Unmarshal([]byte(r.Form.Get("conf")), &conf)
		f.next++
		id := fmt.Sprintf("e%d", f.next)
		f.upcoming[id] = tvh.Entry{UUID: id, Title: conf.Title["eng"], Channel: conf.Channel, Start: conf.Start, Stop: conf.Stop, Comment: conf.Comment, SchedStatus: "scheduled"}
		fmt.Fprintf(w, `{"uuid":%q}`, id)
	case "/api/idnode/delete", "/api/dvr/entry/cancel":
		delete(f.upcoming, r.Form.Get("uuid"))
		fmt.Fprint(w, `{}`)
	case "/api/dvr/entry/remove":
		delete(f.finished, r.Form.Get("uuid"))
		fmt.Fprint(w, `{}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTVH) titles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.upcoming {
		out = append(out, fmt.Sprintf("%s@%d", e.Title, e.Start))
	}
	return out
}

type channels map[string]string

func (c channels) ChannelUUID(_ context.Context, n string) (string, error) {
	if id, ok := c[n]; ok {
		return id, nil
	}
	return "", fmt.Errorf("channel %s not tuned", n)
}

func setup(t *testing.T) (*Manager, *fakeTVH, *guide.Guide, []lineup.Channel, time.Time) {
	t.Helper()
	fake := &fakeTVH{upcoming: map[string]tvh.Entry{}, finished: map[string]tvh.Entry{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	m, err := Open(filepath.Join(t.TempDir(), "dvr.json"), tvh.New(srv.URL, srv.Client()), channels{"9.1": "ch-kusa"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Truncate(time.Hour).Add(2 * time.Hour)
	ep := func(h int, id string, flags ...string) guide.Program {
		s := base.Add(time.Duration(h) * time.Hour)
		return guide.Program{Start: s, End: s.Add(time.Hour), Title: "Nature", SeriesID: "SH1", ProgramID: id, Flags: flags}
	}
	g := &guide.Guide{Programs: map[string][]guide.Program{
		"g9": {ep(0, "EP1", "New"), ep(1, "EP1"), ep(2, "EP2"), {Start: base.Add(3 * time.Hour), End: base.Add(4 * time.Hour), Title: "News", SeriesID: "SH2", ProgramID: "EP9"}},
		"g4": {{Start: base, End: base.Add(time.Hour), Title: "Golf", SeriesID: "SH3", ProgramID: "SP1"}},
	}}
	chans := []lineup.Channel{{Number: "9.1", CallSign: "KUSADT", GuideID: "g9"}, {Number: "4.1", CallSign: "KCNCDT", GuideID: "g4"}}
	return m, fake, g, chans, base
}

func TestSeriesSkipsRepeatsAndOtherShows(t *testing.T) {
	m, fake, g, chans, base := setup(t)
	ctx := context.Background()
	if _, err := m.Add(ctx, Request{Kind: Series, Channel: "9.1", CallSign: "KUSADT", Start: base}, g, chans); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fake.titles(), " ")
	// EP1 airs twice; only the first airing is recorded. News is not part of the series.
	if len(fake.titles()) != 2 || strings.Contains(got, "News") {
		t.Fatalf("scheduled %v, want the first EP1 airing and EP2", fake.titles())
	}

	// Reconciling again is a no-op.
	if err := m.Reconcile(ctx, g, chans); err != nil {
		t.Fatal(err)
	}
	if len(fake.titles()) != 2 {
		t.Fatalf("reconcile duplicated entries: %v", fake.titles())
	}
}

func TestNewOnly(t *testing.T) {
	m, fake, g, chans, base := setup(t)
	if _, err := m.Add(context.Background(), Request{Kind: Series, Channel: "9.1", CallSign: "KUSADT", Start: base, NewOnly: true}, g, chans); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.titles()); n != 1 {
		t.Fatalf("scheduled %d airings, want only the new one", n)
	}
}

func TestSkipAndDeleteRule(t *testing.T) {
	m, fake, g, chans, base := setup(t)
	ctx := context.Background()
	rule, err := m.Add(ctx, Request{Kind: Series, Channel: "9.1", CallSign: "KUSADT", Start: base}, g, chans)
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for id, e := range fake.upcoming {
		if e.Start == base.Unix() {
			first = id
		}
	}
	if err := m.DeleteEntry(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(ctx, g, chans); err != nil {
		t.Fatal(err)
	}
	for _, e := range fake.upcoming {
		if e.Start == base.Unix() {
			t.Fatal("skipped airing was scheduled again")
		}
	}
	if err := m.DeleteRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.titles()); n != 0 {
		t.Fatalf("deleting the rule left %d scheduled", n)
	}
}

func TestUntunedChannelIsUnavailable(t *testing.T) {
	m, fake, g, chans, base := setup(t)
	ctx := context.Background()
	if _, err := m.Add(ctx, Request{Kind: Once, Channel: "4.1", CallSign: "KCNCDT", Start: base}, g, chans); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.titles()); n != 0 {
		t.Fatalf("scheduled %d on an untuned channel", n)
	}
	st, err := m.State(ctx, g, chans)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Upcoming) != 1 || st.Upcoming[0].Status != "unavailable" || st.Upcoming[0].Title != "Golf" {
		t.Fatalf("upcoming = %+v, want Golf marked unavailable", st.Upcoming)
	}
}

func TestPersistence(t *testing.T) {
	m, _, g, chans, base := setup(t)
	if _, err := m.Add(context.Background(), Request{Kind: Series, Channel: "9.1", CallSign: "KUSADT", Start: base}, g, chans); err != nil {
		t.Fatal(err)
	}
	again, err := Open(m.path, m.tvh, m.channels)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.st.Rules) != 1 || again.st.Rules[0].SeriesID != "SH1" || len(again.st.Meta) != 2 {
		t.Fatalf("reloaded rules %+v meta %d", again.st.Rules, len(again.st.Meta))
	}
}

func TestRetainKeepsNewestPerSeries(t *testing.T) {
	m, fake, g, chans, base := setup(t)
	ctx := context.Background()
	rule, err := m.Add(ctx, Request{Kind: Series, Channel: "9.1", CallSign: "KUSADT", Start: base}, g, chans)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	fake.mu.Lock()
	for i, id := range []string{"f1", "f2", "f3"} {
		s := old.Add(time.Duration(i) * 24 * time.Hour).Unix()
		fake.finished[id] = tvh.Entry{UUID: id, Title: "Nature", Start: s, Stop: s + 3600, SchedStatus: "completed", Comment: comment(rule.ID, fmt.Sprintf("9.1@%d", s))}
	}
	fake.finished["other"] = tvh.Entry{UUID: "other", Title: "Movie", Start: old.Unix(), Stop: old.Unix() + 7200, SchedStatus: "completed"}
	fake.mu.Unlock()

	if _, err := m.UpdateRule(rule.ID, RuleUpdate{Keep: 2}); err != nil {
		t.Fatal(err)
	}
	if err := m.Retain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.finished["f1"]; ok || len(fake.finished) != 3 {
		t.Fatalf("after keep=2: %v, want f1 (oldest) removed and the movie kept", keys(fake.finished))
	}

	// Watched recordings go after the configured number of days.
	if err := m.MarkWatched("other", true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetPrefs(Prefs{DeleteWatchedAfterDays: 7}); err != nil {
		t.Fatal(err)
	}
	if err := m.Retain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.finished["other"]; ok {
		t.Fatal("watched recording older than 7 days was kept")
	}
}

func TestProgressMarksWatchedNearTheEnd(t *testing.T) {
	m, _, _, _, _ := setup(t)
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

func keys(m map[string]tvh.Entry) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
