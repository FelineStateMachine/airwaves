package tuner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/tvh"
)

// Tvheadend streams channels from a Tvheadend server, which owns the
// physical tuners (and, in demo mode before one is attached, the demo
// channels).
type Tvheadend struct {
	tvh *tvh.Client
	// Networks names the Tvheadend networks whose channels are offered;
	// channels of any other network (anything not Airwaves') aren't. Nil
	// offers the antenna network's.
	Networks func() []string

	mu      sync.Mutex
	lineup  *tvh.Lineup
	fetched time.Time
}

// NewTvheadend wraps a Tvheadend client.
func NewTvheadend(c *tvh.Client) *Tvheadend { return &Tvheadend{tvh: c} }

// Device implements Tuner.
func (t *Tvheadend) Device() Device {
	return Device{ID: "tvheadend", Name: "Tvheadend", Kind: "tvheadend", BaseURL: t.tvh.Base, Detail: "Tuners and recordings on the server"}
}

// Channel is a channel Tvheadend offers, with the service and multiplex
// it plays from.
type Channel struct {
	tvh.Channel
	Service tvh.Service
	Mux     tvh.Mux
}

// Input implements Tuner.
func (t *Tvheadend) Input(ctx context.Context, number string) (Input, error) {
	ch, err := t.Channel(ctx, number)
	if err != nil {
		return Input{}, err
	}
	in := Input{
		Args:      []string{"-fflags", "+genpts", "-i", t.tvh.StreamURL(ch.UUID)},
		Video:     "0:v:0",
		Audio:     []AudioTrack{{Map: "0:a:0"}},
		Broadcast: true,
	}
	if streams, err := t.tvh.ServiceStreams(ctx, ch.Service.UUID); err == nil {
		if audio, note := AudioTracks(streams); audio != nil || note != "" {
			in.Audio, in.Note = audio, note
		}
	}
	return in, nil
}

// AudioTracks turns a service's streams into playable audio tracks, in
// broadcast order (main track first). AC-4 (ATSC 3.0) cannot be decoded by
// ffmpeg and is skipped, keeping stream numbering intact.
func AudioTracks(streams []tvh.Stream) ([]AudioTrack, string) {
	var out []AudioTrack
	n, skipped := 0, false
	for _, s := range streams {
		switch s.Type {
		case "AC3", "EAC3", "MPEG2AUDIO", "AAC", "MP4A", "AC4":
		default:
			continue
		}
		if s.Type == "AC4" {
			skipped = true
		} else {
			out = append(out, AudioTrack{Map: fmt.Sprintf("0:a:%d", n), Lang: s.Language, Described: s.AudioType == 3})
		}
		n++
	}
	if len(out) == 0 && skipped {
		return nil, "ATSC 3.0 AC-4 audio cannot be decoded; video only"
	}
	return out, ""
}

// ChannelUUID maps a virtual channel number to a Tvheadend channel.
func (t *Tvheadend) ChannelUUID(ctx context.Context, number string) (string, error) {
	ch, err := t.Channel(ctx, number)
	if err != nil {
		return "", err
	}
	return ch.UUID, nil
}

// ErrNotFound is returned for a number that isn't among the channels
// Tvheadend offers.
var ErrNotFound = errors.New("not a channel the tuner found")

// Channel finds an offered channel by number.
func (t *Tvheadend) Channel(ctx context.Context, number string) (Channel, error) {
	chans, err := t.Channels(ctx)
	if err != nil {
		return Channel{}, err
	}
	for _, c := range chans {
		if c.Number == number {
			return c, nil
		}
	}
	return Channel{}, fmt.Errorf("channel %s: %w", number, ErrNotFound)
}

// Channels lists the channels Tvheadend offers, by number: enabled,
// numbered and mapped from a service of a multiplex in one of the offered
// networks. Of two with one number, the first stays.
func (t *Tvheadend) Channels(ctx context.Context) ([]Channel, error) {
	l, err := t.Lineup(ctx)
	if err != nil {
		return nil, err
	}
	nets := []string{tvh.ATSCNetwork}
	if t.Networks != nil {
		nets = t.Networks()
	}
	var out []Channel
	seen := map[string]bool{}
	for _, c := range l.Channels {
		if !c.Enabled || c.Number == "0" || len(c.Services) == 0 || seen[c.Number] {
			continue
		}
		svc, mux, ok := l.Source(c)
		if !ok || !slices.Contains(nets, mux.Network) {
			continue
		}
		seen[c.Number] = true
		out = append(out, Channel{Channel: c, Service: *svc, Mux: *mux})
	}
	slices.SortStableFunc(out, func(a, b Channel) int { return guide.CompareNumbers(a.Number, b.Number) })
	return out, nil
}

// Lineup returns Tvheadend's networks, multiplexes, services and channels,
// fetched at most once a minute.
func (t *Tvheadend) Lineup(ctx context.Context) (*tvh.Lineup, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lineup != nil && time.Since(t.fetched) < time.Minute {
		return t.lineup, nil
	}
	l, err := t.tvh.Lineup(ctx)
	if err != nil {
		return nil, err
	}
	t.lineup, t.fetched = l, time.Now()
	return l, nil
}

// Invalidate drops the cached lineup.
func (t *Tvheadend) Invalidate() {
	t.mu.Lock()
	t.lineup = nil
	t.mu.Unlock()
}
