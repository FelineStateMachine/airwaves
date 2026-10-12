package service

import (
	"context"
	"errors"
	"testing"
)

// A server without the Podcasts app says so, rather than failing oddly.
func TestNoPodcasts(t *testing.T) {
	s := &Service{}
	if info, _ := s.Info(context.Background()); info.Podcasts {
		t.Error("info says there are podcasts")
	}
	if _, err := s.Podcasts(context.Background()); !errors.Is(err, ErrNoPodcasts) {
		t.Errorf("podcasts: %v", err)
	}
	if _, err := s.PlayEpisode(context.Background(), "mac", "e1", 0); !errors.Is(err, ErrNoPodcasts) {
		t.Errorf("play: %v", err)
	}
}
