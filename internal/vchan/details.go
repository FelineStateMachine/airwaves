package vchan

// Channel details: what guides and lineups show of a custom channel
// besides its schedule. A channel folder's are in its channel.json (the
// folder's name stays the source of its number and name, so renaming the
// folder by hand still works); the weather channel has no folder, and
// keeps its whole record, number and name included, in .weather.json in
// the channels folder.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"airwaves/internal/guide"
)

// Categories are what a custom channel can be filed under, for guides'
// filters.
var Categories = []string{"Kids", "Family", "Movies", "Sports", "News", "Music", "Pets", "Gaming", "Documentary", "Weather", "Other"}

const (
	// DetailsFile holds a channel folder's details.
	DetailsFile = "channel.json"
	// WeatherFile holds the weather channel's record, in the channels
	// folder.
	WeatherFile = ".weather.json"
	// LogoName is a channel folder's logo file without its extension
	// ("logo.png"), and WeatherLogoName the weather channel's, beside
	// WeatherFile.
	LogoName        = "logo"
	WeatherLogoName = ".weather-logo"
)

// The weather channel's record when nothing else is set.
const (
	WeatherNumber   = "1.1"
	WeatherName     = "Airwaves Weather"
	WeatherCallSign = "WX"
	weatherBlurb    = "Current conditions, the local and extended forecast, and radar, around the clock."
)

// LogoTypes are the content types of the logo files' extensions.
var LogoTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".svg": "image/svg+xml", ".webp": "image/webp",
}

// logoExts are the extensions a logo is looked for with, in order, when
// the record names none.
var logoExts = []string{".png", ".svg", ".webp", ".jpg", ".jpeg"}

// Details describe a custom channel for guides and lineups.
type Details struct {
	Number      string
	Name        string
	CallSign    string // a short label, "WX" or "TOON"; "" for none
	Category    string // one of Categories
	Description string
	// Logo is the path of the channel's logo file, "" when it has none.
	Logo string
	// Enabled channels are in the HDHomeRun lineup and the app; disabled
	// ones are only on the admin page.
	Enabled bool
}

// record is a details file.
type record struct {
	Number      string `json:"number"` // .weather.json's only
	Name        string `json:"name"`   // likewise
	CallSign    string `json:"callSign"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Logo        string `json:"logo"` // a file beside it, "logo.png"
	Enabled     *bool  `json:"enabled"`
	// Schedule, on a channel that plays its items in order, says when
	// they air; nil airs them around the clock as always.
	Schedule *Schedule `json:"schedule,omitempty"`
}

// recordFile is a details file, read again when it changes.
type recordFile struct {
	mu     sync.Mutex
	path   string
	mod    time.Time
	size   int64
	rec    record
	logged string // the last error logged
}

// read returns the record at path, or an empty one when there's none or
// it can't be read.
func (f *recordFile) read(path string) record {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, err := os.Stat(path)
	if err != nil {
		f.path, f.rec = "", record{}
		return f.rec
	}
	if f.path == path && info.ModTime().Equal(f.mod) && info.Size() == f.size {
		return f.rec
	}
	f.path, f.mod, f.size, f.rec = path, info.ModTime(), info.Size(), record{}
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &f.rec)
	}
	if err != nil {
		f.rec = record{}
		if err.Error() != f.logged {
			log.Printf("%s: %v", path, err)
			f.logged = err.Error()
		}
		return f.rec
	}
	f.logged = ""
	return f.rec
}

// details fills in d, a channel's defaults, from the record; dir holds
// the logo, named base plus an extension unless the record names it.
func (r record) details(dir, base string, d Details) Details {
	if s := strings.TrimSpace(r.CallSign); s != "" {
		d.CallSign = s
	}
	if c := Category(r.Category); c != "" {
		d.Category = c
	}
	if s := strings.TrimSpace(r.Description); s != "" {
		d.Description = s
	}
	d.Enabled = r.Enabled == nil || *r.Enabled
	d.Logo = findLogo(dir, r.Logo, base)
	return d
}

// findLogo returns the path of the logo file named in a record, or with
// none named, the first of base.png, base.svg and so on that's there.
func findLogo(dir, named, base string) string {
	if named != "" {
		if filepath.Base(named) != named || LogoTypes[strings.ToLower(filepath.Ext(named))] == "" {
			return ""
		}
		if p := filepath.Join(dir, named); isFile(p) {
			return p
		}
		return ""
	}
	for _, ext := range logoExts {
		if p := filepath.Join(dir, base+ext); isFile(p) {
			return p
		}
	}
	return ""
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// folderDetails are a channel folder's: its number and name, and the rest
// from channel.json in it.
func folderDetails(f *recordFile, dir, number, name string) Details {
	return f.read(filepath.Join(dir, DetailsFile)).details(dir, LogoName, Details{Number: number, Name: name, Category: "Other"})
}

// folderSchedule is the schedule in channel.json in dir, read, or nil
// when it has none or it can't be read (which is logged).
func folderSchedule(f *recordFile, dir string) *sched {
	r := f.read(filepath.Join(dir, DetailsFile))
	if r.Schedule == nil {
		return nil
	}
	s, err := r.Schedule.read(time.Local)
	if err != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
		if msg := "schedule: " + err.Error(); msg != f.logged {
			log.Printf("%s: %s; airing around the clock", filepath.Join(dir, DetailsFile), msg)
			f.logged = msg
		}
		return nil
	}
	return s
}

// ReadSchedule returns the schedule in channel.json in a channel's folder
// as written, nil when it has none or the file can't be read.
func ReadSchedule(dir string) *Schedule {
	raw, err := os.ReadFile(filepath.Join(dir, DetailsFile))
	if err != nil {
		return nil
	}
	var r record
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return r.Schedule
}

// Category returns the category s names (in any case) as Categories has
// it, or "" for none.
func Category(s string) string {
	s = strings.TrimSpace(s)
	for _, c := range Categories {
		if strings.EqualFold(c, s) {
			return c
		}
	}
	return ""
}

var numberParts = regexp.MustCompile(`^(\d{1,3})[.-](\d{1,3})$`)

// ParseNumber reads a custom channel number such as "1.4" or "104.0": a
// major number 1 to 999 and a minor 0 to 999, joined by a dot (or in a
// folder's name, a dash). It returns the number as written, with a dot:
// "104.0" stays "104.0".
func ParseNumber(s string) (string, bool) {
	m := numberParts.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", false
	}
	if major, _ := strconv.Atoi(m[1]); major < 1 {
		return "", false
	}
	return m[1] + "." + m[2], true
}

// NumberKey is a channel number's value, for telling whether two are the
// same however they're written: "1.05" and "1.5" are both "1.5".
func NumberKey(s string) string {
	major, minor := guide.SplitNumber(strings.Replace(strings.TrimSpace(s), "-", ".", 1))
	return fmt.Sprintf("%d.%d", major, minor)
}

// SameNumber reports whether a and b are the same channel number.
func SameNumber(a, b string) bool { return NumberKey(a) == NumberKey(b) }

// KindOf names a channel's kind: "weather", "folder", "jellyfin" or
// "youtube".
func KindOf(ch Channel) string {
	switch ch.(type) {
	case *Weather:
		return "weather"
	case *Folder:
		return "folder"
	case *Jellyfin:
		return "jellyfin"
	case *YouTube:
		return "youtube"
	}
	return "other"
}

// LogoTag identifies the version of a logo file, for its URLs and ETag;
// "" when there's no file.
func LogoTag(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return logoTag(info)
}

func logoTag(info os.FileInfo) string {
	return strconv.FormatInt(info.ModTime().UnixNano(), 36) + "-" + strconv.FormatInt(info.Size(), 36)
}

// ServeLogo serves a logo file. A request whose v names the file's
// version (as LogoTag gives it) may be cached for good; others briefly,
// and revalidated by ETag.
func ServeLogo(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	tag := logoTag(info)
	h := w.Header()
	h.Set("Content-Type", LogoTypes[strings.ToLower(filepath.Ext(path))])
	h.Set("ETag", `"`+tag+`"`)
	if r.URL.Query().Get("v") == tag {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=300")
	}
	// An SVG opened on its own runs no scripts, and nothing is sniffed.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Access-Control-Allow-Origin", "*")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
