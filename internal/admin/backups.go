package admin

// Backups: copies of the channel setup, to put it back after a lost disk
// or a change gone wrong. A backup is a tar.gz of the files that make each
// channel what it is, laid out as in the channels folder, and nothing
// bulky: every channel folder (those holding only videos too, so they can
// be made again), with its jellyfin.json, youtube.json, channel.json, logo
// and a YouTube channel's schedule (.playout.json, which keeps the guide
// going on); and the channels folder's .jellyfin.json (the Jellyfin
// account), .weather.json and weather logo. A manifest.json comes first:
// when, why and by which airwavesd it was made, and what's in it. Videos,
// YouTube catalogs (listed again) and yt-dlp's .info.json files are left
// out.
//
// They're kept in Server.Backups (/data/backups in the container), the
// files readable only by the server's user, as the account is a secret.
// One is made 30 seconds after the last change through the API (the
// page's and the agent tools'), one a day when the setup changed some
// other way, one before every restore (restore.go), and others by hand or
// by upload. The automatic ones are pruned (see prune); the others stay
// until deleted.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"airwaves/internal/vchan"
)

const (
	backupVersion = 1
	manifestFile  = "manifest.json"
	backupExt     = ".tar.gz"
	// playoutFile is a YouTube channel's schedule (see vchan.YouTube).
	playoutFile = ".playout.json"
	// backupDelay is how long after a change through the API the setup is
	// backed up, so a burst of changes makes one backup.
	backupDelay = 30 * time.Second
	// The setup is checked for changes made some other way (by hand, or
	// lost) a minute after the server starts, then daily.
	firstCheck  = time.Minute
	backupEvery = 24 * time.Hour
	// Automatic backups kept: the newest keepRecent, and the newest of
	// each of the last keepDays days.
	keepRecent = 30
	keepDays   = 14
	// Limits on a backup: its size, unpacked and not, how many files it
	// has, and the size of each.
	maxBackup      = 16 << 20
	maxUnpacked    = 64 << 20
	maxBackupFiles = 10000
	maxSetupFile   = 8 << 20
	maxManifest    = 1 << 20
	maxLabel       = 60
)

// Why a backup was made. The first three are automatic.
const (
	afterChange   = "change"
	daily         = "daily"
	beforeRestore = "restore"
	byHand        = "manual"
	uploaded      = "uploaded"
)

func automatic(reason string) bool {
	return reason == afterChange || reason == daily || reason == beforeRestore
}

var (
	backupNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,150}\.tar\.gz$`)
	// folderNumberRe is a channel folder's name with a number, as the
	// library reads it.
	folderNumberRe = regexp.MustCompile(`^(\d+[.-]\d+)\s+(.+)$`)
	errBackupsOff  = errors.New("backups are off: airwavesd was started without a data folder for them")
	errNotBackup   = errors.New("that isn't an Airwaves backup: it should be a .tar.gz of the channel setup")
)

// backupState is how backups are getting on.
type backupState struct {
	run     sync.Mutex // held while a backup or restore runs: one at a time
	mu      sync.Mutex // the rest
	timer   *time.Timer
	pending bool // a change waits to be backed up
	stopped bool
	delay   time.Duration // backupDelay, but in tests
	read    map[string]readBackup
}

// readBackup is a backup file as last read, kept while it's unchanged.
type readBackup struct {
	size int64
	mod  time.Time
	info backupJSON
}

// ---------- the setup ----------

// setup is a channel setup: the channel folders, and the files that
// matter in them and beside them, by path from the channels folder
// ("201.0 Northernlion/youtube.json", ".jellyfin.json").
type setup struct {
	folders []string // sorted
	files   map[string][]byte
}

// inFolder reports whether a file in a channel folder is part of the
// setup: its settings, details, logo or schedule.
func inFolder(name string) bool {
	switch name {
	case channelFile, youtubeFile, vchan.DetailsFile, playoutFile:
		return true
	}
	return isLogo(name, vchan.LogoName)
}

// atTop reports whether a file beside the channel folders is part of the
// setup: the Jellyfin account, or the weather channel's record or logo.
func atTop(name string) bool {
	return name == accountFile || name == vchan.WeatherFile || isLogo(name, vchan.WeatherLogoName)
}

// isLogo reports whether name is a logo file named base ("logo.png").
func isLogo(name, base string) bool {
	ext, ok := strings.CutPrefix(name, base)
	return ok && strings.LastIndexByte(ext, '.') == 0 && vchan.LogoTypes[strings.ToLower(ext)] != ""
}

// goodFolder reports whether name can be a channel folder's: one plain
// path element, not hidden.
func goodFolder(name string) bool {
	return name != "" && len(name) <= 255 && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, `/\`) &&
		utf8.ValidString(name) && strings.IndexFunc(name, unicode.IsControl) < 0
}

// private reports whether a setup file holds a password or token, so only
// the server's user may read it.
func private(name string) bool {
	return path.Base(name) == accountFile || path.Base(name) == channelFile
}

// readSetup reads the setup from the channels folder. A folder that isn't
// there has none.
func readSetup(root string) (setup, error) {
	st := setup{files: map[string][]byte{}}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	} else if err != nil {
		return st, err
	}
	read := func(rel string) error {
		p := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // gone since: a schedule being replaced, say
		} else if err != nil {
			return err
		}
		if info.Size() > maxSetupFile {
			log.Printf("admin: backups leave out %s: it's over %d MB", rel, maxSetupFile>>20)
			return nil
		}
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // gone since: a schedule being replaced, say
		}
		st.files[rel] = data
		return err
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir() && goodFolder(name):
			st.folders = append(st.folders, name)
			files, err := os.ReadDir(filepath.Join(root, name))
			if err != nil {
				return st, err
			}
			for _, f := range files {
				if f.Type().IsRegular() && inFolder(f.Name()) {
					if err := read(name + "/" + f.Name()); err != nil {
						return st, err
					}
				}
			}
		case e.Type().IsRegular() && atTop(name):
			if err := read(name); err != nil {
				return st, err
			}
		}
	}
	slices.Sort(st.folders)
	return st, nil
}

// in lists the setup's files in a folder, by name.
func (st setup) in(folder string) map[string][]byte {
	out := map[string][]byte{}
	for p, data := range st.files {
		if dir, name, ok := strings.Cut(p, "/"); ok && dir == folder {
			out[name] = data
		}
	}
	return out
}

// top lists the files beside the folders, by name.
func (st setup) top() map[string][]byte {
	out := map[string][]byte{}
	for p, data := range st.files {
		if !strings.Contains(p, "/") {
			out[p] = data
		}
	}
	return out
}

// kindOf is a folder's kind by its files, as the library tells it.
func kindOf(files map[string][]byte) string {
	switch {
	case files[channelFile] != nil:
		return "jellyfin"
	case files[youtubeFile] != nil:
		return "youtube"
	}
	return "folder"
}

// digest sums the setup up, schedules aside (they change by the hour), to
// tell a change from a copy.
func (st setup) digest() string {
	h := sha256.New()
	for _, f := range st.folders {
		fmt.Fprintf(h, "d %q\n", f)
	}
	for _, p := range slices.Sorted(maps.Keys(st.files)) {
		if path.Base(p) == playoutFile {
			continue
		}
		fmt.Fprintf(h, "f %q %d\n", p, len(st.files[p]))
		h.Write(st.files[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// manifest describes a backup. It's the first file in the archive.
type manifest struct {
	Version   int            `json:"version"`
	Created   time.Time      `json:"created"`
	Airwavesd string         `json:"airwavesd,omitempty"`
	Reason    string         `json:"reason"` // "change", "daily", "restore", "manual" or "uploaded"
	Label     string         `json:"label,omitempty"`
	Folders   []backupFolder `json:"folders"`
	Account   bool           `json:"account"`   // the Jellyfin account
	Weather   bool           `json:"weather"`   // the weather channel's record
	Schedules bool           `json:"schedules"` // YouTube channels' schedules
}

// backupFolder is a channel folder in a backup.
type backupFolder struct {
	Folder string   `json:"folder"`
	Kind   string   `json:"kind"` // "folder", "jellyfin" or "youtube"
	Files  []string `json:"files,omitempty"`
}

func (st setup) manifest(reason, label, version string, at time.Time) manifest {
	m := manifest{Version: backupVersion, Created: at, Airwavesd: version, Reason: reason, Label: label, Folders: []backupFolder{}}
	for _, f := range st.folders {
		files := st.in(f)
		bf := backupFolder{Folder: f, Kind: kindOf(files), Files: slices.Sorted(maps.Keys(files))}
		m.Schedules = m.Schedules || files[playoutFile] != nil
		m.Folders = append(m.Folders, bf)
	}
	m.Account = st.files[accountFile] != nil
	m.Weather = st.files[vchan.WeatherFile] != nil
	return m
}

// ---------- the archive ----------

// writeArchive writes a backup: the manifest, the files beside the
// folders, then each folder and its files.
func writeArchive(w io.Writer, m manifest, st setup) error {
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	file := func(name string, data []byte) error {
		mode := int64(0o644)
		if private(name) {
			mode = 0o600
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: int64(len(data)), ModTime: m.Created}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := file(manifestFile, append(raw, '\n')); err != nil {
		return err
	}
	top := st.top()
	for _, name := range slices.Sorted(maps.Keys(top)) {
		if err := file(name, top[name]); err != nil {
			return err
		}
	}
	for _, f := range st.folders {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: f + "/", Mode: 0o755, ModTime: m.Created}); err != nil {
			return err
		}
		files := st.in(f)
		for _, name := range slices.Sorted(maps.Keys(files)) {
			if err := file(f+"/"+name, files[name]); err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

// archive is a backup as read.
type archive struct {
	man     *manifest // nil for a backup without one
	setup   setup
	newest  time.Time // its newest file's time
	skipped []string  // files in it that aren't part of a setup, left out
}

// readArchive reads a backup, checking it on the way: no links, devices,
// absolute paths or "..", at most maxBackupFiles files and maxUnpacked in
// all, and each file a setup has must be sound (JSON, or an image for a
// logo). Files that aren't part of a setup are left out and listed. It
// reads backups without a manifest too, like those made by hand before
// Airwaves made its own (a tar.gz of the channels folder's settings and
// logos), whose channels are the folders their files are in.
func readArchive(r io.Reader) (archive, error) {
	a := archive{setup: setup{files: map[string][]byte{}}}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return a, errNotBackup
	}
	tr := tar.NewReader(zr)
	folders := map[string]bool{}
	var unpacked int64
	for n := 0; ; n++ {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if n == 0 {
				return a, errNotBackup
			}
			return a, fmt.Errorf("the backup is damaged: %w", err)
		}
		if n >= maxBackupFiles {
			return a, fmt.Errorf("the backup has more than %d files; a channel setup has far fewer", maxBackupFiles)
		}
		if unpacked += max(hdr.Size, 0); unpacked > maxUnpacked {
			return a, fmt.Errorf("the backup unpacks to over %d MB; a channel setup is far smaller", maxUnpacked>>20)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		rel, err := entryPath(hdr.Name)
		if err != nil {
			return a, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return a, fmt.Errorf("the backup has a link or device in it (%q), which a backup never has", hdr.Name)
		}
		if rel == "" {
			continue // the channels folder itself
		}
		parts := strings.Split(rel, "/")
		if hdr.Typeflag == tar.TypeDir {
			if len(parts) == 1 && goodFolder(rel) {
				folders[rel] = true
			} else {
				a.skipped = append(a.skipped, rel+"/")
			}
			continue
		}
		want := rel == manifestFile || len(parts) == 1 && atTop(rel) || len(parts) == 2 && goodFolder(parts[0]) && inFolder(parts[1])
		if !want {
			a.skipped = append(a.skipped, rel)
			continue
		}
		limit := int64(maxSetupFile)
		if rel == manifestFile {
			limit = maxManifest
		}
		if hdr.Size > limit {
			return a, fmt.Errorf("%s in the backup is too big, at over %d MB", rel, limit>>20)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return a, fmt.Errorf("the backup is damaged: %w", err)
		}
		if hdr.ModTime.After(a.newest) {
			a.newest = hdr.ModTime
		}
		if rel == manifestFile {
			var m manifest
			if err := json.Unmarshal(data, &m); err != nil {
				return a, fmt.Errorf("the backup's manifest is damaged: %w", err)
			}
			a.man = &m
			continue
		}
		if err := checkSetupFile(path.Base(rel), data); err != nil {
			return a, fmt.Errorf("%s in the backup %w", rel, err)
		}
		a.setup.files[rel] = data
		if len(parts) == 2 {
			folders[parts[0]] = true
		}
	}
	if a.man != nil {
		if a.man.Version > backupVersion {
			return a, errors.New("the backup was made by a newer Airwaves; update airwavesd to restore it")
		}
		// Folders with nothing but videos are only in the manifest.
		for _, f := range a.man.Folders {
			if goodFolder(f.Folder) {
				folders[f.Folder] = true
			}
		}
	}
	a.setup.folders = slices.Sorted(maps.Keys(folders))
	if len(a.setup.folders) == 0 && len(a.setup.files) == 0 {
		return a, errors.New("there are no channels in that backup")
	}
	return a, nil
}

// entryPath is an archive entry's path from the channels folder, without
// "./" or a trailing slash; "" for the folder itself. One that would land
// outside the folder is an error.
func entryPath(name string) (string, error) {
	p := name
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" || p == "." {
		return "", nil
	}
	outside := fmt.Errorf("the backup has a file outside the channels folder (%q), which a backup never has", name)
	if strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || !utf8.ValidString(p) {
		return "", outside
	}
	for _, el := range strings.Split(p, "/") {
		if el == "" || el == "." || el == ".." {
			return "", outside
		}
	}
	return p, nil
}

// checkSetupFile checks a setup file's contents: a settings, details or
// schedule file is a JSON object, and a logo an image.
func checkSetupFile(name string, data []byte) error {
	if strings.HasSuffix(name, ".json") {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return errors.New("is damaged: it isn't a JSON object")
		}
		return nil
	}
	if logoType(data) == "" {
		return errors.New("isn't an image")
	}
	return nil
}

// ---------- the store ----------

// backupJSON is a backup as the page and agents see it.
type backupJSON struct {
	File      string         `json:"file"` // its name, which is also how the API knows it
	Size      int64          `json:"size"`
	Created   time.Time      `json:"created"`
	Reason    string         `json:"reason"`
	Label     string         `json:"label,omitempty"`
	Airwavesd string         `json:"airwavesd,omitempty"`
	Auto      bool           `json:"auto"` // made by itself, so pruned in time
	Folders   []backupFolder `json:"folders"`
	Account   bool           `json:"account"`
	Weather   bool           `json:"weather"`
	Schedules bool           `json:"schedules"`
	// Older is a backup made by hand without a manifest (a tar.gz of the
	// channels folder's settings), which restores the same.
	Older bool   `json:"older,omitempty"`
	Error string `json:"error,omitempty"` // why it can't be read

	digest string
}

func describeBackup(file string, size int64, mod time.Time, a archive) backupJSON {
	m := a.man
	if m == nil {
		at := a.newest
		if at.IsZero() {
			at = mod
		}
		made := a.setup.manifest(byHand, "", "", at)
		m = &made
	}
	b := backupJSON{File: file, Size: size, Created: m.Created, Reason: m.Reason, Label: m.Label, Airwavesd: m.Airwavesd,
		Auto: automatic(m.Reason), Older: a.man == nil, digest: a.setup.digest()}
	if b.Created.IsZero() {
		b.Created = mod
	}
	// What's in it, as read rather than as the manifest says.
	in := a.setup.manifest("", "", "", b.Created)
	b.Folders, b.Account, b.Weather, b.Schedules = in.Folders, in.Account, in.Weather, in.Schedules
	return b
}

// backupPath is where the backup named file is, or "" when that can't be
// a backup's name.
func (s *Server) backupPath(file string) string {
	if !backupNameRe.MatchString(file) {
		return ""
	}
	return filepath.Join(s.Backups, file)
}

// openBackup reads a stored backup.
func (s *Server) openBackup(file string) (archive, backupJSON, error) {
	p := s.backupPath(file)
	if p == "" {
		return archive{}, backupJSON{}, fs.ErrNotExist
	}
	f, err := os.Open(p)
	if err != nil {
		return archive{}, backupJSON{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return archive{}, backupJSON{}, err
	}
	if !info.Mode().IsRegular() {
		return archive{}, backupJSON{}, fs.ErrNotExist
	}
	if info.Size() > maxBackup {
		return archive{}, backupJSON{}, fmt.Errorf("it's over %d MB, too big to be a backup", maxBackup>>20)
	}
	a, err := readArchive(f)
	if err != nil {
		return a, backupJSON{}, err
	}
	return a, describeBackup(file, info.Size(), info.ModTime(), a), nil
}

// backupList lists the backups, newest first.
func (s *Server) backupList() ([]backupJSON, error) {
	list := []backupJSON{}
	entries, err := os.ReadDir(s.Backups)
	if errors.Is(err, fs.ErrNotExist) {
		return list, nil
	} else if err != nil {
		return list, err
	}
	s.bk.mu.Lock()
	defer s.bk.mu.Unlock()
	seen := map[string]readBackup{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !backupNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		r, ok := s.bk.read[e.Name()]
		if !ok || r.size != info.Size() || !r.mod.Equal(info.ModTime()) {
			r = readBackup{size: info.Size(), mod: info.ModTime()}
			if _, b, err := s.openBackup(e.Name()); err != nil {
				r.info = backupJSON{File: e.Name(), Size: info.Size(), Created: info.ModTime(), Reason: byHand, Folders: []backupFolder{}, Error: err.Error()}
			} else {
				r.info = b
			}
		}
		seen[e.Name()] = r
		list = append(list, r.info)
	}
	s.bk.read = seen
	slices.SortFunc(list, func(a, b backupJSON) int {
		if c := b.Created.Compare(a.Created); c != 0 {
			return c
		}
		return strings.Compare(b.File, a.File)
	})
	return list, nil
}

// saveBackup writes a backup of st, described by m, and returns it.
func (s *Server) saveBackup(m manifest, st setup) (backupJSON, error) {
	if err := os.MkdirAll(s.Backups, 0o700); err != nil {
		return backupJSON{}, err
	}
	// Only the server's user may look in: the account is in there.
	_ = os.Chmod(s.Backups, 0o700)
	base := "airwaves-" + m.Created.Local().Format("20060102-150405") + "-" + m.Reason
	name := base + backupExt
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(s.Backups, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d%s", base, i, backupExt)
	}
	tmp, err := os.CreateTemp(s.Backups, ".tmp-*")
	if err != nil {
		return backupJSON{}, err
	}
	defer os.Remove(tmp.Name())
	var buf bytes.Buffer
	if err := writeArchive(&buf, m, st); err != nil {
		tmp.Close()
		return backupJSON{}, err
	}
	_, err = tmp.Write(buf.Bytes())
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(s.Backups, name))
	}
	if err != nil {
		return backupJSON{}, err
	}
	a := archive{man: &m, setup: st}
	return describeBackup(name, int64(buf.Len()), m.Created, a), nil
}

// backUp saves the setup as it is now and prunes the automatic backups.
// An automatic backup is skipped when the setup is as the newest backup
// has it; that one is returned, and made is false.
func (s *Server) backUp(reason, label string) (b backupJSON, made bool, err error) {
	s.bk.run.Lock()
	defer s.bk.run.Unlock()
	return s.backUpLocked(reason, label)
}

// backUpLocked is backUp with s.bk.run held.
func (s *Server) backUpLocked(reason, label string) (backupJSON, bool, error) {
	if s.Backups == "" {
		return backupJSON{}, false, errBackupsOff
	}
	if automatic(reason) && reason != beforeRestore && s.backupsStopped() {
		return backupJSON{}, false, nil
	}
	st, err := readSetup(s.Library.Root)
	if err != nil {
		return backupJSON{}, false, fmt.Errorf("reading the channel setup: %w", err)
	}
	if automatic(reason) {
		if list, err := s.backupList(); err == nil && len(list) > 0 && list[0].Error == "" && list[0].digest == st.digest() {
			return list[0], false, nil
		}
	}
	b, err := s.saveBackup(st.manifest(reason, label, s.Version, time.Now()), st)
	if err != nil {
		return b, false, fmt.Errorf("saving a backup: %w", err)
	}
	log.Printf("admin: backed up the channel setup (%s) as %s", reason, b.File)
	if automatic(reason) {
		s.pruneBackups()
	}
	return b, true, nil
}

// prune picks the automatic backups to remove from list, newest first:
// all but the newest keepRecent, and the newest of each of the last
// keepDays days (today and the 13 before). Backups made by hand or
// uploaded, and files that can't be read, are never removed.
func prune(list []backupJSON, now time.Time) []backupJSON {
	y, m, d := now.Date()
	since := time.Date(y, m, d-keepDays+1, 0, 0, 0, 0, now.Location())
	var drop []backupJSON
	recent := 0
	days := map[string]bool{}
	for _, b := range list {
		if !b.Auto || b.Error != "" {
			continue
		}
		recent++
		day := b.Created.In(now.Location()).Format(time.DateOnly)
		first := !days[day]
		days[day] = true
		if recent <= keepRecent || first && !b.Created.Before(since) {
			continue
		}
		drop = append(drop, b)
	}
	return drop
}

func (s *Server) pruneBackups() {
	list, err := s.backupList()
	if err != nil {
		log.Printf("admin: listing backups: %v", err)
		return
	}
	for _, b := range prune(list, time.Now()) {
		if err := os.Remove(filepath.Join(s.Backups, b.File)); err != nil {
			log.Printf("admin: removing an old backup: %v", err)
		} else {
			log.Printf("admin: removed backup %s, which the automatic ones no longer keep", b.File)
		}
	}
}

// ---------- automatic backups ----------

// backupSoon backs the setup up once changes stop for a while: each
// change through the API starts the wait again.
func (s *Server) backupSoon() {
	if s.Backups == "" {
		return
	}
	b := &s.bk
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	wait := b.delay
	if wait == 0 {
		wait = backupDelay
	}
	b.pending = true
	if b.timer == nil {
		b.timer = time.AfterFunc(wait, s.backupPending)
	} else {
		b.timer.Reset(wait)
	}
}

// backupPending makes the backup changes are waiting for.
func (s *Server) backupPending() {
	b := &s.bk
	b.mu.Lock()
	if !b.pending || b.stopped {
		b.mu.Unlock()
		return
	}
	b.pending = false
	b.mu.Unlock()
	if _, _, err := s.backUp(afterChange, ""); err != nil {
		log.Printf("admin: backing up the channel setup: %v", err)
	}
}

func (s *Server) backupWaiting() bool {
	s.bk.mu.Lock()
	defer s.bk.mu.Unlock()
	return s.bk.pending
}

func (s *Server) backupsStopped() bool {
	s.bk.mu.Lock()
	defer s.bk.mu.Unlock()
	return s.bk.stopped
}

// stopBackups makes no more automatic backups, once one under way is
// done.
func (s *Server) stopBackups() {
	s.bk.mu.Lock()
	s.bk.stopped = true
	if s.bk.timer != nil {
		s.bk.timer.Stop()
	}
	s.bk.mu.Unlock()
	s.bk.run.Lock()
	s.bk.run.Unlock()
}

// Run makes the daily backups until ctx ends: a minute after it starts,
// then every day, the setup is backed up if it changed since the newest
// backup (by hand, say, or with a change the server stopped before
// backing up). Then changes waiting to be backed up are, and backups stop.
func (s *Server) Run(ctx context.Context) {
	if s.Backups == "" {
		return
	}
	t := time.NewTimer(firstCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.backupWaiting() {
				s.backupPending()
			}
			s.stopBackups()
			return
		case <-t.C:
			if _, _, err := s.backUp(daily, ""); err != nil {
				log.Printf("admin: backing up the channel setup: %v", err)
			}
			t.Reset(backupEvery)
		}
	}
}

// codeWriter notes the status code of an answer.
type codeWriter struct {
	http.ResponseWriter
	code int
}

func (w *codeWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *codeWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

// watch backs the setup up after changes through the API: every request
// that may change something (not a GET), and works, starts the wait for
// one again. The backups' own requests don't count (a restore starts the
// wait itself).
func (s *Server) watch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if r.Method == http.MethodGet || r.Method == http.MethodHead || !strings.HasPrefix(p, "/admin/api/") || strings.HasPrefix(p, "/admin/api/backups") {
			next.ServeHTTP(w, r)
			return
		}
		cw := &codeWriter{ResponseWriter: w}
		next.ServeHTTP(cw, r)
		if cw.code < http.StatusBadRequest {
			s.backupSoon()
		}
	})
}

// ---------- API ----------

func (s *Server) addBackupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/backups", s.getBackups)
	mux.HandleFunc("POST /admin/api/backups", s.postBackup)
	mux.HandleFunc("POST /admin/api/backups/upload", s.uploadBackup)
	mux.HandleFunc("GET /admin/api/backups/{file}", s.downloadBackup)
	mux.HandleFunc("DELETE /admin/api/backups/{file}", s.deleteBackup)
	mux.HandleFunc("POST /admin/api/backups/{file}/restore", s.restoreBackup)
}

// backupsOn answers for the backups when they're off.
func (s *Server) backupsOn(w http.ResponseWriter) bool {
	if s.Backups == "" {
		writeErr(w, http.StatusServiceUnavailable, errBackupsOff)
		return false
	}
	return true
}

// getBackups lists the backups, newest first, with where they're kept and
// whether changes are waiting to be backed up.
func (s *Server) getBackups(w http.ResponseWriter, r *http.Request) {
	if !s.backupsOn(w) {
		return
	}
	list, err := s.backupList()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"backups": list, "folder": s.Backups, "pending": s.backupWaiting(),
		"keep": map[string]any{"recent": keepRecent, "days": keepDays, "delaySeconds": int(backupDelay.Seconds())}})
}

// cleanLabel tidies a backup's label: one line of up to maxLabel
// characters.
func cleanLabel(label string) (string, error) {
	label = strings.Join(strings.FieldsFunc(label, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(label) > maxLabel {
		return "", fmt.Errorf("the label is up to %d characters", maxLabel)
	}
	return label, nil
}

// postBackup backs the setup up now, with an optional label.
func (s *Server) postBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupsOn(w) {
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	if !decodeOptional(w, r, &req) {
		return
	}
	label, err := cleanLabel(req.Label)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	b, _, err := s.backUp(byHand, label)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(b)
}

// decodeOptional reads a JSON body that may be left out.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupsOn(w) {
		return
	}
	file := r.PathValue("file")
	p := s.backupPath(file)
	f, err := os.Open(p)
	if p == "" || err != nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no backup %s", file))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no backup %s", file))
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file}))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupsOn(w) {
		return
	}
	file := r.PathValue("file")
	p := s.backupPath(file)
	if info, err := os.Lstat(p); p == "" || err != nil || !info.Mode().IsRegular() {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no backup %s", file))
		return
	}
	s.bk.run.Lock()
	err := os.Remove(p)
	s.bk.run.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("admin: deleted backup %s", file)
	w.WriteHeader(http.StatusNoContent)
}

// uploadBackup takes a backup file (the body, or the first file in a
// multipart form), checks it, and keeps it with the others, to restore
// from. It's kept as an Airwaves backup, whatever its format: with the
// time it was made (or its newest file's), and labelled with its file
// name. A backup that's there already is answered with as it is.
func (s *Server) uploadBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupsOn(w) {
		return
	}
	data, name, err := readUpload(w, r)
	if err != nil {
		code := http.StatusBadRequest
		if tooBig(err) {
			code, err = http.StatusRequestEntityTooLarge, fmt.Errorf("a backup is under %d MB", maxBackup>>20)
		}
		writeErr(w, code, err)
		return
	}
	a, err := readArchive(bytes.NewReader(data))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	m := a.setup.manifest(uploaded, "", "", a.newest)
	if a.man != nil {
		m.Created, m.Airwavesd, m.Label = a.man.Created, a.man.Airwavesd, a.man.Label
	}
	if m.Created.IsZero() {
		m.Created = time.Now()
	}
	if m.Label == "" {
		base := filepath.Base(strings.ReplaceAll(name, `\`, "/"))
		for _, ext := range []string{backupExt, ".tgz", ".gz"} {
			base = strings.TrimSuffix(base, ext)
		}
		m.Label, _ = cleanLabel(string([]rune(base)[:min(utf8.RuneCountInString(base), maxLabel)]))
	}
	s.bk.run.Lock()
	defer s.bk.run.Unlock()
	digest := a.setup.digest()
	if list, err := s.backupList(); err == nil {
		for _, b := range list {
			if b.digest == digest && b.Created.Equal(m.Created) {
				writeJSON(w, map[string]any{"backup": b, "skipped": orEmpty(a.skipped), "existing": true})
				return
			}
		}
	}
	b, err := s.saveBackup(m, a.setup)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("admin: took in an uploaded backup as %s", b.File)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"backup": b, "skipped": orEmpty(a.skipped)})
}

// readUpload reads an uploaded file: the request's body (named by ?name=),
// or the first file in a multipart form.
func readUpload(w http.ResponseWriter, r *http.Request) ([]byte, string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBackup+64<<10)
	var src io.Reader = r.Body
	name := r.URL.Query().Get("name")
	if kind, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(kind, "multipart/") {
		form := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := form.NextPart()
			if err != nil {
				if tooBig(err) {
					return nil, "", err
				}
				return nil, "", errors.New("the form has no backup file")
			}
			if part.FileName() != "" {
				src, name = part, part.FileName()
				break
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(src, maxBackup+1))
	switch {
	case err != nil:
		return nil, "", err
	case len(data) > maxBackup:
		return nil, "", &http.MaxBytesError{Limit: maxBackup}
	case len(data) == 0:
		return nil, "", errors.New("send the backup file")
	}
	return data, name, nil
}
