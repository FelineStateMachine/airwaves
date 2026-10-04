package vchan

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"airwaves/internal/guide"
)

// Library finds folder channels: each subfolder of Root is a channel named
// after the folder. A leading number ("1.2 Cat Calming", 1.0 to 999.999,
// kept as written) sets the channel number; folders without one, or whose
// number another channel has, take the next free 1.x numbers. A folder holding jellyfin.json plays from a Jellyfin server
// instead of its own videos (see Jellyfin), and one holding youtube.json
// plays uploads from YouTube channels (see YouTube). Any channel's
// channel.json holds its details (see Details).
type Library struct {
	Root    string
	FFmpeg  string
	FFprobe string
	// Reserved lists the numbers other channels use (the weather
	// channel's), which folders don't get. The folders are numbered again
	// when they change.
	Reserved func() []string
	// YtDlp runs yt-dlp for YouTube channels; yt-dlp from PATH when nil.
	YtDlp *YtDlp
	// Loudness is the level the channels' sound is evened out to, in LUFS
	// (see DefaultLoudness); 0 leaves it as it is.
	Loudness float64

	mu       sync.Mutex
	scanned  time.Time
	reserved string                    // as of the last scan
	channels map[string]libraryChannel // by path, so probes survive rescans
	list     []Entry
}

// Entry is a channel folder.
type Entry struct {
	Folder  string // folder name, "1.2 Cat Calming"
	Channel Channel
}

type libraryChannel struct {
	Channel
	sig string // number, name and source the channel was made from
}

// jellyfinConfig names the file that makes a folder a Jellyfin channel.
const jellyfinConfig = "jellyfin.json"

// youtubeConfig names the file that makes a folder a YouTube channel.
const youtubeConfig = "youtube.json"

var numbered = regexp.MustCompile(`^(\d+[.-]\d+)\s+(.+)$`)

// Channels returns the folder channels that have something to play, by
// number.
func (l *Library) Channels() []Channel {
	var out []Channel
	for _, e := range l.scan() {
		if c, ok := e.Channel.(interface{ Empty() bool }); !ok || !c.Empty() {
			out = append(out, e.Channel)
		}
	}
	return out
}

// Entries lists every channel folder by number, including ones with
// nothing to play yet.
func (l *Library) Entries() []Entry { return l.scan() }

// Rescan makes the next call read the folders again, after they change.
func (l *Library) Rescan() {
	l.mu.Lock()
	l.scanned = time.Time{}
	l.mu.Unlock()
}

func (l *Library) scan() []Entry {
	var reserved []string
	if l.Reserved != nil {
		reserved = l.Reserved()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.scanned.IsZero() && time.Since(l.scanned) < rescanEvery && strings.Join(reserved, " ") == l.reserved {
		return l.list
	}
	l.scanned, l.reserved = time.Now(), strings.Join(reserved, " ")
	entries, err := os.ReadDir(l.Root)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("channels: %v", err)
		}
		l.list = nil
		return nil
	}
	if l.channels == nil {
		l.channels = map[string]libraryChannel{}
	}
	taken := map[string]bool{}
	// Numbers are taken by value: "1.05" is 1.5.
	for _, n := range reserved {
		taken[NumberKey(n)] = true
	}
	type found struct{ path, number, name string }
	var named, unnamed []found
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fd := found{path: filepath.Join(l.Root, e.Name()), name: e.Name()}
		if m := numbered.FindStringSubmatch(e.Name()); m != nil {
			if n, ok := ParseNumber(m[1]); ok && !taken[NumberKey(n)] {
				fd.number, fd.name = n, strings.TrimSpace(m[2])
				taken[NumberKey(n)] = true
				named = append(named, fd)
				continue
			}
		}
		unnamed = append(unnamed, fd)
	}
	minor := 1
	for i := range unnamed {
		for taken[fmt.Sprintf("1.%d", minor)] {
			minor++
		}
		unnamed[i].number = fmt.Sprintf("1.%d", minor)
		taken[unnamed[i].number] = true
	}

	var list []Entry
	keep := map[string]bool{}
	for _, fd := range append(named, unnamed...) {
		sig := fd.number + "\x00" + fd.name
		cfg := filepath.Join(fd.path, jellyfinConfig)
		if info, err := os.Stat(cfg); err == nil {
			sig += "\x00jellyfin\x00" + info.ModTime().String()
		} else if _, err := os.Stat(filepath.Join(fd.path, youtubeConfig)); err == nil {
			// Not its modification time: a YouTube channel takes in edits
			// itself, so streams under way follow the new schedule.
			sig += "\x00youtube\x00"
		}
		c, ok := l.channels[fd.path]
		if !ok || c.sig != sig {
			c = libraryChannel{sig: sig}
			if strings.Contains(sig, "\x00jellyfin\x00") {
				j := &Jellyfin{Num: fd.number, Title: fd.name, Config: cfg, FFmpeg: l.FFmpeg, Loudness: l.Loudness}
				j.start() // alongside the other channels, not when first asked
				c.Channel = j
			} else if strings.Contains(sig, "\x00youtube\x00") {
				y := &YouTube{Num: fd.number, Title: fd.name, Dir: fd.path, FFmpeg: l.FFmpeg, YtDlp: l.YtDlp, Loudness: l.Loudness}
				y.start()
				c.Channel = y
			} else {
				f := &Folder{Num: fd.number, Title: fd.name, Dir: fd.path, FFmpeg: l.FFmpeg, FFprobe: l.FFprobe, Loudness: l.Loudness}
				if old, ok := l.channels[fd.path].Channel.(*Folder); ok {
					old.mu.Lock()
					f.probed = old.probed
					old.mu.Unlock()
				}
				c.Channel = f
			}
			l.channels[fd.path] = c
		}
		keep[fd.path] = true
		list = append(list, Entry{Folder: filepath.Base(fd.path), Channel: c.Channel})
	}
	for p := range l.channels {
		if !keep[p] {
			delete(l.channels, p)
		}
	}
	slices.SortFunc(list, func(a, b Entry) int { return guide.CompareNumbers(a.Channel.Number(), b.Channel.Number()) })
	l.list = list
	return list
}
