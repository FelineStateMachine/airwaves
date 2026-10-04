package tvh

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// InputStatus is a tuner input as /api/status/inputs reports it. Tvheadend
// 4.3 lists idle inputs too, with no Stream, no subscriptions and weight 0.
type InputStatus struct {
	UUID string `json:"uuid"`
	// Input is the input's name, as Frontend.Name has it.
	Input string `json:"input"`
	// Stream is what the input is tuned to: "605MHz in Airwaves antenna";
	// empty while idle.
	Stream string `json:"stream"`
	Subs   int    `json:"subs"`
	Weight int    `json:"weight"`
	// Signal and SNR are in the units their scales say (see Scale).
	Signal      int64 `json:"signal"`
	SignalScale Scale `json:"signal_scale"`
	SNR         int64 `json:"snr"`
	SNRScale    Scale `json:"snr_scale"`
	// BER and UNC are the driver's own counters, 0 where it has none.
	BER int64 `json:"ber"`
	UNC int64 `json:"unc"`
	// BPS is the data rate the input receives, in bits per second: above
	// zero only while the tuner holds a lock, though a locked input with
	// little to send (a guide scan, a measurement) can read 0 for seconds
	// at a time.
	BPS int64 `json:"bps"`
	// TE and CC count transport and continuity errors in what arrived.
	TE int64 `json:"te"`
	CC int64 `json:"cc"`
	// ECBit/TCBit (bit errors of bits counted) and ECBlock/TCBlock
	// (uncorrected blocks of blocks counted) are filled in by tuners that
	// count them; TCBit and TCBlock are 0 otherwise.
	ECBit   int64 `json:"ec_bit"`
	TCBit   int64 `json:"tc_bit"`
	ECBlock int64 `json:"ec_block"`
	TCBlock int64 `json:"tc_block"`
}

// Scale is how a tuner reports signal strength or SNR.
type Scale int

// Scales, as Tvheadend numbers them.
const (
	ScaleUnknown  Scale = 0 // not reported
	ScaleRelative Scale = 1 // 0 to 65535 for 0 to 100%
	ScaleDecibel  Scale = 2 // thousandths of a dB (dBm for strength)
)

// InUse reports whether something subscribes to the input. An idle input,
// or one Tvheadend keeps tuned for a moment after its last subscription
// ended, can be had by any subscription.
func (in InputStatus) InUse() bool { return in.Subs > 0 }

// Mux names the multiplex and network the input is tuned to, from Stream;
// ok is false for an input that isn't on a multiplex.
func (in InputStatus) Mux() (mux, network string, ok bool) {
	i := strings.LastIndex(in.Stream, " in ")
	if i <= 0 {
		return "", "", false
	}
	return in.Stream[:i], in.Stream[i+len(" in "):], true
}

// Inputs lists the tuner inputs in use, with their signal.
func (c *Client) Inputs(ctx context.Context) ([]InputStatus, error) {
	var body struct {
		Entries []InputStatus `json:"entries"`
	}
	if err := c.get(ctx, "/api/status/inputs", nil, &body); err != nil {
		return nil, err
	}
	return body.Entries, nil
}

// Subscription is something using a tuner (or another source) now: a
// viewer, a recording, or Tvheadend's own guide and channel scans.
type Subscription struct {
	ID    int64 `json:"id"`
	Start int64 `json:"start"`
	// State is "Testing" while Tvheadend waits for data (a tuner that
	// can't lock stays there), "Running" once data arrives, "Bad" when
	// the service failed.
	State   string `json:"state"`
	Title   string `json:"title"`   // "epggrab", "DVR: ...", "HTTP"
	Service string `json:"service"` // "<input>/<network>/<mux>/<service>"
	Errors  int    `json:"errors"`
	// TotalIn counts the bytes it has received.
	TotalIn int64 `json:"total_in"`
}

// On reports whether the subscription is to the multiplex mux of network
// on input.
func (s Subscription) On(input, network, mux string) bool {
	return strings.HasPrefix(s.Service, input+"/"+network+"/"+mux+"/")
}

// Running reports whether data has arrived for it: the tuner locked.
func (s Subscription) Running() bool { return s.State == "Running" }

// Subscriptions lists what is using Tvheadend's inputs now.
func (c *Client) Subscriptions(ctx context.Context) ([]Subscription, error) {
	var body struct {
		Entries []Subscription `json:"entries"`
	}
	if err := c.get(ctx, "/api/status/subscriptions", nil, &body); err != nil {
		return nil, err
	}
	return body.Entries, nil
}

// Lineup is Tvheadend's channels with the services and multiplexes they
// play from.
type Lineup struct {
	Networks []Network
	Muxes    []Mux
	Services []Service
	Channels []Channel
}

// Lineup fetches the networks, multiplexes, services and channels.
func (c *Client) Lineup(ctx context.Context) (*Lineup, error) {
	var l Lineup
	var err error
	if l.Networks, err = c.Networks(ctx); err != nil {
		return nil, err
	}
	if l.Muxes, err = c.Muxes(ctx); err != nil {
		return nil, err
	}
	if l.Services, err = c.Services(ctx); err != nil {
		return nil, err
	}
	if l.Channels, err = c.Channels(ctx); err != nil {
		return nil, err
	}
	return &l, nil
}

// Source finds the service a channel plays from, its first one, and the
// multiplex that carries it.
func (l *Lineup) Source(ch Channel) (*Service, *Mux, bool) {
	for _, id := range ch.Services {
		for i := range l.Services {
			if l.Services[i].UUID != id {
				continue
			}
			s := &l.Services[i]
			for j := range l.Muxes {
				if l.Muxes[j].UUID == s.MultiplexUUID {
					return s, &l.Muxes[j], true
				}
			}
			return s, nil, false
		}
	}
	return nil, nil, false
}

// NetworkMuxes lists the multiplexes of the named network.
func (l *Lineup) NetworkMuxes(name string) []Mux {
	var out []Mux
	for _, m := range l.Muxes {
		if m.Network == name {
			out = append(out, m)
		}
	}
	return out
}

// HasNetwork reports whether the named network exists.
func (l *Lineup) HasNetwork(name string) bool {
	for _, n := range l.Networks {
		if n.Name == name {
			return true
		}
	}
	return false
}

// OpenMux subscribes to a multiplex as a raw stream of its PAT (PID 0)
// only, at weight: the tuner tunes and locks if it can, while hardly any
// data flows. The subscription lasts until the body is closed. A
// subscription of higher weight (a viewer, a recording) takes the tuner
// over; weight 10 is the lowest a user's can have, above Tvheadend's own
// scans (1 to 7).
func (c *Client) OpenMux(ctx context.Context, muxUUID string, weight int) (io.ReadCloser, error) {
	q := url.Values{"pids": {"0"}, "weight": {strconv.Itoa(weight)}}
	return c.open(ctx, "/stream/mux/"+url.PathEscape(muxUUID)+"?"+q.Encode())
}

// OpenChannel subscribes to a channel at weight, as OpenMux does, for
// servers that don't stream whole multiplexes.
func (c *Client) OpenChannel(ctx context.Context, channelUUID string, weight int) (io.ReadCloser, error) {
	q := url.Values{"profile": {"pass"}, "weight": {strconv.Itoa(weight)}}
	return c.open(ctx, "/stream/channel/"+url.PathEscape(channelUUID)+"?"+q.Encode())
}

// StreamError is a stream Tvheadend refused.
type StreamError struct {
	Path   string
	Status int
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("tvheadend %s: HTTP %d", e.Path, e.Status)
}

func (c *Client) open(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return nil, err
	}
	// The client's timeout would end a stream; ctx ends it instead.
	hc := *c.HTTP
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tvheadend: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		p, _, _ := strings.Cut(path, "?")
		return nil, &StreamError{Path: p, Status: resp.StatusCode}
	}
	return resp.Body, nil
}
