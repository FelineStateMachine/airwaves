package frontend

import (
	"io/fs"
	"regexp"
	"slices"
	"testing"
)

// TestDist: the interface is embedded with index.html at the root, and
// the page loads the web app's stand-in for Wails first, then every file
// of the app (js/), none missing.
func TestDist(t *testing.T) {
	d := Dist()
	for _, name := range []string{"index.html", "web.js", "style.css", "js/core.js", "vendor/hls.min.js"} {
		if _, err := fs.Stat(d, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	page, err := fs.ReadFile(d, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	var scripts []string
	for _, m := range regexp.MustCompile(`<script src="([^"]+)"></script>`).FindAllStringSubmatch(string(page), -1) {
		scripts = append(scripts, m[1])
		if _, err := fs.Stat(d, m[1]); err != nil {
			t.Errorf("the page loads %s: %v", m[1], err)
		}
	}
	web, core := slices.Index(scripts, "web.js"), slices.Index(scripts, "js/core.js")
	if web < 0 || core < 0 || web > core {
		t.Errorf("scripts %v: web.js must come before the app", scripts)
	}
	files, err := fs.Glob(d, "js/*.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !slices.Contains(scripts, f) {
			t.Errorf("%s isn't loaded by the page", f)
		}
	}
}

// TestNoNameTwice: the app's files share one scope, as plain scripts do,
// so a name declared at the top of two of them stops the second loading.
func TestNoNameTwice(t *testing.T) {
	d := Dist()
	files, err := fs.Glob(d, "js/*.js")
	if err != nil {
		t.Fatal(err)
	}
	decl := regexp.MustCompile(`(?m)^(?:const|let|var|class|function|async function) ([A-Za-z_$][\w$]*)`)
	where := map[string]string{}
	for _, f := range files {
		src, err := fs.ReadFile(d, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			if prev, ok := where[m[1]]; ok {
				t.Errorf("%s is declared in %s and %s", m[1], prev, f)
			}
			where[m[1]] = f
		}
	}
}
