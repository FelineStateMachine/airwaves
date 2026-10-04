package admin

// Channel logos: an image file in the channel's folder ("logo.png"), or
// the weather channel's beside its record (".weather-logo.png"), named in
// the channel's details. The page uploads one, takes the source's picture
// (the poster of a Jellyfin channel's first pick, the avatar of a YouTube
// channel's first channel), or removes it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"airwaves/internal/vchan"
)

const (
	// maxLogo is the largest logo file taken.
	maxLogo = 2 << 20
	// sourceTime is how long taking a source's picture may take.
	sourceTime = 60 * time.Second
	// sourcePicks is how many of a Jellyfin channel's picks are tried for
	// a picture.
	sourcePicks = 6
)

var (
	errLogoType = errors.New("the logo should be a PNG, JPEG, SVG or WebP image")
	errLogoSize = fmt.Errorf("the logo should be under %d MB", maxLogo>>20)
)

// logoSpot is where a channel's logo goes.
type logoSpot struct {
	ch     vchan.Channel
	dir    string // the folder the logo file is in
	base   string // its name, less the extension
	record string // the details file that names it
}

func (s *Server) logoSpot(number string) (logoSpot, bool) {
	if s.isWeather(number) && s.Weather.Record != "" {
		return logoSpot{ch: s.Weather, dir: filepath.Dir(s.Weather.Record), base: vchan.WeatherLogoName, record: s.Weather.Record}, true
	}
	if e, ok := s.find(number); ok {
		dir := filepath.Join(s.Library.Root, e.Folder)
		return logoSpot{ch: e.Channel, dir: dir, base: vchan.LogoName, record: filepath.Join(dir, vchan.DetailsFile)}, true
	}
	return logoSpot{}, false
}

// save makes data the channel's logo, in place of any other.
func (l logoSpot) save(data []byte) error {
	ext := logoType(data)
	if ext == "" {
		return errLogoType
	}
	old := l.ch.Details().Logo
	name := l.base + ext
	if err := writeFile(filepath.Join(l.dir, name), data, 0o644); err != nil {
		return err
	}
	if err := updateRecord(l.record, func(m map[string]any) { m["logo"] = name }); err != nil {
		return err
	}
	l.removeOthers(old, name)
	return nil
}

// clear leaves the channel without a logo.
func (l logoSpot) clear() error {
	old := l.ch.Details().Logo
	if err := updateRecord(l.record, func(m map[string]any) { delete(m, "logo") }); err != nil {
		return err
	}
	l.removeOthers(old, "")
	return nil
}

// removeOthers removes the logo that was there, and any other found by
// name, but keep.
func (l logoSpot) removeOthers(old, keep string) {
	if old != "" && filepath.Base(old) != keep {
		_ = os.Remove(old)
	}
	for ext := range vchan.LogoTypes {
		if name := l.base + ext; name != keep {
			_ = os.Remove(filepath.Join(l.dir, name))
		}
	}
}

// logoType tells a logo's file extension from its first bytes: PNG, JPEG,
// WebP or SVG. "" for anything else.
func logoType(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return ".jpg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return ".webp"
	}
	head := bytes.TrimSpace(bytes.TrimPrefix(data[:min(len(data), 4096)], []byte("\xef\xbb\xbf")))
	if bytes.HasPrefix(head, []byte("<")) && bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
		return ".svg"
	}
	return ""
}

// readLogo reads an uploaded logo: the request's body, or the first file
// in a multipart form.
func readLogo(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxLogo+64<<10)
	var src io.Reader = r.Body
	if kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(kind, "multipart/") {
		form := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := form.NextPart()
			if err != nil {
				if tooBig(err) {
					return nil, errLogoSize
				}
				return nil, errors.New("the form has no image file")
			}
			if part.FileName() != "" {
				src = part
				break
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(src, maxLogo+1))
	switch {
	case tooBig(err) || len(data) > maxLogo:
		return nil, errLogoSize
	case err != nil:
		return nil, err
	case len(data) == 0:
		return nil, errors.New("send the logo's image")
	}
	return data, nil
}

func tooBig(err error) bool {
	var big *http.MaxBytesError
	return errors.As(err, &big)
}

func (s *Server) getLogo(w http.ResponseWriter, r *http.Request) {
	l, ok := s.logoSpot(r.PathValue("number"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	logo := l.ch.Details().Logo
	if logo == "" {
		http.NotFound(w, r)
		return
	}
	vchan.ServeLogo(w, r, logo)
}

// putLogo takes an uploaded logo.
func (s *Server) putLogo(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	l, ok := s.logoSpot(number)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no channel %s", number))
		return
	}
	data, err := readLogo(w, r)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errLogoSize) {
			code = http.StatusRequestEntityTooLarge
		}
		writeErr(w, code, err)
		return
	}
	s.saveLogo(w, r, l, data)
}

func (s *Server) saveLogo(w http.ResponseWriter, r *http.Request, l logoSpot, data []byte) {
	if err := l.save(data); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errLogoType) {
			code = http.StatusUnsupportedMediaType
		}
		writeErr(w, code, err)
		return
	}
	number := l.ch.Number()
	log.Printf("admin: new logo for channel %s", number)
	s.replyChannel(w, r, number)
}

func (s *Server) deleteLogo(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	l, ok := s.logoSpot(number)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no channel %s", number))
		return
	}
	if err := l.clear(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("admin: removed channel %s's logo", number)
	s.replyChannel(w, r, number)
}

// replyChannel answers with the channel as the page shows it.
func (s *Server) replyChannel(w http.ResponseWriter, r *http.Request, number string) {
	c, ok := s.describeNumber(r.Context(), number)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no channel %s", number))
		return
	}
	writeJSON(w, c)
}

// logoFromSource makes the source's picture the channel's logo: for a
// Jellyfin channel, the poster of its first pick that has one (its
// collections, then playlists, series and movies); for a YouTube channel,
// its first channel's avatar, or its playlist's channel's.
func (s *Server) logoFromSource(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	l, ok := s.logoSpot(number)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no channel %s", number))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTime)
	defer cancel()
	var data []byte
	var err error
	switch ch := l.ch.(type) {
	case *vchan.Jellyfin:
		data, err = s.jellyfinPicture(ctx, l.dir)
	case *vchan.YouTube:
		var image string
		if image, ok = s.youtubeAvatar(w, r, l.dir, ch); !ok {
			return // answered
		}
		data, err = s.fetchImage(ctx, image)
	default:
		writeErr(w, http.StatusBadRequest, errors.New("only Jellyfin and YouTube channels have a picture to take"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.saveLogo(w, r, l, data)
}

// jellyfinPicture fetches the poster of a Jellyfin channel's first pick
// that has one.
func (s *Server) jellyfinPicture(ctx context.Context, dir string) ([]byte, error) {
	cfg, err := readConfig(filepath.Join(dir, channelFile))
	if err != nil {
		return nil, err
	}
	c, err := s.client()
	if err != nil {
		return nil, err
	}
	tried := 0
	for _, src := range []struct {
		kind  string
		names []string
	}{{"BoxSet", cfg.Collections}, {"Playlist", cfg.Playlists}, {"Series", cfg.Series}, {"Movie", cfg.Movies}} {
		found, _, err := c.Find(ctx, src.kind, src.names)
		if err != nil {
			return nil, err
		}
		for _, it := range found {
			if tried++; tried > sourcePicks {
				break
			}
			if data, _, err := c.Image(ctx, it.ID, 400); err == nil && len(data) <= maxLogo {
				return data, nil
			}
		}
	}
	return nil, errors.New("none of the channel's picks has a picture on Jellyfin")
}

// avatarSize is the size option of a YouTube avatar's address ("=s176").
var avatarSize = regexp.MustCompile(`=s\d+`)

// youtubeAvatar finds the avatar of a YouTube channel's first channel, or
// of the channel that made its playlist, a lookup it may have to run
// yt-dlp for. When it can't, it answers the request itself and returns
// false.
func (s *Server) youtubeAvatar(w http.ResponseWriter, r *http.Request, dir string, y *vchan.YouTube) (string, bool) {
	cfg, err := vchan.ReadYouTubeConfig(dir)
	first := ""
	switch {
	case err == nil && cfg.Playlist != "":
		if st := y.Status(); len(st.Sources) > 0 && st.Sources[0].OwnerID != "" {
			first = st.Sources[0].OwnerID
		} else {
			writeErr(w, http.StatusBadRequest, errors.New("the playlist hasn't been listed yet; try again in a minute"))
			return "", false
		}
	case err == nil && len(cfg.Channels) > 0:
		first = cfg.Channels[0]
	default:
		writeErr(w, http.StatusBadRequest, errors.New("the channel plays no YouTube channels yet"))
		return "", false
	}
	page, err := youtubeChannel(first)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return "", false
	}
	l, ok := s.lookedUp(page, "")
	if !ok {
		if !s.ytRun(w, r, lookupTime, func(ctx context.Context, tool *vchan.YtDlp) error {
			found, err := tool.LookupChannel(ctx, page)
			l = lookedUp{ch: channelOut(found), err: err, at: time.Now()}
			return err
		}) {
			return "", false
		}
		s.remember(page, l)
	}
	if l.err != nil {
		writeErr(w, http.StatusBadGateway, errors.New(ytMessage(l.err)))
		return "", false
	}
	if l.ch.Image == "" {
		writeErr(w, http.StatusBadGateway, errors.New("the YouTube channel has no picture"))
		return "", false
	}
	// Asked for at a size fit for a logo.
	image := l.ch.Image
	if i := strings.LastIndex(image, "="); i > 0 {
		image = image[:i] + avatarSize.ReplaceAllString(image[i:], "=s400")
	}
	return image, true
}

// fetchImage downloads a picture.
func (s *Server) fetchImage(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("couldn't fetch the picture: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxLogo+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxLogo {
		return nil, errLogoSize
	}
	return data, nil
}
