// Package store persists JSON documents under the user's cache and config
// directories.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cache is a directory of JSON files keyed by name, each stamped with the
// time it was written.
type Cache struct {
	dir string
}

type envelope struct {
	Saved time.Time       `json:"saved"`
	Data  json.RawMessage `json:"data"`
}

// NewCache opens (creating if needed) the cache directory for app.
func NewCache(app string) (*Cache, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locate cache dir: %w", err)
	}
	dir := filepath.Join(base, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	return &Cache{dir: dir}, nil
}

// OpenCache opens (creating if needed) a cache in dir.
func OpenCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	return &Cache{dir: dir}, nil
}

// Dir returns the directory backing the cache.
func (c *Cache) Dir() string { return c.dir }

func (c *Cache) path(key string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		}
		return '_'
	}, key)
	return filepath.Join(c.dir, clean+".json")
}

// Get decodes the entry for key into v. It returns the time the entry was
// saved, or an error if the entry is missing or unreadable.
func (c *Cache) Get(key string, v any) (time.Time, error) {
	raw, err := os.ReadFile(c.path(key))
	if err != nil {
		return time.Time{}, err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return time.Time{}, err
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return time.Time{}, err
	}
	return env.Saved, nil
}

// Put stores v under key.
func (c *Cache) Put(key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(envelope{Saved: time.Now(), Data: data})
	if err != nil {
		return err
	}
	tmp := c.path(key) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path(key))
}

// Load returns the cached value for key when it is younger than maxAge,
// otherwise it calls fetch and caches the result. If fetch fails and a stale
// entry exists, the stale entry is returned along with no error so callers
// keep working offline. A nil cache always fetches.
func Load[T any](ctx context.Context, c *Cache, key string, maxAge time.Duration, refresh bool, fetch func(context.Context) (T, error)) (T, error) {
	var cached T
	var saved time.Time
	var cacheErr error = errors.New("no cache")
	if c != nil {
		saved, cacheErr = c.Get(key, &cached)
		if cacheErr == nil && !refresh && time.Since(saved) < maxAge {
			return cached, nil
		}
	}
	v, err := fetch(ctx)
	if err != nil {
		if cacheErr == nil {
			return cached, nil
		}
		return v, err
	}
	if c != nil {
		// A failed write only costs a refetch next time.
		_ = c.Put(key, v)
	}
	return v, nil
}
