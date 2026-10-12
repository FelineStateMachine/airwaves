package stream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSession adds a session without ffmpeg, last used at used.
func fakeSession(t *testing.T, s *Server, client string, used time.Time) *session {
	t.Helper()
	dir := filepath.Join(t.TempDir(), client)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream_0.m3u8"), []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(context.Background())
	sess := &session{id: client + "-id", client: client, dir: dir, started: used, cancel: cancel, done: make(chan struct{}), stderr: &tail{max: 64}}
	close(sess.done)
	sess.touch(used)
	s.mu.Lock()
	s.sessions[client] = sess
	s.mu.Unlock()
	return sess
}

// TestIdleStreams: a stream nobody fetches from or touches is stopped once
// idle too long; one fetched from or touched is kept.
func TestIdleStreams(t *testing.T) {
	s := &Server{sessions: map[string]*session{}, idle: time.Minute, quit: make(chan struct{})}
	start := time.Now().Add(-2 * time.Minute)
	gone := fakeSession(t, s, "gone", start)
	fakeSession(t, s, "paused", start)
	fetched := fakeSession(t, s, "watching", start)

	if !s.Touch("paused") {
		t.Fatal("Touch: no stream for paused")
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/live/"+fetched.id+"/stream_0.m3u8", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("playlist: HTTP %d", rec.Code)
	}

	s.reapIdle(time.Now())
	if s.Touch("gone") {
		t.Error("an idle stream wasn't stopped")
	}
	if _, err := os.Stat(gone.dir); err == nil {
		t.Error("an idle stream's files are left")
	}
	for _, client := range []string{"paused", "watching"} {
		if !s.Touch(client) {
			t.Errorf("%s: stopped though in use", client)
		}
	}

	// Later, with nothing since, they go too.
	s.reapIdle(time.Now().Add(2 * time.Minute))
	for _, client := range []string{"paused", "watching"} {
		if s.Touch(client) {
			t.Errorf("%s: still there after idling", client)
		}
	}
}
