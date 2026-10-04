package vchan

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// YtDlp runs yt-dlp for YouTube channels. With Update, it keeps its own
// copy at Path: the latest release is downloaded on first use and replaced
// about daily, so YouTube's changes don't wait for a new image. The release
// is a Python zipapp, so python3 must be installed, and YouTube needs a
// JavaScript runtime for yt-dlp (deno by default).
type YtDlp struct {
	Path   string // "yt-dlp" from PATH when empty
	Update bool
	// HTTP downloads yt-dlp and reads channel feeds; http.DefaultClient
	// when nil.
	HTTP *http.Client

	mu       sync.Mutex
	checked  time.Time // the last install or update check
	updating bool
	jobs     sync.Mutex // one catalog job at a time, across channels
	ahead    sync.Mutex // one video found ahead of time at a time, likewise
}

const (
	ytdlpReleases    = "https://github.com/yt-dlp/yt-dlp/releases"
	ytdlpUpdateEvery = 24 * time.Hour
)

func (t *YtDlp) client() *http.Client { return cmp.Or(t.HTTP, http.DefaultClient) }

// command returns the yt-dlp to run, installing it first when it's
// missing, and starts the daily update when one is due.
func (t *YtDlp) command(ctx context.Context) (string, error) {
	p := cmp.Or(t.Path, "yt-dlp")
	if !t.Update {
		return p, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	info, err := os.Stat(p)
	if err != nil {
		if err := t.install(ctx, p); err != nil {
			return "", fmt.Errorf("install yt-dlp: %w", err)
		}
		t.checked = time.Now()
		return p, nil
	}
	if t.checked.IsZero() {
		t.checked = info.ModTime() // installed or last checked then
	}
	if time.Since(t.checked) > ytdlpUpdateEvery && !t.updating {
		t.checked, t.updating = time.Now(), true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := t.install(ctx, p); err != nil {
				log.Printf("yt-dlp: update: %v", err)
			}
			t.mu.Lock()
			t.updating = false
			t.mu.Unlock()
		}()
	}
	return p, nil
}

// install puts the latest release at p unless it's already there. The new
// copy is checked against the release's checksums and run once before it
// replaces the old one, which running jobs keep using until they end.
func (t *YtDlp) install(ctx context.Context, p string) error {
	tag, err := t.latest(ctx)
	if err != nil {
		return err
	}
	if have, _ := ytdlpVersion(ctx, p); have == tag {
		now := time.Now()
		return os.Chtimes(p, now, now) // checked; next check in a day
	}
	bin, err := t.get(ctx, ytdlpReleases+"/download/"+tag+"/yt-dlp")
	if err != nil {
		return err
	}
	sums, err := t.get(ctx, ytdlpReleases+"/download/"+tag+"/SHA2-256SUMS")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	want := ""
	for line := range strings.Lines(string(sums)) {
		if f := strings.Fields(line); len(f) == 2 && f[1] == "yt-dlp" {
			want = f[0]
		}
	}
	if want != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("yt-dlp %s: checksum mismatch", tag)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	if v, err := ytdlpVersion(ctx, tmp); err != nil || v != tag {
		os.Remove(tmp)
		return fmt.Errorf("yt-dlp %s doesn't run (is python3 installed?): %v", tag, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
		return err
	}
	log.Printf("yt-dlp %s installed at %s", tag, p)
	return nil
}

// latest is the newest release's tag, read from where GitHub redirects
// the latest release page.
func (t *YtDlp) latest(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, ytdlpReleases+"/latest", nil)
	if err != nil {
		return "", err
	}
	c := *t.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/releases/tag/") {
		return "", fmt.Errorf("latest yt-dlp release: HTTP %d", resp.StatusCode)
	}
	return path.Base(loc), nil
}

func (t *YtDlp) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func ytdlpVersion(ctx context.Context, p string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p, "--version").Output()
	return strings.TrimSpace(string(out)), err
}

// run runs yt-dlp with args and returns what it writes to stdout. Errors
// carry yt-dlp's own error message, without addresses.
func (t *YtDlp) run(ctx context.Context, args ...string) ([]byte, error) {
	p, err := t.command(ctx)
	if err != nil {
		return nil, err
	}
	common := []string{"--ignore-config", "--no-warnings", "--no-progress"}
	if t.Update {
		// Beside the managed copy, so caches survive restarts.
		common = append(common, "--cache-dir", filepath.Join(filepath.Dir(p), "cache"))
	}
	cmd := exec.CommandContext(ctx, p, append(common, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ytdlpError{msg: ytdlpMessage(stderr.String(), err)}
	}
	return out, nil
}

type ytdlpError struct{ msg string }

func (e *ytdlpError) Error() string { return "yt-dlp: " + e.msg }

// ytdlpMessage picks yt-dlp's error line out of its output.
func ytdlpMessage(stderr string, err error) string {
	msg := ""
	for line := range strings.Lines(stderr) {
		if s, ok := strings.CutPrefix(strings.TrimSpace(line), "ERROR: "); ok {
			msg = s
		}
	}
	if msg == "" {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if lines := strings.Split(strings.TrimSpace(stderr), "\n"); lines[len(lines)-1] != "" {
				msg = lines[len(lines)-1]
			}
		}
	}
	msg = urls.ReplaceAllString(cmp.Or(msg, err.Error()), "<url>")
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// gone reports whether a yt-dlp error says the video can't be played
// here at all, rather than for now.
func gone(err error) bool {
	var e *ytdlpError
	if !errors.As(err, &e) {
		return false
	}
	m := strings.ToLower(e.msg)
	if strings.Contains(m, "not a bot") {
		return false
	}
	for _, s := range []string{"video unavailable", "private video", "has been removed", "members-only",
		"join this channel", "confirm your age", "not available in your country", "terminated"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
