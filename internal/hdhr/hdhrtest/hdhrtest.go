// Package hdhrtest is a fake HDHomeRun for tests: its HTTP API (discover,
// lineup, scan and status) and its streams of whole RF channels, with
// tuners that can be taken by someone else and RF channels that carry a
// multiplex or can't be locked.
package hdhrtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"airwaves/internal/hdhr"
)

// OtherFrequency is what a tuner taken by someone else (Busy) is tuned to
// in status.json: RF 2.
const OtherFrequency = 57_000_000

// OtherTarget is where a tuner taken by someone else sends its stream.
const OtherTarget = "192.0.2.9"

// Device describes the fake device.
type Device struct {
	// ID, FriendlyName, Model, Firmware and Version default to a FLEX
	// DUO's.
	ID           string
	FriendlyName string
	Model        string
	Firmware     string
	Version      string
	// Tuners defaults to 2.
	Tuners int
	// Lineup is what lineup.json lists, and Rescan what it lists after a
	// channel scan (nil keeps Lineup).
	Lineup []hdhr.Channel
	Rescan []hdhr.Channel
	// ScanTime is how long a scan runs; 50 ms by default.
	ScanTime time.Duration
	// Mux is what the RF channel at freqHz carries, sent over and over
	// while a client reads. Nil, or an empty multiplex, can't be locked:
	// the request then waits without an answer until the client goes
	// away, while status.json shows the tuner on that frequency.
	Mux func(freqHz int64) []byte
	// Rate is how fast an RF channel streams, in bytes a second: about a
	// broadcast's 19.4 Mb/s by default.
	Rate int
	// Signal is what status.json reports for a tuner on freqHz: strength,
	// signal to noise quality and symbol quality, in percent. By default
	// 90, 95 and 100 where Mux has something, else 40, 0 and 0.
	Signal func(freqHz int64) (strength, quality, symbol int)
}

// Server is a running fake device.
type Server struct {
	d    Device
	srv  *httptest.Server
	quit chan struct{}

	mu       sync.Mutex
	lineup   []hdhr.Channel
	tuners   []tuner
	scanning bool
	scanFrom time.Time
	scans    int
}

type tuner struct {
	freq   int64 // what it's tuned to; 0 while idle
	open   bool  // a client of ours streams from it
	locked bool  // its RF channel has a multiplex
	busy   bool  // someone else has it
}

// New starts a fake device, closed when the test ends.
func New(t testing.TB, d Device) *Server {
	t.Helper()
	d.ID = or(d.ID, fmt.Sprintf("%08X", hdhr.DeviceIDFor("hdhrtest")))
	d.FriendlyName = or(d.FriendlyName, "HDHomeRun FLEX DUO")
	d.Model = or(d.Model, "HDFX-2US")
	d.Firmware = or(d.Firmware, "hdhomerun_dvr_atsc")
	d.Version = or(d.Version, "20250623")
	if d.Tuners <= 0 {
		d.Tuners = 2
	}
	if d.ScanTime <= 0 {
		d.ScanTime = 50 * time.Millisecond
	}
	s := &Server{d: d, quit: make(chan struct{}), lineup: slices.Clone(d.Lineup), tuners: make([]tuner, d.Tuners)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /discover.json", s.discover)
	mux.HandleFunc("GET /lineup.json", s.lineupJSON)
	mux.HandleFunc("GET /lineup_status.json", s.lineupStatus)
	mux.HandleFunc("POST /lineup.post", s.lineupPost)
	mux.HandleFunc("GET /status.json", s.status)
	mux.HandleFunc("GET /{tuner}/{ch}", s.stream)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		close(s.quit)
		s.srv.CloseClientConnections()
		s.srv.Close()
	})
	return s
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// URL is the device's base URL, for its API and its streams alike.
func (s *Server) URL() string { return s.srv.URL }

// Busy has someone else take tuner, as another app on the network would.
func (s *Server) Busy(tuner int) {
	s.mu.Lock()
	s.tuners[tuner].busy = true
	s.mu.Unlock()
}

// Free gives back a tuner Busy took.
func (s *Server) Free(tuner int) {
	s.mu.Lock()
	s.tuners[tuner].busy = false
	s.mu.Unlock()
}

// Streams counts the streams open now.
func (s *Server) Streams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.tuners {
		if t.open {
			n++
		}
	}
	return n
}

// Scans counts the channel scans started.
func (s *Server) Scans() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scans
}

// settle ends a scan whose time is up. s.mu is held.
func (s *Server) settle() {
	if s.scanning && time.Since(s.scanFrom) >= s.d.ScanTime {
		s.scanning = false
		if s.d.Rescan != nil {
			s.lineup = slices.Clone(s.d.Rescan)
		}
	}
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"FriendlyName": s.d.FriendlyName, "ModelNumber": s.d.Model,
		"FirmwareName": s.d.Firmware, "FirmwareVersion": s.d.Version, "DeviceID": s.d.ID,
		"BaseURL": s.srv.URL, "LineupURL": s.srv.URL + "/lineup.json", "TunerCount": s.d.Tuners,
	})
}

func (s *Server) lineupJSON(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.settle()
	lineup := slices.Clone(s.lineup)
	s.mu.Unlock()
	tuning := r.URL.Query().Has("tuning")
	out := make([]map[string]any, 0, len(lineup))
	for _, c := range lineup {
		e := map[string]any{"GuideNumber": c.Number, "GuideName": c.Name}
		if tuning {
			e["TransportStreamID"], e["Modulation"], e["Frequency"], e["ProgramNumber"] = c.TSID, or(c.Modulation, "8vsb"), c.FrequencyHz, c.Program
		}
		e["VideoCodec"], e["AudioCodec"] = or(c.VideoCodec, "MPEG2"), or(c.AudioCodec, "AC3")
		for name, on := range map[string]bool{"HD": c.HD, "Favorite": c.Favorite, "DRM": c.DRM, "ATSC3": c.ATSC3} {
			if on {
				e[name] = 1
			}
		}
		e["SignalStrength"], e["SignalQuality"] = c.SignalStrength, c.SignalQuality
		e["URL"] = s.srv.URL + "/auto/v" + c.Number
		out = append(out, e)
	}
	writeJSON(w, out)
}

func (s *Server) lineupStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.settle()
	scanning, from, found := s.scanning, s.scanFrom, len(s.lineup)
	s.mu.Unlock()
	if scanning {
		progress := min(99, int(100*time.Since(from)/s.d.ScanTime))
		writeJSON(w, map[string]any{"ScanInProgress": 1, "Progress": progress, "Found": found})
		return
	}
	writeJSON(w, map[string]any{"ScanInProgress": 0, "ScanPossible": 1, "Source": "Antenna", "SourceList": []string{"Antenna", "Cable"}})
}

func (s *Server) lineupPost(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle()
	switch r.URL.Query().Get("scan") {
	case "start":
		s.scans++
		s.scanning, s.scanFrom = true, time.Now()
	case "abort":
		s.scanning = false
	default:
		http.Error(w, "bad request", http.StatusBadRequest)
	}
}

func (s *Server) signal(freq int64, locked bool) (int, int, int) {
	if s.d.Signal != nil {
		return s.d.Signal(freq)
	}
	if locked {
		return 90, 95, 100
	}
	return 40, 0, 0
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	tuners := slices.Clone(s.tuners)
	s.mu.Unlock()
	out := make([]map[string]any, 0, len(tuners))
	for i, t := range tuners {
		e := map[string]any{"Resource": fmt.Sprintf("tuner%d", i)}
		switch {
		case t.busy:
			e["Frequency"], e["TargetIP"], e["NetworkRate"] = OtherFrequency, OtherTarget, 19392658
			e["SignalStrengthPercent"], e["SignalQualityPercent"], e["SymbolQualityPercent"] = 90, 95, 100
		case t.open:
			ss, snq, seq := s.signal(t.freq, t.locked)
			e["Frequency"], e["TargetIP"] = t.freq, "127.0.0.1"
			e["SignalStrengthPercent"], e["SignalQualityPercent"], e["SymbolQualityPercent"] = ss, snq, seq
			if t.locked {
				e["NetworkRate"] = 19392658
			} else {
				e["NetworkRate"] = 0
			}
		}
		out = append(out, e)
	}
	writeJSON(w, out)
}

// stream serves /auto/ch<freq> and /tuner<n>/ch<freq>: the whole RF
// channel, on any free tuner or the one asked for.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	where, ch := r.PathValue("tuner"), r.PathValue("ch")
	freq, err := strconv.ParseInt(strings.TrimPrefix(ch, "ch"), 10, 64)
	if !strings.HasPrefix(ch, "ch") || err != nil || freq < 1_000_000 {
		refuse(w, http.StatusNotFound, "801 Unknown Channel")
		return
	}
	var mux []byte
	if s.d.Mux != nil {
		mux = s.d.Mux(freq)
	}
	n, status, reason := s.take(where, freq, len(mux) > 0)
	if n < 0 {
		refuse(w, status, reason)
		return
	}
	defer s.release(n)
	if len(mux) == 0 {
		// No lock: no answer at all, as the device does.
		select {
		case <-r.Context().Done():
		case <-s.quit:
		}
		return
	}
	w.Header().Set("Content-Type", "video/mpeg")
	w.Header().Set("X-HDHomeRun-Resource", fmt.Sprintf("tuner%d", n))
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	// Whole packets every 20 ms, at Rate, round and round the multiplex.
	rate := s.d.Rate
	if rate <= 0 {
		rate = 2_425_000
	}
	chunk := max(rate/50/188, 1) * 188
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for at := 0; ; {
		for n := 0; n < chunk; {
			part := mux[at:min(len(mux), at+chunk-n)]
			if _, err := w.Write(part); err != nil {
				return
			}
			n += len(part)
			at = (at + len(part)) % len(mux)
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.quit:
			return
		case <-tick.C:
		}
	}
}

// take allocates a tuner for a stream; n is -1 when it can't, with the
// status and X-HDHomeRun-Error to refuse with.
func (s *Server) take(where string, freq int64, locked bool) (n, status int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settle()
	if s.scanning {
		return -1, http.StatusServiceUnavailable, "803 System Busy"
	}
	free := func(i int) bool { return !s.tuners[i].open && !s.tuners[i].busy }
	n = -1
	if where == "auto" {
		for i := range s.tuners {
			if free(i) {
				n = i
				break
			}
		}
		if n < 0 {
			return -1, http.StatusServiceUnavailable, "805 All Tuners In Use"
		}
	} else {
		i, err := strconv.Atoi(strings.TrimPrefix(where, "tuner"))
		if !strings.HasPrefix(where, "tuner") || err != nil || i < 0 || i >= len(s.tuners) {
			return -1, http.StatusNotFound, ""
		}
		if !free(i) {
			return -1, http.StatusServiceUnavailable, "804 Tuner In Use"
		}
		n = i
	}
	s.tuners[n] = tuner{freq: freq, open: true, locked: locked}
	return n, 0, ""
}

func (s *Server) release(n int) {
	s.mu.Lock()
	busy := s.tuners[n].busy
	s.tuners[n] = tuner{busy: busy}
	s.mu.Unlock()
}

func refuse(w http.ResponseWriter, status int, reason string) {
	if reason != "" {
		w.Header().Set("X-HDHomeRun-Error", reason)
	}
	http.Error(w, http.StatusText(status), status)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
