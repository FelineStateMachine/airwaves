package sd

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Account is a Schedules Direct login as the server keeps it: the user
// name and the SHA-1 of the password, which is what the API takes, so the
// password itself is never stored.
type Account struct {
	User         string `json:"user"`
	PasswordSHA1 string `json:"passwordSha1"`
}

// Set reports whether there's a login.
func (a Account) Set() bool { return a.User != "" && a.PasswordSHA1 != "" }

// HashPassword is the form of a password the API takes: its SHA-1, in hex.
func HashPassword(password string) string {
	sum := sha1.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

// LoadAccount reads the login kept at path; a missing file is no login.
func LoadAccount(path string) (Account, error) {
	var a Account
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal(raw, &a)
	a.User = strings.TrimSpace(a.User)
	return a, err
}

// SaveAccount keeps a at path, readable by this user only; an empty
// account removes the file.
func SaveAccount(path string, a Account) error {
	if !a.Set() {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, raw, 0o600)
}

// writeFile replaces path with raw atomically, with the given mode.
func writeFile(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
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
	}
	return err
}
