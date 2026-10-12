package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Info is a video's details from yt-dlp -J, and with a format chosen,
// where to stream it from.
type Info struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Duration    float64 `json:"duration"`
	Timestamp   float64 `json:"timestamp"`
	Description string  `json:"description"`
	Language    string  `json:"language"`
	LiveStatus  string  `json:"live_status"`
	MediaType   string  `json:"media_type"`
	FormatID    string  `json:"format_id"`
	Format
	RequestedFormats []Format `json:"requested_formats"`
	// Subtitles are the uploader's, AutoCaptions YouTube's speech
	// recognition, by language ("en", "en-US", "en-orig").
	Subtitles    map[string][]Subtitle `json:"subtitles"`
	AutoCaptions map[string][]Subtitle `json:"automatic_captions"`
	// Thumbnails are its pictures, largest last as yt-dlp lists them.
	Thumbnails []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"thumbnails"`
}

// Format is a stream of a video: its picture, its sound, or both.
type Format struct {
	URL         string            `json:"url"`
	VCodec      string            `json:"vcodec"`
	ACodec      string            `json:"acodec"`
	Height      int               `json:"height"`
	Language    string            `json:"language"`
	HTTPHeaders map[string]string `json:"http_headers"`
}

// Subtitle is a subtitle track in one format.
type Subtitle struct {
	Ext      string `json:"ext"`
	URL      string `json:"url"`
	Protocol string `json:"protocol"` // "m3u8_native" for a playlist of pieces
}

// ParseInfo reads yt-dlp -J's details of a video.
func ParseInfo(raw []byte) (Info, error) {
	var i Info
	if err := json.Unmarshal(raw, &i); err != nil {
		return i, err
	}
	if i.ID == "" {
		return i, errors.New("no video in yt-dlp's output")
	}
	return i, nil
}

// Inputs are the streams to play a resolved video from, picture first.
func (i Info) Inputs() []Format {
	fs := i.RequestedFormats
	if len(fs) == 0 && i.URL != "" {
		fs = []Format{i.Format}
	}
	out := make([]Format, 0, len(fs))
	for _, f := range fs {
		if f.VCodec != "none" && f.VCodec != "" {
			out = append(out, f)
		}
	}
	for _, f := range fs {
		if f.VCodec == "none" || f.VCodec == "" {
			out = append(out, f)
		}
	}
	return out
}

// AudioLang is the sound's language, "" when YouTube doesn't say.
func (i Info) AudioLang() string {
	for _, f := range i.Inputs() {
		if f.ACodec != "none" && f.ACodec != "" && f.Language != "" {
			return f.Language
		}
	}
	return i.Language
}

// Captions picks the video's English captions: the uploader's, else
// YouTube's automatic ones, as json3, which times each word of automatic
// captions, else WebVTT or SRT. ok is false when there are none.
func (i Info) Captions() (sub Subtitle, ok bool) {
	pick := func(tracks map[string][]Subtitle, langs ...string) (Subtitle, bool) {
		for _, l := range langs {
			for _, ext := range []string{"json3", "vtt", "srt"} {
				for _, s := range tracks[l] {
					if s.Ext == ext && s.URL != "" {
						return s, true
					}
				}
			}
		}
		return Subtitle{}, false
	}
	var others []string
	for l := range i.Subtitles {
		if strings.HasPrefix(l, "en-") {
			others = append(others, l)
		}
	}
	slices.Sort(others)
	if s, ok := pick(i.Subtitles, append([]string{"en"}, others...)...); ok {
		return s, true
	}
	return pick(i.AutoCaptions, "en-orig", "en")
}

// FormatSpec picks H.264 picture up to height and AAC sound, English
// sound of a video dubbed in several languages, else the best single file
// up to height.
func FormatSpec(height int) string {
	return fmt.Sprintf("bv*[height<=%[1]d][vcodec^=avc1]+ba[ext=m4a][language^=en]/bv*[height<=%[1]d][vcodec^=avc1]+ba[ext=m4a]/b[height<=%[1]d]", height)
}

// InputArgs are ffmpeg's inputs for a resolved video, played from offset
// in: picture first, with yt-dlp's headers, reconnecting when a
// connection drops. A stream that fails for good stops ffmpeg (-xerror)
// rather than ending the video early, so it can be reopened. chunked
// fetches in 10 MB ranges as yt-dlp does, which YouTube serves without
// throttling (see HTTPChunks). audio is the sound's stream specifier.
func InputArgs(fs []Format, offset time.Duration, chunked bool) (args []string, audio string) {
	args = []string{"-xerror"}
	for i, f := range fs {
		args = append(args, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_on_network_error", "1",
			"-reconnect_delay_max", "4", "-rw_timeout", "15000000")
		if chunked {
			args = append(args, "-request_size", "10485760", "-multiple_requests", "1")
		}
		if len(f.HTTPHeaders) > 0 {
			var h strings.Builder
			for _, k := range sortedKeys(f.HTTPHeaders) {
				h.WriteString(k + ": " + f.HTTPHeaders[k] + "\r\n")
			}
			args = append(args, "-headers", h.String())
		}
		if offset > 0 {
			args = append(args, "-ss", strconv.FormatFloat(offset.Seconds(), 'f', 3, 64))
		}
		args = append(args, "-i", f.URL)
		if audio == "" && f.ACodec != "none" && f.ACodec != "" {
			audio = fmt.Sprintf("%d:a:0", i)
		}
	}
	return args, audio
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var chunks sync.Map // ffmpeg path: bool

// HTTPChunks reports whether ffmpeg can fetch HTTP in ranges
// (-request_size, FFmpeg 8).
func HTTPChunks(ffmpeg string) bool {
	if ok, found := chunks.Load(ffmpeg); found {
		return ok.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-h", "protocol=http").Output()
	ok := strings.Contains(string(out), "-request_size")
	chunks.Store(ffmpeg, ok)
	return ok
}

// AddressesUntil is when a video's addresses, found at, may stop working:
// ten minutes before the earliest "expire" they carry (YouTube's last six
// hours), or an hour after they were found when they carry none.
func AddressesUntil(info Info, at time.Time) time.Time {
	until := at.Add(time.Hour)
	var earliest time.Time
	for _, f := range info.Inputs() {
		u, err := url.Parse(f.URL)
		if err != nil {
			return until
		}
		sec, err := strconv.ParseInt(u.Query().Get("expire"), 10, 64)
		if err != nil || sec <= 0 {
			return until
		}
		if t := time.Unix(sec, 0); earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	if earliest.IsZero() {
		return until
	}
	if latest := at.Add(6 * time.Hour); earliest.After(latest) {
		return latest
	}
	return earliest.Add(-10 * time.Minute)
}

// FetchCaptions fetches a caption track: a json3, WebVTT or SRT file, or a
// playlist of WebVTT pieces, joined.
func FetchCaptions(ctx context.Context, client *http.Client, sub Subtitle) ([]byte, error) {
	raw, err := get(ctx, client, sub.URL)
	if err != nil || !strings.HasPrefix(sub.Protocol, "m3u8") {
		return raw, err
	}
	base, err := url.Parse(sub.URL)
	if err != nil {
		return nil, errors.New("captions: bad address")
	}
	var all []byte
	pieces := 0
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if pieces++; pieces > 500 {
			break
		}
		u, err := base.Parse(line)
		if err != nil {
			return nil, errors.New("captions: bad address")
		}
		piece, err := get(ctx, client, u.String())
		if err != nil {
			return nil, err
		}
		all = append(append(all, piece...), "\n\n"...)
	}
	return all, nil
}

// get fetches captions, leaving their address, which is signed, out of
// errors.
func get(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("captions: bad address")
	}
	resp, err := client.Do(req)
	if uerr := (*url.Error)(nil); errors.As(err, &uerr) {
		return nil, fmt.Errorf("captions: %w", uerr.Err)
	}
	if err != nil {
		return nil, fmt.Errorf("captions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("captions: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

var (
	separator = regexp.MustCompile(`(?m)^\s*[=\-_*~#]{5,}\s*$`)
	label     = regexp.MustCompile(`^.{0,40}:$`)
)

// Description makes a short description of a YouTube description: its
// first paragraph that says something, before the links section, without
// addresses or the labels left of them ("Game on Steam:").
func Description(s string) string {
	if i := separator.FindStringIndex(s); i != nil {
		s = s[:i[0]]
	}
	for para := range strings.SplitSeq(strings.ReplaceAll(s, "\r", ""), "\n\n") {
		var keep []string
		for line := range strings.SplitSeq(para, "\n") {
			line = strings.TrimSpace(urls.ReplaceAllString(line, ""))
			if line != "" && !label.MatchString(line) {
				keep = append(keep, strings.TrimSuffix(line, ":"))
			}
		}
		if len(keep) > 0 {
			return firstParagraph(strings.Join(keep, "\n"), 300)
		}
	}
	return ""
}

// firstParagraph is s's first paragraph on one line, cut at a word to n
// characters at most.
func firstParagraph(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		cut := string(r[:n])
		if i := strings.LastIndex(cut, " "); i > n/2 {
			cut = cut[:i]
		}
		s = cut + "…"
	}
	return s
}
