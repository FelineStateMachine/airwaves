// Package tvh is a small client for the Tvheadend HTTP API: channels, live
// streams, timed DVR entries and network setup.
//
// Tvheadend owns the tuners and writes recordings; Airwaves decides what to
// record and how to present it.
package tvh

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// numberSplit is how Tvheadend packs "major.minor" into one integer.
const numberSplit = 1_000_000

// Client talks to one Tvheadend server.
type Client struct {
	Base string // e.g. http://tvheadend:9981
	HTTP *http.Client
}

// New returns a client for base.
func New(base string, c *http.Client) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: c}
}

// Info describes the server.
type Info struct {
	Version    string `json:"sw_version"`
	APIVersion int    `json:"api_version"`
	Name       string `json:"name"`
}

// ServerInfo checks the server is reachable.
func (c *Client) ServerInfo(ctx context.Context) (Info, error) {
	var i Info
	return i, c.get(ctx, "/api/serverinfo", nil, &i)
}

// Channel is a mapped Tvheadend channel.
type Channel struct {
	UUID     string   `json:"uuid"`
	Name     string   `json:"name"`
	Number   string   `json:"number"` // "7.1"
	Enabled  bool     `json:"enabled"`
	Services []string `json:"services"`
}

// Channels lists all channels.
func (c *Client) Channels(ctx context.Context) ([]Channel, error) {
	var body struct {
		Entries []struct {
			UUID     string     `json:"uuid"`
			Name     string     `json:"name"`
			Number   gridNumber `json:"number"`
			Enabled  bool       `json:"enabled"`
			Services []string   `json:"services"`
		} `json:"entries"`
	}
	if err := c.get(ctx, "/api/channel/grid", url.Values{"limit": {"5000"}}, &body); err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(body.Entries))
	for _, e := range body.Entries {
		out = append(out, Channel{UUID: e.UUID, Name: strings.TrimSpace(e.Name), Number: cmp.Or(string(e.Number), "0"), Enabled: e.Enabled, Services: e.Services})
	}
	return out, nil
}

// Stream is one elementary stream of a service.
type Stream struct {
	Index     int    `json:"index"`
	Type      string `json:"type"` // "MPEG2VIDEO", "AC3", "EAC3", "AC4", ...
	Language  string `json:"language"`
	AudioType int    `json:"audio_type"` // 3: visual impaired commentary
}

// ServiceStreams lists a service's video, audio and subtitle streams, as
// last seen by Tvheadend, without tuning it.
func (c *Client) ServiceStreams(ctx context.Context, serviceUUID string) ([]Stream, error) {
	var body struct {
		Streams []Stream `json:"streams"`
	}
	if err := c.get(ctx, "/api/service/streams", url.Values{"uuid": {serviceUUID}}, &body); err != nil {
		return nil, err
	}
	out := body.Streams[:0]
	for _, s := range body.Streams {
		if s.Index > 0 { // PCR and PMT entries have no index
			out = append(out, s)
		}
	}
	return out, nil
}

// gridNumber is a channel number as the channel grid writes it: a packed
// integer (7000001), or for a real ATSC channel a string ("7.1").
type gridNumber string

func (n *gridNumber) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		*n = gridNumber(strings.TrimSpace(s))
		return nil
	}
	var packed int64
	if err := json.Unmarshal(raw, &packed); err != nil {
		return fmt.Errorf("channel number %s: %w", raw, err)
	}
	*n = gridNumber(FormatNumber(packed))
	return nil
}

// FormatNumber renders a packed channel number: 7000001 → "7.1", 27 → "27".
func FormatNumber(n int64) string {
	major, minor := n/numberSplit, n%numberSplit
	if n < numberSplit {
		return strconv.FormatInt(n, 10)
	}
	if minor == 0 {
		return strconv.FormatInt(major, 10)
	}
	return fmt.Sprintf("%d.%d", major, minor)
}

// PackNumber is the inverse of FormatNumber.
func PackNumber(major, minor int) int64 {
	return int64(major)*numberSplit + int64(minor)
}

// StreamURL is the raw MPEG-TS stream of a channel.
func (c *Client) StreamURL(channelUUID string) string {
	return c.Base + "/stream/channel/" + channelUUID + "?profile=pass"
}

// FileURL is the recorded file of a DVR entry.
func (c *Client) FileURL(entryUUID string) string {
	return c.Base + "/dvrfile/" + entryUUID
}

// Entry is a DVR entry, scheduled, recording or finished.
type Entry struct {
	UUID        string `json:"uuid"`
	Title       string `json:"disp_title"`
	Subtitle    string `json:"disp_subtitle"`
	Description string `json:"disp_description"`
	Channel     string `json:"channel"`
	ChannelName string `json:"channelname"`
	Start       int64  `json:"start"`
	Stop        int64  `json:"stop"`
	StartReal   int64  `json:"start_real"`
	StopReal    int64  `json:"stop_real"`
	// SchedStatus is "scheduled", "recording", "completed",
	// "completedError" and so on.
	SchedStatus string `json:"sched_status"`
	Status      string `json:"status"` // human readable
	Comment     string `json:"comment"`
	FileSize    int64  `json:"filesize"`
	ErrorCode   int    `json:"errorcode"`
}

// NewEntry schedules a recording by time.
type NewEntry struct {
	Channel     string // channel UUID
	Start, Stop int64  // unix seconds
	Title       string
	Subtitle    string
	Description string
	Comment     string
	// Padding in minutes.
	StartExtra, StopExtra int
}

// Upcoming lists scheduled and in-progress recordings.
func (c *Client) Upcoming(ctx context.Context) ([]Entry, error) {
	return c.entries(ctx, "grid_upcoming")
}

// Finished lists completed recordings.
func (c *Client) Finished(ctx context.Context) ([]Entry, error) {
	return c.entries(ctx, "grid_finished")
}

// Failed lists recordings that did not complete.
func (c *Client) Failed(ctx context.Context) ([]Entry, error) {
	return c.entries(ctx, "grid_failed")
}

func (c *Client) entries(ctx context.Context, grid string) ([]Entry, error) {
	var body struct {
		Entries []Entry `json:"entries"`
	}
	err := c.get(ctx, "/api/dvr/entry/"+grid, url.Values{"limit": {"5000"}}, &body)
	return body.Entries, err
}

// CreateEntry schedules a recording and returns its UUID.
func (c *Client) CreateEntry(ctx context.Context, e NewEntry) (string, error) {
	conf := map[string]any{
		"channel":     e.Channel,
		"start":       e.Start,
		"stop":        e.Stop,
		"title":       map[string]string{"eng": e.Title},
		"subtitle":    map[string]string{"eng": e.Subtitle},
		"description": map[string]string{"eng": e.Description},
		"comment":     e.Comment,
		"start_extra": e.StartExtra,
		"stop_extra":  e.StopExtra,
	}
	var out struct {
		UUID string `json:"uuid"`
	}
	if err := c.post(ctx, "/api/dvr/entry/create", form("conf", conf), &out); err != nil {
		return "", err
	}
	if out.UUID == "" {
		return "", fmt.Errorf("tvheadend did not create the recording %q", e.Title)
	}
	return out.UUID, nil
}

// DeleteScheduled removes a recording that has not started.
func (c *Client) DeleteScheduled(ctx context.Context, uuid string) error {
	return c.post(ctx, "/api/idnode/delete", url.Values{"uuid": {uuid}}, nil)
}

// Cancel stops a recording in progress and discards it.
func (c *Client) Cancel(ctx context.Context, uuid string) error {
	return c.post(ctx, "/api/dvr/entry/cancel", url.Values{"uuid": {uuid}}, nil)
}

// Remove deletes a finished or failed recording and its file.
func (c *Client) Remove(ctx context.Context, uuid string) error {
	return c.post(ctx, "/api/dvr/entry/remove", url.Values{"uuid": {uuid}}, nil)
}

func form(key string, v any) url.Values {
	b, _ := json.Marshal(v)
	return url.Values{key: {string(b)}}
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, strings.NewReader(body.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("tvheadend: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("tvheadend: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tvheadend %s: HTTP %d: %s", req.URL.Path, resp.StatusCode, bytes.TrimSpace(raw[:min(len(raw), 200)]))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("tvheadend %s: %w", req.URL.Path, err)
	}
	return nil
}
