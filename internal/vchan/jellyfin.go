package vchan

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/jellyfin"
)

// Jellyfin is a channel that loops videos from a Jellyfin server, the way
// ErsatzTV builds channels from a media library: the series, movies,
// collections, playlists and libraries named in the channel folder's
// jellyfin.json play on a fixed clock with a full guide, streamed from the
// server only while someone watches. The account to sign in with can be in
// jellyfin.json, or in .jellyfin.json in the channels folder, shared by
// every Jellyfin channel.
type Jellyfin struct {
	Num    string
	Title  string
	Config string // path to jellyfin.json
	FFmpeg string
	// Loudness is the level the videos' sound is evened out to, in LUFS;
	// 0 leaves it as it is.
	Loudness float64

	mu        sync.Mutex
	ready     chan struct{} // closed when the first fetch ends
	waitUntil time.Time     // callers wait for the first fetch until then
	busy      bool          // a fetch is running
	next      time.Time     // when the list is next refreshed
	checked   time.Time     // when the account file was last looked at
	acctMod   time.Time     // its modification time as of the last fetch
	items     []item
	unmatched []string
	err       error  // the last fetch's
	logged    string // the last error logged
	record    recordFile
	levels    loudnessFile // remembered, by video and file
	audible   []string     // the keys in levels of the videos with sound
}

// jellyfinChannel is a channel's jellyfin.json.
type jellyfinChannel struct {
	jellyfin.Account
	Collections []string `json:"collections"`
	Playlists   []string `json:"playlists"`
	Series      []string `json:"series"` // "Name", or "Name (Year)"
	Movies      []string `json:"movies"` // likewise
	Libraries   []string `json:"libraries"`
	Order       string   `json:"order"`      // "shuffle" (the default) or "aired"
	MaxBitrate  float64  `json:"maxBitrate"` // Mbps; 0 plays original files
}

const (
	// jellyfinAccount names the shared account file in the channels folder.
	jellyfinAccount = ".jellyfin.json"
	jellyfinRefresh = 30 * time.Minute
	jellyfinRetry   = 5 * time.Minute
	// jellyfinWait is how long a new channel's first fetch is waited for,
	// so it can join the lineup straight away.
	jellyfinWait    = 15 * time.Second
	jellyfinTimeout = 5 * time.Minute
)

// Number implements Channel.
func (j *Jellyfin) Number() string { return j.Num }

// Name implements Channel.
func (j *Jellyfin) Name() string { return j.Title }

// Details implements Channel, from channel.json beside jellyfin.json.
func (j *Jellyfin) Details() Details {
	return folderDetails(&j.record, filepath.Dir(j.Config), j.Num, j.Title)
}

// Empty reports whether the channel has nothing to play.
func (j *Jellyfin) Empty() bool { return len(j.list()) == 0 }

// Programs implements Channel: one entry per video, in channel order, on
// the channel's schedule when it has one.
func (j *Jellyfin) Programs(from, to time.Time) []Program { return guideFor(j.lineup, from, to) }

// Stream implements Channel.
func (j *Jellyfin) Stream(ctx context.Context, w io.Writer) error {
	if len(j.list()) == 0 {
		return fmt.Errorf("channel %s: nothing to play from Jellyfin", j.Num)
	}
	return play(ctx, w, j.FFmpeg, "channel "+j.Num, j.lineup)
}

// Sequence implements Sequenced: the videos in channel order.
func (j *Jellyfin) Sequence() ([]SequenceItem, bool) { return sequence(j.list()), true }

func (j *Jellyfin) lineup() ([]item, *sched) {
	return j.list(), folderSchedule(&j.record, filepath.Dir(j.Config))
}

// Status reports how many videos the channel has, the names in its config
// that matched nothing on the server, and the last fetch's error.
func (j *Jellyfin) Status() (items int, unmatched []string, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.kick()
	return len(j.items), slices.Clone(j.unmatched), j.err
}

// start begins the first fetch without waiting for it.
func (j *Jellyfin) start() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.kick()
}

// list returns the channel's videos in order. Only a new channel's first
// fetch is waited for, and briefly, so a slow or unreachable server can't
// hold up the lineup; later refreshes happen in the background.
func (j *Jellyfin) list() []item {
	j.mu.Lock()
	j.kick()
	ready, wait := j.ready, time.Until(j.waitUntil)
	j.mu.Unlock()
	if wait > 0 {
		select {
		case <-ready:
		case <-time.After(wait):
		}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.items
}

// Leveled is how many of the videos with sound have had their loudness
// measured as they played, of how many.
func (j *Jellyfin) Leveled() (measured, total int) {
	j.mu.Lock()
	keys := j.audible
	j.mu.Unlock()
	return j.levels.count(keys), len(keys)
}

// kick starts a fetch when one is due: the first, every half hour, sooner
// after a failure, and when the account file changes. j.mu is held.
func (j *Jellyfin) kick() {
	now := time.Now()
	switch {
	case j.ready == nil:
		j.ready = make(chan struct{})
		j.waitUntil = now.Add(jellyfinWait)
	case j.busy:
		return
	case now.Before(j.next) && !j.accountChanged(now):
		return
	}
	j.busy = true
	go j.refresh()
}

// accountChanged reports whether the account file has changed since the
// last fetch, looking at most every rescanEvery. (The library replaces the
// channel when its own jellyfin.json changes.) j.mu is held.
func (j *Jellyfin) accountChanged(now time.Time) bool {
	if now.Sub(j.checked) < rescanEvery {
		return false
	}
	j.checked = now
	return !modTime(j.accountPath()).Equal(j.acctMod)
}

func (j *Jellyfin) accountPath() string {
	return filepath.Join(filepath.Dir(filepath.Dir(j.Config)), jellyfinAccount)
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// refresh fetches the list, keeping the last good one if that fails.
func (j *Jellyfin) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), jellyfinTimeout)
	defer cancel()
	acctMod := modTime(j.accountPath())
	items, unmatched, err := j.fetch(ctx, acctMod)

	j.mu.Lock()
	defer j.mu.Unlock()
	first := true
	select {
	case <-j.ready:
		first = false
	default:
		defer close(j.ready)
	}
	j.busy, j.acctMod, j.err = false, acctMod, err
	if err != nil {
		j.next = time.Now().Add(jellyfinRetry)
		if err.Error() != j.logged {
			log.Printf("channel %s: %v", j.Num, err)
			j.logged = err.Error()
		}
		return
	}
	j.next = time.Now().Add(jellyfinRefresh)
	if first || len(items) != len(j.items) || j.logged != "" {
		log.Printf("channel %s: %d videos from Jellyfin", j.Num, len(items))
	}
	j.items, j.unmatched, j.logged = items, unmatched, ""
	j.audible = nil
	for _, it := range items {
		if it.Level != nil {
			j.audible = append(j.audible, it.Level.Key)
		}
	}
	if j.Loudness != 0 {
		j.levels.use(filepath.Join(filepath.Dir(j.Config), loudnessFileName), j.audible)
	}
}

// fetch reads the config and builds the list from the server, returning
// the names that matched nothing too.
func (j *Jellyfin) fetch(ctx context.Context, acctMod time.Time) ([]item, []string, error) {
	cfg, err := j.settings()
	if err != nil {
		return nil, nil, err
	}
	c := jellyfin.New(cfg.Account, jellyfinDevice(cfg.Account), nil)
	// A password the server refused is only tried again once the files
	// change, since repeated failures lock the account.
	changed := modTime(j.Config)
	if acctMod.After(changed) {
		changed = acctMod
	}
	c.Retry(changed)

	j.mu.Lock()
	reported := j.unmatched
	j.mu.Unlock()
	var from []jellyfin.Item
	var unmatched []string
	for _, src := range []struct {
		kind, what string
		names      []string
	}{
		{"BoxSet", "collection", cfg.Collections},
		{"Playlist", "playlist", cfg.Playlists},
		{"Series", "series", cfg.Series},
		{"Movie", "movie", cfg.Movies},
		{"Library", "library", cfg.Libraries},
	} {
		found, missing, err := c.Find(ctx, src.kind, src.names)
		if err != nil {
			return nil, nil, err
		}
		for _, n := range missing {
			if !slices.Contains(reported, n) {
				log.Printf("channel %s: no %s named %q on %s", j.Num, src.what, n, c.Server())
			}
		}
		from = append(from, found...)
		unmatched = append(unmatched, missing...)
	}
	videos, err := c.Videos(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Order == "aired" {
		airedOrder(videos)
	} else {
		shuffle(videos, j.Num+"\x00"+j.Title)
	}
	// Captions already fetched are kept.
	j.mu.Lock()
	had := map[string]*captions{}
	for _, it := range j.items {
		if it.Captions != nil && !it.Captions.failed() {
			had[it.Captions.key] = it.Captions
		}
	}
	j.mu.Unlock()
	var items []item
	for _, v := range videos {
		if it, ok := jellyfinItem(c, v, cfg.MaxBitrate); ok {
			if it.Captions != nil && had[it.Captions.key] != nil {
				it.Captions = had[it.Captions.key]
			}
			if j.Loudness != 0 && it.Audio {
				it.Level = &itemLevel{Target: j.Loudness, Key: v.ID + "/" + v.Source, Memory: &j.levels}
			}
			items = append(items, it)
		}
	}
	return items, unmatched, nil
}

// settings reads jellyfin.json. The account comes from the shared account
// file when the channel's own file names no user or token, and the server
// does when it names none.
func (j *Jellyfin) settings() (jellyfinChannel, error) {
	var cfg jellyfinChannel
	raw, err := os.ReadFile(j.Config)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", j.Config, err)
	}
	shared, err := jellyfin.LoadAccount(j.accountPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}
	if cfg.User == "" && cfg.Token == "" {
		server := cfg.Server
		cfg.Account = shared
		if server != "" {
			cfg.Server = server
		}
	} else if cfg.Server == "" {
		cfg.Server = shared.Server
	}
	if cfg.Server == "" || (cfg.User == "" && cfg.Token == "") {
		return cfg, fmt.Errorf("%s: no Jellyfin server, and user or token, here or in %s", j.Config, jellyfinAccount)
	}
	if cfg.Order != "" && cfg.Order != "shuffle" && cfg.Order != "aired" {
		return cfg, fmt.Errorf("%s: order %q is not shuffle or aired", j.Config, cfg.Order)
	}
	return cfg, nil
}

// jellyfinDevice identifies Airwaves to the server, the same across
// restarts for an account.
func jellyfinDevice(a jellyfin.Account) string {
	sum := sha256.Sum256([]byte(strings.ToLower(a.Server + "\x00" + a.User)))
	return "airwavesd-" + hex.EncodeToString(sum[:6])
}

// shuffle orders videos by a hash of the channel and each video, so the
// order depends only on which videos there are: the schedule holds until
// that set changes, and each channel's differs.
func shuffle(vs []jellyfin.Video, seed string) {
	key := map[string]uint64{}
	for _, v := range vs {
		h := fnv.New64a()
		h.Write([]byte(seed + "\x00" + v.ID))
		key[v.ID] = h.Sum64()
	}
	slices.SortFunc(vs, func(a, b jellyfin.Video) int {
		return cmp.Or(cmp.Compare(key[a.ID], key[b.ID]), strings.Compare(a.ID, b.ID))
	})
}

// airedOrder plays each series through by season and episode, specials
// last, and movies on their own, placing each by when it first aired.
func airedOrder(vs []jellyfin.Video) {
	group := func(v jellyfin.Video) string {
		if v.Type == "Episode" && v.SeriesID != "" {
			return v.SeriesID
		}
		return v.ID
	}
	aired := func(v jellyfin.Video) string {
		switch {
		case v.Premiere != "":
			return v.Premiere
		case v.Year > 0:
			return strconv.Itoa(v.Year)
		}
		return "9999" // unknown: last
	}
	season := func(v jellyfin.Video) int {
		if v.Season == 0 {
			return math.MaxInt
		}
		return v.Season
	}
	first := map[string]string{}
	for _, v := range vs {
		if d, ok := first[group(v)]; !ok || aired(v) < d {
			first[group(v)] = aired(v)
		}
	}
	slices.SortFunc(vs, func(a, b jellyfin.Video) int {
		return cmp.Or(
			cmp.Compare(first[group(a)], first[group(b)]),
			strings.Compare(group(a), group(b)),
			cmp.Compare(season(a), season(b)),
			cmp.Compare(a.Episode, b.Episode),
			strings.Compare(aired(a), aired(b)),
			strings.Compare(a.ID, b.ID),
		)
	})
}

// jellyfinItem is a video's place in the loop and the guide, or false for
// one too short to schedule.
func jellyfinItem(c *jellyfin.Client, v jellyfin.Video, mbps float64) (item, bool) {
	frames := int64(v.Runtime * loopFPS / time.Second)
	if frames < loopFPS {
		return item{}, false
	}
	it := item{
		Frames: frames, Audio: v.Audio, Title: v.Name, Description: firstParagraph(v.Overview, 300), Image: v.Image,
		Input: jellyfinInput(c, v, mbps),
	}
	if v.Type == "Movie" {
		it.Category = "Movie"
	} else if v.Series != "" {
		it.Title, it.Subtitle = v.Series, v.Name
		if v.Season > 0 && v.Episode > 0 {
			it.Season, it.Episode = strconv.Itoa(v.Season), strconv.Itoa(v.Episode)
		}
	}
	// English sound when there's a choice; a transcode is asked for it, and
	// has the one track.
	if a, ok := v.PreferredAudio(); ok {
		it.AudioLang = a.Language
		if mbps <= 0 && a.Position > 0 {
			it.AudioStream = fmt.Sprintf("0:a:%d", a.Position)
		}
	}
	if s, ok := v.EnglishSubtitle(cc.Foreign(it.AudioLang)); ok {
		it.Captions = newCaptions(v.ID+"\x00"+strconv.Itoa(s.Index), func(ctx context.Context) ([]byte, error) {
			return c.Subtitle(ctx, v, s)
		})
	}
	return it, true
}

// jellyfinInput reads a video from the server in real time: the original
// file, seeking here, or with a bitrate cap the server's transcode, which
// it seeks in. The token goes in a header, never the URL, so it stays out
// of logs.
func jellyfinInput(c *jellyfin.Client, v jellyfin.Video, mbps float64) func(time.Duration) []string {
	return func(offset time.Duration) []string {
		args := []string{
			"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_on_network_error", "1", "-reconnect_delay_max", "5",
			"-rw_timeout", "30000000", // 30 s in microseconds: give up on a stalled server
			"-headers", "Authorization: " + c.Authorization() + "\r\n",
		}
		if mbps <= 0 {
			if offset > 0 {
				args = append(args, "-ss", strconv.FormatFloat(offset.Seconds(), 'f', 3, 64))
			}
			return append(args, "-re", "-i", c.FileURL(v))
		}
		t := jellyfin.Transcode{Bitrate: int(mbps * 1e6), MaxWidth: loopWidth, MaxHeight: loopHeight}
		return append(args, "-re", "-i", c.TranscodeURL(v, offset, t))
	}
}
