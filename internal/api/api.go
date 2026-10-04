// Package api exposes the Airwaves engine over HTTP (airwavesd) and
// provides a client with the same shape, so the desktop app can use a local
// engine or a remote server interchangeably.
package api

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"airwaves/internal/dvr"
	"airwaves/internal/phase"
	"airwaves/internal/service"
	"airwaves/internal/stream"
	"airwaves/internal/weather"
)

// Backend is what a UI needs from an engine.
type Backend interface {
	Info(ctx context.Context) (service.Info, error)
	Config(ctx context.Context) (service.Config, error)
	SetConfig(ctx context.Context, c service.Config) (service.Config, error)
	Snapshot(ctx context.Context, refresh bool) (*service.Snapshot, error)
	// Signal is what the tuners measured, by RF channel and by channel.
	Signal(ctx context.Context) (*service.SignalReport, error)
	// Measure starts measuring every RF channel on an idle tuner.
	Measure(ctx context.Context) (*service.SweepStatus, error)
	Weather(ctx context.Context) (*weather.Report, error)
	Tune(ctx context.Context, client, number string) (*stream.Playback, error)
	Stop(ctx context.Context, client string) error
	DVR(ctx context.Context) (*dvr.State, error)
	Record(ctx context.Context, req dvr.Request) (*dvr.Rule, error)
	DeleteRule(ctx context.Context, id string) error
	DeleteRecording(ctx context.Context, id string) error
	PlayRecording(ctx context.Context, client, id string, from float64) (*stream.Playback, error)
	SaveProgress(ctx context.Context, id string, position, duration float64) error
	MarkWatched(ctx context.Context, id string, watched bool) error
	UpdateRule(ctx context.Context, id string, u dvr.RuleUpdate) (*dvr.Rule, error)
	SetDVRPrefs(ctx context.Context, p dvr.Prefs) (dvr.Prefs, error)
}

var (
	_ Backend = (*service.Service)(nil)
	_ Backend = (*Client)(nil)
)

// Server serves a Backend.
type Server struct {
	Backend Backend
	// Streams serves /live/ HLS files.
	Streams http.Handler
	// Extra serves other unauthenticated paths (weather images, the
	// WeatherStar redirect).
	Extra http.Handler
	// Logos serves custom channels' logos at /channel-logos/<number>,
	// unauthenticated like Extra.
	Logos http.Handler
	// Token, when set, must be sent as "Authorization: Bearer <token>" on
	// /api/ requests. Stream URLs are unguessable and stay open so native
	// players can fetch them.
	Token string
	// Admin serves the owner's channel admin page at /admin/. Without an
	// AdminPassword it is only as private as the address the server
	// listens on.
	Admin http.Handler
	// AdminPassword, when set, is asked for on /admin/ (the page, its API
	// and the MCP endpoint at /admin/mcp): as HTTP Basic auth with any user
	// name, or as "Authorization: Bearer <password>" from MCP clients.
	AdminPassword string
	// TV is the TV interface (frontend/dist), served at /tv/ for browsers
	// and the Android TV app. The page needs no token; the API it calls
	// does.
	TV fs.FS
}

type clientReq struct {
	Client string  `json:"client"`
	Number string  `json:"number,omitempty"`
	ID     string  `json:"id,omitempty"`
	From   float64 `json:"from,omitempty"`
}

// metricsReq is a channel change as the app timed it.
type metricsReq struct {
	Number string `json:"number"`
	// Kind is the channel's: "antenna", "folder", "jellyfin", "youtube"
	// or "weather".
	Kind         string  `json:"kind"`
	FirstFrameMS float64 `json:"firstFrameMs"`
}

type progressReq struct {
	ID       string  `json:"id"`
	Position float64 `json:"position"`
	Duration float64 `json:"duration"`
	Watched  *bool   `json:"watched,omitempty"`
}

// Handler routes requests.
func (s *Server) Handler() http.Handler {
	b := s.Backend
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(b.Info(r.Context()))
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(b.Config(r.Context()))
	})
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		var c service.Config
		if !decode(w, r, &c) {
			return
		}
		reply(w)(b.SetConfig(r.Context(), c))
	})
	mux.HandleFunc("GET /api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(b.Snapshot(r.Context(), r.URL.Query().Get("refresh") == "1"))
	})
	mux.HandleFunc("GET /api/signal", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(b.Signal(r.Context()))
	})
	mux.HandleFunc("POST /api/signal/measure", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(b.Measure(r.Context()))
	})
	mux.HandleFunc("GET /api/weather", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(b.Weather(r.Context()))
	})
	mux.HandleFunc("POST /api/tune", func(w http.ResponseWriter, r *http.Request) {
		var req clientReq
		if !decode(w, r, &req) {
			return
		}
		reply(w)(b.Tune(r.Context(), req.Client, req.Number))
	})
	mux.HandleFunc("POST /api/stop", func(w http.ResponseWriter, r *http.Request) {
		var req clientReq
		if !decode(w, r, &req) {
			return
		}
		reply(w)(struct{}{}, b.Stop(r.Context(), req.Client))
	})
	// The app reports how long its channel changes took, from the key
	// press to the first frame playing, for the admin page.
	mux.HandleFunc("POST /api/metrics", func(w http.ResponseWriter, r *http.Request) {
		var req metricsReq
		if !decode(w, r, &req) {
			return
		}
		phase.Tunes.Add(req.Kind, phase.FirstFrame, time.Duration(req.FirstFrameMS*float64(time.Millisecond)))
		reply(w)(struct{}{}, nil)
	})
	mux.HandleFunc("GET /api/metrics", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(phase.Tunes.Summaries(), nil)
	})
	mux.HandleFunc("GET /api/dvr", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(b.DVR(r.Context()))
	})
	mux.HandleFunc("POST /api/dvr/record", func(w http.ResponseWriter, r *http.Request) {
		var req dvr.Request
		if !decode(w, r, &req) {
			return
		}
		reply(w)(b.Record(r.Context(), req))
	})
	mux.HandleFunc("DELETE /api/dvr/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(struct{}{}, b.DeleteRule(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("DELETE /api/dvr/recordings/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w)(struct{}{}, b.DeleteRecording(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/dvr/play", func(w http.ResponseWriter, r *http.Request) {
		var req clientReq
		if !decode(w, r, &req) {
			return
		}
		reply(w)(b.PlayRecording(r.Context(), req.Client, req.ID, req.From))
	})
	mux.HandleFunc("POST /api/dvr/progress", func(w http.ResponseWriter, r *http.Request) {
		var req progressReq
		if !decode(w, r, &req) {
			return
		}
		if req.Watched != nil {
			reply(w)(struct{}{}, b.MarkWatched(r.Context(), req.ID, *req.Watched))
			return
		}
		reply(w)(struct{}{}, b.SaveProgress(r.Context(), req.ID, req.Position, req.Duration))
	})
	mux.HandleFunc("PUT /api/dvr/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		var u dvr.RuleUpdate
		if !decode(w, r, &u) {
			return
		}
		reply(w)(b.UpdateRule(r.Context(), r.PathValue("id"), u))
	})
	mux.HandleFunc("PUT /api/dvr/prefs", func(w http.ResponseWriter, r *http.Request) {
		var p dvr.Prefs
		if !decode(w, r, &p) {
			return
		}
		reply(w)(b.SetDVRPrefs(r.Context(), p))
	})
	if s.Streams != nil {
		mux.Handle("/live/", s.Streams)
	}
	if s.Extra != nil {
		mux.Handle("/wximg/", s.Extra)
		mux.Handle("/weatherstar", s.Extra)
		mux.Handle("/weatherstar/", s.Extra)
		mux.Handle("/wxmusic/", s.Extra)
	}
	if s.Logos != nil {
		mux.Handle("/channel-logos/", s.Logos)
	}
	if s.Admin != nil {
		admin := s.Admin
		if s.AdminPassword != "" {
			admin = adminAuth(s.AdminPassword, admin)
		}
		mux.Handle("/admin/", admin)
		mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusFound))
	}
	if s.TV != nil {
		mux.Handle("GET /tv/", http.StripPrefix("/tv", tvFiles(s.TV)))
		mux.Handle("GET /tv", http.RedirectHandler("/tv/", http.StatusFound))
	}
	// For container health checks: up, without the token.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	return s.auth(gzipJSON(mux))
}

// adminAuth asks for the admin password. Both sides are hashed before the
// constant-time compare, so neither the password nor its length leaks
// through timing. Wrong passwords are logged with the client's address,
// never the password.
func adminAuth(password string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, sent := "", true
		if _, p, ok := r.BasicAuth(); ok {
			got = p
		} else if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			got = b
		} else {
			sent = false
		}
		h := sha256.Sum256([]byte(got))
		if !sent || subtle.ConstantTimeCompare(h[:], want[:]) != 1 {
			if sent {
				log.Printf("admin: wrong password from %s", r.RemoteAddr)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="Airwaves admin", charset="UTF-8"`)
			http.Error(w, "The admin page needs its password.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				writeErr(w, http.StatusUnauthorized, errors.New("missing or wrong token"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

// reply writes a value or an error. Used as reply(w)(f()).
func reply(w http.ResponseWriter) func(any, error) {
	return func(v any, err error) {
		if err != nil {
			writeErr(w, errStatus(err), err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
}

// errStatus is the HTTP status for a backend error: 501 for what this
// server doesn't have (an antenna, a tuner, recording, a location for the
// weather), 503 for a channel with no signal right now, else 500.
func errStatus(err error) int {
	var noSignal *service.NoSignalError
	switch {
	case errors.Is(err, service.ErrNoAntenna), errors.Is(err, service.ErrNoTuner), errors.Is(err, service.ErrNoDVR), errors.Is(err, service.ErrNoLocation):
		return http.StatusNotImplemented
	case errors.As(err, &noSignal):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

type errorBody struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(errorBody{Error: err.Error()})
}

// gzipJSON compresses API responses; snapshots are a few megabytes of JSON.
func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(gzipWriter{ResponseWriter: w, w: gz}, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	w io.Writer
}

func (g gzipWriter) Write(b []byte) (int, error) { return g.w.Write(b) }
