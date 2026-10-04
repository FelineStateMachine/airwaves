package vchan

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseNumber(t *testing.T) {
	// Numbers are kept as written: the user's "104.0" stays "104.0".
	for in, want := range map[string]string{
		"1.4": "1.4", " 1.4 ": "1.4", "104.0": "104.0", "1.0": "1.0", "01.004": "01.004", "999.999": "999.999", "12-3": "12.3",
		"0.4": "", "000.4": "", "1000.1": "", "1.1000": "", "1": "", "1.": "", "a.b": "", "1.2.3": "", "": "",
	} {
		got, ok := ParseNumber(in)
		if got != want || ok != (want != "") {
			t.Errorf("ParseNumber(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// They're the same number by value, however written.
	for _, c := range []struct {
		a, b string
		same bool
	}{{"1.05", "1.5", true}, {"104.0", "104.00", true}, {"1-2", "1.2", true}, {"104.0", "104.1", false}, {"1.1", "11.0", false}} {
		if SameNumber(c.a, c.b) != c.same {
			t.Errorf("SameNumber(%q, %q) = %v", c.a, c.b, !c.same)
		}
	}
}

// TestLibraryKeepsNumbersAsWritten: the user's "104.0" folders load with
// their numbers as written, sort by value, and a number written another
// way doesn't get a second channel.
func TestLibraryKeepsNumbersAsWritten(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"104.0 Cartoons", "105.0 Game Shows", "106.0 Marvel", "1.10 Ten", "1.2 Fish", "01.2 Clash", "1.01 Weather Too"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	l := &Library{Root: root, Reserved: func() []string { return []string{"1.1"} }}
	var got []string
	for _, e := range l.Entries() {
		got = append(got, e.Channel.Number()+" "+e.Channel.Name())
	}
	// The first folder by name gets a number; "1.2 Fish" comes after
	// "01.2 Clash", and 1.01 is the weather channel's 1.1.
	want := "01.2 Clash|1.3 1.01 Weather Too|1.4 1.2 Fish|1.10 Ten|104.0 Cartoons|105.0 Game Shows|106.0 Marvel"
	if strings.Join(got, "|") != want {
		t.Errorf("got  %q\nwant %q", strings.Join(got, "|"), want)
	}
}

// touch gives a file a new modification time, so a rewrite of the same
// size is noticed however coarse the file system's clock.
func touch(t *testing.T, path string, d time.Duration) {
	t.Helper()
	at := time.Now().Add(d)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestFolderDetails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "1.4 Cartoons")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &Folder{Num: "1.4", Title: "Cartoons", Dir: dir}
	if d := f.Details(); d != (Details{Number: "1.4", Name: "Cartoons", Category: "Other", Enabled: true}) {
		t.Errorf("without channel.json: %+v", d)
	}

	// A logo dropped in is found without being named.
	writeFile(t, filepath.Join(dir, "logo.svg"), "<svg/>")
	if d := f.Details(); d.Logo != filepath.Join(dir, "logo.svg") {
		t.Errorf("logo by name: %q", d.Logo)
	}

	record := filepath.Join(dir, DetailsFile)
	writeFile(t, record, `{"callSign": " TOON ", "category": "kids", "description": "Saturday mornings.", "logo": "toon.png", "enabled": false}`)
	writeFile(t, filepath.Join(dir, "toon.png"), "png")
	d := f.Details()
	if d.CallSign != "TOON" || d.Category != "Kids" || d.Description != "Saturday mornings." || d.Enabled ||
		d.Logo != filepath.Join(dir, "toon.png") || d.Number != "1.4" || d.Name != "Cartoons" {
		t.Errorf("from channel.json: %+v", d)
	}

	// Edits are read again; a category that isn't one, and a logo outside
	// the folder, are left out.
	writeFile(t, record, `{"category": "Cooking", "logo": "../toon.png"}`)
	touch(t, record, time.Second)
	if d := f.Details(); d.Category != "Other" || d.Logo != "" || !d.Enabled || d.CallSign != "" {
		t.Errorf("after an edit: %+v", d)
	}
	writeFile(t, record, `{"callSign": `)
	touch(t, record, 2*time.Second)
	if d := f.Details(); d.Category != "Other" || !d.Enabled || d.Logo != filepath.Join(dir, "logo.svg") {
		t.Errorf("unreadable channel.json: %+v", d)
	}

	// Jellyfin and YouTube channels read theirs the same way.
	writeFile(t, record, `{"callSign": "NL"}`)
	touch(t, record, 3*time.Second)
	j := &Jellyfin{Num: "1.4", Title: "Cartoons", Config: filepath.Join(dir, jellyfinConfig)}
	y := &YouTube{Num: "1.4", Title: "Cartoons", Dir: dir}
	if j.Details().CallSign != "NL" || y.Details().CallSign != "NL" {
		t.Errorf("Jellyfin %+v, YouTube %+v", j.Details(), y.Details())
	}
}

func TestWeatherRecord(t *testing.T) {
	if d := (&Weather{}).Details(); d.Number != "1.1" || d.Name != "Airwaves Weather" || d.CallSign != "WX" ||
		d.Category != "Weather" || d.Description == "" || !d.Enabled || d.Logo != "" {
		t.Errorf("without a record: %+v", d)
	}
	root := t.TempDir()
	wx := &Weather{Record: filepath.Join(root, WeatherFile)}
	if wx.Number() != "1.1" || wx.Name() != "Airwaves Weather" {
		t.Errorf("no record yet: %s %s", wx.Number(), wx.Name())
	}
	writeFile(t, wx.Record, `{"number": "2.05", "name": " Weather Now ", "callSign": "WTHR", "description": "The forecast.", "enabled": false}`)
	writeFile(t, filepath.Join(root, WeatherLogoName+".png"), "png")
	d := wx.Details()
	if d.Number != "2.05" || d.Name != "Weather Now" || d.CallSign != "WTHR" || d.Category != "Weather" ||
		d.Description != "The forecast." || d.Enabled || d.Logo != filepath.Join(root, ".weather-logo.png") {
		t.Errorf("from the record: %+v", d)
	}
	if wx.Number() != "2.05" || wx.Name() != "Weather Now" {
		t.Errorf("Number and Name: %s %s", wx.Number(), wx.Name())
	}
	writeFile(t, wx.Record, `{"number": "0.5", "name": " "}`)
	touch(t, wx.Record, time.Second)
	if wx.Number() != "1.1" || wx.Name() != "Airwaves Weather" {
		t.Errorf("a bad number and blank name keep the defaults: %s %s", wx.Number(), wx.Name())
	}
}

func TestLibraryGivesWayToTheWeather(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"1.2 Fish", "Birds", "1000.1 Too Big"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	weather := "1.1"
	l := &Library{Root: root, Reserved: func() []string { return []string{weather} }}
	numbers := func() string {
		var got []string
		for _, e := range l.Entries() {
			got = append(got, e.Channel.Number()+" "+e.Channel.Name())
		}
		return strings.Join(got, "|")
	}
	if got := numbers(); got != "1.2 Fish|1.3 1000.1 Too Big|1.4 Birds" {
		t.Errorf("first: %s", got)
	}
	// The weather channel moving is noticed at once, not at the next scan.
	weather = "1.2"
	if got := numbers(); got != "1.1 1.2 Fish|1.3 1000.1 Too Big|1.4 Birds" {
		t.Errorf("after the weather moved: %s", got)
	}
}

func TestServeLogo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logo.svg")
	writeFile(t, path, `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	tag := LogoTag(path)
	if tag == "" || LogoTag(path+".gone") != "" {
		t.Fatalf("tags: %q", tag)
	}
	get := func(query string, header http.Header) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/channel-logos/1.4"+query, nil)
		for k, v := range header {
			r.Header[k] = v
		}
		w := httptest.NewRecorder()
		ServeLogo(w, r, path)
		return w
	}
	w := get("", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || w.Header().Get("ETag") != `"`+tag+`"` ||
		w.Header().Get("Cache-Control") != "public, max-age=300" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") ||
		!strings.HasPrefix(w.Body.String(), "<svg") {
		t.Errorf("logo: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if w := get("?v="+tag, nil); w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("versioned: %v", w.Header())
	}
	if w := get("", http.Header{"If-None-Match": {`"` + tag + `"`}}); w.Code != http.StatusNotModified {
		t.Errorf("revalidated: %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	w = httptest.NewRecorder()
	ServeLogo(w, r, path+".gone")
	if w.Code != http.StatusNotFound {
		t.Errorf("missing: %d", w.Code)
	}
}
