package jellyfin_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"airwaves/internal/jellyfin"
	"airwaves/internal/jellyfin/jellyfintest"
)

// library is a small Jellyfin library: two series (one in a collection),
// same-named movies, and a playlist.
func library() []jellyfintest.Item {
	return []jellyfintest.Item{
		{ID: "lib-shows", Type: "CollectionFolder", Name: "Shows"},
		{ID: "lib-movies", Type: "CollectionFolder", Name: "Movies"},
		{ID: "fut", Type: "Series", Name: "Futurama", Parent: "lib-shows", Year: 1999, Genres: []string{"Animation"}, Overview: "Pizza delivery in the year 3000.", Image: true},
		{ID: "fut-1", Type: "Season", Name: "Season 1", Parent: "fut"},
		{ID: "fut-1-1", Type: "Episode", Name: "Space Pilot 3000", Parent: "fut-1", Season: 1, Episode: 1, Premiere: "1999-03-28", Runtime: 22 * time.Minute, Image: true, Overview: "Fry is frozen."},
		{ID: "fut-1-2", Type: "Episode", Name: "The Series Has Landed", Parent: "fut-1", Season: 1, Episode: 2, Premiere: "1999-04-04", Runtime: 22 * time.Minute, Silent: true},
		{ID: "fut-1-9", Type: "Episode", Name: "Not Downloaded", Parent: "fut-1", Season: 1, Episode: 9, Missing: true},
		{ID: "btas", Type: "Series", Name: "Batman: The Animated Series", Parent: "lib-shows", Year: 1992},
		{ID: "btas-1-1", Type: "Episode", Name: "On Leather Wings", Parent: "btas", Season: 1, Episode: 1, Runtime: 22 * time.Minute},
		{ID: "thing82", Type: "Movie", Name: "The Thing", Parent: "lib-movies", Year: 1982, Runtime: 109 * time.Minute, Image: true},
		{ID: "thing11", Type: "Movie", Name: "The Thing", Parent: "lib-movies", Year: 2011, Runtime: 103 * time.Minute},
		{ID: "giant", Type: "Movie", Name: "The Iron Giant", Parent: "lib-movies", Year: 1999, Runtime: 86 * time.Minute},
		{ID: "sat", Type: "BoxSet", Name: "Saturday Morning", Children: []string{"btas", "giant"}},
		{ID: "mix", Type: "Playlist", Name: "Mix", Children: []string{"fut-1-1", "thing82"}},
		{ID: "songs", Type: "Playlist", Name: "Songs", MediaType: "Audio"},
	}
}

func TestLoginIsSharedAndRenewed(t *testing.T) {
	srv := jellyfintest.New(t, "shared", "hunter2", library()...)
	acct := jellyfin.Account{Server: srv.URL + "/", User: "shared", Password: "hunter2"}
	ctx := context.Background()
	a, b := jellyfin.New(acct, "device-a", nil), jellyfin.New(acct, "device-b", nil)
	if _, err := a.Series(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Movies(ctx); err != nil {
		t.Fatal(err)
	}
	if n := srv.Logins(); n != 1 {
		t.Errorf("two clients of one account signed in %d times, want once", n)
	}
	srv.Revoke()
	if _, err := b.Collections(ctx); err != nil {
		t.Fatalf("after the token was revoked: %v", err)
	}
	if n := srv.Logins(); n != 2 {
		t.Errorf("signed in %d times, want a second sign-in after the token was revoked", n)
	}
	for _, r := range srv.Requests() {
		if !strings.Contains(r.Authorization, `DeviceId="device-a"`) {
			t.Errorf("%s %s: authorization %q, want the first client's device", r.Method, r.URL, r.Authorization)
		}
		if strings.Contains(r.URL, "session-") || strings.Contains(r.URL, "hunter2") {
			t.Errorf("secret in URL %s", r.URL)
		}
	}
	if h := a.Authorization(); !strings.Contains(h, `Token="session-2"`) || !strings.HasPrefix(h, "MediaBrowser ") {
		t.Errorf("Authorization() = %q", h)
	}
}

func TestRefusedPasswordIsNotRetried(t *testing.T) {
	srv := jellyfintest.New(t, "careful", "right", library()...)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "careful", Password: "wrong-pw"}, "dev", nil)
	ctx := context.Background()
	_, err := c.Series(ctx)
	if err == nil || strings.Contains(err.Error(), "wrong-pw") {
		t.Fatalf("err = %v, want a refusal that doesn't repeat the password", err)
	}
	before := time.Now()
	for range 3 {
		if _, err := c.Movies(ctx); err == nil {
			t.Fatal("a refused password worked")
		}
	}
	c.Retry(before.Add(-time.Hour)) // nothing changed since
	_, _ = c.Movies(ctx)
	if n := srv.Logins(); n != 1 {
		t.Fatalf("tried a refused password %d times, want once", n)
	}
	c.Retry(time.Now().Add(time.Second)) // the account file changed
	_, _ = c.Movies(ctx)
	if n := srv.Logins(); n != 2 {
		t.Errorf("after Retry: %d sign-ins, want 2", n)
	}
	if err := c.Login(ctx); err == nil || srv.Logins() != 3 {
		t.Errorf("Login: %v after %d sign-ins, want a third, refused", err, srv.Logins())
	}
}

func TestAPIKey(t *testing.T) {
	srv := jellyfintest.New(t, "keyed", "pw", library()...)
	srv.APIKey = "api-key-1"
	ctx := context.Background()
	// Without a user, an API key sees the server's libraries.
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, Token: "api-key-1"}, "dev", nil)
	libs, err := c.Libraries(ctx)
	if err != nil || len(libs) != 2 {
		t.Fatalf("libraries = %v, %v", libs, err)
	}
	// With one, its own.
	u := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "KEYED", Token: "api-key-1"}, "dev", nil)
	if _, err := u.Libraries(ctx); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if last := reqs[len(reqs)-1]; !strings.HasPrefix(last.URL, "/UserViews?userId=user-1") {
		t.Errorf("libraries for a user: %s", last.URL)
	}
	if srv.Logins() != 0 {
		t.Error("signed in with a password despite the token")
	}
	bad := jellyfin.New(jellyfin.Account{Server: srv.URL, Token: "revoked-key"}, "dev", nil)
	if _, err := bad.Series(ctx); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("bad token: %v", err)
	}
}

func TestListings(t *testing.T) {
	srv := jellyfintest.New(t, "lister", "pw", library()...)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "lister", Password: "pw"}, "dev", nil)
	ctx := context.Background()
	series, err := c.Series(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("series = %+v", series)
	}
	fut := series[1]
	if fut.Name != "Futurama" || fut.Year != 1999 || fut.Count != 3 || !fut.HasImage ||
		!slices.Equal(fut.Genres, []string{"Animation"}) || fut.Overview == "" || fut.Key() != "Futurama (1999)" {
		t.Errorf("Futurama = %+v, key %q", fut, fut.Key())
	}
	movies, err := c.Movies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, m := range movies {
		keys = append(keys, m.Key())
	}
	if want := []string{"The Iron Giant (1999)", "The Thing (1982)", "The Thing (2011)"}; !slices.Equal(keys, want) {
		t.Errorf("movies = %q, want %q", keys, want)
	}
	if movies[0].Runtime != 86*time.Minute {
		t.Errorf("runtime = %v", movies[0].Runtime)
	}
	cols, err := c.Collections(ctx)
	if err != nil || len(cols) != 1 || cols[0].Count != 2 || cols[0].Type != "BoxSet" {
		t.Errorf("collections = %+v, %v", cols, err)
	}
	lists, err := c.Playlists(ctx)
	if err != nil || len(lists) != 1 || lists[0].Name != "Mix" {
		t.Errorf("playlists = %+v, %v", lists, err)
	}
	if (jellyfin.Item{Name: "Mix"}).Key() != "Mix" {
		t.Error("Key without a year")
	}
}

func TestFind(t *testing.T) {
	srv := jellyfintest.New(t, "finder", "pw", library()...)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "finder", Password: "pw"}, "dev", nil)
	ctx := context.Background()
	found, missing, err := c.Find(ctx, "Movie", []string{"the thing (1982)", "The Iron Giant", "Spider-Man", "THE THING"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range found {
		ids = append(ids, it.ID)
	}
	if want := []string{"thing82", "giant", "thing82", "thing11"}; !slices.Equal(ids, want) {
		t.Errorf("found %q, want %q", ids, want)
	}
	if !slices.Equal(missing, []string{"Spider-Man"}) {
		t.Errorf("unmatched = %q", missing)
	}
	found, missing, err = c.Find(ctx, "Library", []string{"movies", "Music"})
	if err != nil || len(found) != 1 || found[0].ID != "lib-movies" || !slices.Equal(missing, []string{"Music"}) {
		t.Errorf("libraries: %+v %q %v", found, missing, err)
	}
}

func TestVideos(t *testing.T) {
	srv := jellyfintest.New(t, "viewer", "pw", library()...)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "viewer", Password: "pw"}, "dev", nil)
	ctx := context.Background()
	from := []jellyfin.Item{
		{ID: "sat", Type: "BoxSet"},    // a series and a movie
		{ID: "mix", Type: "Playlist"},  // an episode and a movie
		{ID: "fut", Type: "Series"},    // overlaps the playlist
		{ID: "thing11", Type: "Movie"}, // by itself
		{ID: "giant", Type: "Movie"},   // again
		{ID: "lib-none", Type: "Library"},
	}
	vs, err := c.Videos(ctx, from)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]jellyfin.Video{}
	var ids []string
	for _, v := range vs {
		got[v.ID] = v
		ids = append(ids, v.ID)
	}
	slices.Sort(ids)
	if want := []string{"btas-1-1", "fut-1-1", "fut-1-2", "giant", "thing11", "thing82"}; !slices.Equal(ids, want) {
		t.Fatalf("videos %q, want %q", ids, want)
	}
	ep := got["fut-1-1"]
	if ep.Series != "Futurama" || ep.SeriesID != "fut" || ep.Season != 1 || ep.Episode != 1 || ep.Premiere != "1999-03-28" ||
		ep.Runtime != 22*time.Minute || !ep.Audio || ep.Source != "src-fut-1-1" || ep.Overview != "Fry is frozen." {
		t.Errorf("episode = %+v", ep)
	}
	if ep.Image != srv.URL+"/Items/fut-1-1/Images/Primary?maxWidth=640&tag=tag-fut-1-1" {
		t.Errorf("episode image = %q", ep.Image)
	}
	if v := got["fut-1-2"]; v.Audio || !strings.Contains(v.Image, "/Items/fut/Images/Primary") {
		t.Errorf("silent episode without a still = %+v", v)
	}
	if v := got["thing82"]; v.Type != "Movie" || v.Year != 1982 || !strings.Contains(v.Image, "/Items/thing82/Images/Primary") {
		t.Errorf("movie = %+v", v)
	}
	if v := got["giant"]; v.Image != "" {
		t.Errorf("movie without art has image %q", v.Image)
	}
}

func TestStreamURLs(t *testing.T) {
	c := jellyfin.New(jellyfin.Account{Server: "jellyfin.example.com/", User: "u", Password: "p"}, "dev", nil)
	v := jellyfin.Video{ID: "abc", Source: "src-abc"}
	if u := c.FileURL(v); u != "https://jellyfin.example.com/Videos/abc/stream?mediaSourceId=src-abc&static=true" {
		t.Errorf("FileURL = %s", u)
	}
	u := c.TranscodeURL(v, 90*time.Second, jellyfin.Transcode{Bitrate: 4_000_000, MaxWidth: 1280, MaxHeight: 720})
	for _, want := range []string{
		"https://jellyfin.example.com/Videos/abc/stream.ts?", "mediaSourceId=src-abc", "startTimeTicks=900000000",
		"videoCodec=h264", "audioCodec=aac", "videoBitRate=3872000", "audioBitRate=128000", "maxWidth=1280", "maxHeight=720",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("TranscodeURL lacks %q: %s", want, u)
		}
	}
	if u := c.TranscodeURL(v, 0, jellyfin.Transcode{Bitrate: 2_000_000}); strings.Contains(u, "startTimeTicks") {
		t.Errorf("from the start: %s", u)
	}
}

func TestImage(t *testing.T) {
	srv := jellyfintest.New(t, "pictures", "pw", library()...)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "pictures", Password: "pw"}, "dev", nil)
	b, typ, err := c.Image(context.Background(), "fut", 300)
	if err != nil || typ != "image/jpeg" || string(b) != "\xff\xd8\xff\xe0jpeg fut Primary 300" {
		t.Errorf("image = %q, %q, %v", b, typ, err)
	}
	if _, _, err := c.Image(context.Background(), "btas", 300); err == nil {
		t.Error("an item without art gave an image")
	}
}

func TestQuickConnect(t *testing.T) {
	srv := jellyfintest.New(t, "coded", "unused", library()...)
	ctx := context.Background()
	if on, err := jellyfin.QuickConnectEnabled(ctx, srv.URL, nil); err != nil || on {
		t.Errorf("enabled = %v, %v; want off", on, err)
	}
	if _, err := jellyfin.StartQuickConnect(ctx, srv.URL, "airwaves-admin", nil); err == nil {
		t.Error("started Quick Connect while it's off")
	}
	srv.QuickConnect = true
	if on, err := jellyfin.QuickConnectEnabled(ctx, srv.URL, nil); err != nil || !on {
		t.Errorf("enabled = %v, %v; want on", on, err)
	}
	qc, err := jellyfin.StartQuickConnect(ctx, srv.URL, "airwaves-admin", nil)
	if err != nil || qc.Code == "" || qc.Secret == "" {
		t.Fatalf("start = %+v, %v", qc, err)
	}
	if ok, err := jellyfin.CheckQuickConnect(ctx, srv.URL, qc.Secret, nil); ok || err != nil {
		t.Errorf("before approval: %v, %v", ok, err)
	}
	if _, err := jellyfin.FinishQuickConnect(ctx, srv.URL, qc.Secret, "airwaves-admin", nil); err == nil {
		t.Error("finished before approval")
	}
	if !srv.Approve(qc.Code) {
		t.Fatal("unknown code")
	}
	if ok, err := jellyfin.CheckQuickConnect(ctx, srv.URL, qc.Secret, nil); !ok || err != nil {
		t.Errorf("after approval: %v, %v", ok, err)
	}
	acct, err := jellyfin.FinishQuickConnect(ctx, srv.URL, qc.Secret, "airwaves-admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Server != srv.URL || acct.User != "coded" || acct.UserID != "user-1" || acct.Token == "" || acct.DeviceID != "airwaves-admin" || acct.Password != "" {
		t.Errorf("account = %+v", acct)
	}
	// The token works from the device it was issued to, whatever device the
	// client would otherwise be, without a password sign-in.
	c := jellyfin.New(acct, "airwavesd-other", nil)
	if _, err := c.Libraries(ctx); err != nil {
		t.Fatal(err)
	}
	if srv.Logins() != 0 {
		t.Error("signed in with a password")
	}
	for _, r := range srv.Requests() {
		if strings.HasPrefix(r.URL, "/Users/Me") {
			t.Error("looked up the user Quick Connect named")
		}
	}
	if _, err := jellyfin.CheckQuickConnect(ctx, srv.URL, "no-such-secret", nil); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("unknown secret: %v", err)
	}
}

func TestErrorsLeaveSecretsOut(t *testing.T) {
	srv := jellyfintest.New(t, "x", "y")
	url := srv.URL
	srv.Close()
	ctx := context.Background()
	_, err := jellyfin.CheckQuickConnect(ctx, url, "the-secret", nil)
	if err == nil || strings.Contains(err.Error(), "the-secret") {
		t.Errorf("err = %v", err)
	}
	c := jellyfin.New(jellyfin.Account{Server: url, User: "x", Password: "pa55word"}, "dev", nil)
	if _, err := c.Series(ctx); err == nil || strings.Contains(err.Error(), "pa55word") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadAccount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".jellyfin.json")
	if _, err := jellyfin.LoadAccount(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"server": "https://jf.example.com", "user": "viewer", "password": "pw"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := jellyfin.LoadAccount(path)
	if err != nil || a != (jellyfin.Account{Server: "https://jf.example.com", User: "viewer", Password: "pw"}) {
		t.Errorf("account = %+v, %v", a, err)
	}
}

func TestStreamsAndSubtitles(t *testing.T) {
	const full = "1\n00:00:01,000 --> 00:00:02,000\nHello.\n"
	srv := jellyfintest.New(t, "subs", "pw",
		jellyfintest.Item{ID: "lib", Type: "CollectionFolder", Name: "Movies"},
		jellyfintest.Item{ID: "akira", Type: "Movie", Name: "Akira", Parent: "lib", Runtime: 124 * time.Minute, Streams: []jellyfintest.Stream{
			{Type: "Audio", Language: "jpn", Codec: "flac", Default: true},
			{Type: "Audio", Language: "eng", Codec: "ac3", Title: "Commentary"},
			{Type: "Audio", Language: "eng", Codec: "ac3", Title: "English Dub"},
			{Type: "Subtitle", Language: "eng", Codec: "pgssub"},
			{Type: "Subtitle", Language: "eng", Codec: "subrip", Forced: true, SRT: "forced"},
			{Type: "Subtitle", Language: "eng", Codec: "subrip", Title: "English SDH", SRT: "sdh"},
			{Type: "Subtitle", Language: "fre", Codec: "subrip", SRT: "français"},
			{Type: "Subtitle", Language: "eng", Codec: "subrip", External: true, SRT: full},
		}},
	)
	c := jellyfin.New(jellyfin.Account{Server: srv.URL, User: "subs", Password: "pw"}, "dev", nil)
	ctx := context.Background()
	vs, err := c.Videos(ctx, []jellyfin.Item{{ID: "akira", Type: "Movie"}})
	if err != nil || len(vs) != 1 {
		t.Fatalf("videos = %+v, %v", vs, err)
	}
	v := vs[0]
	if len(v.AudioStreams) != 3 || len(v.Subtitles) != 5 {
		t.Fatalf("audio %+v, subtitles %+v", v.AudioStreams, v.Subtitles)
	}
	if a := v.AudioStreams[2]; a.Index != 3 || a.Position != 2 || a.Language != "eng" || a.Title != "English Dub" {
		t.Errorf("third audio stream = %+v", a)
	}
	if s := v.Subtitles[0]; s.Text || s.Position != 0 {
		t.Errorf("PGS subtitle = %+v", s)
	}
	if s := v.Subtitles[1]; !s.Forced || !s.Text || s.Position != 1 {
		t.Errorf("forced subtitle = %+v", s)
	}
	if s := v.Subtitles[4]; !s.External || s.Position != -1 || s.Index != 8 {
		t.Errorf("external subtitle = %+v", s)
	}
	if a, ok := v.PreferredAudio(); !ok || a.Index != 3 {
		t.Errorf("preferred audio = %+v, %v; want the English dub", a, ok)
	}
	sub, ok := v.EnglishSubtitle(true)
	if !ok || sub.Index != 8 {
		t.Fatalf("subtitle = %+v, %v; want the full English one", sub, ok)
	}
	b, err := c.Subtitle(ctx, v, sub)
	if err != nil || string(b) != full {
		t.Errorf("subtitle = %q, %v", b, err)
	}
	reqs := srv.Requests()
	last := reqs[len(reqs)-1]
	if last.URL != "/Videos/akira/src-akira/Subtitles/8/0/Stream.srt" || !strings.Contains(last.Authorization, `Token="session-`) {
		t.Errorf("fetched %s with %q", last.URL, last.Authorization)
	}
	if _, err := c.Subtitle(ctx, v, v.Subtitles[0]); err == nil {
		t.Error("fetched a picture subtitle as text")
	}
	if _, err := c.Subtitle(ctx, v, jellyfin.Stream{Index: 99, Text: true}); err == nil {
		t.Error("fetched a subtitle that isn't there")
	}
	if u := c.TranscodeURL(v, 0, jellyfin.Transcode{Bitrate: 4_000_000}); !strings.Contains(u, "audioStreamIndex=3") {
		t.Errorf("transcode doesn't pick the English sound: %s", u)
	}
}

func TestPreferredAudio(t *testing.T) {
	audio := func(list ...jellyfin.Stream) jellyfin.Video { return jellyfin.Video{AudioStreams: list} }
	for _, c := range []struct {
		name string
		v    jellyfin.Video
		want int // Index; -1 for none
	}{
		{"none", audio(), -1},
		{"one", audio(jellyfin.Stream{Index: 1, Language: "jpn"}), 1},
		{"English over the default", audio(jellyfin.Stream{Index: 1, Language: "jpn", Default: true}, jellyfin.Stream{Index: 2, Language: "eng"}), 2},
		{"default English", audio(jellyfin.Stream{Index: 1, Language: "en"}, jellyfin.Stream{Index: 2, Language: "eng", Default: true}), 2},
		{"no English: the default", audio(jellyfin.Stream{Index: 1, Language: "spa"}, jellyfin.Stream{Index: 2, Language: "fre", Default: true}), 2},
		{"no English or default: the first", audio(jellyfin.Stream{Index: 1, Language: "spa"}, jellyfin.Stream{Index: 2, Language: "fre"}), 1},
		{"not the English commentary", audio(jellyfin.Stream{Index: 1, Language: "jpn", Default: true}, jellyfin.Stream{Index: 2, Language: "eng", Title: "Director's Commentary"}), 1},
		{"not described video", audio(jellyfin.Stream{Index: 1, Language: "eng", Title: "Audio Description"}, jellyfin.Stream{Index: 2, Language: "eng"}), 2},
	} {
		a, ok := c.v.PreferredAudio()
		if got := map[bool]int{true: a.Index, false: -1}[ok]; got != c.want {
			t.Errorf("%s: stream %d, want %d", c.name, got, c.want)
		}
	}
}

func TestEnglishSubtitle(t *testing.T) {
	sub := func(i int, lang string, f func(*jellyfin.Stream)) jellyfin.Stream {
		s := jellyfin.Stream{Index: i, Language: lang, Codec: "subrip", Text: true}
		if f != nil {
			f(&s)
		}
		return s
	}
	forced := func(s *jellyfin.Stream) { s.Forced = true }
	sdh := func(s *jellyfin.Stream) { s.HearingImpaired = true }
	picture := func(s *jellyfin.Stream) { s.Text, s.Codec = false, "pgssub" }
	for _, c := range []struct {
		name    string
		subs    []jellyfin.Stream
		english int // pick for English sound, Index or -1
		other   int // for sound in another language
	}{
		{"none", nil, -1, -1},
		{"full over SDH", []jellyfin.Stream{sub(1, "eng", sdh), sub(2, "eng", nil)}, 2, 2},
		{"SDH by title", []jellyfin.Stream{sub(1, "eng", func(s *jellyfin.Stream) { s.Title = "English (SDH)" }), sub(2, "eng", nil)}, 2, 2},
		{"SDH when that's all", []jellyfin.Stream{sub(1, "fre", nil), sub(2, "eng", sdh)}, 2, 2},
		{"forced only for other sound", []jellyfin.Stream{sub(1, "eng", forced)}, -1, 1},
		{"forced by title", []jellyfin.Stream{sub(1, "eng", func(s *jellyfin.Stream) { s.Title = "Signs & Songs" }), sub(2, "spa", nil)}, -1, 1},
		{"full over forced", []jellyfin.Stream{sub(1, "eng", forced), sub(2, "eng", nil)}, 2, 2},
		{"no pictures", []jellyfin.Stream{sub(1, "eng", picture)}, -1, -1},
		{"untagged", []jellyfin.Stream{sub(1, "", nil), sub(2, "ger", nil)}, 1, 1},
		{"English over untagged", []jellyfin.Stream{sub(1, "und", nil), sub(2, "en", sdh)}, 2, 2},
		{"the default first", []jellyfin.Stream{sub(1, "eng", nil), sub(2, "eng", func(s *jellyfin.Stream) { s.Default = true })}, 2, 2},
	} {
		v := jellyfin.Video{Subtitles: c.subs}
		for _, f := range []struct {
			forced bool
			want   int
		}{{false, c.english}, {true, c.other}} {
			s, ok := v.EnglishSubtitle(f.forced)
			if got := map[bool]int{true: s.Index, false: -1}[ok]; got != f.want {
				t.Errorf("%s (forced %v): subtitle %d, want %d", c.name, f.forced, got, f.want)
			}
		}
	}
}
