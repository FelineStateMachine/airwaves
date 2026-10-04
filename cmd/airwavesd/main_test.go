package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFeatures(t *testing.T) {
	for _, c := range []struct {
		antenna, hdhr, discovery string
		want                     features
	}{
		{"", "", "", features{antenna: true, hdhr: true, discovery: true}}, // the home server
		{"off", "", "", features{}},                                        // custom channels only
		{"OFF", "on", "", features{hdhr: true}},                            // HTTP, no discovery
		{"off", "on", "on", features{hdhr: true, discovery: true}},         // and discovery
		{"on", "off", "on", features{antenna: true}},                       // no discovery without the device
		{"true", "", "false", features{antenna: true, hdhr: true}},         // other spellings
		{" yes ", "1", "0", features{antenna: true, hdhr: true}},
	} {
		got, err := resolveFeatures(c.antenna, c.hdhr, c.discovery)
		if err != nil || got != c.want {
			t.Errorf("resolveFeatures(%q, %q, %q) = %+v, %v; want %+v", c.antenna, c.hdhr, c.discovery, got, err, c.want)
		}
	}
	if _, err := resolveFeatures("", "maybe", ""); err == nil || !strings.Contains(err.Error(), "AIRWAVES_HDHR") {
		t.Errorf("bad setting: %v", err)
	}
}

func TestWritable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := writable(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v behind", entries)
	}
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if err := writable(dir); err == nil || !strings.Contains(err.Error(), "chown") {
		t.Errorf("read-only folder: %v", err)
	}
}

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	if err := healthcheck(addr); err != nil {
		t.Errorf("up: %v", err)
	}
	_, port, _ := strings.Cut(addr, ":")
	if err := healthcheck(":" + port); err != nil {
		t.Errorf("up, on all addresses: %v", err)
	}
	srv.Close()
	if err := healthcheck(addr); err == nil {
		t.Error("down, yet healthy")
	}
}
