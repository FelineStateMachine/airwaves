package vchan

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"airwaves/internal/jellyfin"
	"airwaves/internal/jellyfin/jellyfintest"
)

func jellyfinLibrary() []jellyfintest.Item {
	return []jellyfintest.Item{
		{ID: "lib-shows", Type: "CollectionFolder", Name: "Shows"},
		{ID: "fut", Type: "Series", Name: "Futurama", Parent: "lib-shows", Year: 1999, Image: true},
		{ID: "fut-1", Type: "Season", Name: "Season 1", Parent: "fut"},
		{ID: "fut-1-1", Type: "Episode", Name: "Space Pilot 3000", Parent: "fut-1", Season: 1, Episode: 1, Premiere: "1999-03-28",
			Runtime: 22 * time.Minute, Image: true, Overview: "Fry is frozen\nfor a thousand years.\n\nSpoilers follow."},
		{ID: "fut-1-2", Type: "Episode", Name: "The Series Has Landed", Parent: "fut-1", Season: 1, Episode: 2, Premiere: "1999-04-04", Runtime: 22 * time.Minute},
		{ID: "fut-1-9", Type: "Episode", Name: "Not Downloaded", Parent: "fut-1", Season: 1, Episode: 9, Missing: true},
		{ID: "btas", Type: "Series", Name: "Batman: The Animated Series", Parent: "lib-shows", Year: 1992},
		{ID: "btas-1-1", Type: "Episode", Name: "On Leather Wings", Parent: "btas", Season: 1, Episode: 1, Premiere: "1992-09-06", Runtime: 22 * time.Minute},
		{ID: "btas-1-2", Type: "Episode", Name: "Christmas with the Joker", Parent: "btas", Season: 1, Episode: 2, Premiere: "1992-11-13", Runtime: 22 * time.Minute},
		{ID: "btas-sp", Type: "Episode", Name: "Behind the Cowl", Parent: "btas", Premiere: "1993-01-01", Runtime: 22 * time.Minute},
		{ID: "giant", Type: "Movie", Name: "The Iron Giant", Year: 1999, Premiere: "1999-08-06", Runtime: 86 * time.Minute, Image: true},
		{ID: "thing82", Type: "Movie", Name: "The Thing", Year: 1982, Runtime: 109 * time.Minute},
		{ID: "thing11", Type: "Movie", Name: "The Thing", Year: 2011, Runtime: 103 * time.Minute},
		{ID: "bumper", Type: "Movie", Name: "Bumper", Runtime: 500 * time.Millisecond},
		{ID: "sat", Type: "BoxSet", Name: "Saturday Morning", Children: []string{"btas", "giant", "bumper"}},
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// jellyfinChannel writes a channel folder ("1.5 Cartoons") holding config
// as jellyfin.json, as the library would find it.
func newJellyfinChannel(t *testing.T, root, folder, config string) *Jellyfin {
	path := filepath.Join(root, folder, jellyfinConfig)
	writeFile(t, path, config)
	num, name, _ := strings.Cut(folder, " ")
	return &Jellyfin{Num: num, Title: name, Config: path, FFmpeg: "ffmpeg"}
}

// captureLogs collects log output for the rest of the test.
func captureLogs(t *testing.T) *lockedBuffer {
	b := &lockedBuffer{}
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return b
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestJellyfinChannelResolvesNames(t *testing.T) {
	logs := captureLogs(t)
	srv := jellyfintest.New(t, "viewer", "s3cret-pw", jellyfinLibrary()...)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, jellyfinAccount), fmt.Sprintf(`{"server": %q, "user": "viewer", "password": "s3cret-pw"}`, srv.URL))
	cartoons := newJellyfinChannel(t, root, "1.5 Saturday Cartoons", `{
		"collections": ["saturday morning"],
		"series": ["Futurama (1999)", "Nope Show"],
		"movies": ["The Thing (1982)", "Spider-Man"]
	}`)
	if cartoons.Empty() {
		t.Fatal("channel reads as empty")
	}
	// Batman's three, the Iron Giant (not the one-second bumper), Futurama's
	// two on file and the 1982 Thing.
	n, unmatched, err := cartoons.Status()
	if n != 7 || err != nil || !slices.Equal(unmatched, []string{"Nope Show", "Spider-Man"}) {
		t.Errorf("status = %d videos, unmatched %q, %v", n, unmatched, err)
	}
	for _, want := range []string{`no series named "Nope Show"`, `no movie named "Spider-Man"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}

	from := time.Now()
	progs := cartoons.Programs(from, from.Add(6*time.Hour))
	titles := map[string]bool{}
	for _, p := range progs {
		titles[p.Title] = true
	}
	for _, want := range []string{"Futurama", "Batman: The Animated Series", "The Iron Giant", "The Thing"} {
		if !titles[want] {
			t.Errorf("guide lacks %q: %v", want, titles)
		}
	}

	// The same videos keep the same schedule.
	cartoons.refresh()
	if again := cartoons.Programs(from, from.Add(6*time.Hour)); !slices.Equal(progs, again) {
		t.Error("schedule changed on refresh")
	}

	// Another channel on the account shares the sign-in.
	movies := newJellyfinChannel(t, root, "1.6 Movies", `{"movies": ["the thing"]}`)
	if n, _, err := movies.Status(); movies.Empty() || err != nil {
		t.Errorf("movies channel: %d, %v", n, err)
	}
	if n := srv.Logins(); n != 1 {
		t.Errorf("signed in %d times for two channels, want once", n)
	}
	if strings.Contains(logs.String(), "s3cret-pw") {
		t.Errorf("password in logs:\n%s", logs)
	}
}

func TestLibraryFindsJellyfinChannels(t *testing.T) {
	srv := jellyfintest.New(t, "lineup", "pw", jellyfinLibrary()...)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, jellyfinAccount), fmt.Sprintf(`{"server": %q, "user": "lineup", "password": "pw"}`, srv.URL))
	writeFile(t, filepath.Join(root, "1.5 Cartoons", jellyfinConfig), `{"series": ["Futurama"]}`)
	writeFile(t, filepath.Join(root, "1.6 Nothing", jellyfinConfig), `{"series": ["Nope"]}`)
	l := &Library{Root: root}
	var got []string
	for _, c := range l.Channels() {
		got = append(got, c.Number()+" "+c.Name())
	}
	if !slices.Equal(got, []string{"1.5 Cartoons"}) {
		t.Errorf("lineup = %q", got)
	}
	if n := srv.Logins(); n != 1 {
		t.Errorf("signed in %d times, want once", n)
	}
}

func TestJellyfinChannelReauthenticates(t *testing.T) {
	srv := jellyfintest.New(t, "renew", "pw", jellyfinLibrary()...)
	root := t.TempDir()
	j := newJellyfinChannel(t, root, "1.5 Cartoons", fmt.Sprintf(`{"server": %q, "user": "renew", "password": "pw", "series": ["Futurama"]}`, srv.URL))
	if j.Empty() {
		t.Fatal("empty")
	}
	srv.Revoke()
	j.refresh()
	if n, _, err := j.Status(); n != 2 || err != nil {
		t.Errorf("after the token was revoked: %d videos, %v", n, err)
	}
	if n := srv.Logins(); n != 2 {
		t.Errorf("signed in %d times, want twice", n)
	}
}

func TestJellyfinGuide(t *testing.T) {
	srv := jellyfintest.New(t, "guide", "pw", jellyfinLibrary()...)
	root := t.TempDir()
	j := newJellyfinChannel(t, root, "1.5 Cartoons", fmt.Sprintf(`{"server": %q, "user": "guide", "password": "pw",
		"series": ["Futurama"], "movies": ["The Iron Giant"], "order": "aired"}`, srv.URL))
	from := time.Now()
	progs := j.Programs(from, from.Add(5*time.Hour))
	find := func(title, subtitle string) Program {
		for _, p := range progs {
			if p.Title == title && p.Subtitle == subtitle {
				return p
			}
		}
		t.Fatalf("no %q %q in %+v", title, subtitle, progs)
		return Program{}
	}
	ep := find("Futurama", "Space Pilot 3000")
	if ep.Season != "1" || ep.Episode != "1" || ep.Description != "Fry is frozen for a thousand years." ||
		ep.Image != srv.URL+"/Items/fut-1-1/Images/Primary?maxWidth=640&tag=tag-fut-1-1" || ep.End.Sub(ep.Start) != 22*time.Minute {
		t.Errorf("episode = %+v", ep)
	}
	// An episode without a still shows its series' poster.
	if p := find("Futurama", "The Series Has Landed"); p.Image != srv.URL+"/Items/fut/Images/Primary?maxWidth=640&tag=tag-fut" {
		t.Errorf("episode without a still: %q", p.Image)
	}
	mv := find("The Iron Giant", "")
	if mv.Category != "Movie" || mv.Season != "" || mv.End.Sub(mv.Start) != 86*time.Minute {
		t.Errorf("movie = %+v", mv)
	}
	// Aired order: the 1999 season, then the movie from that August.
	for i := range progs {
		if progs[i].Subtitle == "Space Pilot 3000" && i+2 < len(progs) {
			if progs[i+1].Subtitle != "The Series Has Landed" || progs[i+2].Title != "The Iron Giant" {
				t.Errorf("order after the pilot: %q, %q", progs[i+1].Subtitle, progs[i+2].Title)
			}
			break
		}
	}
	for _, p := range progs {
		if s := fmt.Sprint(p); strings.Contains(s, "session-") {
			t.Errorf("secret in guide: %s", s)
		}
	}
}

func TestJellyfinShuffle(t *testing.T) {
	var vs []jellyfin.Video
	for i := range 30 {
		vs = append(vs, jellyfin.Video{ID: fmt.Sprintf("v%02d", i)})
	}
	ids := func(vs []jellyfin.Video) []string {
		var out []string
		for _, v := range vs {
			out = append(out, v.ID)
		}
		return out
	}
	a := slices.Clone(vs)
	shuffle(a, "1.5\x00Cartoons")
	b := slices.Clone(vs)
	slices.Reverse(b)
	shuffle(b, "1.5\x00Cartoons")
	if !slices.Equal(ids(a), ids(b)) {
		t.Error("same videos, different order")
	}
	if slices.Equal(ids(a), ids(vs)) {
		t.Error("not shuffled")
	}
	c := slices.Clone(vs)
	shuffle(c, "1.6\x00Movies")
	if slices.Equal(ids(a), ids(c)) {
		t.Error("two channels share an order")
	}
	// A new video goes in somewhere; the rest keep their order.
	d := append(slices.Clone(vs), jellyfin.Video{ID: "new"})
	shuffle(d, "1.5\x00Cartoons")
	if got := slices.DeleteFunc(ids(d), func(id string) bool { return id == "new" }); !slices.Equal(got, ids(a)) {
		t.Error("adding a video reordered the others")
	}
}

func TestJellyfinAiredOrder(t *testing.T) {
	vs := []jellyfin.Video{
		{ID: "fut-1-2", Type: "Episode", SeriesID: "fut", Season: 1, Episode: 2, Premiere: "1999-04-04"},
		{ID: "giant", Type: "Movie", Premiere: "1999-08-06"},
		{ID: "undated", Type: "Movie"},
		{ID: "btas-special", Type: "Episode", SeriesID: "btas", Premiere: "1993-01-01"},
		{ID: "fut-1-1", Type: "Episode", SeriesID: "fut", Season: 1, Episode: 1, Premiere: "1999-03-28"},
		{ID: "btas-2-1", Type: "Episode", SeriesID: "btas", Season: 2, Episode: 1, Premiere: "1994-05-01"},
		{ID: "btas-1-2", Type: "Episode", SeriesID: "btas", Season: 1, Episode: 2, Premiere: "1992-11-13"},
		{ID: "btas-1-1", Type: "Episode", SeriesID: "btas", Season: 1, Episode: 1, Premiere: "1992-09-06"},
		{ID: "metropolis", Type: "Movie", Year: 1927},
	}
	airedOrder(vs)
	var got []string
	for _, v := range vs {
		got = append(got, v.ID)
	}
	want := []string{"metropolis", "btas-1-1", "btas-1-2", "btas-2-1", "btas-special", "fut-1-1", "fut-1-2", "giant", "undated"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestJellyfinInput(t *testing.T) {
	srv := jellyfintest.New(t, "input", "pw", jellyfinLibrary()...)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "input", Password: "pw"}, "dev", nil)
	if _, err := c.Series(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := jellyfin.Video{ID: "fut-1-1", Source: "src-fut-1-1", Type: "Episode", Series: "Futurama", Runtime: 22 * time.Minute, Audio: true}
	if _, ok := jellyfinItem(c, jellyfin.Video{ID: "x", Runtime: 900 * time.Millisecond}, 0); ok {
		t.Error("scheduled a video under a second")
	}
	at := func(args []string, s string) int { return slices.Index(args, s) }
	for _, mbps := range []float64{0, 4} {
		it, ok := jellyfinItem(c, v, mbps)
		if !ok {
			t.Fatal("episode not scheduled")
		}
		args := decodeArgs(it, 90*loopFPS) // 90 s in
		in := at(args, "-i")
		h := at(args, "-headers")
		if in < 1 || args[in-1] != "-re" || h < 0 || h > in {
			t.Fatalf("maxBitrate %v: args %q", mbps, args)
		}
		if hdr := args[h+1]; !strings.HasPrefix(hdr, "Authorization: MediaBrowser ") || !strings.Contains(hdr, `Token="session-1"`) || !strings.HasSuffix(hdr, "\r\n") {
			t.Errorf("maxBitrate %v: header %q", mbps, hdr)
		}
		u := args[in+1]
		if strings.Contains(u, "session-") || strings.Contains(u, "Token") || strings.Contains(u, "api_key") {
			t.Errorf("maxBitrate %v: token in URL %s", mbps, u)
		}
		if !slices.Contains(args, "-reconnect") {
			t.Errorf("maxBitrate %v: no reconnect", mbps)
		}
		ss := at(args, "-ss")
		switch mbps {
		case 0:
			if ss < 0 || ss > in || args[ss+1] != "90.000" || !strings.Contains(u, "/Videos/fut-1-1/stream?") || !strings.Contains(u, "static=true") {
				t.Errorf("original file: seek %d, URL %s", ss, u)
			}
		default:
			if ss >= 0 || !strings.Contains(u, "startTimeTicks=900000000") || !strings.Contains(u, "videoBitRate=3872000") || !strings.Contains(u, "maxHeight=720") {
				t.Errorf("transcode: seek %d, URL %s", ss, u)
			}
		}
		if from0 := decodeArgs(it, 0); slices.Contains(from0, "-ss") || strings.Contains(from0[at(from0, "-i")+1], "startTimeTicks") {
			t.Errorf("maxBitrate %v: seeks from the start: %q", mbps, from0)
		}
	}
}

func TestJellyfinAccountFile(t *testing.T) {
	logs := captureLogs(t)
	srv := jellyfintest.New(t, "friend", "right-pw", jellyfinLibrary()...)
	root := t.TempDir()
	account := filepath.Join(root, jellyfinAccount)
	writeFile(t, account, fmt.Sprintf(`{"server": %q, "user": "friend", "password": "wrong-pw"}`, srv.URL))
	j := newJellyfinChannel(t, root, "1.5 Cartoons", `{"series": ["Futurama"]}`)
	if !j.Empty() {
		t.Fatal("played with a refused password")
	}
	_, _, err := j.Status()
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("status error = %v", err)
	}

	// A channel's own account wins over the shared one.
	own := newJellyfinChannel(t, root, "1.6 Own", fmt.Sprintf(`{"server": %q, "user": "friend", "password": "right-pw", "series": ["Futurama"]}`, srv.URL))
	if own.Empty() {
		t.Error("the channel's own account wasn't used")
	}

	// Fixing the account file is noticed without the channel changing.
	writeFile(t, account, fmt.Sprintf(`{"server": %q, "user": "friend", "password": "right-pw"}`, srv.URL))
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(account, later, later); err != nil {
		t.Fatal(err)
	}
	j.mu.Lock()
	j.checked = time.Time{} // as if rescanEvery had passed
	j.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n, _, err := j.Status()
		if n == 2 && err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("account change not picked up: %d videos, %v", n, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(logs.String(), "wrong-pw") || strings.Contains(logs.String(), "right-pw") {
		t.Errorf("password in logs:\n%s", logs)
	}
}

func TestJellyfinQuickConnectAccount(t *testing.T) {
	srv := jellyfintest.New(t, "coded", "unused", jellyfinLibrary()...)
	srv.QuickConnect = true
	ctx := context.Background()
	qc, err := jellyfin.StartQuickConnect(ctx, srv.URL, "airwaves-admin", nil)
	if err != nil || !srv.Approve(qc.Code) {
		t.Fatalf("quick connect: %v", err)
	}
	acct, err := jellyfin.FinishQuickConnect(ctx, srv.URL, qc.Secret, "airwaves-admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(acct)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, jellyfinAccount), string(raw))
	// The fake server only takes the token from the device it was issued to.
	j := newJellyfinChannel(t, root, "1.5 Cartoons", `{"series": ["Futurama"]}`)
	if n, _, err := j.Status(); j.Empty() || err != nil {
		t.Errorf("with a Quick Connect account: %d videos, %v", n, err)
	}
	if srv.Logins() != 0 {
		t.Error("signed in with a password")
	}
}

func TestJellyfinBadConfig(t *testing.T) {
	root := t.TempDir()
	srv := jellyfintest.New(t, "x", "y")
	url := srv.URL
	srv.Close()
	for config, want := range map[string]string{
		`{"series": ["Futurama"]}`: "no Jellyfin server",
		fmt.Sprintf(`{"server": %q, "user": "x", "password": "hidden-pw", "order": "random"}`, url):      `order "random"`,
		fmt.Sprintf(`{"server": %q, "user": "x", "password": "hidden-pw", "series": ["Futurama"]}`, url): "jellyfin: ",
		`{"series": [`: "unexpected end",
	} {
		j := newJellyfinChannel(t, root, "1.5 Bad", config)
		if !j.Empty() {
			t.Errorf("%s: not empty", config)
		}
		_, _, err := j.Status()
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "hidden-pw") {
			t.Errorf("%s: err = %v, want %q", config, err, want)
		}
	}
}

// TestJellyfinDemo plays from Jellyfin's public demo server in both modes.
// It needs the internet, so it only runs with AIRWAVES_JELLYFIN_DEMO=1.
func TestJellyfinDemo(t *testing.T) {
	if os.Getenv("AIRWAVES_JELLYFIN_DEMO") == "" {
		t.Skip("set AIRWAVES_JELLYFIN_DEMO=1 to play from demo.jellyfin.org")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, jellyfinAccount), `{"server": "https://demo.jellyfin.org/stable", "user": "demo", "password": ""}`)
	for i, mbps := range []float64{0, 2} {
		t.Run(fmt.Sprintf("maxBitrate=%v", mbps), func(t *testing.T) {
			j := newJellyfinChannel(t, root, fmt.Sprintf("2.%d Demo", i+1), fmt.Sprintf(`{"series": ["Pioneer One"],
				"movies": ["Caminandes: Llama Drama", "Night of the Living Dead (1968)"], "order": "aired", "maxBitrate": %v}`, mbps))
			j.FFmpeg = ffmpeg
			now := time.Now()
			progs := j.Programs(now, now.Add(4*time.Hour))
			if len(progs) == 0 {
				_, _, err := j.Status()
				t.Fatalf("no programs: %v", err)
			}
			for _, p := range progs {
				t.Logf("%s-%s  %s | %s | S%sE%s | %s", p.Start.Format("15:04:05"), p.End.Format("15:04:05"), p.Title, p.Subtitle, p.Season, p.Episode, p.Image)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var out bytes.Buffer
			if err := j.Stream(ctx, &out); err != nil {
				t.Fatal(err)
			}
			ts := filepath.Join(t.TempDir(), "out.ts")
			if err := os.WriteFile(ts, out.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,width,height:format=duration", "-of", "compact", ts).CombinedOutput()
			if err != nil {
				t.Fatalf("ffprobe %d bytes: %v: %s", out.Len(), err, probe)
			}
			t.Logf("%d bytes:\n%s", out.Len(), probe)
			for _, want := range []string{"codec_name=h264|width=1280|height=720", "codec_name=aac"} {
				if !strings.Contains(string(probe), want) {
					t.Errorf("stream lacks %q", want)
				}
			}
		})
	}
}

// TestJellyfinSchedule: a Jellyfin channel in aired order on a schedule
// from channel.json beside jellyfin.json, one a night from the second.
func TestJellyfinSchedule(t *testing.T) {
	srv := jellyfintest.New(t, "sched", "pw", jellyfinLibrary()...)
	root := t.TempDir()
	j := newJellyfinChannel(t, root, "1.5 Cartoons", fmt.Sprintf(`{"server": %q, "user": "sched", "password": "pw",
		"series": ["Futurama"], "movies": ["The Iron Giant"], "order": "aired"}`, srv.URL))
	details := filepath.Join(root, "1.5 Cartoons", DetailsFile)
	writeFile(t, details, `{"schedule": {"start": "2026-10-04", "first": 2, "blocks": [{"at": "20:00", "count": 1}]}}`)
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, time.Local) }

	seq, ok := j.Sequence()
	var names []string
	for _, s := range seq {
		names = append(names, cmp.Or(s.Subtitle, s.Title))
	}
	if !ok || !slices.Equal(names, []string{"Space Pilot 3000", "The Series Has Landed", "The Iron Giant"}) || seq[0].Season != "1" || seq[2].Length != 86*time.Minute {
		t.Fatalf("sequence: %+v", seq)
	}

	// Sunday the second episode, Monday the movie, Tuesday back to the first.
	progs := j.Programs(at(5, 12, 0), at(6, 12, 0))
	if len(progs) != 3 || !progs[0].OffAir || progs[1].Title != "The Iron Giant" || !progs[1].Start.Equal(at(5, 20, 0)) || !progs[2].OffAir {
		t.Fatalf("guide: %+v", progs)
	}
	if d := progs[0].Description; d != "Back at 8:00 PM with The Iron Giant." {
		t.Errorf("afternoon: %q", d)
	}
	if d := progs[2].Description; d != "Back Tue, Oct 6 at 8:00 PM with Futurama, S1 E1." {
		t.Errorf("overnight: %q", d)
	}
	if j.Empty() {
		t.Error("empty while off the air")
	}

	// The cue plays the movie from where it is.
	it, skip, err := scheduled("test", j.lineup)(at(5, 20, 30), true)
	if err != nil || it.Title != "The Iron Giant" || skip != 30*60*loopFPS {
		t.Errorf("cue: %q %d %v", it.Title, skip, err)
	}

	// Taking the schedule out of channel.json loops on the clock again.
	writeFile(t, details, `{"callSign": "TOON"}`)
	for _, p := range j.Programs(at(5, 12, 0), at(6, 12, 0)) {
		if p.OffAir {
			t.Fatalf("off the air without a schedule: %+v", p)
		}
	}
}
