// Package phase times the steps of starting a stream, so a slow channel
// change shows where its time went: "tune 9.1 (antenna): first data 1.1s,
// first frame 1.9s, first segment 3.8s, ready 5.6s". It also keeps recent
// tune times by kind of channel, as the server and the app measured them,
// for the admin page.
package phase

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Timer records when each step of one stream's start happened.
type Timer struct {
	// Label names the stream in logs: "9.1 (antenna)".
	Label string
	// Kind is the kind of channel for the stats: "antenna", "folder",
	// "jellyfin", "youtube" or "weather"; empty for none.
	Kind  string
	start time.Time

	mu    sync.Mutex
	marks []Mark
}

// Mark is one step, at a time since the start.
type Mark struct {
	Name string        `json:"name"`
	At   time.Duration `json:"-"`
	MS   int64         `json:"ms"`
}

// New starts timing a stream.
func New(label, kind string) *Timer {
	return &Timer{Label: label, Kind: kind, start: time.Now()}
}

// Mark records that a step happened now. Only a step's first time counts.
// A nil Timer records nothing.
func (t *Timer) Mark(name string) {
	if t == nil {
		return
	}
	at := time.Since(t.start)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.marks {
		if m.Name == name {
			return
		}
	}
	t.marks = append(t.marks, Mark{Name: name, At: at, MS: at.Milliseconds()})
}

// Has reports whether a step was marked.
func (t *Timer) Has(name string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.marks {
		if m.Name == name {
			return true
		}
	}
	return false
}

// Since is how long ago the timer started.
func (t *Timer) Since() time.Duration {
	if t == nil {
		return 0
	}
	return time.Since(t.start)
}

// Marks lists the steps in the order they happened.
func (t *Timer) Marks() []Mark {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Mark, len(t.marks))
	copy(out, t.marks)
	return out
}

// String lists the steps: "first data 1.1s, ready 5.6s".
func (t *Timer) String() string {
	var parts []string
	for _, m := range t.Marks() {
		parts = append(parts, fmt.Sprintf("%s %.1fs", m.Name, m.At.Seconds()))
	}
	if len(parts) == 0 {
		return "no steps"
	}
	return strings.Join(parts, ", ")
}

type key struct{}

// NewContext returns ctx carrying t, for the code that starts the stream
// to mark its steps on.
func NewContext(ctx context.Context, t *Timer) context.Context {
	return context.WithValue(ctx, key{}, t)
}

// FromContext returns the Timer ctx carries, or nil.
func FromContext(ctx context.Context) *Timer {
	t, _ := ctx.Value(key{}).(*Timer)
	return t
}

// Step marks a step on ctx's Timer, if it has one.
func Step(ctx context.Context, name string) { FromContext(ctx).Mark(name) }
