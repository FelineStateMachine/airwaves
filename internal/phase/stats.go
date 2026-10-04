package phase

import (
	"slices"
	"sync"
	"time"
)

// Measures of a channel change.
const (
	// Ready is the server's: from the tune call to the stream being ready.
	Ready = "ready"
	// FirstFrame is the app's: from the key press to the first frame
	// playing.
	FirstFrame = "first frame"
)

// Kinds are the kinds of channel the stats keep.
var Kinds = []string{"antenna", "folder", "jellyfin", "youtube", "weather"}

// keep is how many recent tunes each kind and measure keeps.
const keep = 100

// Stats keeps recent tune times by kind of channel and measure.
type Stats struct {
	mu      sync.Mutex
	samples map[[2]string][]time.Duration // by kind and measure, oldest first
}

// Tunes are this server's.
var Tunes = &Stats{}

// Add records a tune's time. Kinds and measures not known, and times
// that can't be right, are left out.
func (s *Stats) Add(kind, measure string, d time.Duration) {
	if !slices.Contains(Kinds, kind) || (measure != Ready && measure != FirstFrame) || d <= 0 || d > 2*time.Minute {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.samples == nil {
		s.samples = map[[2]string][]time.Duration{}
	}
	k := [2]string{kind, measure}
	xs := append(s.samples[k], d)
	if len(xs) > keep {
		xs = xs[len(xs)-keep:]
	}
	s.samples[k] = xs
}

// Summary describes one kind and measure's recent tunes, in seconds.
type Summary struct {
	Kind    string  `json:"kind"`
	Measure string  `json:"measure"`
	Count   int     `json:"count"`
	P50     float64 `json:"p50"`
	P90     float64 `json:"p90"`
	Max     float64 `json:"max"`
}

// Summaries lists the kinds and measures with tunes, by kind in the order
// of Kinds, the server's measure first.
func (s *Stats) Summaries() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Summary
	for _, kind := range Kinds {
		for _, measure := range []string{Ready, FirstFrame} {
			xs := slices.Clone(s.samples[[2]string{kind, measure}])
			if len(xs) == 0 {
				continue
			}
			slices.Sort(xs)
			out = append(out, Summary{
				Kind: kind, Measure: measure, Count: len(xs),
				P50: quantile(xs, 0.5).Seconds(), P90: quantile(xs, 0.9).Seconds(), Max: xs[len(xs)-1].Seconds(),
			})
		}
	}
	return out
}

// quantile is the q quantile of sorted xs, nearest rank.
func quantile(xs []time.Duration, q float64) time.Duration {
	i := int(q*float64(len(xs))+0.999999) - 1
	return xs[min(max(i, 0), len(xs)-1)]
}
