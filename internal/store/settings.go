package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Settings are the app's persisted preferences. Everything else (location,
// tuner, recordings) lives on the server.
type Settings struct {
	// Server is the airwavesd address ("nas" or "http://nas:8089").
	Server string `json:"server"`
	Token  string `json:"token"`
	// ClientID identifies this app to the server so its streams are its own.
	ClientID string `json:"clientId"`
	// LastChannel is restored on launch.
	LastChannel string `json:"lastChannel"`
	// Favorites and Hidden hold channel keys: "number|callSign" for antenna
	// channels, "number|custom|name" for the server's custom channels, and
	// "weather" for its weather channel (the app moves older keys along).
	Favorites []string `json:"favorites,omitempty"`
	Hidden    []string `json:"hidden,omitempty"`
	// Captions shows closed captions when a channel carries them.
	Captions bool `json:"captions"`
	// AudioLang is the preferred audio language (BCP 47, e.g. "es").
	AudioLang string `json:"audioLang,omitempty"`
	// Scale is the interface size in percent; 0 picks one for the screen.
	Scale int `json:"scale,omitempty"`
}

// DefaultSettings returns settings for a first launch.
func DefaultSettings() Settings {
	return Settings{}
}

func (s Settings) normalized() Settings {
	if s.Scale != 0 && (s.Scale < 50 || s.Scale > 300) {
		s.Scale = 0
	}
	if s.ClientID == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		s.ClientID = "app-" + hex.EncodeToString(b)
	}
	return s
}

func settingsPath(app string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(base, app, "settings.json"), nil
}

// LoadSettings reads the settings for app, falling back to defaults.
func LoadSettings(app string) (Settings, error) {
	p, err := settingsPath(app)
	if err != nil {
		return DefaultSettings(), err
	}
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultSettings().normalized(), nil
	}
	if err != nil {
		return DefaultSettings(), err
	}
	var s Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("parse %s: %w", p, err)
	}
	return s.normalized(), nil
}

// SaveSettings writes s for app and returns the normalized value stored.
func SaveSettings(app string, s Settings) (Settings, error) {
	s = s.normalized()
	p, err := settingsPath(app)
	if err != nil {
		return s, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return s, err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return s, err
	}
	return s, os.WriteFile(p, raw, 0o644)
}
