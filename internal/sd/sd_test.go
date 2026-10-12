package sd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSD is just enough of Schedules Direct: one OTA lineup of two
// stations, a day of airings each, and the programs they air.
type fakeSD struct {
	mu       sync.Mutex
	password string // the SHA-1 it accepts
	calls    map[string]int
	asked    []string // schedule station-days requested
	programs []string // program IDs requested
	added    bool
	expire   bool // the next request gets 4006 once
	md5      string
}

func (f *fakeSD) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/")
	f.calls[r.Method+" "+path]++
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	fail := func(status, code int, msg string) {
		w.WriteHeader(status)
		reply(map[string]any{"code": code, "response": msg, "message": msg})
	}
	if !strings.HasPrefix(r.UserAgent(), "airwaves/") {
		fail(400, 1003, "NO_USER_AGENT")
		return
	}
	if path == "token" {
		var in struct{ Username, Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Password != f.password {
			fail(400, 4003, "INVALID_USER")
			return
		}
		now := time.Now().Unix()
		reply(map[string]any{"code": 0, "token": "tok", "tokenExpires": now + 86400, "serverTime": now})
		return
	}
	if r.Header.Get("Token") != "tok" {
		fail(403, 4006, "TOKEN_EXPIRED")
		return
	}
	if f.expire {
		f.expire = false
		fail(403, 4006, "TOKEN_EXPIRED")
		return
	}
	today := time.Now().UTC().Format(time.DateOnly)
	switch path {
	case "status":
		lineups := []map[string]any{}
		if f.added {
			lineups = append(lineups, map[string]any{"lineup": "USA-OTA-80302", "modified": "2026-10-01T00:00:00Z"})
		}
		reply(map[string]any{"code": 0, "account": map[string]any{"expires": "2026-10-19T00:00:00Z", "maxLineups": 4},
			"lineups": lineups, "systemStatus": []any{map[string]any{"status": "Online"}}})
	case "lineups/USA-OTA-80302":
		if r.Method == http.MethodPut {
			f.added = true
			reply(map[string]any{"code": 0, "response": "OK", "changesRemaining": 5})
			return
		}
		reply(map[string]any{
			"map": []any{
				map[string]any{"stationID": "20370", "uhfVhf": 36, "atscMajor": 2, "atscMinor": 1},
				map[string]any{"stationID": "21252", "uhfVhf": 35, "atscMajor": 4, "atscMinor": 1},
			},
			"stations": []any{
				map[string]any{"stationID": "20370", "callsign": "KWGNDT", "name": "KWGNDT (KWGN-DT)", "affiliate": "CW",
					"stationLogo": []any{map[string]any{"URL": "https://example.com/kwgn-dark.png", "category": "dark"}, map[string]any{"URL": "https://example.com/kwgn-light.png", "category": "light"}}},
				map[string]any{"stationID": "21252", "callsign": "KCNCDT", "affiliate": "CBS"},
			},
		})
	case "schedules/md5":
		out := map[string]any{}
		for _, id := range []string{"20370", "21252"} {
			out[id] = map[string]any{today: map[string]any{"code": 0, "md5": f.md5}}
		}
		out["99999"] = []any{} // a station with nothing
		reply(out)
	case "schedules":
		var in []stationDates
		_ = json.NewDecoder(r.Body).Decode(&in)
		var out []any
		start := time.Now().UTC().Truncate(time.Hour)
		for _, s := range in {
			for _, d := range s.Date {
				f.asked = append(f.asked, s.StationID+"@"+d)
				progs := []any{
					map[string]any{"programID": "EP013657560045", "airDateTime": start.Format(time.RFC3339), "duration": 3600, "md5": "p1",
						"new": true, "audioProperties": []string{"cc", "stereo", "dvs"}, "videoProperties": []string{"hdtv"},
						"ratings": []any{map[string]any{"body": "USA Parental Rating", "code": "TV14"}}},
					map[string]any{"programID": "MV000087710000", "airDateTime": start.Add(time.Hour).Format(time.RFC3339), "duration": 7200, "md5": "p2"},
				}
				if s.StationID == "21252" {
					progs = []any{map[string]any{"programID": "SP003190180000", "airDateTime": start.Format(time.RFC3339), "duration": 10800, "md5": "p3", "liveTapeDelay": "Live"}}
				}
				out = append(out, map[string]any{"stationID": s.StationID, "programs": progs, "metadata": map[string]any{"md5": f.md5, "startDate": d}})
			}
		}
		reply(out)
	case "programs":
		var ids []string
		_ = json.NewDecoder(r.Body).Decode(&ids)
		f.programs = append(f.programs, ids...)
		all := map[string]any{
			"EP013657560045": map[string]any{"programID": "EP013657560045", "md5": "p1", "titles": []any{map[string]any{"title120": "The Voice"}},
				"episodeTitle150": "The Knockouts", "originalAirDate": "2026-10-07", "genres": []string{"Reality", "Music"},
				"descriptions": map[string]any{"description100": []any{map[string]any{"descriptionLanguage": "en", "description": "Short."}},
					"description1000": []any{map[string]any{"descriptionLanguage": "en", "description": "The knockouts continue, longer."}}},
				"cast":     []any{map[string]any{"billingOrder": "02", "name": "Second"}, map[string]any{"billingOrder": "01", "name": "First", "characterName": "Coach"}},
				"metadata": []any{map[string]any{"Gracenote": map[string]any{"season": 28, "episode": 12}}}, "entityType": "Episode"},
			"MV000087710000": map[string]any{"programID": "MV000087710000", "md5": "p2", "titles": []any{map[string]any{"title120": "The Wizard of Oz"}},
				"entityType": "Movie", "contentRating": []any{map[string]any{"body": "Motion Picture Association", "code": "PG"}},
				"movie": map[string]any{"year": "1939", "duration": 6120, "qualityRating": []any{map[string]any{"ratingsBody": "Gracenote", "rating": "4"}}}},
			"SP003190180000": map[string]any{"programID": "SP003190180000", "md5": "p3", "titles": []any{map[string]any{"title120": "NFL Football"}},
				"entityType": "Sports", "genres": []string{"Sports event", "Football"},
				"eventDetails": map[string]any{"teams": []any{map[string]any{"name": "Kansas City Chiefs"}, map[string]any{"name": "Denver Broncos", "isHome": true}}}},
		}
		var out []any
		for _, id := range ids {
			if p, ok := all[id]; ok {
				out = append(out, p)
			} else {
				out = append(out, map[string]any{"programID": id, "code": 6000})
			}
		}
		reply(out)
	default:
		fail(404, 1008, "INCORRECT_REQUEST")
	}
}

func newSource(t *testing.T) (*Source, *fakeSD) {
	t.Helper()
	f := &fakeSD{password: HashPassword("hunter2"), calls: map[string]int{}, md5: "day1"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s := &Source{Dir: t.TempDir(), ZIP: "80302", HTTP: srv.Client(), UserAgent: "airwaves/test", BaseURL: srv.URL + "/"}
	return s, f
}

func (f *fakeSD) count(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[call]
}

// TestUpdate: a first update logs in, adds the antenna lineup and fetches
// everything; a second asks what changed and fetches nothing more; the
// listings come out as the guide has them.
func TestUpdate(t *testing.T) {
	s, f := newSource(t)
	if err := s.SetAccount("viewer", "hunter2"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, _ := LoadAccount(s.accountPath())
	s.update(ctx, a)
	st := s.Status()
	if !st.OK || st.Error != "" || st.Lineup != "USA-OTA-80302" || st.Stations != 2 || st.Programs == 0 || st.Expires.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if f.count("POST token") != 1 || f.count("PUT lineups/USA-OTA-80302") != 1 || len(f.programs) != 3 {
		t.Fatalf("calls %v, programs %v", f.calls, f.programs)
	}

	// Nothing changed: no new login, no schedules, no programs.
	asked := len(f.asked)
	s.update(ctx, a)
	if f.count("POST token") != 1 || len(f.asked) != asked || len(f.programs) != 3 || f.count("PUT lineups/USA-OTA-80302") != 1 {
		t.Fatalf("second update: calls %v, asked %d more", f.calls, len(f.asked)-asked)
	}
	// A changed day is fetched again, its unchanged programs not.
	f.mu.Lock()
	f.md5 = "day2"
	f.mu.Unlock()
	s.update(ctx, a)
	if len(f.asked) == asked || len(f.programs) != 3 {
		t.Fatalf("changed day: asked %d more, programs %v", len(f.asked)-asked, f.programs)
	}

	g := s.Guide()
	if g == nil || len(g.Channels) != 2 || g.Channels[0].Number != "2.1" || g.Channels[0].CallSign != "KWGNDT" || g.Channels[0].Logo != "https://example.com/kwgn-light.png" || g.Channels[0].LogoDark != "https://example.com/kwgn-dark.png" {
		t.Fatalf("guide channels = %+v", g)
	}
	ps := g.Programs["20370"]
	if len(ps) != 2 {
		t.Fatalf("2.1 programs = %+v", ps)
	}
	ep := ps[0]
	if ep.Title != "The Voice" || ep.EpisodeTitle != "The Knockouts" || ep.Description != "The knockouts continue, longer." ||
		ep.Season != "28" || ep.Episode != "12" || ep.SeriesID != "SH01365756" || ep.Rating != "TV-14" ||
		strings.Join(ep.Flags, ",") != "New" || strings.Join(ep.Tags, ",") != "CC,Stereo,DVS,HD" ||
		strings.Join(ep.Cast, ",") != "First (Coach),Second" || ep.OriginalAirDate != "2026-10-07" || strings.Join(ep.Genres, ",") != "music,Reality" {
		t.Errorf("episode = %+v", ep)
	}
	if mv := ps[1]; mv.Year != "1939" || mv.Runtime != 102 || mv.Stars != 4 || mv.Rating != "PG" || mv.SeriesID != "" || mv.Genres[0] != "movie" {
		t.Errorf("movie = %+v", mv)
	}
	game := g.Programs["21252"][0]
	if game.HomeTeam != "Denver Broncos" || game.AwayTeam != "Kansas City Chiefs" || game.Flags[0] != "Live" || game.Genres[0] != "sports" {
		t.Errorf("game = %+v", game)
	}

	// Kept across a restart.
	again := &Source{Dir: s.Dir, ZIP: "80302", HTTP: s.HTTP, UserAgent: "airwaves/test", BaseURL: s.BaseURL}
	if g := again.Guide(); g == nil || len(g.Programs["20370"]) != 2 || again.Status().Lineup != "USA-OTA-80302" {
		t.Fatalf("after a restart: %+v", again.Status())
	}
}

// TestWrongPassword: a refused login stops updating, with no more logins,
// until the account is saved again.
func TestWrongPassword(t *testing.T) {
	s, f := newSource(t)
	if err := s.SetAccount("viewer", "wrong"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); s.Status().Stopped == ""; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("not stopped")
		}
	}
	if st := s.Status(); !strings.Contains(st.Stopped, "user name and password") || st.OK {
		t.Fatalf("status = %+v", st)
	}
	s.Kick()
	time.Sleep(100 * time.Millisecond)
	if n := f.count("POST token"); n != 1 {
		t.Fatalf("%d logins after a refusal", n)
	}
	// Saving the account again tries again, once.
	if err := s.SetAccount("viewer", "hunter2"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); !s.Status().OK; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("not updated after fixing the password: %+v", s.Status())
		}
	}
	if n := f.count("POST token"); n != 2 {
		t.Fatalf("%d logins", n)
	}
	cancel()
	<-done
}

// TestTokenExpired: a token refused mid-update is renewed once, and the
// request retried.
func TestTokenExpired(t *testing.T) {
	s, f := newSource(t)
	_ = s.SetAccount("viewer", "hunter2")
	a, _ := LoadAccount(s.accountPath())
	s.update(context.Background(), a)
	f.mu.Lock()
	f.expire = true
	f.md5 = "day3"
	f.mu.Unlock()
	s.update(context.Background(), a)
	if st := s.Status(); !st.OK || f.count("POST token") != 2 {
		t.Fatalf("status %+v, %d logins", st, f.count("POST token"))
	}
}

// TestNotJSON: a page that isn't JSON (a load balancer's block) stops
// updating rather than knocking again.
func TestNotJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<html>blocked</html>")
	}))
	defer srv.Close()
	s := &Source{Dir: t.TempDir(), ZIP: "80302", HTTP: srv.Client(), UserAgent: "airwaves/test", BaseURL: srv.URL + "/"}
	_ = s.SetAccount("viewer", "hunter2")
	a, _ := LoadAccount(s.accountPath())
	s.update(context.Background(), a)
	if st := s.Status(); !strings.Contains(st.Stopped, "other than JSON") {
		t.Fatalf("status = %+v", st)
	}
}

func TestPasswordHash(t *testing.T) {
	if got := HashPassword("password"); got != "5baa61e4c9b93f3f0682250b6cf8331b7ee68fd8" {
		t.Fatalf("SHA-1 = %s", got)
	}
	s, _ := newSource(t)
	_ = s.SetAccount("viewer", "hunter2")
	raw, _ := LoadAccount(s.accountPath())
	if raw.PasswordSHA1 != HashPassword("hunter2") || strings.Contains(fmt.Sprint(raw), "hunter2") {
		t.Fatalf("kept %+v", raw)
	}
	// An empty password keeps the saved one for the same user.
	if err := s.SetAccount("viewer", ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := LoadAccount(s.accountPath()); a.PasswordSHA1 != HashPassword("hunter2") {
		t.Fatal("the saved password was dropped")
	}
	if err := s.SetAccount("someone", ""); err == nil {
		t.Fatal("a new user without a password was taken")
	}
}

// TestRunOncePerDay: after an update, Run waits for the next day, however
// short the listings are (here, a day of them).
func TestRunOncePerDay(t *testing.T) {
	s, f := newSource(t)
	_ = s.SetAccount("viewer", "hunter2")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); !s.Status().OK; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no update")
		}
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if n := f.count("POST schedules/md5"); n != 1 {
		t.Fatalf("%d updates, want one", n)
	}
	if next := s.Status().Next; time.Until(next) < 11*time.Hour || time.Until(next) > 37*time.Hour {
		t.Fatalf("next update %s", next)
	}
}

// TestOldLogos: stations kept before their logos' variants were have the
// lineup read again at the next start, without the schedules.
func TestOldLogos(t *testing.T) {
	s, f := newSource(t)
	_ = s.SetAccount("viewer", "hunter2")
	a, _ := LoadAccount(s.accountPath())
	s.update(context.Background(), a)
	st, _ := loadState(s.statePath())
	for i := range st.Stations {
		st.Stations[i].Logos, st.Stations[i].Logo = nil, "https://example.com/white.png"
	}
	if err := st.save(s.statePath()); err != nil {
		t.Fatal(err)
	}
	again := &Source{Dir: s.Dir, ZIP: "80302", HTTP: s.HTTP, UserAgent: "airwaves/test", BaseURL: s.BaseURL}
	if g := again.Guide(); g == nil || g.Channels[0].Logo != "https://example.com/white.png" {
		t.Fatalf("before: %+v", g)
	}
	asked := len(f.asked)
	again.update(context.Background(), a)
	if g := again.Guide(); g.Channels[0].Logo != "https://example.com/kwgn-light.png" || len(f.asked) != asked || f.count("GET lineups/USA-OTA-80302") != 2 {
		t.Fatalf("after: logo %q, %d schedules asked, calls %v", g.Channels[0].Logo, len(f.asked)-asked, f.calls)
	}
}
