package hdhr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Device is a real HDHomeRun on the network, reached over its HTTP API.
type Device struct {
	ID           string // "1099B035"
	FriendlyName string // "HDHomeRun FLEX DUO"
	Model        string // its ModelNumber, "HDFX-2US"
	Firmware     string // its FirmwareName, "hdhomerun_dvr_atsc"
	Version      string // its FirmwareVersion, "20250623"
	Tuners       int
	// BaseURL is the API, "http://192.168.1.30", and StreamURL where the
	// streams are, on port 5004 ("http://192.168.1.30:5004"); a BaseURL
	// with a port of its own (a test server) serves both.
	BaseURL   string
	StreamURL string
	// HTTP makes the API calls, with a timeout; streams use a copy
	// without one.
	HTTP *http.Client
}

// Open reads discover.json at base: "192.168.1.30", "hdhomerun.local" or
// "http://...". A nil client gets one with a 10 s timeout.
func Open(ctx context.Context, base string, c *http.Client) (*Device, error) {
	u, err := apiURL(base)
	if err != nil {
		return nil, err
	}
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	d := &Device{BaseURL: u, StreamURL: streamURL(u), HTTP: c}
	if err := d.Refresh(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// apiURL is a device's base URL from an address as someone wrote it.
func apiURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("no HDHomeRun address")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("HDHomeRun address %q isn't a host", s)
	}
	if u.Port() == "80" {
		return u.Scheme + "://" + u.Hostname(), nil
	}
	return u.Scheme + "://" + u.Host, nil
}

// streamURL is where a device at base serves streams: port 5004, unless
// base names a port of its own other than HTTP's (a discovery reply gives
// "http://192.168.1.30:80").
func streamURL(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Port() != "" && u.Port() != "80" {
		return base
	}
	return u.Scheme + "://" + net.JoinHostPort(u.Hostname(), "5004")
}

// Refresh reads discover.json again, updating the device's fields in
// place; it isn't safe while other goroutines read them.
func (d *Device) Refresh(ctx context.Context) error {
	var body struct {
		FriendlyName    flexString
		ModelNumber     flexString
		FirmwareName    flexString
		FirmwareVersion flexString
		DeviceID        flexString
		TunerCount      flexInt
	}
	if err := d.get(ctx, "/discover.json", &body); err != nil {
		return err
	}
	d.ID, d.FriendlyName, d.Model = string(body.DeviceID), string(body.FriendlyName), string(body.ModelNumber)
	d.Firmware, d.Version, d.Tuners = string(body.FirmwareName), string(body.FirmwareVersion), int(body.TunerCount)
	return nil
}

// ATSC3 reports whether the device's firmware receives ATSC 3.0
// ("hdhomerun_dvr_atsc3" on the 4K models).
func (d *Device) ATSC3() bool { return strings.Contains(d.Firmware, "atsc3") }

// Channel is one entry of the device's lineup, from its last channel scan.
type Channel struct {
	Number      string // "2.1"
	Name        string // "KWGN-DT"
	FrequencyHz int64
	Program     int // the MPEG program number in the RF channel's multiplex
	TSID        int
	Modulation  string // "8vsb"
	VideoCodec  string // "MPEG2", "H264", "HEVC"
	AudioCodec  string // "AC3"
	HD          bool
	Favorite    bool
	DRM         bool
	ATSC3       bool
	// SignalStrength and SignalQuality are what the scan measured, in
	// percent.
	SignalStrength int
	SignalQuality  int
}

// Lineup lists the channels the device's last scan found, with how to tune
// each (lineup.json?tuning).
func (d *Device) Lineup(ctx context.Context) ([]Channel, error) {
	var body []struct {
		GuideNumber       flexString
		GuideName         flexString
		Frequency         flexInt
		ProgramNumber     flexInt
		TransportStreamID flexInt
		Modulation        flexString
		VideoCodec        flexString
		AudioCodec        flexString
		HD                flexInt
		Favorite          flexInt
		DRM               flexInt
		ATSC3             flexInt
		SignalStrength    flexInt
		SignalQuality     flexInt
	}
	if err := d.get(ctx, "/lineup.json?tuning", &body); err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(body))
	for _, e := range body {
		out = append(out, Channel{
			Number: string(e.GuideNumber), Name: strings.TrimSpace(string(e.GuideName)),
			FrequencyHz: int64(e.Frequency), Program: int(e.ProgramNumber), TSID: int(e.TransportStreamID),
			Modulation: string(e.Modulation), VideoCodec: string(e.VideoCodec), AudioCodec: string(e.AudioCodec),
			HD: e.HD != 0, Favorite: e.Favorite != 0, DRM: e.DRM != 0, ATSC3: e.ATSC3 != 0,
			SignalStrength: int(e.SignalStrength), SignalQuality: int(e.SignalQuality),
		})
	}
	return out, nil
}

// ScanStatus is how the device's channel scan is going.
type ScanStatus struct {
	InProgress bool
	// Possible is whether a scan can start now.
	Possible bool
	// Progress is in percent, and Found the channels found so far, while
	// a scan runs.
	Progress int
	Found    int
	Source   string // "Antenna"
}

// ScanStatus reads lineup_status.json.
func (d *Device) ScanStatus(ctx context.Context) (ScanStatus, error) {
	var body struct {
		ScanInProgress flexInt
		ScanPossible   flexInt
		Progress       flexInt
		Found          flexInt
		Source         flexString
	}
	if err := d.get(ctx, "/lineup_status.json", &body); err != nil {
		return ScanStatus{}, err
	}
	return ScanStatus{
		InProgress: body.ScanInProgress != 0, Possible: body.ScanPossible != 0,
		Progress: int(body.Progress), Found: int(body.Found), Source: string(body.Source),
	}, nil
}

// StartScan starts the device's channel scan of its antenna input. While
// it runs, the device takes its tuners and refuses streams (803 System
// Busy); afterwards Lineup lists what it found.
func (d *Device) StartScan(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/lineup.post?scan=start&source=Antenna", nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return fmt.Errorf("HDHomeRun: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return deviceError(resp)
	}
	return nil
}

// TunerStatus is one tuner as status.json reports it.
type TunerStatus struct {
	Tuner int // from its resource name, "tuner1"; -1 if it has another
	// Channel and Name are the virtual channel, when it was tuned to one.
	Channel string
	Name    string
	// FrequencyHz is what it's tuned to, 0 while idle.
	FrequencyHz int64
	// StrengthPct is signal strength, QualityPct signal to noise quality
	// (SNQ) and SymbolPct symbol quality (SEQ, the share of data that
	// arrived intact or corrected), in percent; nil while idle.
	StrengthPct *int
	QualityPct  *int
	SymbolPct   *int
	TargetIP    string
	// NetworkRate is what the tuner sends, in bits a second.
	NetworkRate int64
}

// Tuned reports whether the tuner is in use.
func (t TunerStatus) Tuned() bool { return t.FrequencyHz > 0 || t.Channel != "" }

// Status reads every tuner's status (status.json). The device measures a
// tuner's signal only while something has it tuned.
func (d *Device) Status(ctx context.Context) ([]TunerStatus, error) {
	var body []struct {
		Resource              flexString
		VctNumber             flexString
		VctName               flexString
		Frequency             flexInt
		SignalStrengthPercent *flexInt
		SignalQualityPercent  *flexInt
		SymbolQualityPercent  *flexInt
		TargetIP              flexString
		NetworkRate           flexInt
	}
	if err := d.get(ctx, "/status.json", &body); err != nil {
		return nil, err
	}
	out := make([]TunerStatus, 0, len(body))
	for _, e := range body {
		n, ok := tunerNumber(string(e.Resource))
		if !ok {
			n = -1
		}
		out = append(out, TunerStatus{
			Tuner: n, Channel: string(e.VctNumber), Name: string(e.VctName), FrequencyHz: int64(e.Frequency),
			StrengthPct: e.SignalStrengthPercent.int(), QualityPct: e.SignalQualityPercent.int(), SymbolPct: e.SymbolQualityPercent.int(),
			TargetIP: string(e.TargetIP), NetworkRate: int64(e.NetworkRate),
		})
	}
	return out, nil
}

// Stream is a stream from one of the device's tuners. Closing it frees the
// tuner.
type Stream struct {
	io.ReadCloser
	// Tuner is the tuner the device used (from X-HDHomeRun-Resource).
	Tuner int
}

// OpenRF streams the whole RF channel at freqHz, every program in its
// multiplex, from tuner (-1 for any free one). It returns once the device
// answers, which for a channel it can't lock is never: ctx bounds the wait,
// and ends the stream.
func (d *Device) OpenRF(ctx context.Context, tuner int, freqHz int64) (*Stream, error) {
	where := "auto"
	if tuner >= 0 {
		where = "tuner" + strconv.Itoa(tuner)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s/ch%d", d.StreamURL, where, freqHz), nil)
	if err != nil {
		return nil, err
	}
	// The client's timeout would end the stream; ctx ends it instead.
	hc := *d.client()
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HDHomeRun: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, deviceError(resp)
	}
	s := &Stream{ReadCloser: resp.Body, Tuner: tuner}
	if n, ok := tunerNumber(resp.Header.Get("X-HDHomeRun-Resource")); ok {
		s.Tuner = n
	}
	return s, nil
}

// Error is a request the device refused.
type Error struct {
	Status int // the HTTP status
	// Code and Reason are from its X-HDHomeRun-Error header ("805 All
	// Tuners In Use"), when it gave one.
	Code   int
	Reason string
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return strings.TrimSpace(fmt.Sprintf("HDHomeRun: %d %s", e.Code, e.Reason))
	}
	if e.Reason != "" {
		return "HDHomeRun: " + e.Reason
	}
	return fmt.Sprintf("HDHomeRun: HTTP %d", e.Status)
}

// Busy reports whether the device refused for want of a tuner: all in use
// (805), the one asked for in use (804), or busy scanning (803).
func (e *Error) Busy() bool {
	switch e.Code {
	case 803, 804, 805:
		return true
	case 0:
		return e.Status == http.StatusServiceUnavailable
	}
	return false
}

func deviceError(resp *http.Response) *Error {
	e := &Error{Status: resp.StatusCode}
	if h := strings.TrimSpace(resp.Header.Get("X-HDHomeRun-Error")); h != "" {
		code, reason, _ := strings.Cut(h, " ")
		if n, err := strconv.Atoi(code); err == nil {
			e.Code, e.Reason = n, strings.TrimSpace(reason)
		} else {
			e.Reason = h
		}
	}
	return e
}

// tunerNumber reads a tuner's resource name: "tuner1" is 1.
func tunerNumber(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(s), "tuner"))
	if err != nil || !strings.HasPrefix(strings.TrimSpace(s), "tuner") || n < 0 {
		return 0, false
	}
	return n, true
}

func (d *Device) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return http.DefaultClient
}

func (d *Device) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BaseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return fmt.Errorf("HDHomeRun: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return deviceError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("HDHomeRun %s: %w", strings.SplitN(path, "?", 2)[0], err)
	}
	return nil
}

// flexInt is a number as devices write it: a number (rounded), a numeric
// string, or a boolean flag (1 or 0).
type flexInt int64

func (n *flexInt) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	switch s {
	case "", "null", "false":
		*n = 0
		return nil
	case "true":
		*n = 1
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return fmt.Errorf("%s is not a number", raw)
	}
	*n = flexInt(math.Round(f))
	return nil
}

// int is the number, nil when it was absent.
func (n *flexInt) int() *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

// flexString is a string, or a number or flag written without quotes.
type flexString string

func (s *flexString) UnmarshalJSON(raw []byte) error {
	var v string
	if err := json.Unmarshal(raw, &v); err == nil {
		*s = flexString(v)
		return nil
	}
	switch t := strings.TrimSpace(string(raw)); {
	case t == "null":
		*s = ""
	case strings.HasPrefix(t, "{") || strings.HasPrefix(t, "["):
		return fmt.Errorf("%s is not a string", raw)
	default:
		*s = flexString(t)
	}
	return nil
}
