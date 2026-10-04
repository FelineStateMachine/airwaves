// Package web holds the shared HTTP client used for public data sources.
package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// UserAgent identifies requests. Some listing endpoints reject the Go
// default, so present as a desktop browser.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

type uaTransport struct{ next http.RoundTripper }

func (t uaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("User-Agent", UserAgent)
	}
	return t.next.RoundTrip(r)
}

// NewClient returns a client with a browser User-Agent and a sane timeout.
func NewClient() *http.Client {
	return &http.Client{
		Timeout:   45 * time.Second,
		Transport: uaTransport{next: http.DefaultTransport},
	}
}

// Get fetches url and returns the body, failing on non-200 responses.
func Get(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}
