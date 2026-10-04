package tvh

import (
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

type mux struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	NetworkUUID string `json:"network_uuid"`
	URL         string `json:"iptv_url"`
}

func (c *Client) muxes(ctx context.Context, networkUUID string) ([]mux, error) {
	var body struct {
		Entries []mux `json:"entries"`
	}
	if err := c.get(ctx, "/api/mpegts/mux/grid", url.Values{"limit": {"5000"}}, &body); err != nil {
		return nil, err
	}
	var out []mux
	for _, m := range body.Entries {
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
		for _, ch := range s.channels() {
			_ = c.post(ctx, "/api/idnode/delete", url.Values{"uuid": {ch}}, nil)
		}
	}
	return c.post(ctx, "/api/idnode/delete", url.Values{"uuid": {net.UUID}}, nil)
}

type service struct {
	UUID          string `json:"uuid"`
	Name          string `json:"svcname"`
	MultiplexUUID string `json:"multiplex_uuid"`
	Channel       any    `json:"channel"` // list of channel UUIDs, or a string
	Encrypted     bool   `json:"encrypted"`
}

func (s service) channels() []string {
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

func (c *Client) services(ctx context.Context, networkUUID string) ([]service, error) {
	muxes, err := c.muxes(ctx, networkUUID)
	if err != nil {
		return nil, err
	}
	inNet := make(map[string]bool, len(muxes))
	for _, m := range muxes {
		inNet[m.UUID] = true
	}
	var body struct {
		Entries []service `json:"entries"`
	}
	if err := c.get(ctx, "/api/mpegts/service/grid", url.Values{"limit": {"5000"}}, &body); err != nil {
		return nil, err
	}
	var out []service
	for _, s := range body.Entries {
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
			if !s.Encrypted && len(s.channels()) == 0 {
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
	Class string
	Name  string
}

// Frontends finds ATSC tuner frontends: USB or PCIe tuners
// (linuxdvb_frontend_atsc_t, e.g. both halves of a Hauppauge WinTV-dualHD)
// and network HDHomeRuns (tvhdhomerun_frontend_atsc_t).
func (c *Client) Frontends(ctx context.Context) ([]Frontend, error) {
	var out []Frontend
	var walk func(uuid string, depth int) error
	walk = func(uuid string, depth int) error {
		var nodes []struct {
			UUID string `json:"uuid"`
			Text string `json:"text"`
			Leaf bool   `json:"leaf"`
		}
		if err := c.get(ctx, "/api/hardware/tree", url.Values{"uuid": {uuid}}, &nodes); err != nil {
			return err
		}
		for _, n := range nodes {
			class, err := c.class(ctx, n.UUID)
			if err != nil {
				return err
			}
			if strings.Contains(class, "_frontend_atsc") {
				out = append(out, Frontend{UUID: n.UUID, Class: class, Name: n.Text})
			}
			if !n.Leaf && depth < 3 {
				if err := walk(n.UUID, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return out, walk("root", 0)
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
