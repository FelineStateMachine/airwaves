package tvh_test

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"airwaves/internal/tvh"
	"airwaves/internal/tvh/tvhtest"
)

// TestInputsShape reads /api/status/inputs as a real Tvheadend 4.3 writes
// it for an HDHomeRun: both tuners on guide scans, strength and quality on
// the relative scale.
func TestInputsShape(t *testing.T) {
	var body struct {
		Entries []tvh.InputStatus `json:"entries"`
	}
	if err := json.Unmarshal(tvhtest.Raw(t, "status_inputs.json"), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Entries) != 2 {
		t.Fatalf("entries = %d", len(body.Entries))
	}
	in := body.Entries[0]
	mux, net, ok := in.Mux()
	if !ok || mux != "605MHz" || net != "Airwaves antenna" || in.Input != tvhtest.Tuner0 {
		t.Errorf("mux %q, network %q, %v, input %q", mux, net, ok, in.Input)
	}
	if in.Signal != 55704 || in.SignalScale != tvh.ScaleRelative || in.SNR != 54394 || in.SNRScale != tvh.ScaleRelative ||
		in.BPS != 231616 || in.Subs != 1 || in.Weight != 4 || in.TCBit != 0 {
		t.Errorf("input = %+v", in)
	}
	if _, _, ok := (tvh.InputStatus{Stream: ""}).Mux(); ok {
		t.Error("an input on nothing named a multiplex")
	}

	var subs struct {
		Entries []tvh.Subscription `json:"entries"`
	}
	if err := json.Unmarshal(tvhtest.Raw(t, "status_subscriptions.json"), &subs); err != nil {
		t.Fatal(err)
	}
	if len(subs.Entries) != 2 || subs.Entries[0].Title != "epggrab" || subs.Entries[0].State != "Running" ||
		!strings.HasSuffix(subs.Entries[0].Service, "/587MHz/Raw PID Subscription") {
		t.Errorf("subscriptions = %+v", subs.Entries)
	}
}

// TestLineup joins the real grids: 38 channels from 38 services on 8 of the
// antenna network's 36 multiplexes.
func TestLineup(t *testing.T) {
	srv := tvhtest.New(t)
	c := tvh.New(srv.URL, srv.Client())
	l, err := c.Lineup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Networks) != 1 || !l.HasNetwork(tvh.ATSCNetwork) || l.HasNetwork(tvh.DemoNetwork) ||
		len(l.Muxes) != 36 || len(l.Services) != 38 || len(l.Channels) != 38 || len(l.NetworkMuxes(tvh.ATSCNetwork)) != 36 {
		t.Fatalf("lineup: %d networks, %d muxes, %d services, %d channels", len(l.Networks), len(l.Muxes), len(l.Services), len(l.Channels))
	}
	byNumber := map[string]tvh.Channel{}
	for _, ch := range l.Channels {
		byNumber[ch.Number] = ch
	}
	svc, mux, ok := l.Source(byNumber["9.4"])
	if !ok || svc.Name != "KUSA-HD" || svc.Major != 9 || svc.Minor != 4 || mux.Name != "575MHz" || mux.FrequencyHz != 575_000_000 ||
		mux.ScanResult != tvh.ScanOK || mux.ScanLast != 1791129355 || !bool(mux.Enabled) || mux.Scanning() || mux.NumSvc != 6 {
		t.Errorf("9.4: %+v on %+v (%v)", svc, mux, ok)
	}
	failed := 0
	for _, m := range l.Muxes {
		if m.ScanResult == tvh.ScanFail {
			failed++
			if m.ScanLast != 0 || m.NumSvc != 0 {
				t.Errorf("failed mux %+v", m)
			}
		}
	}
	if failed != 28 {
		t.Errorf("%d multiplexes failed their scan, want 28", failed)
	}
}

// TestFrontendsFromTree reads the tuners, their names as the input status
// gives them and the model, from the hardware tree as Tvheadend 4.3 sends
// it, class and parameters included, so without a lookup per node.
func TestFrontendsFromTree(t *testing.T) {
	srv := tvhtest.New(t)
	fes, err := tvh.New(srv.URL, srv.Client()).Frontends(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(fes) != 2 || fes[0].Name != tvhtest.Tuner0 || fes[1].Name != tvhtest.Tuner1 || fes[0].Model != "hdhomerun_dvr_atsc" ||
		fes[0].Class != "tvhdhomerun_frontend_atsc_t" {
		t.Fatalf("frontends: %+v", fes)
	}
	for _, r := range srv.Requests() {
		if strings.Contains(r, "/api/idnode/load") {
			t.Errorf("looked up a class the tree had: %s", r)
		}
	}
}

// TestOpenMux subscribes to a multiplex at a weight, and the tuner shows it
// in the input status until the stream is closed.
func TestOpenMux(t *testing.T) {
	srv := tvhtest.New(t)
	c := tvh.New(srv.URL, srv.Client())
	ctx := t.Context()
	l, err := c.Lineup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m tvh.Mux
	for _, x := range l.Muxes {
		if x.Name == "575MHz" {
			m = x
		}
	}
	body, err := c.OpenMux(ctx, m.UUID, 10)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 188)
	if _, err := io.ReadFull(body, buf); err != nil || buf[0] != 0x47 {
		t.Fatalf("read %x, %v", buf[:1], err)
	}
	ins, err := c.Inputs(ctx)
	if err != nil || len(ins) != 1 || ins[0].Stream != "575MHz in Airwaves antenna" || ins[0].Weight != 10 || ins[0].BPS == 0 {
		t.Fatalf("inputs %+v, %v", ins, err)
	}
	body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for len(srv.Streams()) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s := srv.Streams(); len(s) != 0 {
		t.Errorf("still streaming %v", s)
	}
	var found bool
	for _, r := range srv.Requests() {
		found = found || r == "GET /stream/mux/"+m.UUID+"?pids=0&weight=10"
	}
	if !found {
		t.Errorf("requests: %v", srv.Requests())
	}

	srv.NoMuxStreams = true
	var se *tvh.StreamError
	if _, err := c.OpenMux(ctx, m.UUID, 10); !errors.As(err, &se) || se.Status != 403 {
		t.Errorf("refused: %v", err)
	}
}
