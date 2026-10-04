// Package terrain samples ground elevation along a path from the AWS Terrain
// Tiles dataset (Terrarium encoding; SRTM, USGS NED and other sources).
// Tiles are cached on disk, so each area is downloaded once.
package terrain

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"airwaves/internal/geo"
	"airwaves/internal/web"
)

const (
	tileURL = "https://s3.amazonaws.com/elevation-tiles-prod/terrarium/%d/%d/%d.png"
	// zoom 10 gives roughly 120 m pixels at US latitudes.
	zoom      = 10
	tileSize  = 256
	spacingKm = 0.15
)

// Profile is ground elevation sampled at equal steps from the receiver
// (index 0) to the transmitter (last index).
type Profile struct {
	DistanceKm float64   `json:"distanceKm"`
	Elevations []float64 `json:"elevations"`
}

type tileKey struct{ x, y int }

type tile struct {
	once sync.Once
	elev []float32
	err  error
}

// Source reads elevations, downloading tiles on demand.
type Source struct {
	HTTP *http.Client
	Dir  string // tile cache directory

	mu    sync.Mutex
	tiles map[tileKey]*tile
}

// NewSource caches tiles under dir.
func NewSource(c *http.Client, dir string) *Source {
	return &Source{HTTP: c, Dir: dir, tiles: make(map[tileKey]*tile)}
}

// Profile samples the ground from rx to tx.
func (s *Source) Profile(ctx context.Context, rx, tx geo.Point) (Profile, error) {
	d := geo.DistanceKm(rx, tx)
	n := max(int(math.Ceil(d/spacingKm))+1, 16)
	elev := make([]float64, n)
	for i := range n {
		e, err := s.Elevation(ctx, geo.Intermediate(rx, tx, float64(i)/float64(n-1)))
		if err != nil {
			return Profile{}, err
		}
		elev[i] = e
	}
	return Profile{DistanceKm: d, Elevations: elev}, nil
}

// Elevation returns meters above sea level at p.
func (s *Source) Elevation(ctx context.Context, p geo.Point) (float64, error) {
	scale := float64(int(1)<<zoom) * tileSize
	lat := p.Lat * math.Pi / 180
	px := (p.Lon + 180) / 360 * scale
	py := (1 - math.Log(math.Tan(lat)+1/math.Cos(lat))/math.Pi) / 2 * scale
	ix, iy := int(px), int(py)
	t, err := s.tile(ctx, tileKey{ix / tileSize, iy / tileSize})
	if err != nil {
		return 0, err
	}
	return float64(t[(iy%tileSize)*tileSize+ix%tileSize]), nil
}

func (s *Source) tile(ctx context.Context, k tileKey) ([]float32, error) {
	s.mu.Lock()
	t, ok := s.tiles[k]
	if !ok {
		t = &tile{}
		s.tiles[k] = t
	}
	s.mu.Unlock()
	t.once.Do(func() { t.elev, t.err = s.load(ctx, k) })
	if t.err != nil {
		// Let a later call retry a failed download.
		s.mu.Lock()
		if s.tiles[k] == t {
			delete(s.tiles, k)
		}
		s.mu.Unlock()
	}
	return t.elev, t.err
}

func (s *Source) load(ctx context.Context, k tileKey) ([]float32, error) {
	path := filepath.Join(s.Dir, strconv.Itoa(zoom), strconv.Itoa(k.x), strconv.Itoa(k.y)+".png")
	raw, err := os.ReadFile(path)
	if err != nil {
		raw, err = web.Get(ctx, s.HTTP, fmt.Sprintf(tileURL, zoom, k.x, k.y))
		if err != nil {
			return nil, fmt.Errorf("terrain tile %d/%d/%d: %w", zoom, k.x, k.y, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = os.WriteFile(path, raw, 0o644)
		}
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode terrain tile %d/%d/%d: %w", zoom, k.x, k.y, err)
	}
	return decodeTerrarium(img), nil
}

// decodeTerrarium converts pixels to meters: (R*256 + G + B/256) - 32768.
func decodeTerrarium(img image.Image) []float32 {
	b := img.Bounds()
	out := make([]float32, tileSize*tileSize)
	for y := range tileSize {
		for x := range tileSize {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			out[y*tileSize+x] = float32(float64(r>>8)*256+float64(g>>8)+float64(bl>>8)/256) - 32768
		}
	}
	return out
}
