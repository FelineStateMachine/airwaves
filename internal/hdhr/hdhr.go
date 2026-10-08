// Package hdhr speaks SiliconDust's HDHomeRun protocols both ways.
//
// Server makes Airwaves look like an HDHomeRun network tuner, so apps such
// as Channels DVR, Jellyfin, Emby, VLC and the HDHomeRun app can use its
// channels, including custom ones that have no tuner behind them. It
// serves the HDHomeRun HTTP API (discover.json, lineup.json, /auto/v
// streams) plus an M3U playlist and XMLTV guide, and answers the UDP
// discovery broadcast on port 65001.
//
// Device is a real HDHomeRun, found by the same discovery broadcast and
// reached over its HTTP API: its lineup and channel scan, its tuners'
// status and signal, and streams of whole RF channels.
package hdhr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
)

// Entry is one channel in the lineup.
type Entry struct {
	Number string // "7.1"
	Name   string // "KMGH" or "Airwaves Weather"
	HD     bool
	// Logo is an absolute URL, or a path on this server
	// ("/channel-logos/1.4"); optional.
	Logo  string
	Group string // M3U group, e.g. "Antenna" or "Airwaves"
}

// Backend supplies the lineup, streams and guide.
type Backend interface {
	Lineup(ctx context.Context) ([]Entry, error)
	// Stream writes the channel's MPEG-TS to w until ctx ends or the
	// source stops.
	Stream(ctx context.Context, number string, w io.Writer) error
	// XMLTV writes the guide for the lineup. abs makes a path on this
	// server an absolute URL, as the client reached it.
	XMLTV(ctx context.Context, w io.Writer, abs func(path string) string) error
}

// Server is the emulated device.
type Server struct {
	Backend      Backend
	DeviceID     uint32
	FriendlyName string
	Tuners       func() int
	// Logos serves /channel-logos/, the custom channels' logos the
	// lineup's paths point at; optional.
	Logos http.Handler
	// FromHost takes the address clients reach the device at from their
	// requests' Host header, not the connection's local address. Set it
	// when the device isn't discovered on the LAN: clients then use an
	// address someone gave them, while the local address may be a
	// container's own, behind a port mapping.
	FromHost bool
}

// baseURL is the address the client used to reach us, so replies work for
// LAN and tailnet clients alike.
func (s *Server) baseURL(r *http.Request) string {
	if s.FromHost && r.Host != "" {
		return "http://" + r.Host
	}
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		return "http://" + addr.String()
	}
	return "http://" + r.Host
}

// Handler serves the HDHomeRun API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /discover.json", func(w http.ResponseWriter, r *http.Request) {
		base := s.baseURL(r)
		writeJSON(w, map[string]any{
			"FriendlyName":    s.FriendlyName,
			"Manufacturer":    "Silicondust",
			"ModelNumber":     "HDTC-2US",
			"FirmwareName":    "hdhomeruntc_atsc",
			"FirmwareVersion": "20250101",
			"DeviceID":        fmt.Sprintf("%08X", s.DeviceID),
			"DeviceAuth":      "airwaves",
			"TunerCount":      s.Tuners(),
			"BaseURL":         base,
			"LineupURL":       base + "/lineup.json",
		})
	})
	mux.HandleFunc("GET /lineup_status.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ScanInProgress": 0, "ScanPossible": 1, "Source": "Antenna", "SourceList": []string{"Antenna"}})
	})
	// Apps ask the tuner to rescan. A scan takes every real tuner, so
	// that's left to Reception's Scan for channels: accept and do nothing.
	mux.HandleFunc("POST /lineup.post", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /lineup.json", func(w http.ResponseWriter, r *http.Request) {
		lineup, err := s.Backend.Lineup(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		base := s.baseURL(r)
		out := make([]map[string]any, 0, len(lineup))
		for _, e := range lineup {
			m := map[string]any{"GuideNumber": e.Number, "GuideName": e.Name, "URL": base + "/auto/v" + e.Number}
			if e.HD {
				m["HD"] = 1
			}
			out = append(out, m)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /channels.m3u", func(w http.ResponseWriter, r *http.Request) {
		lineup, err := s.Backend.Lineup(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		base := s.baseURL(r)
		w.Header().Set("Content-Type", "audio/x-mpegurl")
		fmt.Fprintf(w, "#EXTM3U url-tvg=\"%s/xmltv.xml\"\n", base)
		for _, e := range lineup {
			logo := e.Logo
			if strings.HasPrefix(logo, "/") {
				logo = base + logo
			}
			fmt.Fprintf(w, "#EXTINF:-1 tvg-id=%q tvg-chno=%q tvg-name=%q tvg-logo=%q group-title=%q,%s\n%s/auto/v%s\n",
				ChannelID(e.Number), e.Number, e.Name, logo, e.Group, e.Name, base, e.Number)
		}
	})
	mux.HandleFunc("GET /xmltv.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		base := s.baseURL(r)
		if err := s.Backend.XMLTV(r.Context(), w, func(path string) string { return base + path }); err != nil {
			log.Printf("xmltv: %v", err)
		}
	})
	if s.Logos != nil {
		mux.Handle("GET /channel-logos/{number}", s.Logos)
	}
	stream := func(w http.ResponseWriter, r *http.Request) {
		number := strings.TrimPrefix(r.PathValue("ch"), "v")
		w.Header().Set("Content-Type", "video/mp2t")
		if err := s.Backend.Stream(r.Context(), number, w); err != nil && r.Context().Err() == nil {
			log.Printf("hdhr stream %s: %v", number, err)
		}
	}
	mux.HandleFunc("GET /auto/{ch}", stream)
	// Some apps address a specific tuner: /tuner0/v7.1.
	mux.HandleFunc("GET /{tuner}/{ch}", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.PathValue("tuner"), "tuner") {
			http.NotFound(w, r)
			return
		}
		stream(w, r)
	})
	return mux
}

// ChannelID is the XMLTV channel id for a virtual channel number.
func ChannelID(number string) string { return "airwaves." + number }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
