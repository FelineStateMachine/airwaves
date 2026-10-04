package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"airwaves/internal/reception"
)

// TestTV: the TV interface is at /tv/ without the API's token, pages and
// scripts revalidated on every load, fonts and libraries cached; the API
// it calls still wants the token.
func TestTV(t *testing.T) {
	files := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>Airwaves</title>")},
		"app.js":               {Data: []byte("'use strict';")},
		"fonts/plex.woff2":     {Data: []byte("font")},
		"vendor/hls.min.js":    {Data: []byte("hls")},
		"vendor/kenney/a.svg":  {Data: []byte("<svg/>")},
		"vendor/kenney/b.svg":  {Data: []byte("<svg/>")},
		"vendor/pretext/x.mjs": {Data: []byte("x")},
	}
	srv := httptest.NewServer((&Server{Backend: &fake{}, Token: "s3cret", TV: files}).Handler())
	defer srv.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string, header ...string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body)
	}

	if resp, _ := get("/tv"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/tv/" {
		t.Errorf("/tv: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := get("/tv/")
	if resp.StatusCode != 200 || body != "<!doctype html><title>Airwaves</title>" {
		t.Fatalf("/tv/: %d %q", resp.StatusCode, body)
	}
	if cc, ct := resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Type"); cc != "no-cache" || ct != "text/html; charset=utf-8" {
		t.Errorf("/tv/: Cache-Control %q, Content-Type %q", cc, ct)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("/tv/: no ETag")
	}
	if resp, _ := get("/tv/", "If-None-Match", etag); resp.StatusCode != http.StatusNotModified {
		t.Errorf("/tv/ again: %d, want 304", resp.StatusCode)
	}
	if resp, body := get("/tv/app.js"); resp.StatusCode != 200 || body != "'use strict';" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("app.js: %d %q %q", resp.StatusCode, body, resp.Header.Get("Cache-Control"))
	}
	if resp, _ := get("/tv/app.js", "If-None-Match", etag); resp.StatusCode != 200 {
		t.Errorf("app.js with index.html's ETag: %d", resp.StatusCode)
	}
	for _, path := range []string{"/tv/fonts/plex.woff2", "/tv/vendor/hls.min.js", "/tv/vendor/kenney/a.svg"} {
		if resp, _ := get(path); resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "public, max-age=86400" {
			t.Errorf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/tv/vendor/", "/tv/vendor/kenney/", "/tv/missing.js"} {
		if resp, _ := get(path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, resp.StatusCode)
		}
	}
	if resp, _ := get("/tv/index.html"); resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "./" {
		t.Errorf("/tv/index.html: %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := get("/api/info"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/info without the token: %d", resp.StatusCode)
	}

	// Without the interface there's nothing at /tv/.
	bare := httptest.NewServer((&Server{Backend: &fake{}}).Handler())
	defer bare.Close()
	if resp, err := http.Get(bare.URL + "/tv/"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("no TV: %v %v", resp, err)
	}
}

// TestPresets: the web app gets the antenna presets the desktop app has
// built in.
func TestPresets(t *testing.T) {
	srv := httptest.NewServer((&Server{Backend: &fake{}, Token: "s3cret"}).Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/presets", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without the token: %d", resp.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer s3cret")
	if resp, err = http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []reception.Preset
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(reception.Presets) || got[0].Name != "indoor" || got[2].Name != "rooftop" || got[2].Label != reception.Presets[2].Label ||
		got[1].HeightM != reception.Presets[1].HeightM || got[0].GainDBi["UHF"] != reception.Presets[0].GainDBi["UHF"] {
		t.Errorf("presets = %+v", got)
	}
}
