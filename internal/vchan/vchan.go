// Package vchan provides custom channels: streams with a guide schedule
// but no tuner behind them, such as the Airwaves Weather display. They only
// do work while someone is watching.
package vchan

import (
	"context"
	"io"
	"time"
)

// Program is one scheduled item in a custom channel's guide.
type Program struct {
	Start       time.Time
	End         time.Time
	Title       string
	Subtitle    string
	Description string
	Category    string
	Season      string
	Episode     string
	Image       string // URL
	New         bool   // a first run
	// Date is when it was made, as XMLTV writes it: "2014", "202603" or
	// "20140304".
	Date string
	// AudioLang is the sound's language as tagged ("eng", "jpn"), "" when
	// unknown, which is taken for English.
	AudioLang string
	Captions  bool // has English captions
	// OffAir marks a gap in a scheduled channel's airings; its
	// Description says what's next and when.
	OffAir bool
}

// Channel is a custom channel.
type Channel interface {
	Number() string
	Name() string
	// Details describe the channel for guides and lineups, as of now:
	// they're read again when their file changes.
	Details() Details
	// Programs lists the schedule between from and to.
	Programs(from, to time.Time) []Program
	// Stream writes MPEG-TS (H.264 and AAC) to w until ctx ends.
	Stream(ctx context.Context, w io.Writer) error
}
