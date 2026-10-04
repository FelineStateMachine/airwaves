package tuner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"airwaves/internal/tvh"
)

// Tvheadend streams channels from a Tvheadend server, which owns the
// physical tuners (and, before one is attached, the demo channels).
type Tvheadend struct {
	tvh *tvh.Client

	mu      sync.Mutex
	chans   []tvh.Channel
	fetched time.Time
}

// NewTvheadend wraps a Tvheadend client.
func NewTvheadend(c *tvh.Client) *Tvheadend { return &Tvheadend{tvh: c} }

// Device implements Tuner.
func (t *Tvheadend) Device() Device {
	return Device{ID: "tvheadend", Name: "Tvheadend", Kind: "tvheadend", BaseURL: t.tvh.Base, Detail: "Tuners and recordings on the server"}
}

// Input implements Tuner.
func (t *Tvheadend) Input(ctx context.Context, number string) (Input, error) {
	ch, err := t.channel(ctx, number)
	if err != nil {
		return Input{}, err
	}
	in := Input{
		Args:      []string{"-fflags", "+genpts", "-i", t.tvh.StreamURL(ch.UUID)},
		Video:     "0:v:0",
		Audio:     []AudioTrack{{Map: "0:a:0"}},
		Broadcast: true,
	}
	if len(ch.Services) > 0 {
		if streams, err := t.tvh.ServiceStreams(ctx, ch.Services[0]); err == nil {
			if audio, note := AudioTracks(streams); audio != nil || note != "" {
				in.Audio, in.Note = audio, note
			}
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
	ch, err := t.channel(ctx, number)
	if err != nil {
		return "", err
	}
	return ch.UUID, nil
}

func (t *Tvheadend) channel(ctx context.Context, number string) (tvh.Channel, error) {
	chans, err := t.Channels(ctx)
	if err != nil {
		return tvh.Channel{}, err
	}
	for _, c := range chans {
		if c.Number == number && c.Enabled {
			return c, nil
		}
	}
	return tvh.Channel{}, fmt.Errorf("channel %s is not available on the server", number)
}

// Channels lists Tvheadend's channels, cached for a minute.
func (t *Tvheadend) Channels(ctx context.Context) ([]tvh.Channel, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.chans != nil && time.Since(t.fetched) < time.Minute {
		return t.chans, nil
	}
	chans, err := t.tvh.Channels(ctx)
	if err != nil {
		return nil, err
	}
	t.chans, t.fetched = chans, time.Now()
	return chans, nil
}

// Invalidate drops the cached channel list.
func (t *Tvheadend) Invalidate() {
	t.mu.Lock()
	t.chans = nil
	t.mu.Unlock()
}
