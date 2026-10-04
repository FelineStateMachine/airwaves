package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"airwaves/internal/phase"
)

// TestMetrics: the app's channel change times are kept by kind, and read
// back as each kind's recent times.
func TestMetrics(t *testing.T) {
	srv := httptest.NewServer((&Server{Backend: &fake{}}).Handler())
	defer srv.Close()
	for _, body := range []string{
		`{"number":"1.4","kind":"jellyfin","firstFrameMs":1500}`,
		`{"number":"1.4","kind":"jellyfin","firstFrameMs":2500}`,
		`{"number":"9.9","kind":"made up","firstFrameMs":100}`,
	} {
		resp, err := http.Post(srv.URL+"/api/metrics", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s: HTTP %d", body, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []phase.Summary
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	var jf *phase.Summary
	for i, s := range got {
		if s.Kind == "made up" {
			t.Errorf("kept an unknown kind: %+v", s)
		}
		if s.Kind == "jellyfin" && s.Measure == phase.FirstFrame {
			jf = &got[i]
		}
	}
	if jf == nil || jf.Count < 2 || jf.Max < 2.5 {
		t.Errorf("jellyfin first frames: %+v in %+v", jf, got)
	}
}
