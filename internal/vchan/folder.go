package vchan

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/cc"
)

// Folder is a channel that loops a folder of videos on a fixed clock, the
// way a cable channel runs its library: the guide lists each video, and
// tuning in joins whatever is on part way through. Nothing is decoded unless
// someone is watching.
type Folder struct {
	Num     string // channel number, "1.2"
	Title   string // channel name, "Cat Calming"
	Dir     string
	FFmpeg  string
	FFprobe string
	// Loudness is the level the videos' sound is evened out to, in LUFS;
	// 0 leaves it as it is.
	Loudness float64

	mu      sync.Mutex
	scanned time.Time
	items   []item
	probed  map[string]item      // by path, size and modification time
	subs    map[string]*captions // likewise, by their file's
	record  recordFile
	levels  loudnessFile // remembered, by file name, size and modification time
	audible []string     // the keys in levels of the videos with sound
}

// rescanEvery is how soon new or removed videos are noticed.
const rescanEvery = 30 * time.Second

var videoExts = []string{".mp4", ".mkv", ".webm", ".mov", ".m4v", ".ts"}

// partial matches files yt-dlp is still writing: "x.mp4.part",
// "x.temp.mp4", and the separate streams it merges ("x.f137.mp4").
var partial = regexp.MustCompile(`\.(part|ytdl)$|\.temp\.[^.]+$|\.f\d+\.[^.]+$`)

// videoID matches the " [id]" suffix yt-dlp adds to file names.
var videoID = regexp.MustCompile(`\s*\[[A-Za-z0-9_-]{6,}\]$`)

// Number implements Channel.
func (f *Folder) Number() string { return f.Num }

// Name implements Channel.
func (f *Folder) Name() string { return f.Title }

// Details implements Channel, from channel.json in the folder.
func (f *Folder) Details() Details { return folderDetails(&f.record, f.Dir, f.Num, f.Title) }

// Empty reports whether the folder has no playable videos.
func (f *Folder) Empty() bool { return len(f.list()) == 0 }

// Videos is how many playable videos the folder has.
func (f *Folder) Videos() int { return len(f.list()) }

// list returns the folder's videos in file name order, rescanning when the
// last scan is stale. New files are probed once.
func (f *Folder) list() []item {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.scanned.IsZero() && time.Since(f.scanned) < rescanEvery {
		return f.items
	}
	f.scanned = time.Now()
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		log.Printf("channel %s: %v", f.Num, err)
		f.items = nil
		return nil
	}
	if f.probed == nil {
		f.probed = map[string]item{}
	}
	var items []item
	var audible []string // the keys in levels of the videos with sound
	seen := map[string]bool{}
	names := map[string]os.DirEntry{}
	for _, e := range entries {
		names[e.Name()] = e
	}
	subs := map[string]*captions{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || partial.MatchString(name) ||
			!slices.Contains(videoExts, strings.ToLower(filepath.Ext(name))) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(f.Dir, name)
		key := fmt.Sprintf("%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
		seen[key] = true
		it, ok := f.probed[key]
		if !ok {
			if it, err = f.probe(path); err != nil {
				// Still downloading, or not a video; try again next scan.
				log.Printf("channel %s: skipping %s: %v", f.Num, name, err)
				continue
			}
			f.probed[key] = it
		}
		if e, ok := sidecar(name, names); ok {
			it.Captions = f.captions(filepath.Join(f.Dir, e.Name()), e, subs)
		}
		if f.Loudness != 0 && it.Audio {
			key := fmt.Sprintf("%s|%d|%d", name, info.Size(), info.ModTime().UnixNano())
			it.Level = &itemLevel{Target: f.Loudness, Key: key, Memory: &f.levels}
			audible = append(audible, key)
		}
		items = append(items, it)
	}
	for k := range f.probed {
		if !seen[k] {
			delete(f.probed, k)
		}
	}
	f.items, f.subs, f.audible = items, subs, audible
	if f.Loudness != 0 {
		f.levels.use(filepath.Join(f.Dir, loudnessFileName), audible)
	}
	return items
}

// Leveled is how many of the videos with sound have had their loudness
// measured as they played, of how many.
func (f *Folder) Leveled() (measured, total int) {
	f.list()
	f.mu.Lock()
	keys := f.audible
	f.mu.Unlock()
	return f.levels.count(keys), len(keys)
}

// sidecar finds a video's English subtitles beside it, among the folder's
// files: "<name>.en.srt", "<name>.en.vtt" (as yt-dlp writes them),
// "<name>.eng.srt", or "<name>.srt".
func sidecar(video string, files map[string]os.DirEntry) (os.DirEntry, bool) {
	base := strings.TrimSuffix(video, filepath.Ext(video))
	for _, ext := range []string{".en.srt", ".en.vtt", ".eng.srt", ".srt", ".vtt"} {
		if e, ok := files[base+ext]; ok && !e.IsDir() {
			return e, true
		}
	}
	return nil, false
}

// captions are a subtitle file's, kept while it's unchanged. f.mu is held.
func (f *Folder) captions(path string, e os.DirEntry, keep map[string]*captions) *captions {
	info, err := e.Info()
	if err != nil {
		return nil
	}
	key := fmt.Sprintf("%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
	c := f.subs[key]
	if c == nil {
		c = newCaptions(key, func(context.Context) ([]byte, error) { return os.ReadFile(path) })
	}
	keep[key] = c
	return c
}

// probe reads a video's length and sound, and its guide details from
// yt-dlp's .info.json when there is one.
func (f *Folder) probe(path string) (item, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, f.FFprobe, "-v", "error",
		"-show_entries", "format=duration:stream=codec_type:stream_tags=language,title:stream_disposition=default,comment,visual_impaired",
		"-of", "json", path).Output()
	if err != nil {
		return item{}, fmt.Errorf("ffprobe: %w", err)
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType   string            `json:"codec_type"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return item{}, err
	}
	secs, _ := strconv.ParseFloat(p.Format.Duration, 64)
	it := item{Path: path, Frames: int64(math.Round(secs * loopFPS))}
	video := false
	var sound []audioTrack
	for _, s := range p.Streams {
		video = video || s.CodecType == "video"
		if s.CodecType == "audio" {
			sound = append(sound, audioTrack{
				lang:  s.Tags["language"],
				extra: s.Disposition["comment"] == 1 || s.Disposition["visual_impaired"] == 1 || extraAudio.MatchString(s.Tags["title"]),
				def:   s.Disposition["default"] == 1,
			})
		}
	}
	if !video || it.Frames < loopFPS {
		return item{}, fmt.Errorf("no video, or shorter than a second")
	}
	var lang string
	it.Title, it.Description, lang = describe(path)
	if i := preferredAudio(sound); i >= 0 {
		it.Audio = true
		it.AudioStream = fmt.Sprintf("0:a:%d", i)
		it.AudioLang = cmp.Or(sound[i].lang, lang)
	}
	return it, nil
}

// audioTrack is a sound stream in a file.
type audioTrack struct {
	lang  string // as tagged
	extra bool   // a commentary, or described video
	def   bool   // the default
}

// extraAudio matches the titles of commentary and described video tracks.
var extraAudio = regexp.MustCompile(`(?i)comment|descri`)

// preferredAudio picks the sound to play, as jellyfin.Video.PreferredAudio
// does: English when there is some, else the default, else the first, with
// commentaries and described video last. -1 when there is none.
func preferredAudio(tracks []audioTrack) int {
	best, score := -1, -1
	for i, t := range tracks {
		n := 0
		if !t.extra {
			n += 4
		}
		if cc.English(t.lang) {
			n += 2
		}
		if t.def {
			n++
		}
		if n > score {
			best, score = i, n
		}
	}
	return best
}

// describe returns a video's guide title and description, and the
// language it's in: from yt-dlp's .info.json beside it, else the file name
// without yt-dlp's " [id]", and no language.
func describe(path string) (title, desc, lang string) {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	title = videoID.ReplaceAllString(filepath.Base(base), "")
	raw, err := os.ReadFile(base + ".info.json")
	if err != nil {
		return title, "", ""
	}
	var info struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Language    string `json:"language"`
	}
	if json.Unmarshal(raw, &info) != nil {
		return title, "", ""
	}
	if info.Title != "" {
		title = info.Title
	}
	return title, firstParagraph(info.Description, 300), info.Language
}

// firstParagraph trims a video description (often a wall of links) to its
// opening paragraph, at most n characters.
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

// Programs implements Channel: one entry per video, in file name order,
// on the channel's schedule when it has one.
func (f *Folder) Programs(from, to time.Time) []Program { return guideFor(f.lineup, from, to) }

// Stream implements Channel.
func (f *Folder) Stream(ctx context.Context, w io.Writer) error {
	if len(f.list()) == 0 {
		return fmt.Errorf("channel %s: no videos in %s", f.Num, f.Dir)
	}
	return play(ctx, w, f.FFmpeg, "channel "+f.Num, f.lineup)
}

// Sequence implements Sequenced: the videos in file name order.
func (f *Folder) Sequence() ([]SequenceItem, bool) { return sequence(f.list()), true }

func (f *Folder) lineup() ([]item, *sched) { return f.list(), folderSchedule(&f.record, f.Dir) }
