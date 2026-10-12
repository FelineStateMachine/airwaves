package sd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Pictures: Schedules Direct has Gracenote's artwork for most series and
// movies, behind the token and with a daily cap on downloads. Each one is
// downloaded once and kept, a few hundred an update at most, the shows on
// soonest first; a key without pictures is asked about again only after
// a week.
const (
	maxArtChunk   = 500  // keys per /metadata/programs
	maxArtLookups = 1000 // keys looked up per update
	maxArtFetches = 500  // downloads per update
	artRecheck    = 7 * 24 * time.Hour
)

// ArtPath is where the pictures are served from, for the guide.
const ArtPath = "/guide-art/"

// artKey is what a program's picture is kept under: its series' ID for an
// episode or a show ("SH" and the series number, 14 characters, where
// Schedules Direct keeps series artwork), else the program's own.
func artKey(programID string) string {
	if len(programID) == 14 && (strings.HasPrefix(programID, "EP") || strings.HasPrefix(programID, "SH")) {
		return "SH" + programID[2:10] + "0000"
	}
	return programID
}

// artwork looks up pictures for the programs aired that have none yet,
// and downloads the chosen ones it hasn't got, into dir. It never fails
// the update: pictures are a nicety, and a cap only pauses them.
func (c *client) artwork(ctx context.Context, dir string, now time.Time) {
	if now.Before(c.st.ImagesPaused) {
		return
	}
	if c.st.Art == nil {
		c.st.Art = map[string]string{}
	}
	if c.st.ArtChecked == nil {
		c.st.ArtChecked = map[string]time.Time{}
	}
	// The keys to look up, by when they first air.
	first := map[string]time.Time{}
	for _, days := range c.st.Days {
		for _, d := range days {
			for _, a := range d.Airings {
				if p, ok := c.st.Programs[a.ProgramID]; !ok || !p.Art {
					continue
				}
				k := artKey(a.ProgramID)
				if t, ok := first[k]; !ok || a.Start.Before(t) {
					first[k] = a.Start
				}
			}
		}
	}
	var keys []string
	for k := range first {
		if c.st.Art[k] == "" && now.Sub(c.st.ArtChecked[k]) > artRecheck {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(first[a].Compare(first[b]), strings.Compare(a, b)) })
	if len(keys) > maxArtLookups {
		keys = keys[:maxArtLookups]
	}
	for chunk := range slices.Chunk(keys, maxArtChunk) {
		if err := c.lookupArt(ctx, chunk, now); err != nil {
			log.Printf("listings: artwork: %v", err)
			return
		}
	}
	c.fetchArt(ctx, dir, now)
}

// lookupArt asks which pictures keys have, and picks one for each.
func (c *client) lookupArt(ctx context.Context, keys []string, now time.Time) error {
	var out []struct {
		ProgramID string      `json:"programID"`
		Code      int         `json:"code"`
		Data      []sdPicture `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "metadata/programs/", keys, &out); err != nil {
		return err
	}
	for _, k := range keys {
		c.st.ArtChecked[k] = now
	}
	for _, e := range out {
		if e.Code != 0 {
			continue
		}
		if uri := pickPicture(e.Data); uri != "" {
			c.st.ArtWanted[e.ProgramID] = uri
		}
	}
	return nil
}

// sdPicture is one picture (in one size) of a program or series.
type sdPicture struct {
	URI      string  `json:"uri"`
	Width    flexInt `json:"width"`
	Height   flexInt `json:"height"`
	Aspect   string  `json:"aspect"`
	Category string  `json:"category"`
}

// pickPicture picks the picture for the guide: landscape (16:9, else
// 4:3) and without the title written on it ("Iconic"), else with it, at
// about 720 pixels wide.
func pickPicture(ps []sdPicture) string {
	categories := []string{"Iconic", "Banner-L2", "Banner-L1", "Banner-L3", "Banner", "Staple", "Banner-LO"}
	rank := func(p sdPicture) (int, int, int) {
		c := slices.Index(categories, p.Category)
		if c < 0 {
			c = len(categories)
		}
		a := map[string]int{"16x9": 0, "4x3": 1}[p.Aspect]
		if p.Aspect != "16x9" && p.Aspect != "4x3" {
			a = 2
		}
		w := int(p.Width) - 720
		if w < 0 {
			w = -w * 2 // smaller than wanted is worse than larger
		}
		return a, c, w
	}
	best := -1
	for i, p := range ps {
		if p.URI == "" {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		a1, c1, w1 := rank(p)
		a2, c2, w2 := rank(ps[best])
		if cmp.Or(cmp.Compare(a1, a2), cmp.Compare(c1, c2), cmp.Compare(w1, w2)) < 0 {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return ps[best].URI
}

// fetchArt downloads the pictures picked and not yet kept, up to
// maxArtFetches; a daily cap pauses them until midnight UTC, and a picture
// Schedules Direct hasn't got isn't asked for again.
func (c *client) fetchArt(ctx context.Context, dir string, now time.Time) {
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range c.st.ArtWanted {
			if !yield(k) {
				return
			}
		}
	})
	fetched := 0
	for _, k := range keys {
		uri := c.st.ArtWanted[k]
		if c.st.BadArt[uri] {
			delete(c.st.ArtWanted, k)
			continue
		}
		name := artFile(uri)
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			c.st.Art[k] = name
			delete(c.st.ArtWanted, k)
			continue
		}
		if fetched == maxArtFetches || ctx.Err() != nil {
			return
		}
		fetched++
		err := c.fetchPicture(ctx, uri, filepath.Join(dir, name))
		var ae *apiError
		switch {
		case err == nil:
			c.st.Art[k] = name
			delete(c.st.ArtWanted, k)
		case errors.As(err, &ae) && (ae.Code == 5002 || ae.Code == 5003 || ae.Code == 5004):
			c.st.ImagesPaused = now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
			log.Printf("listings: picture downloads paused until %s: %v", c.st.ImagesPaused.Format(time.RFC3339), err)
			return
		case errors.As(err, &ae) && ae.Code == 5000:
			if c.st.BadArt == nil {
				c.st.BadArt = map[string]bool{}
			}
			c.st.BadArt[uri] = true
			delete(c.st.ArtWanted, k)
		default:
			log.Printf("listings: picture %s: %v", uri, err)
			return
		}
	}
}

// artFile is the file a picture is kept as: its URI's last part, which
// Schedules Direct makes a checksum.
func artFile(uri string) string {
	name := filepath.Base(strings.Split(uri, "?")[0])
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 {
			return '_'
		}
		return r
	}, name)
}

// fetchPicture downloads one picture to path. Schedules Direct answers a
// picture with a redirect to where it is; an error comes as JSON.
func (c *client) fetchPicture(ctx context.Context, uri, path string) error {
	u := uri
	if !strings.HasPrefix(u, "https://") {
		u = cmp.Or(c.base, API) + "image/" + strings.TrimPrefix(uri, "/")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Token", c.st.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "json") || bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		var env envelope
		if json.Unmarshal(raw, &env) == nil && env.Code != nil && *env.Code != 0 {
			if msg, ok := stopCodes[*env.Code]; ok {
				return &StopError{Code: *env.Code, Message: msg}
			}
			return &apiError{Code: *env.Code, Response: env.Response, Message: env.Message, Status: resp.StatusCode}
		}
		return fmt.Errorf("not a picture (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || len(raw) == 0 {
		return &apiError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	return writeFile(path, raw, 0o644)
}

// ArtHandler serves the pictures kept, at ArtPath; they never change.
func (s *Source) ArtHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, ArtPath)
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, filepath.Join(s.Dir, "images", name))
	})
}
