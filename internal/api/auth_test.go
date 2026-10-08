package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"airwaves/internal/service"
)

// TestAdminPassword: with a password, the admin page, its API and the MCP
// endpoint ask for it (Basic auth with any user name, or a bearer token);
// the app's API, logos and the health check don't.
func TestAdminPassword(t *testing.T) {
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "admin "+r.URL.Path) })
	logos := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "logo") })
	open := httptest.NewServer((&Server{Backend: &fake{}, Admin: admin}).Handler())
	defer open.Close()
	locked := httptest.NewServer((&Server{Backend: &fake{}, Admin: admin, Logos: logos, AdminPassword: "correct horse"}).Handler())
	defer locked.Close()

	do := func(method, url string, auth func(*http.Request)) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, url, nil)
		if auth != nil {
			auth(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}
	basic := func(user, pass string) func(*http.Request) {
		return func(r *http.Request) { r.SetBasicAuth(user, pass) }
	}
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}

	if code, body, _ := do("GET", open.URL+"/admin/", nil); code != 200 || body != "admin /admin/" {
		t.Errorf("no password: %d %q", code, body)
	}
	for _, path := range []string{"/admin/", "/admin/api/state", "/admin/mcp"} {
		code, body, h := do("GET", locked.URL+path, nil)
		if code != http.StatusUnauthorized || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Basic ") || strings.Contains(body, "admin /") {
			t.Errorf("%s without the password: %d %q %v", path, code, body, h)
		}
		if code, _, _ := do("GET", locked.URL+path, basic("anyone", "wrong")); code != http.StatusUnauthorized {
			t.Errorf("%s with a wrong password: %d", path, code)
		}
		if code, _, _ := do("GET", locked.URL+path, basic("anyone", "correct hors")); code != http.StatusUnauthorized {
			t.Errorf("%s with a prefix of the password: %d", path, code)
		}
		if code, body, _ := do("GET", locked.URL+path, basic("anyone", "correct horse")); code != 200 || body != "admin "+path {
			t.Errorf("%s with the password: %d %q", path, code, body)
		}
	}
	if code, _, _ := do("POST", locked.URL+"/admin/mcp", bearer("correct horse")); code != 200 {
		t.Errorf("MCP with the password as a bearer token: %d", code)
	}
	if code, _, _ := do("POST", locked.URL+"/admin/mcp", bearer("nope")); code != http.StatusUnauthorized {
		t.Errorf("MCP with a wrong bearer token: %d", code)
	}
	for _, path := range []string{"/api/info", "/channel-logos/1.4", "/healthz"} {
		if code, _, _ := do("GET", locked.URL+path, nil); code != 200 {
			t.Errorf("%s asked for the admin password: %d", path, code)
		}
	}
}

// TestHealthNeedsNoToken: container health checks know no token.
func TestHealthNeedsNoToken(t *testing.T) {
	srv := httptest.NewServer((&Server{Backend: &fake{}, Token: "s3cret"}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok\n" {
		t.Errorf("healthz: %d %q", resp.StatusCode, body)
	}
}

// noAntenna is a server with custom channels only.
type noAntenna struct{ fake }

func (noAntenna) Signal(context.Context) (*service.SignalReport, error) {
	return nil, service.ErrNoAntenna
}

// TestNotAvailable: what a server doesn't have is a 501 with the reason,
// which the client passes on.
func TestNotAvailable(t *testing.T) {
	srv := httptest.NewServer((&Server{Backend: &noAntenna{}}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/signal")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status %d", resp.StatusCode)
	}
	if _, err := NewClient(srv.URL, "").Signal(t.Context()); err == nil || err.Error() != service.ErrNoAntenna.Error() {
		t.Errorf("client error: %v", err)
	}
	if errStatus(errors.New("boom")) != http.StatusInternalServerError {
		t.Error("other errors aren't 500")
	}
}

// TestCrossSite: a browser on another site can't change anything, while
// the app's own page and clients that aren't browsers can.
func TestCrossSite(t *testing.T) {
	admin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "admin "+r.URL.Path) })
	srv := httptest.NewServer((&Server{Backend: &fake{}, Admin: admin}).Handler())
	defer srv.Close()
	for _, tc := range []struct {
		name, fetchSite string
		want            int
	}{
		{"another site", "cross-site", http.StatusForbidden},
		{"the app's page", "same-origin", http.StatusOK},
		{"not a browser", "", http.StatusOK},
	} {
		for _, path := range []string{"/api/signal/measure", "/admin/api/backups/x/restore"} {
			req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "text/plain")
			if tc.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if refused := resp.StatusCode == http.StatusForbidden; refused != (tc.want == http.StatusForbidden) {
				t.Errorf("%s, POST %s: %d", tc.name, path, resp.StatusCode)
			}
		}
	}
}
