package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"airwaves/internal/dvr"
	"airwaves/internal/service"
	"airwaves/internal/stream"
	"airwaves/internal/weather"
)

// Client is a Backend backed by a remote airwavesd.
type Client struct {
	Base  string // e.g. http://nas:8089
	Token string
	HTTP  *http.Client
}

// NewClient returns a client for base. A bare host ("nas") gets http://
// and the default port; full URLs are used as given.
func NewClient(base, token string) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
		if u, err := url.Parse(base); err == nil && u.Port() == "" {
			u.Host += ":" + strconv.Itoa(DefaultPort)
			base = u.String()
		}
	}
	// Generous timeout: a first snapshot on a fresh server samples terrain
	// and downloads listings.
	return &Client{Base: base, Token: token, HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

// DefaultPort is where airwavesd listens.
const DefaultPort = 8089

// Info implements Backend.
func (c *Client) Info(ctx context.Context) (service.Info, error) {
	var i service.Info
	return i, c.call(ctx, http.MethodGet, "/api/info", nil, &i)
}

// Config implements Backend.
func (c *Client) Config(ctx context.Context) (service.Config, error) {
	var cfg service.Config
	return cfg, c.call(ctx, http.MethodGet, "/api/config", nil, &cfg)
}

// SetConfig implements Backend.
func (c *Client) SetConfig(ctx context.Context, cfg service.Config) (service.Config, error) {
	var out service.Config
	return out, c.call(ctx, http.MethodPut, "/api/config", cfg, &out)
}

// Snapshot implements Backend.
func (c *Client) Snapshot(ctx context.Context, refresh bool) (*service.Snapshot, error) {
	p := "/api/snapshot"
	if refresh {
		p += "?refresh=1"
	}
	var s service.Snapshot
	return &s, c.call(ctx, http.MethodGet, p, nil, &s)
}

// Signal implements Backend.
func (c *Client) Signal(ctx context.Context) (*service.SignalReport, error) {
	var r service.SignalReport
	return &r, c.call(ctx, http.MethodGet, "/api/signal", nil, &r)
}

// Measure implements Backend.
func (c *Client) Measure(ctx context.Context) (*service.SweepStatus, error) {
	var st service.SweepStatus
	return &st, c.call(ctx, http.MethodPost, "/api/signal/measure", nil, &st)
}

// Weather implements Backend.
func (c *Client) Weather(ctx context.Context) (*weather.Report, error) {
	var r weather.Report
	return &r, c.call(ctx, http.MethodGet, "/api/weather", nil, &r)
}

// Tune implements Backend.
func (c *Client) Tune(ctx context.Context, client, number string) (*stream.Playback, error) {
	var pb stream.Playback
	if err := c.call(ctx, http.MethodPost, "/api/tune", clientReq{Client: client, Number: number}, &pb); err != nil {
		return nil, err
	}
	pb.URL = c.Base + pb.Path
	return &pb, nil
}

// Stop implements Backend.
func (c *Client) Stop(ctx context.Context, client string) error {
	return c.call(ctx, http.MethodPost, "/api/stop", clientReq{Client: client}, nil)
}

// DVR implements Backend.
func (c *Client) DVR(ctx context.Context) (*dvr.State, error) {
	var s dvr.State
	return &s, c.call(ctx, http.MethodGet, "/api/dvr", nil, &s)
}

// Record implements Backend.
func (c *Client) Record(ctx context.Context, req dvr.Request) (*dvr.Rule, error) {
	var r dvr.Rule
	return &r, c.call(ctx, http.MethodPost, "/api/dvr/record", req, &r)
}

// DeleteRule implements Backend.
func (c *Client) DeleteRule(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/api/dvr/rules/"+url.PathEscape(id), nil, nil)
}

// DeleteRecording implements Backend.
func (c *Client) DeleteRecording(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/api/dvr/recordings/"+url.PathEscape(id), nil, nil)
}

// PlayRecording implements Backend.
func (c *Client) PlayRecording(ctx context.Context, client, id string, from float64) (*stream.Playback, error) {
	var pb stream.Playback
	if err := c.call(ctx, http.MethodPost, "/api/dvr/play", clientReq{Client: client, ID: id, From: from}, &pb); err != nil {
		return nil, err
	}
	pb.URL = c.Base + pb.Path
	return &pb, nil
}

// SaveProgress implements Backend.
func (c *Client) SaveProgress(ctx context.Context, id string, position, duration float64) error {
	return c.call(ctx, http.MethodPost, "/api/dvr/progress", progressReq{ID: id, Position: position, Duration: duration}, nil)
}

// MarkWatched implements Backend.
func (c *Client) MarkWatched(ctx context.Context, id string, watched bool) error {
	return c.call(ctx, http.MethodPost, "/api/dvr/progress", progressReq{ID: id, Watched: &watched}, nil)
}

// UpdateRule implements Backend.
func (c *Client) UpdateRule(ctx context.Context, id string, u dvr.RuleUpdate) (*dvr.Rule, error) {
	var r dvr.Rule
	return &r, c.call(ctx, http.MethodPut, "/api/dvr/rules/"+url.PathEscape(id), u, &r)
}

// SetDVRPrefs implements Backend.
func (c *Client) SetDVRPrefs(ctx context.Context, p dvr.Prefs) (dvr.Prefs, error) {
	var out dvr.Prefs
	return out, c.call(ctx, http.MethodPut, "/api/dvr/prefs", p, &out)
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("airwaves server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e errorBody
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("airwaves server: HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
