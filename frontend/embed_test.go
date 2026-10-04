package frontend

import (
	"io/fs"
	"strings"
	"testing"
)

// TestDist: the interface is embedded with index.html at the root, and
// the page loads the web app's stand-in for Wails before the app.
func TestDist(t *testing.T) {
	d := Dist()
	for _, name := range []string{"index.html", "app.js", "web.js", "style.css", "vendor/hls.min.js"} {
		if _, err := fs.Stat(d, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	page, err := fs.ReadFile(d, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	web, app := strings.Index(html, `<script src="web.js">`), strings.Index(html, `<script src="app.js">`)
	if web < 0 || app < 0 || web > app {
		t.Errorf("web.js at %d, app.js at %d: web.js must come first", web, app)
	}
}
