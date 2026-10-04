package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// QuickConnect is a code sign-in in progress. Code is shown to the user,
// who enters it in a signed-in Jellyfin app; Secret stays on the server.
type QuickConnect struct{ Code, Secret string }

// anonymous is a client that hasn't signed in, for Quick Connect.
func anonymous(server, deviceID string, hc *http.Client) *Client {
	if hc == nil {
		hc = defaultHTTP
	}
	return &Client{s: &session{server: Account{Server: server}.baseURL(), deviceID: deviceID}, hc: hc}
}

// QuickConnectEnabled reports whether the server allows signing in with a
// code.
func QuickConnectEnabled(ctx context.Context, server string, hc *http.Client) (bool, error) {
	var on bool
	_, err := anonymous(server, "", hc).do(ctx, http.MethodGet, "/QuickConnect/Enabled", nil, nil, "", &on)
	return on, err
}

// StartQuickConnect starts a code sign-in. Pass the same deviceID to
// FinishQuickConnect.
func StartQuickConnect(ctx context.Context, server, deviceID string, hc *http.Client) (QuickConnect, error) {
	var qc QuickConnect
	status, err := anonymous(server, deviceID, hc).do(ctx, http.MethodPost, "/QuickConnect/Initiate", nil, nil, "", &qc)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return qc, fmt.Errorf("jellyfin: %s doesn't allow Quick Connect", server)
	}
	if err == nil && (qc.Code == "" || qc.Secret == "") {
		err = fmt.Errorf("jellyfin: %s: Quick Connect gave no code", server)
	}
	return qc, err
}

// CheckQuickConnect reports whether the user has approved the code. It
// fails once the code has expired.
func CheckQuickConnect(ctx context.Context, server, secret string, hc *http.Client) (approved bool, err error) {
	var r struct{ Authenticated bool }
	status, err := anonymous(server, "", hc).do(ctx, http.MethodGet, "/QuickConnect/Connect", url.Values{"secret": {secret}}, nil, "", &r)
	if status == http.StatusNotFound {
		return false, fmt.Errorf("jellyfin: the Quick Connect code expired")
	}
	return r.Authenticated, err
}

// FinishQuickConnect trades an approved code's secret for an account that
// signs in with a token issued to deviceID.
func FinishQuickConnect(ctx context.Context, server, secret, deviceID string, hc *http.Client) (Account, error) {
	c := anonymous(server, deviceID, hc)
	var auth struct {
		AccessToken string
		User        struct {
			ID   string `json:"Id"`
			Name string
		}
	}
	body := map[string]string{"Secret": secret}
	if _, err := c.do(ctx, http.MethodPost, "/Users/AuthenticateWithQuickConnect", nil, body, "", &auth); err != nil {
		return Account{}, err
	}
	if auth.AccessToken == "" {
		return Account{}, fmt.Errorf("jellyfin: %s: Quick Connect gave no token", server)
	}
	return Account{Server: c.s.server, User: auth.User.Name, UserID: auth.User.ID, Token: auth.AccessToken, DeviceID: deviceID}, nil
}
