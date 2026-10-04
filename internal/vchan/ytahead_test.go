package vchan

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"airwaves/internal/phase"
)

// TestYouTubeFindsAhead: the videos on now and next are found ahead,
// once each, with their captions, so tuning in uses what was found
// without yt-dlp; addresses near their expiry are found again, and a
// video opened again is found anew.
func TestYouTubeFindsAhead(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "yt-dlp.log")
	t.Setenv("AIRWAVES_FAKE_YTDLP", calls)
	runs := func() int {
		raw, _ := os.ReadFile(calls)
		return strings.Count(string(raw), " -f ")
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	stub := &feedStub{down: true}
	y := &YouTube{Num: "1.8", Dir: t.TempDir(), loaded: true, now: func() time.Time { return t0 },
		YtDlp: &YtDlp{Path: os.Args[0], HTTP: &http.Client{Transport: stub}},
		cfg:   ytConfig{MaxHeight: 720},
		pool:  &ytPool{Basis: "test", Lock: time.Hour}, plan: ytPlayout{Basis: "test", Airings: []ytAiring{
			{ID: "aaaaaaaaaaa", Channel: "UCa", Title: "A", Start: t0.Add(-time.Minute), End: t0.Add(10 * time.Minute)},
			{ID: "bbbbbbbbbbb", Channel: "UCb", Title: "B", Start: t0.Add(10 * time.Minute), End: t0.Add(30 * time.Minute)},
			{ID: "ccccccccccc", Channel: "UCa", Title: "C", Start: t0.Add(30 * time.Minute), End: t0.Add(40 * time.Minute)},
		}},
		resolved: map[string]ytResolved{}, ahead: map[string]*ytAhead{}, missed: map[string]time.Time{},
	}
	due := func() []string {
		y.mu.Lock()
		defer y.mu.Unlock()
		return y.warmDue(time.Now())
	}

	ids := due()
	if strings.Join(ids, " ") != "aaaaaaaaaaa bbbbbbbbbbb" {
		t.Fatalf("due %q, want what's on and next", ids)
	}
	for _, id := range ids {
		y.lookAhead(id)
	}
	if n := runs(); n != 2 {
		t.Fatalf("%d yt-dlp runs, want 2", n)
	}
	if ids := due(); len(ids) != 0 {
		t.Errorf("due %q once found", ids)
	}

	// Tuning in uses what was found.
	timer := phase.New("tune", "youtube")
	r, err := y.resolve(phase.NewContext(context.Background(), timer), "aaaaaaaaaaa", false)
	if err != nil || len(r.info.inputs()) == 0 {
		t.Fatalf("resolve: %v %+v", err, r)
	}
	if n := runs(); n != 2 || !timer.Has("found ahead") {
		t.Errorf("tuning in ran yt-dlp (%d runs; steps %s)", n, timer)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if r.caps == nil {
		t.Fatal("no captions")
	} else if cues, err := r.caps.get(ctx); err != nil || len(cues) != 1 {
		t.Errorf("captions: %v %+v", err, cues)
	}
	// And again: what's found is kept for the next tune.
	if again, err := y.resolve(context.Background(), "aaaaaaaaaaa", false); err != nil || runs() != 2 || again.caps != r.caps {
		t.Errorf("a second tune ran yt-dlp, or fetched the captions again: %v, %d runs", err, runs())
	}
	y.mu.Lock()
	bcaps := y.resolved["bbbbbbbbbbb"].caps
	y.mu.Unlock()
	if bcaps == nil {
		t.Fatal("no captions for the next video")
	}
	_, _ = bcaps.get(ctx)
	stub.mu.Lock()
	if stub.captions != 2 {
		t.Errorf("fetched captions %d times, want once for each video", stub.captions)
	}
	stub.mu.Unlock()

	// Addresses about to expire are found again.
	y.mu.Lock()
	b := y.resolved["bbbbbbbbbbb"]
	b.until = time.Now().Add(ytMinLeft / 2)
	y.resolved["bbbbbbbbbbb"] = b
	y.mu.Unlock()
	if ids := due(); strings.Join(ids, " ") != "bbbbbbbbbbb" {
		t.Errorf("due %q with B's addresses expiring", ids)
	}

	// A video opened again is found anew.
	if _, err := y.resolve(context.Background(), "aaaaaaaaaaa", true); err != nil || runs() != 3 {
		t.Errorf("a fresh resolve: %v, %d runs", err, runs())
	}

	// Once the next video is on, the one after it is found.
	t0 = t0.Add(11 * time.Minute)
	if ids := due(); strings.Join(ids, " ") != "bbbbbbbbbbb ccccccccccc" {
		t.Errorf("due %q after A, want B (expiring) and C", ids)
	}
}

func TestAddressesUntil(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	exp := func(d time.Duration) string {
		return "https://rr1.example/videoplayback?expire=" + strconv.FormatInt(at.Add(d).Unix(), 10) + "&sig=x"
	}
	two := ytInfo{RequestedFormats: []ytFormat{{URL: exp(6 * time.Hour), VCodec: "avc1"}, {URL: exp(5 * time.Hour), VCodec: "none", ACodec: "mp4a"}}}
	if got, want := addressesUntil(two, at), at.Add(5*time.Hour-10*time.Minute); !got.Equal(want) {
		t.Errorf("two addresses: until %v, want %v", got, want)
	}
	none := ytInfo{ytFormat: ytFormat{URL: "https://example.com/v.mp4", VCodec: "avc1", ACodec: "mp4a"}}
	if got, want := addressesUntil(none, at), at.Add(time.Hour); !got.Equal(want) {
		t.Errorf("no expiry: until %v, want %v", got, want)
	}
	far := ytInfo{ytFormat: ytFormat{URL: exp(48 * time.Hour), VCodec: "avc1", ACodec: "mp4a"}}
	if got, want := addressesUntil(far, at), at.Add(6*time.Hour); !got.Equal(want) {
		t.Errorf("far expiry: until %v, want %v", got, want)
	}
}
