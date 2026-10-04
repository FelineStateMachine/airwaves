package tvh

import (
	"cmp"
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Network names Airwaves manages. Anything else in Tvheadend is left alone.
const (
	DemoNetwork = "Airwaves demo"
	ATSCNetwork = "Airwaves antenna"
)

// Network is a Tvheadend mux network.
type Network struct {
	UUID   string `json:"uuid"`
	Name   string `json:"networkname"`
	NumMux int    `json:"num_mux"`
	NumSvc int    `json:"num_svc"`
	NumChn int    `json:"num_chn"`
}

// Networks lists all networks.
func (c *Client) Networks(ctx context.Context) ([]Network, error) {
	var body struct {
		Entries []Network `json:"entries"`
	}
	err := c.get(ctx, "/api/mpegts/network/grid", url.Values{"limit": {"500"}}, &body)
	return body.Entries, err
}

func (c *Client) network(ctx context.Context, name string) (*Network, error) {
	nets, err := c.Networks(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nets {
		if nets[i].Name == name {
			return &nets[i], nil
		}
	}
	return nil, nil
}

// Mux is a multiplex: what one RF channel (or one IPTV source) carries.
type Mux struct {
	UUID        string   `json:"uuid"`
	Name        string   `json:"name"` // "605MHz"
	Network     string   `json:"network"`
	NetworkUUID string   `json:"network_uuid"`
	Enabled     flexBool `json:"enabled"`
	// FrequencyHz is an antenna multiplex's center frequency; 0 for IPTV.
	FrequencyHz int64 `json:"frequency"`
	// ScanState is 0 when idle, 1 while queued and 2 while Tvheadend scans
	// the multiplex.
	ScanState  int        `json:"scan_state"`
	ScanResult ScanResult `json:"scan_result"`
	// ScanLast is when a scan last found the multiplex (unix seconds); 0
	// when none has.
	ScanLast int64  `json:"scan_last"`
	NumSvc   int    `json:"num_svc"`
	NumChn   int    `json:"num_chn"`
	URL      string `json:"iptv_url"`
}

// ScanResult is how Tvheadend's last scan of a multiplex went.
type ScanResult int

// Scan results, as Tvheadend numbers them.
const (
	ScanNone    ScanResult = 0 // not scanned yet
	ScanOK      ScanResult = 1 // locked, and found the services
	ScanFail    ScanResult = 2 // no lock, or nothing found
	ScanPartial ScanResult = 3 // locked, with some tables missing
	ScanIgnore  ScanResult = 4
)

// Scanning reports whether Tvheadend is scanning the multiplex, or about to.
func (m Mux) Scanning() bool { return m.ScanState != 0 }

// Muxes lists every multiplex.
func (c *Client) Muxes(ctx context.Context) ([]Mux, error) {
	var body struct {
		Entries []Mux `json:"entries"`
	}
	if err := c.get(ctx, "/api/mpegts/mux/grid", url.Values{"limit": {"5000"}}, &body); err != nil {
		return nil, err
	}
	return body.Entries, nil
}

func (c *Client) muxes(ctx context.Context, networkUUID string) ([]Mux, error) {
	all, err := c.Muxes(ctx)
	if err != nil {
		return nil, err
	}
	var out []Mux
	for _, m := range all {
		if m.NetworkUUID == networkUUID {
			out = append(out, m)
		}
	}
	return out, nil
}

// DemoChannel is one generated channel in the demo network.
type DemoChannel struct {
	Major, Minor int
	Name         string // no spaces; becomes the service and channel name
	// Command is the ffmpeg command line producing MPEG-TS on stdout.
	// Tvheadend splits it on spaces, so arguments must not contain any.
	Command string
}

// EnsureDemo makes the demo IPTV network hold exactly the given channels.
// New muxes are scanned by Tvheadend in the background; MapServices picks
// them up once scanned.
func (c *Client) EnsureDemo(ctx context.Context, chans []DemoChannel) error {
	net, err := c.network(ctx, DemoNetwork)
	if err != nil {
		return err
	}
	if net == nil {
		var out struct {
			UUID string `json:"uuid"`
		}
		conf := map[string]any{"networkname": DemoNetwork, "max_streams": 6, "skipinitscan": false}
		body := form("conf", conf)
		body.Set("class", "iptv_network")
		if err := c.post(ctx, "/api/mpegts/network/create", body, &out); err != nil {
			return fmt.Errorf("create demo network: %w", err)
		}
		net = &Network{UUID: out.UUID, Name: DemoNetwork}
	}
	existing, err := c.muxes(ctx, net.UUID)
	if err != nil {
		return err
	}
	want := make(map[string]DemoChannel, len(chans))
	for _, ch := range chans {
		want[demoMuxName(ch)] = ch
	}
	have := make(map[string]bool, len(existing))
	for _, m := range existing {
		// Unknown channels, and ones whose command changed, are replaced.
		if ch, ok := want[m.Name]; !ok || m.URL != "pipe://"+ch.Command {
			if err := c.post(ctx, "/api/idnode/delete", url.Values{"uuid": {m.UUID}}, nil); err != nil {
				return err
			}
			continue
		}
		have[m.Name] = true
	}
	for name, ch := range want {
		if have[name] {
			continue
		}
		conf := map[string]any{
			"iptv_url":       "pipe://" + ch.Command,
			"iptv_muxname":   name,
			"iptv_sname":     ch.Name,
			"channel_number": PackNumber(ch.Major, ch.Minor),
			"iptv_respawn":   false,
			"epg":            0, // no over-the-air guide to read; avoids idle tuning
		}
		body := form("conf", conf)
		body.Set("uuid", net.UUID)
		if err := c.post(ctx, "/api/mpegts/network/mux_create", body, nil); err != nil {
			return fmt.Errorf("create demo mux %s: %w", name, err)
		}
	}
	return nil
}

func demoMuxName(ch DemoChannel) string {
	return fmt.Sprintf("Demo %d.%d %s", ch.Major, ch.Minor, ch.Name)
}

// RemoveNetwork deletes a managed network and the channels mapped from it.
func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	net, err := c.network(ctx, name)
	if err != nil || net == nil {
		return err
	}
	svcs, err := c.services(ctx, net.UUID)
	if err != nil {
		return err
	}
	for _, s := range svcs {
		for _, ch := range s.Channels() {
			_ = c.post(ctx, "/api/idnode/delete", url.Values{"uuid": {ch}}, nil)
		}
	}
	return c.post(ctx, "/api/idnode/delete", url.Values{"uuid": {net.UUID}}, nil)
}

// Service is a program in a multiplex, which Tvheadend maps to a channel.
type Service struct {
	UUID          string `json:"uuid"`
	Name          string `json:"svcname"` // as broadcast, "KTVD-HD"
	Network       string `json:"network"`
	Multiplex     string `json:"multiplex"` // the multiplex's name
	MultiplexUUID string `json:"multiplex_uuid"`
	Enabled       bool   `json:"enabled"`
	Encrypted     bool   `json:"encrypted"`
	// Major and Minor are the virtual channel the broadcast gives (PSIP).
	Major   int `json:"lcn"`
	Minor   int `json:"lcn_minor"`
	Channel any `json:"channel"` // list of channel UUIDs, or a string
}

// Channels lists the UUIDs of the channels mapped from the service.
func (s Service) Channels() []string {
	switch v := s.Channel.(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []any:
		var out []string
		for _, x := range v {
			if id, ok := x.(string); ok && id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	return nil
}

// Services lists every service.
func (c *Client) Services(ctx context.Context) ([]Service, error) {
	var body struct {
		Entries []Service `json:"entries"`
	}
	if err := c.get(ctx, "/api/mpegts/service/grid", url.Values{"limit": {"5000"}}, &body); err != nil {
		return nil, err
	}
	return body.Entries, nil
}

func (c *Client) services(ctx context.Context, networkUUID string) ([]Service, error) {
	muxes, err := c.muxes(ctx, networkUUID)
	if err != nil {
		return nil, err
	}
	inNet := make(map[string]bool, len(muxes))
	for _, m := range muxes {
		inNet[m.UUID] = true
	}
	all, err := c.Services(ctx)
	if err != nil {
		return nil, err
	}
	var out []Service
	for _, s := range all {
		if inNet[s.MultiplexUUID] {
			out = append(out, s)
		}
	}
	return out, nil
}

// MapServices turns scanned, unencrypted services in the managed networks
// into channels. It returns how many were mapped.
func (c *Client) MapServices(ctx context.Context) (int, error) {
	var todo []string
	for _, name := range []string{DemoNetwork, ATSCNetwork} {
		net, err := c.network(ctx, name)
		if err != nil {
			return 0, err
		}
		if net == nil {
			continue
		}
		svcs, err := c.services(ctx, net.UUID)
		if err != nil {
			return 0, err
		}
		for _, s := range svcs {
			if !s.Encrypted && len(s.Channels()) == 0 {
				todo = append(todo, s.UUID)
			}
		}
	}
	if len(todo) == 0 {
		return 0, nil
	}
	node := map[string]any{
		"services": todo, "encrypted": false, "merge_same_name": false, "check_availability": false,
		"type_tags": false, "provider_tags": false, "network_tags": false,
	}
	return len(todo), c.post(ctx, "/api/service/mapper/save", form("node", node), nil)
}

// Frontend is a tuner input on the server.
type Frontend struct {
	UUID  string
	Class string // "tvhdhomerun_frontend_atsc_t", "linuxdvb_frontend_atsc_t"
	// Name is how Tvheadend names the input, also in its input status:
	// "HDHomeRun ATSC-T Tuner #0 (169.254.1.2)".
	Name string
	// Model is the device's model as Tvheadend reports it
	// ("hdhomerun_dvr_atsc"), when it does.
	Model string
}

// treeNode is one entry of Tvheadend's hardware tree. Newer versions send
// each node's class and parameters along; older ones only the text.
type treeNode struct {
	UUID   string   `json:"uuid"`
	Text   string   `json:"text"`
	Class  string   `json:"class"`
	Leaf   flexBool `json:"leaf"`
	Params []struct {
		ID    string `json:"id"`
		Value any    `json:"value"`
	} `json:"params"`
}

func (n treeNode) param(id string) string {
	for _, p := range n.Params {
		if p.ID == id {
			if s, ok := p.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// Frontends finds ATSC tuner frontends: USB or PCIe tuners
// (linuxdvb_frontend_atsc_t, e.g. both halves of a Hauppauge WinTV-dualHD)
// and network HDHomeRuns (tvhdhomerun_frontend_atsc_t).
func (c *Client) Frontends(ctx context.Context) ([]Frontend, error) {
	var out []Frontend
	var walk func(uuid, model string, depth int) error
	walk = func(uuid, model string, depth int) error {
		var nodes []treeNode
		if err := c.get(ctx, "/api/hardware/tree", url.Values{"uuid": {uuid}}, &nodes); err != nil {
			return err
		}
		for _, n := range nodes {
			class := n.Class
			if class == "" {
				var err error
				if class, err = c.class(ctx, n.UUID); err != nil {
					return err
				}
			}
			m := cmp.Or(n.param("deviceModel"), model)
			if strings.Contains(class, "_frontend_atsc") {
				out = append(out, Frontend{UUID: n.UUID, Class: class, Name: cmp.Or(n.param("displayname"), n.Text), Model: m})
			}
			if !bool(n.Leaf) && depth < 3 {
				if err := walk(n.UUID, m, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return out, walk("root", "", 0)
}

// flexBool is a JSON boolean that Tvheadend sometimes writes as 0 or 1
// (the hardware tree's "leaf").
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	switch string(raw) {
	case "true", "1":
		*b = true
	case "false", "0", "null":
		*b = false
	default:
		return fmt.Errorf("%s is not a boolean", raw)
	}
	return nil
}

func (c *Client) class(ctx context.Context, uuid string) (string, error) {
	var body struct {
		Entries []struct {
			Class string `json:"class"`
		} `json:"entries"`
	}
	if err := c.get(ctx, "/api/idnode/load", url.Values{"uuid": {uuid}, "meta": {"0"}}, &body); err != nil {
		return "", err
	}
	if len(body.Entries) == 0 {
		return "", nil
	}
	return body.Entries[0].Class, nil
}

// EnsureATSC creates the antenna network from the US ATSC channel list and
// attaches every ATSC frontend to it. Tvheadend then scans every RF channel;
// MapServices adds the stations it finds. It reports whether anything
// changed.
func (c *Client) EnsureATSC(ctx context.Context, fes []Frontend) (bool, error) {
	if len(fes) == 0 {
		return false, nil
	}
	net, err := c.network(ctx, ATSCNetwork)
	if err != nil {
		return false, err
	}
	changed := false
	if net == nil {
		scanfile, err := c.usScanfile(ctx)
		if err != nil {
			return false, err
		}
		var out struct {
			UUID string `json:"uuid"`
		}
		conf := map[string]any{"networkname": ATSCNetwork, "scanfile": scanfile, "skipinitscan": false}
		body := form("conf", conf)
		body.Set("class", "dvb_network_atsc_t")
		if err := c.post(ctx, "/api/mpegts/network/create", body, &out); err != nil {
			return false, fmt.Errorf("create antenna network: %w", err)
		}
		net = &Network{UUID: out.UUID}
		changed = true
	}
	for _, fe := range fes {
		node := map[string]any{"uuid": fe.UUID, "enabled": true, "networks": []string{net.UUID}, "ota_epg": true}
		if err := c.post(ctx, "/api/idnode/save", form("node", node), nil); err != nil {
			return changed, fmt.Errorf("enable %s: %w", fe.Name, err)
		}
	}
	return changed, nil
}

// usScanfile finds the nationwide 8VSB center-frequency list. Tvheadend's
// keys are truncated, so match on the label.
func (c *Client) usScanfile(ctx context.Context) (string, error) {
	var body struct {
		Entries []struct {
			Key string `json:"key"`
			Val string `json:"val"`
		} `json:"entries"`
	}
	if err := c.get(ctx, "/api/dvb/scanfile/list", url.Values{"type": {"atsc-t"}}, &body); err != nil {
		return "", err
	}
	for _, e := range body.Entries {
		if strings.HasSuffix(e.Val, "us-ATSC-center-frequencies-8VSB") {
			return e.Key, nil
		}
	}
	return "", fmt.Errorf("tvheadend has no US ATSC scan file")
}

// RemoveOrphanChannels deletes channels left without any service, for
// example after demo channels are rebuilt. It reports how many it removed.
func (c *Client) RemoveOrphanChannels(ctx context.Context) (int, error) {
	chans, err := c.Channels(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ch := range chans {
		if len(ch.Services) > 0 {
			continue
		}
		if err := c.post(ctx, "/api/idnode/delete", url.Values{"uuid": {ch.UUID}}, nil); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
