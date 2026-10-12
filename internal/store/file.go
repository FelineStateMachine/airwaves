package store

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The server's own records (recordings, measurements, its configuration)
// are written so that neither a crash nor a mistake loses them: each save
// replaces the file atomically and keeps the one it replaces as .bak, and
// the first save of a day keeps the file as it was in backups/ beside it,
// a week of them. A file that can't be read is set aside and the newest
// backup read instead.

// BackupDays is how many daily backups of a file are kept.
const BackupDays = 7

// ErrCorrupt is ReadFile's error when neither the file nor any backup of
// it can be read.
var ErrCorrupt = errors.New("unreadable, and no backup could be read")

// now is the clock, for tests.
var now = time.Now

// WriteFile replaces path with raw atomically, synced to disk, with mode.
// The file it replaces is kept as path.bak; on the first save of a day
// (UTC) it's also copied to backups/<name>.<date> beside path, and the
// copies beyond BackupDays removed. Writing what the file already holds
// does nothing.
func WriteFile(path string, raw []byte, mode fs.FileMode) error {
	prev, err := os.ReadFile(path)
	switch {
	case err == nil && bytes.Equal(prev, raw):
		return nil
	case err == nil && len(prev) > 0:
		if err := writeAtomic(path+".bak", prev, mode); err != nil {
			return err
		}
		if err := daily(path, prev, mode); err != nil {
			log.Printf("backup of %s: %v", path, err)
		}
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return writeAtomic(path, raw, mode)
}

// daily keeps prev, the file before the day's first save, in backups/.
func daily(path string, prev []byte, mode fs.FileMode) error {
	dir := filepath.Join(filepath.Dir(path), "backups")
	name := filepath.Base(path) + "."
	today := filepath.Join(dir, name+now().UTC().Format(time.DateOnly))
	if _, err := os.Stat(today); err == nil {
		return nil
	}
	if err := writeAtomic(today, prev, mode); err != nil {
		return err
	}
	old := backups(path)
	for len(old) > BackupDays {
		if err := os.Remove(old[len(old)-1]); err != nil {
			return err
		}
		old = old[:len(old)-1]
	}
	return nil
}

// backups lists path's daily backups, newest first.
func backups(path string) []string {
	name := filepath.Base(path) + "."
	entries, _ := os.ReadDir(filepath.Join(filepath.Dir(path), "backups"))
	var out []string
	for _, e := range entries {
		day, ok := strings.CutPrefix(e.Name(), name)
		if _, err := time.Parse(time.DateOnly, day); ok && err == nil {
			out = append(out, filepath.Join(filepath.Dir(path), "backups", e.Name()))
		}
	}
	slices.Sort(out)
	slices.Reverse(out)
	return out
}

// ReadFile reads path with parse. A missing file is an error matching
// fs.ErrNotExist: a fresh start. One that's empty or that parse rejects is
// set aside, as path.corrupt-<Unix time>, and the newest of path.bak and
// the daily backups that parse accepts is read instead, and put back as
// path. parse is called afresh for each, and must keep nothing from a
// call that fails. When nothing can be read the error matches ErrCorrupt.
func ReadFile(path string, parse func([]byte) error) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	err = errors.New("empty")
	if len(bytes.TrimSpace(raw)) > 0 {
		if err = parse(raw); err == nil {
			return nil
		}
	}
	mode := fs.FileMode(0o644)
	if st, serr := os.Stat(path); serr == nil {
		mode = st.Mode().Perm()
	}
	aside := fmt.Sprintf("%s.corrupt-%d", path, now().Unix())
	log.Printf("WARNING: %s can't be read (%v); set aside as %s, trying its backups", path, err, aside)
	if rerr := os.Rename(path, aside); rerr != nil {
		log.Printf("set aside %s: %v", path, rerr)
	}
	for _, b := range append([]string{path + ".bak"}, backups(path)...) {
		raw, berr := os.ReadFile(b)
		if berr != nil || len(bytes.TrimSpace(raw)) == 0 || parse(raw) != nil {
			continue
		}
		log.Printf("WARNING: %s read from its backup %s instead", path, b)
		if werr := writeAtomic(path, raw, mode); werr != nil {
			log.Printf("restore %s: %v", path, werr)
		}
		return nil
	}
	log.Printf("WARNING: %s: no backup of it could be read", path)
	return fmt.Errorf("%s: %w (%v)", path, ErrCorrupt, err)
}

// writeAtomic replaces path with raw: a temporary file beside it, synced,
// renamed over it.
func writeAtomic(path string, raw []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	// The rename itself, to disk; not every system can sync a directory.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
