package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type doc struct {
	N int `json:"n"`
}

func save(t *testing.T, path string, n int) {
	t.Helper()
	raw, _ := json.Marshal(doc{N: n})
	if err := WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func load(path string) (doc, error) {
	var d doc
	err := ReadFile(path, func(raw []byte) error {
		var x doc
		if err := json.Unmarshal(raw, &x); err != nil {
			return err
		}
		d = x
		return nil
	})
	return d, err
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// clock sets the day for daily backups.
func clock(t *testing.T, at time.Time) {
	t.Helper()
	now = func() time.Time { return at }
	t.Cleanup(func() { now = time.Now })
}

// TestWriteKeepsBak: each save keeps the file it replaces as .bak.
func TestWriteKeepsBak(t *testing.T) {
	clock(t, time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "dvr.json")
	save(t, path, 1)
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a first save left a .bak: %v", err)
	}
	save(t, path, 2)
	save(t, path, 3)
	if got := read(t, path); got != `{"n":3}` {
		t.Errorf("file: %s", got)
	}
	if got := read(t, path+".bak"); got != `{"n":2}` {
		t.Errorf(".bak: %s", got)
	}
	// The day's backup is the file before the day's first save.
	if got := read(t, filepath.Join(filepath.Dir(path), "backups", "dvr.json.2026-10-12")); got != `{"n":1}` {
		t.Errorf("daily backup: %s", got)
	}
	// Saving the same again changes nothing.
	save(t, path, 3)
	if got := read(t, path+".bak"); got != `{"n":2}` {
		t.Errorf(".bak after an unchanged save: %s", got)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("mode: %v, %v", fi.Mode(), err)
	}
}

// TestDailyBackupsKeepAWeek: one backup a day, the last BackupDays.
func TestDailyBackupsKeepAWeek(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signal.json")
	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := range 10 {
		clock(t, day.AddDate(0, 0, i))
		save(t, path, 2*i)
		save(t, path, 2*i+1)
	}
	got := backups(path)
	if len(got) != BackupDays {
		t.Fatalf("%d backups: %v", len(got), got)
	}
	if filepath.Base(got[0]) != "signal.json.2026-10-10" || filepath.Base(got[BackupDays-1]) != "signal.json.2026-10-04" {
		t.Errorf("backups: %v", got)
	}
	// The 10th's is the file as the 9th left it.
	if s := read(t, got[0]); s != `{"n":17}` {
		t.Errorf("newest backup: %s", s)
	}
}

// TestCorruptFallsBack: an unreadable file is set aside and its .bak read
// and put back; with no good .bak, the newest good daily backup.
func TestCorruptFallsBack(t *testing.T) {
	clock(t, time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "config.json")
	save(t, path, 1)
	save(t, path, 2)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, err := load(path); err != nil || d.N != 1 {
		t.Fatalf("from .bak: %+v, %v", d, err)
	}
	if got := read(t, path); got != `{"n":1}` {
		t.Errorf("not put back: %s", got)
	}
	aside, _ := filepath.Glob(path + ".corrupt-*")
	if len(aside) != 1 || read(t, aside[0]) != "{not json" {
		t.Errorf("set aside: %v", aside)
	}

	// An empty file and a bad .bak: the daily backup.
	clock(t, time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC))
	save(t, path, 5) // backs up {"n":1} for the 13th
	for _, p := range []string{path, path + ".bak"} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if d, err := load(path); err != nil || d.N != 1 {
		t.Fatalf("from the daily backup: %+v, %v", d, err)
	}

	// Nothing readable at all.
	if err := os.RemoveAll(filepath.Join(filepath.Dir(path), "backups")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + ".bak"} {
		if err := os.WriteFile(p, []byte("[]"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := load(path); !errors.Is(err, ErrCorrupt) {
		t.Errorf("nothing readable: %v", err)
	}
}

// TestMissingIsFresh: no file is a fresh start, not a corrupt one.
func TestMissingIsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dvr.json")
	if _, err := load(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if m, _ := filepath.Glob(path + "*"); len(m) != 0 {
		t.Errorf("files made: %v", m)
	}
}
