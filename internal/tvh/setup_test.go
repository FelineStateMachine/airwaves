package tvh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFrontendsHDHomeRun walks a hardware tree as Tvheadend 4.3 serves it
// for a network HDHomeRun, "leaf" a number, down to its ATSC tuners.
func TestFrontendsHDHomeRun(t *testing.T) {
	tree := map[string]string{
		"root":   `[{"uuid":"dev","text":"HDHomeRun(10A0B0C0) - 169.254.1.2","leaf":0}]`,
		"dev":    `[{"uuid":"tuner0","text":"HDHomeRun ATSC-T Tuner #0 (169.254.1.2)","leaf":1},{"uuid":"tuner1","text":"HDHomeRun ATSC-T Tuner #1 (169.254.1.2)","leaf":true}]`,
		"tuner0": `[]`, "tuner1": `[]`,
	}
	class := map[string]string{"dev": "tvhdhomerun_client", "tuner0": "tvhdhomerun_frontend_atsc_t", "tuner1": "tvhdhomerun_frontend_atsc_t"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uuid := r.URL.Query().Get("uuid")
		switch r.URL.Path {
		case "/api/hardware/tree":
			w.Write([]byte(tree[uuid]))
		case "/api/idnode/load":
			json.NewEncoder(w).Encode(map[string]any{"entries": []map[string]string{{"class": class[uuid]}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fes, err := New(srv.URL, srv.Client()).Frontends(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fes) != 2 || fes[0].UUID != "tuner0" || fes[1].Class != "tvhdhomerun_frontend_atsc_t" {
		t.Fatalf("frontends: %+v", fes)
	}
}
