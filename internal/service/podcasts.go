package service

import (
	"context"
	"errors"

	"airwaves/internal/podcast"
	"airwaves/internal/stream"
)

// ErrNoPodcasts is returned by the Podcasts app's calls on a server
// without it.
var ErrNoPodcasts = errors.New("this Airwaves server has no podcasts")

// Podcasts is the Podcasts app's library: its shows and their episodes.
func (s *Service) Podcasts(context.Context) (*podcast.State, error) {
	if s.podcasts == nil {
		return nil, ErrNoPodcasts
	}
	st := s.podcasts.State()
	return &st, nil
}

// RefreshPodcasts lists every show again now, and returns the library
// as refreshed.
func (s *Service) RefreshPodcasts(ctx context.Context) (*podcast.State, error) {
	if s.podcasts == nil {
		return nil, ErrNoPodcasts
	}
	st := s.podcasts.Refresh(ctx)
	return &st, nil
}

// SearchPodcasts finds YouTube channels to add as shows.
func (s *Service) SearchPodcasts(ctx context.Context, query string) ([]podcast.Found, error) {
	if s.podcasts == nil {
		return nil, ErrNoPodcasts
	}
	return s.podcasts.Search(ctx, query)
}

// AddPodcast adds a show: a YouTube channel, its live streams' tab, or a
// playlist; minMinutes is the shortest video that's an episode, 0 for the
// default.
func (s *Service) AddPodcast(ctx context.Context, url string, minMinutes float64) (*podcast.Show, error) {
	if s.podcasts == nil {
		return nil, ErrNoPodcasts
	}
	show, err := s.podcasts.Add(ctx, url, minMinutes)
	if err != nil {
		return nil, err
	}
	return &show, nil
}

// RemovePodcast takes a show out of the library.
func (s *Service) RemovePodcast(_ context.Context, id string) error {
	if s.podcasts == nil {
		return ErrNoPodcasts
	}
	return s.podcasts.Remove(id)
}

// PlayEpisode streams an episode to client from from seconds in.
func (s *Service) PlayEpisode(ctx context.Context, client, id string, from float64) (*stream.Playback, error) {
	if s.podcasts == nil {
		return nil, ErrNoPodcasts
	}
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	in, err := s.podcasts.Input(ctx, id, from)
	if err != nil {
		return nil, err
	}
	return s.streams.Start(ctx, client, in)
}

// EpisodeProgress records how far an episode has been watched, and
// whether it has.
func (s *Service) EpisodeProgress(_ context.Context, id string, position, duration float64, watched bool) error {
	if s.podcasts == nil {
		return ErrNoPodcasts
	}
	return s.podcasts.Progress(id, position, duration, watched)
}
