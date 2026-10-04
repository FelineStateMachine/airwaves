// Package jellyfin is a small client for a Jellyfin media server: signing
// in, browsing series, movies, collections and playlists, and the URLs that
// stream them. Airwaves uses it to build channels from a friend's library.
//
// The password and access token never appear in errors or URLs; requests
// carry the token in the Authorization header only.
package jellyfin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Account is how Airwaves signs in to a Jellyfin server: a user and
// password, or a Token, which is an API key or comes from Quick Connect.
type Account struct {
	Server   string `json:"server"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Token    string `json:"token,omitempty"`
	UserID   string `json:"userId,omitempty"`   // set by Quick Connect along with Token
	DeviceID string `json:"deviceId,omitempty"` // the device Token was issued to
}

// LoadAccount reads an account file such as <channels>/.jellyfin.json.
func LoadAccount(path string) (Account, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Account{}, err
	}
	var a Account
	if err := json.Unmarshal(raw, &a); err != nil {
		return Account{}, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

// baseURL is the server address without a trailing slash, https by default.
func (a Account) baseURL() string {
	s := strings.TrimRight(strings.TrimSpace(a.Server), "/")
	if s != "" && !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return s
}

// clientVersion is the version Airwaves reports to the server.
const clientVersion = "0.2"

// Client talks to one server as one account. Clients for the same account
// share a sign-in, so several channels on one server log in once.
type Client struct {
	s  *session
	hc *http.Client
}

// session is a sign-in shared by the clients of one account.
type session struct {
	acct     Account
	server   string // acct.baseURL()
	deviceID string

	mu       sync.Mutex
	token    string // the API key, or the access token from signing in
	userID   string
	resolved bool // userID is known; API keys may have no user
	// refused is set when the server turned the password down. Signing in
	// isn't tried again until Login or Retry, since repeated failures lock
	// a Jellyfin account.
	refused   error
	refusedAt time.Time
}

var sessions struct {
	sync.Mutex
	m map[string]*session
}

// defaultHTTP bounds each request, so a stalled server can't hold a
// sign-in, which other clients of the account wait on, for long.
var defaultHTTP = &http.Client{Timeout: time.Minute}

// New returns a client for acct. deviceID identifies Airwaves to the server
// and should stay the same across restarts; acct.DeviceID takes its place
// when set, and clients sharing a sign-in use the first one's. hc may be
// nil.
func New(acct Account, deviceID string, hc *http.Client) *Client {
	if hc == nil {
		hc = defaultHTTP
	}
	sum := sha256.Sum256([]byte(acct.Password + "\x00" + acct.Token))
	key := acct.baseURL() + "\x00" + strings.ToLower(acct.User) + "\x00" + hex.EncodeToString(sum[:])
	sessions.Lock()
	defer sessions.Unlock()
	if sessions.m == nil {
		sessions.m = map[string]*session{}
	}
	s := sessions.m[key]
	if s == nil {
		if acct.DeviceID != "" {
			deviceID = acct.DeviceID
		}
		s = &session{acct: acct, server: acct.baseURL(), deviceID: strings.ReplaceAll(deviceID, `"`, "")}
		sessions.m[key] = s
	}
	return &Client{s: s, hc: hc}
}

// Server is the server's base URL.
func (c *Client) Server() string { return c.s.server }

// Login checks the account works, signing in again even if the server
// refused the password before.
func (c *Client) Login(ctx context.Context) error {
	c.s.mu.Lock()
	c.s.refused = nil
	c.s.mu.Unlock()
	_, err := c.Libraries(ctx)
	return err
}

// Retry allows signing in again after the server refused the password, if
// the account's details have changed since: changed is when they last did.
func (c *Client) Retry(changed time.Time) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.refused != nil && changed.After(c.s.refusedAt) {
		c.s.refused = nil
	}
}

// Authorization is the Authorization header value for requests made
// outside the client, such as ffmpeg reading a stream. It holds the token:
// never log it.
func (c *Client) Authorization() string {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	return c.s.header(c.s.token)
}

func (s *session) header(token string) string {
	h := `MediaBrowser Client="Airwaves", Device="airwavesd"`
	if s.deviceID != "" {
		h += fmt.Sprintf(`, DeviceId="%s"`, s.deviceID)
	}
	h += fmt.Sprintf(`, Version="%s"`, clientVersion)
	if token != "" {
		h += fmt.Sprintf(`, Token="%s"`, token)
	}
	return h
}

// signIn returns the access token and user, signing in when there is no
// token yet.
func (c *Client) signIn(ctx context.Context) (token, userID string, err error) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.server == "":
		return "", "", fmt.Errorf("jellyfin: no server")
	case s.acct.Token == "" && s.acct.User == "":
		return "", "", fmt.Errorf("jellyfin: %s: no user or token", s.server)
	case s.refused != nil:
		return "", "", s.refused
	}
	if s.acct.Token != "" {
		s.token = s.acct.Token
		if !s.resolved {
			s.userID = s.acct.UserID
			if s.userID == "" {
				s.userID = c.tokenUser(ctx)
			}
			s.resolved = true
		}
		return s.token, s.userID, nil
	}
	if s.token != "" {
		return s.token, s.userID, nil
	}
	var auth struct {
		AccessToken string
		User        struct {
			ID string `json:"Id"`
		}
	}
	body := map[string]string{"Username": s.acct.User, "Pw": s.acct.Password}
	status, err := c.do(ctx, http.MethodPost, "/Users/AuthenticateByName", nil, body, "", &auth)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		s.refused = fmt.Errorf("jellyfin: %s refused the password for %q", s.server, s.acct.User)
		s.refusedAt = time.Now()
		return "", "", s.refused
	}
	if err != nil {
		return "", "", err
	}
	if auth.AccessToken == "" {
		return "", "", fmt.Errorf("jellyfin: %s: signing in gave no token", s.server)
	}
	s.token, s.userID, s.resolved = auth.AccessToken, auth.User.ID, true
	return s.token, s.userID, nil
}

// tokenUser finds the user a token acts as: its own user, else the
// account's user by name, else none (an API key sees every library). s.mu
// is held.
func (c *Client) tokenUser(ctx context.Context) string {
	var me struct {
		ID string `json:"Id"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/Users/Me", nil, nil, c.s.token, &me); err == nil && me.ID != "" {
		return me.ID
	}
	if c.s.acct.User == "" {
		return ""
	}
	var users []struct {
		ID   string `json:"Id"`
		Name string
	}
	if _, err := c.do(ctx, http.MethodGet, "/Users", nil, nil, c.s.token, &users); err == nil {
		for _, u := range users {
			if strings.EqualFold(u.Name, c.s.acct.User) {
				return u.ID
			}
		}
	}
	return ""
}

// expire drops token after the server rejected it, unless another request
// has already replaced it.
func (s *session) expire(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token && s.acct.Token == "" {
		s.token = ""
	}
}

// get fetches path as JSON into out, signed in. A rejected access token is
// renewed once, by signing in again.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	for retried := false; ; retried = true {
		token, userID, err := c.signIn(ctx)
		if err != nil {
			return err
		}
		if userID != "" && q != nil && !q.Has("userId") {
			q.Set("userId", userID)
		}
		status, err := c.do(ctx, http.MethodGet, path, q, nil, token, out)
		if status == http.StatusUnauthorized && !retried && c.s.acct.Token == "" {
			c.s.expire(token)
			continue
		}
		if status == http.StatusUnauthorized && c.s.acct.Token != "" {
			return fmt.Errorf("jellyfin: %s refused the token", c.s.server)
		}
		return err
	}
}

// fetch gets path, signed in, returning at most limit bytes of it. A
// rejected access token is renewed once, as get does.
func (c *Client) fetch(ctx context.Context, path string, limit int64) ([]byte, error) {
	for retried := false; ; retried = true {
		token, _, err := c.signIn(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := c.request(ctx, http.MethodGet, path, nil, nil, token)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized && !retried && c.s.acct.Token == "":
			c.s.expire(token)
			continue
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("jellyfin: GET %s: %s", path, resp.Status)
		case err != nil:
			return nil, fmt.Errorf("jellyfin: GET %s: %w", path, err)
		}
		return b, nil
	}
}

// do makes one request, decoding a JSON response into out, and returns the
// HTTP status (0 if there was none).
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, token string, out any) (int, error) {
	resp, err := c.request(ctx, method, path, q, body, token)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, fmt.Errorf("jellyfin: %s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("jellyfin: %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) request(ctx context.Context, method, path string, q url.Values, body any, token string) (*http.Response, error) {
	u := c.s.server + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	var resp *http.Response
	if err == nil {
		req.Header.Set("Authorization", c.s.header(token))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err = c.hc.Do(req)
	}
	if uerr := (*url.Error)(nil); errors.As(err, &uerr) {
		// Leave the query, which can hold a Quick Connect secret, out.
		return nil, fmt.Errorf("jellyfin: %s %s: %w", method, path, uerr.Err)
	}
	return resp, err
}
