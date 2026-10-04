// Package tvhtest runs a fake Tvheadend for tests: the grids, status and
// hardware endpoints Airwaves reads, the calls it makes to set Tvheadend
// up, and streams that tune a fake tuner. It starts from a real server's
// answers (testdata): an HDHomeRun with two ATSC tuners whose scan found 38
// channels on 8 of the 36 US ATSC multiplexes around Denver.
package tvhtest

import (
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:embed testdata
var testdata embed.FS

// Tuner names, as Tvheadend names the inputs in the testdata.
const (
	Tuner0 = "HDHomeRun ATSC-T Tuner #0 (169.254.1.2)"
	Tuner1 = "HDHomeRun ATSC-T Tuner #1 (169.254.1.2)"
)

// Network is the antenna network's name in the testdata.
const Network = "Airwaves antenna"

// Signal is how a fake tuner receives a multiplex.
type Signal struct {
	Lock bool
	// Strength and Quality are percentages, reported on Tvheadend's
	// relative scale as an HDHomeRun's are.
	Strength, Quality float64
}

// Server is a fake Tvheadend.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	grids    map[string][]map[string]any // by grid: "channel", "mux", "service", "network"
	tree     map[string][]map[string]any // hardware tree by parent UUID
	classes  map[string]string
	inputs   []map[string]any // inputs busy with something else
	subs     []map[string]any
	signals  map[string]Signal // by multiplex name
	open     map[int]*stream
	nextID   int
	requests []string
	hardware map[string][]map[string]any // the testdata's tree, for SetTuner
	streams  []string                    // "input mux" of every stream opened
	maxOpen  int
	// NoMuxStreams refuses /stream/mux, as a server whose user may not
	// stream raw multiplexes does.
	NoMuxStreams bool
	// Tuners is how many inputs the fake has; 2 as in the testdata.
	Tuners int
	// OnStream, when set, is called as a stream tunes a multiplex.
	OnStream func(mux string)
}

type stream struct {
	input  string
	mux    string
	weight int
	done   chan struct{}
	once   sync.Once
}

func (s *stream) end() { s.once.Do(func() { close(s.done) }) }

// New starts a fake Tvheadend with the testdata loaded. Both tuners are
// idle (the testdata's guide scans are in Inputs, not loaded), and every
// multiplex the scan found locks at 85% strength and 83% quality; the
// others have no signal.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		grids: map[string][]map[string]any{}, tree: map[string][]map[string]any{}, classes: map[string]string{},
		signals: map[string]Signal{}, open: map[int]*stream{}, Tuners: 2,
	}
	for grid, file := range map[string]string{"channel": "channel_grid.json", "mux": "mux_grid.json", "service": "service_grid.json", "network": "network_grid.json"} {
		var body struct {
			Entries []map[string]any `json:"entries"`
		}
		mustLoad(t, file, &body)
		s.grids[grid] = body.Entries
	}
	var root, dev []map[string]any
	mustLoad(t, "hardware_root.json", &root)
	mustLoad(t, "hardware_device.json", &dev)
	s.tree["root"] = root
	s.tree[root[0]["uuid"].(string)] = dev
	s.hardware = maps.Clone(s.tree)
	for _, list := range s.tree {
		for _, n := range list {
			s.classes[n["uuid"].(string)] = n["class"].(string)
		}
	}
	for _, m := range s.grids["mux"] {
		if num(m["scan_result"]) == 1 {
			s.signals[m["name"].(string)] = Signal{Lock: true, Strength: 85, Quality: 83}
		}
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Close ends open streams and stops the server.
func (s *Server) Close() {
	s.mu.Lock()
	for _, st := range s.open {
		st.end()
	}
	s.mu.Unlock()
	s.Server.Close()
}

func mustLoad(t testing.TB, name string, v any) {
	t.Helper()
	raw, err := testdata.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// Raw returns a testdata file, for parsing tests.
func Raw(t testing.TB, name string) []byte {
	t.Helper()
	raw, err := testdata.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case bool:
		if n {
			return 1
		}
	}
	return 0
}

// SetSignal sets how the tuners receive a multiplex, by name ("605MHz").
func (s *Server) SetSignal(mux string, sig Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals[mux] = sig
}

// Busy has an input tuned to a multiplex for something other than
// Airwaves' measuring: a viewer, a recording or a guide scan, at weight.
func (s *Server) Busy(input, mux string, weight int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = slices.DeleteFunc(s.inputs, func(in map[string]any) bool { return in["input"] == input })
	s.inputs = append(s.inputs, s.inputStatus(input, mux, weight))
}

// Free ends what Busy started on an input.
func (s *Server) Free(input string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = slices.DeleteFunc(s.inputs, func(in map[string]any) bool { return in["input"] == input })
}

// TakeOver has a viewer take every tuner a measuring stream holds, as a
// subscription of higher weight does: the stream ends and the input is
// the viewer's, on another multiplex.
func (s *Server) TakeOver(mux string) {
	s.mu.Lock()
	var taken []string
	for id, st := range s.open {
		taken = append(taken, st.input)
		st.end()
		delete(s.open, id)
	}
	s.mu.Unlock()
	for _, in := range taken {
		s.Busy(in, mux, 150)
	}
}

// Streams lists the multiplexes measuring streams are open on now.
func (s *Server) Streams() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, st := range s.open {
		out = append(out, st.mux)
	}
	slices.Sort(out)
	return out
}

// Requests lists the requests made, as "METHOD /path?query", in order.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// Entries returns a grid's entries ("channel", "mux", "service",
// "network"), for checking what was changed.
func (s *Server) Entries(grid string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.grids[grid])
}

// Add puts an entry in a grid, for setting up other networks.
func (s *Server) Add(grid string, e map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grids[grid] = append(s.grids[grid], e)
}

// SetTuner plugs the HDHomeRun in (as in the testdata) or takes it away,
// leaving Tvheadend with no tuner.
func (s *Server) SetTuner(present bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if present {
		s.tree = maps.Clone(s.hardware)
	} else {
		s.tree = map[string][]map[string]any{"root": {}}
	}
}

// Opened lists the streams opened so far, as "<input> <mux>", and the most
// that were open at once.
func (s *Server) Opened() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.streams), s.maxOpen
}

// inputStatus is an input's status on a multiplex, as /api/status/inputs
// writes it. The caller holds mu.
func (s *Server) inputStatus(input, mux string, weight int) map[string]any {
	sig := s.signals[mux]
	bps := 0
	if sig.Lock {
		bps = 231616
	}
	return map[string]any{
		"uuid": fmt.Sprintf("mmi-%s-%s", strings.Fields(input)[3], mux), "input": input, "stream": mux + " in " + Network,
		"subs": 1, "weight": weight, "pids": []int{0},
		"signal": int(sig.Strength * 655.35), "signal_scale": 1, "snr": int(sig.Quality * 655.35), "snr_scale": 1,
		"ber": 0, "unc": 0, "bps": bps, "te": 0, "cc": 0, "ec_bit": 0, "tc_bit": 0, "ec_block": 0, "tc_block": 0,
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
	s.mu.Unlock()
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
	}
	path := r.URL.Path
	switch {
	case path == "/api/serverinfo":
		raw, _ := testdata.ReadFile("testdata/serverinfo.json")
		w.Write(raw)
	case path == "/api/channel/grid":
		s.writeGrid(w, "channel")
	case path == "/api/mpegts/mux/grid":
		s.writeGrid(w, "mux")
	case path == "/api/mpegts/service/grid":
		s.writeGrid(w, "service")
	case path == "/api/mpegts/network/grid":
		s.writeGrid(w, "network")
	case path == "/api/status/inputs":
		s.mu.Lock()
		list := slices.Clone(s.inputs)
		for _, st := range s.open {
			list = append(list, s.inputStatus(st.input, st.mux, st.weight))
		}
		s.mu.Unlock()
		writeJSON(w, map[string]any{"entries": list, "totalCount": len(list)})
	case path == "/api/status/subscriptions":
		s.mu.Lock()
		list := slices.Clone(s.subs)
		s.mu.Unlock()
		writeJSON(w, map[string]any{"entries": list, "totalCount": len(list)})
	case path == "/api/hardware/tree":
		s.mu.Lock()
		nodes := s.tree[r.URL.Query().Get("uuid")]
		s.mu.Unlock()
		if nodes == nil {
			nodes = []map[string]any{}
		}
		writeJSON(w, nodes)
	case path == "/api/idnode/load":
		s.mu.Lock()
		class := s.classes[r.URL.Query().Get("uuid")]
		s.mu.Unlock()
		writeJSON(w, map[string]any{"entries": []map[string]string{{"class": class}}})
	case path == "/api/idnode/delete" && r.Method == http.MethodPost:
		s.delete(r.Form.Get("uuid"))
		writeJSON(w, map[string]any{})
	case path == "/api/dvr/entry/grid_upcoming", path == "/api/dvr/entry/grid_finished", path == "/api/dvr/entry/grid_failed":
		writeJSON(w, map[string]any{"entries": []any{}})
	case path == "/api/dvb/scanfile/list":
		writeJSON(w, map[string]any{"entries": []map[string]string{
			{"key": "atsc-t/us-ATSC-center-frequencies-8VSB", "val": "United States: us-ATSC-center-frequencies-8VSB"},
		}})
	case path == "/api/mpegts/network/create" && r.Method == http.MethodPost:
		var conf map[string]any
		_ = json.Unmarshal([]byte(r.Form.Get("conf")), &conf)
		s.mu.Lock()
		s.nextID++
		uuid := fmt.Sprintf("net-%d", s.nextID)
		s.grids["network"] = append(s.grids["network"], map[string]any{"uuid": uuid, "networkname": conf["networkname"], "num_mux": 0, "num_svc": 0, "num_chn": 0})
		s.mu.Unlock()
		writeJSON(w, map[string]any{"uuid": uuid})
	case r.Method == http.MethodPost:
		// Other setup calls (idnode save, mux create, the mapper):
		// accepted.
		writeJSON(w, map[string]any{})
	case strings.HasPrefix(path, "/stream/mux/"):
		if s.NoMuxStreams {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		s.stream(w, r, s.muxName(strings.TrimPrefix(path, "/stream/mux/")))
	case strings.HasPrefix(path, "/stream/channel/"):
		s.stream(w, r, s.channelMux(strings.TrimPrefix(path, "/stream/channel/")))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) writeGrid(w http.ResponseWriter, grid string) {
	s.mu.Lock()
	list := slices.Clone(s.grids[grid])
	s.mu.Unlock()
	writeJSON(w, map[string]any{"entries": list, "total": len(list)})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// delete removes an idnode: a network takes its multiplexes and their
// services along, as Tvheadend's does.
func (s *Server) delete(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is := func(e map[string]any) bool { return e["uuid"] == uuid }
	gone := map[string]bool{}
	for _, n := range s.grids["network"] {
		if is(n) {
			for _, m := range s.grids["mux"] {
				if m["network_uuid"] == uuid {
					gone[m["uuid"].(string)] = true
				}
			}
		}
	}
	for _, m := range s.grids["mux"] {
		if is(m) {
			gone[m["uuid"].(string)] = true
		}
	}
	for g := range maps.Keys(s.grids) {
		s.grids[g] = slices.DeleteFunc(s.grids[g], is)
	}
	s.grids["mux"] = slices.DeleteFunc(s.grids["mux"], func(e map[string]any) bool { return gone[e["uuid"].(string)] })
	s.grids["service"] = slices.DeleteFunc(s.grids["service"], func(e map[string]any) bool {
		return gone[fmt.Sprint(e["multiplex_uuid"])]
	})
	// Channels whose services are gone keep no services.
	alive := map[string]bool{}
	for _, sv := range s.grids["service"] {
		alive[sv["uuid"].(string)] = true
	}
	for _, ch := range s.grids["channel"] {
		var keep []any
		for _, id := range anyList(ch["services"]) {
			if alive[fmt.Sprint(id)] {
				keep = append(keep, id)
			}
		}
		ch["services"] = keep
	}
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

func (s *Server) muxName(uuid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.grids["mux"] {
		if m["uuid"] == uuid {
			return m["name"].(string)
		}
	}
	return ""
}

func (s *Server) channelMux(uuid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.grids["channel"] {
		if ch["uuid"] != uuid {
			continue
		}
		for _, id := range anyList(ch["services"]) {
			for _, sv := range s.grids["service"] {
				if sv["uuid"] == id {
					return fmt.Sprint(sv["multiplex"])
				}
			}
		}
	}
	return ""
}

// stream tunes a free input to mux for as long as the client reads,
// sending TS packets while the multiplex locks.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, mux string) {
	if mux == "" {
		http.NotFound(w, r)
		return
	}
	weight, _ := strconv.Atoi(r.URL.Query().Get("weight"))
	s.mu.Lock()
	used := map[string]bool{}
	for _, in := range s.inputs {
		used[in["input"].(string)] = true
	}
	for _, st := range s.open {
		used[st.input] = true
	}
	input := ""
	for _, name := range []string{Tuner0, Tuner1}[:min(s.Tuners, 2)] {
		if !used[name] {
			input = name
			break
		}
	}
	if input == "" {
		s.mu.Unlock()
		http.Error(w, "No input source available", http.StatusServiceUnavailable)
		return
	}
	st := &stream{input: input, mux: mux, weight: weight, done: make(chan struct{})}
	s.nextID++
	id := s.nextID
	s.open[id] = st
	s.streams = append(s.streams, input+" "+mux)
	s.maxOpen = max(s.maxOpen, len(s.open))
	lock := s.signals[mux].Lock
	hook := s.OnStream
	s.mu.Unlock()
	if hook != nil {
		hook(mux)
	}
	defer func() {
		s.mu.Lock()
		if s.open[id] == st {
			delete(s.open, id)
		}
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "video/mp2t")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	packet := make([]byte, 188)
	packet[0] = 0x47
	for {
		select {
		case <-r.Context().Done():
			return
		case <-st.done:
			return
		case <-tick.C:
			if lock {
				if _, err := w.Write(packet); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}
}
