package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log"
	"net/http"
	"strings"
)

// tvFiles serves the TV interface (frontend/dist) with the prefix /tv
// stripped. Pages, scripts and styles are checked again on every load
// (no-cache, with an ETag, so it's a 304 when unchanged) and a new
// airwavesd shows its own at once; fonts and the vendored libraries are
// kept for a day. There are no directory listings.
func tvFiles(fsys fs.FS) http.Handler {
	etags := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		etags[p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	if err != nil {
		log.Printf("tv: %v", err)
	}
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		etag, ok := etags[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", etag)
		if strings.HasPrefix(name, "fonts/") || strings.HasPrefix(name, "vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
