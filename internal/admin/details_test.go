package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"airwaves/internal/vchan"
)

// send makes a request with a body of the given type.
func (h *harness) send(method, path, kind string, body []byte) (int, map[string]any, string) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.url+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", kind)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, string(raw)
}

// get fetches a path, returning the response with its body read.
func (h *harness) get(path string) (*http.Response, string) {
	h.t.Helper()
	resp, err := http.Get(h.url + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// record reads a details file.
func record(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return m
}

// withWeather gives the harness a weather channel, its record in the
// channels folder.
func (h *harness) withWeather() *vchan.Weather {
	wx := &vchan.Weather{Record: filepath.Join(h.root, vchan.WeatherFile)}
	h.admin.Weather = wx
	h.lib.Reserved = func() []string { return []string{wx.Number()} }
	h.lib.Rescan()
	return wx
}

var pngLogo = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

func TestAdminDetails(t *testing.T) {
	h := newHarness(t)
	_, state, _ := h.do("GET", "/admin/api/state", nil)
	if cats := state["categories"].([]any); len(cats) != len(vchan.Categories) || cats[0] != "Kids" || cats[len(cats)-1] != "Other" {
		t.Errorf("categories: %v", cats)
	}
	if c := h.channel("1.2"); c["callSign"] != "" || c["category"] != "Other" || c["description"] != "" || c["enabled"] != true || c["logo"] != nil {
		t.Errorf("defaults: %v", c)
	}

	// A folder channel's details, and its number and name renaming the
	// folder.
	code, ch, raw := h.do("PUT", "/admin/api/channels/1.2", map[string]any{
		"kind": "folder", "number": " 1.3 ", "name": "Cats", "callSign": " cat  tv ", "category": "pets",
		"description": "  Birds\n and  fish. ", "enabled": false,
	})
	if code != 200 || ch["number"] != "1.3" || ch["folder"] != "1.3 Cats" || ch["kind"] != "folder" || ch["callSign"] != "cat tv" ||
		ch["category"] != "Pets" || ch["description"] != "Birds and fish." || ch["enabled"] != false {
		t.Fatalf("folder channel: %d %s", code, raw)
	}
	path := filepath.Join(h.root, "1.3 Cats", vchan.DetailsFile)
	if m := record(t, path); m["callSign"] != "cat tv" || m["category"] != "Pets" || m["description"] != "Birds and fish." || m["enabled"] != false ||
		m["number"] != nil || m["name"] != nil {
		t.Errorf("channel.json: %v", m)
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.2 Cat Calming")); !os.IsNotExist(err) {
		t.Error("the old folder is still there")
	}

	// Details left out stay, as does anything else in the file; empty ones
	// go back to the defaults.
	if err := os.WriteFile(path, []byte(`{"callSign": "CATS", "category": "Pets", "description": "Birds.", "enabled": false, "mine": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, ch, raw = h.do("PUT", "/admin/api/channels/1.3", map[string]any{"kind": "folder", "number": "1.3", "name": "Cats", "enabled": true, "category": ""})
	if code != 200 || ch["callSign"] != "CATS" || ch["category"] != "Other" || ch["enabled"] != true || ch["folder"] != "1.3 Cats" {
		t.Errorf("partial: %d %s", code, raw)
	}
	if m := record(t, path); m["mine"] != 1.0 || m["category"] != nil || m["description"] != "Birds." {
		t.Errorf("kept: %v", m)
	}

	// A folder without a number keeps its name when only details change.
	if err := os.Mkdir(filepath.Join(h.root, "Fish"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.lib.Rescan()
	fish := ""
	for _, e := range h.lib.Entries() {
		if e.Folder == "Fish" {
			fish = e.Channel.Number()
		}
	}
	if code, ch, raw := h.do("PUT", "/admin/api/channels/"+fish, map[string]any{"kind": "folder", "number": fish, "name": "Fish", "callSign": "FISH"}); code != 200 ||
		ch["folder"] != "Fish" || ch["callSign"] != "FISH" {
		t.Errorf("unnumbered folder: %d %s", code, raw)
	}

	// A Jellyfin channel's details go beside its jellyfin.json.
	code, ch, raw = h.do("PUT", "/admin/api/channels/new", map[string]any{
		"number": "1.4", "name": "Cartoons", "series": []string{"Futurama (1999)"}, "callSign": "TOON", "category": "Kids",
	})
	if code != 200 || ch["callSign"] != "TOON" || ch["category"] != "Kids" || ch["enabled"] != true {
		t.Fatalf("jellyfin channel: %d %s", code, raw)
	}
	if m := record(t, filepath.Join(h.root, "1.4 Cartoons", vchan.DetailsFile)); m["callSign"] != "TOON" || m["category"] != "Kids" {
		t.Errorf("jellyfin channel.json: %v", m)
	}
	if _, err := os.Stat(filepath.Join(h.root, "1.4 Cartoons", channelFile)); err != nil {
		t.Error(err)
	}

	// Numbers stay as written ("104.0"), and save again unchanged.
	if err := os.Mkdir(filepath.Join(h.root, "104.0 Toons"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.lib.Rescan()
	if c := h.channel("104.0"); c["name"] != "Toons" || c["folder"] != "104.0 Toons" {
		t.Errorf("104.0: %v", c)
	}
	code, ch, raw = h.do("PUT", "/admin/api/channels/104.0", map[string]any{"kind": "folder", "number": "104.0", "name": "Toons", "callSign": "TOON"})
	if code != 200 || ch["number"] != "104.0" || ch["folder"] != "104.0 Toons" || ch["callSign"] != "TOON" {
		t.Errorf("104.0 saved: %d %s", code, raw)
	}
	code, ch, raw = h.do("PUT", "/admin/api/channels/104.0", map[string]any{"kind": "folder", "number": "105.0", "name": "Toons"})
	if code != 200 || ch["number"] != "105.0" || ch["folder"] != "105.0 Toons" {
		t.Errorf("105.0: %d %s", code, raw)
	}

	for _, c := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"number": "0.5", "name": "x"}, 400, "1 to 999, a dot, then 0 to 999"},
		{map[string]any{"number": "105.00", "name": "x"}, 409, "taken by Toons"},
		{map[string]any{"number": "01.3", "name": "x"}, 409, "taken by Cats"},
		{map[string]any{"number": "1000.1", "name": "x"}, 400, "then 0 to 999"},
		{map[string]any{"number": "1.1000", "name": "x"}, 400, "then 0 to 999"},
		{map[string]any{"number": "1.9", "name": "x", "callSign": "WAYTOOLONG"}, 400, "call sign"},
		{map[string]any{"number": "1.9", "name": "x", "callSign": "<b>"}, 400, "call sign"},
		{map[string]any{"number": "1.9", "name": "x", "category": "Cooking"}, 400, "category is one of Kids"},
		{map[string]any{"number": "1.9", "name": "x", "description": strings.Repeat("a", 501)}, 400, "500 characters"},
		{map[string]any{"kind": "folder", "number": "1.9", "name": "x"}, 400, "adding a folder"},
		{map[string]any{"kind": "weather", "number": "1.9", "name": "x"}, 404, "isn't the weather channel"},
		{map[string]any{"number": "1.3", "name": "x"}, 409, "taken by Cats"},
	} {
		if code, _, raw := h.do("PUT", "/admin/api/channels/new", c.body); code != c.code || !strings.Contains(raw, c.want) {
			t.Errorf("%v: %d %s", c.body, code, raw)
		}
	}
	if code, _, raw := h.do("PUT", "/admin/api/channels/1.3", map[string]any{"kind": "youtube", "number": "1.3", "name": "Cats",
		"youtube": map[string]any{"channels": []string{"@boilerroom"}}}); code != 400 || !strings.Contains(raw, "folder channel and can't become a YouTube one") {
		t.Errorf("folder to YouTube: %d %s", code, raw)
	}
}

func TestAdminWeather(t *testing.T) {
	h := newHarness(t)
	h.withWeather()
	if err := os.Mkdir(filepath.Join(h.root, "1.5 Fish"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, state, _ := h.do("GET", "/admin/api/state", nil)
	wx := state["channels"].([]any)[0].(map[string]any)
	if wx["kind"] != "weather" || wx["number"] != "1.1" || wx["name"] != "Airwaves Weather" || wx["callSign"] != "WX" ||
		wx["category"] != "Weather" || wx["enabled"] != true {
		t.Fatalf("weather: %v", wx)
	}

	if code, _, raw := h.do("PUT", "/admin/api/channels/1.1", map[string]any{"kind": "weather", "number": "1.5", "name": "Weather"}); code != 409 ||
		!strings.Contains(raw, "taken by Fish") {
		t.Errorf("taken: %d %s", code, raw)
	}
	code, ch, raw := h.do("PUT", "/admin/api/channels/1.1", map[string]any{
		"kind": "weather", "number": "1.7", "name": "Weather Now", "callSign": "WTHR", "description": "The forecast.", "enabled": false,
	})
	if code != 200 || ch["number"] != "1.7" || ch["name"] != "Weather Now" || ch["callSign"] != "WTHR" || ch["category"] != "Weather" ||
		ch["description"] != "The forecast." || ch["enabled"] != false || ch["kind"] != "weather" {
		t.Fatalf("weather saved: %d %s", code, raw)
	}
	if m := record(t, filepath.Join(h.root, vchan.WeatherFile)); m["number"] != "1.7" || m["name"] != "Weather Now" || m["callSign"] != "WTHR" || m["enabled"] != false {
		t.Errorf(".weather.json: %v", m)
	}

	// It's at its new number now, and other channels can't have it.
	if code, _, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "1.7", "name": "Clash"}); code != 409 || !strings.Contains(raw, "taken by Weather Now") {
		t.Errorf("weather's number: %d %s", code, raw)
	}
	if code, _, raw := h.do("PUT", "/admin/api/channels/1.1", map[string]any{"kind": "weather", "number": "1.1", "name": "Weather"}); code != 404 {
		t.Errorf("old number: %d %s", code, raw)
	}
	_, sched, raw := h.do("GET", "/admin/api/channels/1.7/schedule", nil)
	if progs, _ := sched["programs"].([]any); len(progs) == 0 || progs[0].(map[string]any)["title"] != "Local Forecast" {
		t.Errorf("weather schedule: %s", raw)
	}
	// A folder named for its number gives way.
	if err := os.Mkdir(filepath.Join(h.root, "1.7 Clash"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.lib.Rescan()
	for _, e := range h.lib.Entries() {
		if e.Folder == "1.7 Clash" && e.Channel.Number() == "1.7" {
			t.Error("a folder took the weather channel's number")
		}
	}
}

func TestAdminAntennaCollisions(t *testing.T) {
	h := newHarness(t)
	h.admin.Antenna = func(context.Context) map[string]string {
		return map[string]string{"9.1": "KUSA", "1.2": "KTEST", "7.1": "KMGH"}
	}
	_, state, raw := h.do("GET", "/admin/api/state", nil)
	list := state["antenna"].([]any)
	if len(list) != 3 || list[0].(map[string]any)["number"] != "1.2" || list[2].(map[string]any)["name"] != "KUSA" {
		t.Errorf("antenna: %s", raw)
	}
	if c := h.channel("1.2"); c["antenna"] != "KTEST" {
		t.Errorf("collision: %v", c)
	}
	// Taking an antenna channel's number is allowed, with a warning.
	code, ch, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "7.1", "name": "Cartoons"})
	if code != 200 || ch["antenna"] != "KMGH" {
		t.Errorf("save over an antenna channel: %d %s", code, raw)
	}
}

func TestAdminLogos(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.root, "1.2 Cat Calming")
	code, ch, raw := h.send("PUT", "/admin/api/channels/1.2/logo", "image/png", pngLogo)
	logo, _ := ch["logo"].(string)
	if code != 200 || !strings.HasPrefix(logo, "/admin/api/channels/1.2/logo?v=") {
		t.Fatalf("upload: %d %s", code, raw)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "logo.png")); !bytes.Equal(got, pngLogo) {
		t.Errorf("logo.png: %q", got)
	}
	if m := record(t, filepath.Join(dir, vchan.DetailsFile)); m["logo"] != "logo.png" {
		t.Errorf("channel.json: %v", m)
	}
	resp, body := h.get(logo)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || body != string(pngLogo) ||
		resp.Header.Get("ETag") == "" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("served: %d %v", resp.StatusCode, resp.Header)
	}

	// An SVG sent in a form takes its place.
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("note", "hi")
	fw, _ := mw.CreateFormFile("logo", "cat.svg")
	io.WriteString(fw, "<?xml version=\"1.0\"?>\n<!-- a cat -->\n<svg xmlns=\"http://www.w3.org/2000/svg\"><circle r=\"4\"/></svg>")
	mw.Close()
	code, ch, raw = h.send("POST", "/admin/api/channels/1.2/logo", mw.FormDataContentType(), form.Bytes())
	if code != 200 || ch["logo"] == logo {
		t.Fatalf("form upload: %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "logo.png")); !os.IsNotExist(err) {
		t.Error("the old logo is still there")
	}
	if m := record(t, filepath.Join(dir, vchan.DetailsFile)); m["logo"] != "logo.svg" {
		t.Errorf("channel.json: %v", m)
	}
	if resp, _ := h.get(ch["logo"].(string)); resp.Header.Get("Content-Type") != "image/svg+xml" {
		t.Errorf("svg: %v", resp.Header)
	}

	// Not an image, too big, or no file.
	if code, _, raw := h.send("PUT", "/admin/api/channels/1.2/logo", "text/plain", []byte("hello")); code != http.StatusUnsupportedMediaType || !strings.Contains(raw, "PNG, JPEG, SVG or WebP") {
		t.Errorf("text: %d %s", code, raw)
	}
	big := append(slicesClone(pngLogo), make([]byte, maxLogo)...)
	if code, _, raw := h.send("PUT", "/admin/api/channels/1.2/logo", "image/png", big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("too big: %d %s", code, raw)
	}
	form.Reset()
	mw = multipart.NewWriter(&form)
	_ = mw.WriteField("note", "no file")
	mw.Close()
	if code, _, raw := h.send("POST", "/admin/api/channels/1.2/logo", mw.FormDataContentType(), form.Bytes()); code != 400 || !strings.Contains(raw, "no image file") {
		t.Errorf("no file: %d %s", code, raw)
	}
	if code, _, _ := h.send("PUT", "/admin/api/channels/4.4/logo", "image/png", pngLogo); code != 404 {
		t.Errorf("no such channel: %d", code)
	}

	// The logo moves with the folder.
	code, ch, raw = h.do("PUT", "/admin/api/channels/1.2", map[string]any{"kind": "folder", "number": "1.3", "name": "Cats"})
	if code != 200 || !strings.HasPrefix(ch["logo"].(string), "/admin/api/channels/1.3/logo?v=") {
		t.Fatalf("renamed: %d %s", code, raw)
	}
	if resp, _ := h.get("/admin/api/channels/1.3/logo"); resp.StatusCode != 200 {
		t.Errorf("after the rename: %d", resp.StatusCode)
	}

	// Removing it.
	code, ch, raw = h.do("DELETE", "/admin/api/channels/1.3/logo", nil)
	if code != 200 || ch["logo"] != nil {
		t.Errorf("remove: %d %s", code, raw)
	}
	if left, _ := filepath.Glob(filepath.Join(h.root, "1.3 Cats", "logo.*")); len(left) != 0 {
		t.Errorf("left: %v", left)
	}
	if m := record(t, filepath.Join(h.root, "1.3 Cats", vchan.DetailsFile)); m["logo"] != nil {
		t.Errorf("channel.json: %v", m)
	}
	if resp, _ := h.get("/admin/api/channels/1.3/logo"); resp.StatusCode != 404 {
		t.Errorf("removed logo: %d", resp.StatusCode)
	}

	// The weather channel's goes beside its record.
	h.withWeather()
	if code, ch, raw := h.send("PUT", "/admin/api/channels/1.1/logo", "image/png", pngLogo); code != 200 || ch["kind"] != "weather" || ch["logo"] == nil {
		t.Errorf("weather logo: %d %s", code, raw)
	}
	if _, err := os.Stat(filepath.Join(h.root, ".weather-logo.png")); err != nil {
		t.Error(err)
	}
	if m := record(t, filepath.Join(h.root, vchan.WeatherFile)); m["logo"] != ".weather-logo.png" {
		t.Errorf(".weather.json: %v", m)
	}

	// Deleting a channel takes its details and logo with it.
	if code, _, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "1.4", "name": "Cartoons", "callSign": "TOON"}); code != 200 {
		t.Fatalf("new: %d %s", code, raw)
	}
	if code, _, raw := h.send("PUT", "/admin/api/channels/1.4/logo", "image/png", pngLogo); code != 200 {
		t.Fatalf("logo: %d %s", code, raw)
	}
	if code, _, raw := h.do("DELETE", "/admin/api/channels/1.4", nil); code != http.StatusNoContent {
		t.Errorf("delete: %d %s", code, raw)
	}
	if left, err := os.ReadDir(filepath.Join(h.root, "1.4 Cartoons")); !os.IsNotExist(err) {
		t.Errorf("left behind: %v", left)
	}
}

func slicesClone(b []byte) []byte { return append([]byte(nil), b...) }

func TestAdminLogoFromSource(t *testing.T) {
	h := newHarness(t)
	if code, _, raw := h.do("PUT", "/admin/api/account", map[string]string{"server": h.jf.URL, "user": "viewer", "password": "s3cret-pw"}); code != 200 {
		t.Fatalf("account: %d %s", code, raw)
	}
	// Family Feud has no poster, so Futurama's is taken.
	if code, _, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "1.4", "name": "Cartoons",
		"series": []string{"Family Feud (1976)", "Futurama (1999)"}}); code != 200 {
		t.Fatalf("new: %d %s", code, raw)
	}
	code, ch, raw := h.do("POST", "/admin/api/channels/1.4/logo/source", nil)
	if code != 200 || ch["logo"] == nil {
		t.Fatalf("from Jellyfin: %d %s", code, raw)
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "1.4 Cartoons", "logo.jpg")); string(got) != "\xff\xd8\xff\xe0jpeg fut Primary 400" {
		t.Errorf("logo.jpg: %q", got)
	}

	// A YouTube channel's first channel's avatar, asked for bigger.
	seedYouTube(t, filepath.Join(h.root, "1.8 Northernlion"))
	h.lib.Rescan()
	var asked string
	img := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.Write(pngLogo)
	}))
	defer img.Close()
	h.admin.remember("https://www.youtube.com/@Northernlion", lookedUp{
		ch: ytChannelJSON{ID: "UC3tNpTOHsTnkmbwztCs30sA", URL: "https://www.youtube.com/@Northernlion", Image: img.URL + "/nl=s176-c-k-c0x00ffffff-no-rj"},
		at: time.Now(),
	})
	code, ch, raw = h.do("POST", "/admin/api/channels/1.8/logo/source", nil)
	if code != 200 || ch["logo"] == nil || asked != "/nl=s400-c-k-c0x00ffffff-no-rj" {
		t.Fatalf("from YouTube: %d %s (asked for %q)", code, raw, asked)
	}
	if got, _ := os.ReadFile(filepath.Join(h.root, "1.8 Northernlion", "logo.png")); !bytes.Equal(got, pngLogo) {
		t.Errorf("logo.png: %q", got)
	}
	if code, _, raw := h.do("POST", "/admin/api/channels/1.2/logo/source", nil); code != 400 {
		t.Errorf("folder channel: %d %s", code, raw)
	}
	h.settle("1.8")
}

// TestAdminSchedule: a channel's schedule saves in its channel.json with
// the details, checked, kept when left out and taken away with null, and
// the guide shows the time off the air.
func TestAdminSchedule(t *testing.T) {
	h := newHarness(t)
	if code, _, raw := h.do("PUT", "/admin/api/account", map[string]string{"server": h.jf.URL, "user": "viewer", "password": "s3cret-pw"}); code != 200 {
		t.Fatalf("account: %s", raw)
	}
	path := filepath.Join(h.root, "1.2 Cat Calming", vchan.DetailsFile)
	put := func(number string, body map[string]any) (int, map[string]any, string) {
		h.t.Helper()
		return h.do("PUT", "/admin/api/channels/"+number, body)
	}
	folder := func(more map[string]any) map[string]any {
		b := map[string]any{"kind": "folder", "number": "1.2", "name": "Cat Calming"}
		for k, v := range more {
			b[k] = v
		}
		return b
	}

	code, ch, raw := put("1.2", folder(map[string]any{"schedule": map[string]any{
		"start": " 2026-10-04 ", "first": 2, "blocks": []any{map[string]any{"days": []string{"Fri", " sat"}, "at": "20:00", "count": 4}},
	}}))
	if code != 200 {
		t.Fatalf("save: %d %s", code, raw)
	}
	want := `{"blocks":[{"at":"20:00","count":4,"days":["fri","sat"]}],"first":2,"start":"2026-10-04"}`
	if got, _ := json.Marshal(ch["schedule"]); string(got) != want {
		t.Errorf("schedule:\n%s\nwant\n%s", got, want)
	}
	if got, _ := json.Marshal(record(t, path)["schedule"]); string(got) != want {
		t.Errorf("channel.json: %s", got)
	}

	// Details saved without one keep it.
	if code, ch, raw := put("1.2", folder(map[string]any{"callSign": "CATS"})); code != 200 || ch["schedule"] == nil || ch["callSign"] != "CATS" {
		t.Errorf("details alone: %d %s", code, raw)
	}
	for _, c := range []struct {
		schedule any
		want     string
	}{
		{map[string]any{"start": "Oct 4"}, "the schedule: the start"},
		{map[string]any{"start": "2026-10-04", "blocks": []any{map[string]any{"at": "8pm", "count": 4}}}, "the schedule: block 1 starts at"},
		{map[string]any{"start": "2026-10-04", "blocks": []any{map[string]any{"at": "20:00"}}}, "needs a count or a time to stop"},
		{map[string]any{"start": "2026-10-04", "blocks": []any{map[string]any{"at": "20:00", "count": 101}}}, "airs 1 to 100 items"},
		{map[string]any{"start": "2026-10-04", "repeat": true}, `unknown field`},
		{"tonight", "the schedule should be"},
	} {
		if code, _, raw := put("1.2", folder(map[string]any{"schedule": c.schedule})); code != 400 || !strings.Contains(raw, c.want) {
			t.Errorf("%v: %d %s", c.schedule, code, raw)
		}
	}
	if m := record(t, path); m["schedule"] == nil || m["callSign"] != "CATS" {
		t.Errorf("after refusals: %v", m)
	}

	// null takes it away.
	if code, ch, raw := put("1.2", folder(map[string]any{"schedule": nil})); code != 200 || ch["schedule"] != nil {
		t.Errorf("null: %d %s", code, raw)
	}
	if m := record(t, path); m["schedule"] != nil || m["callSign"] != "CATS" {
		t.Errorf("channel.json after null: %v", m)
	}

	// A Jellyfin channel's videos in order, and its guide before it starts.
	code, ch, raw = put("new", map[string]any{"number": "1.4", "name": "Futurama", "series": []string{"Futurama (1999)"}, "order": "aired",
		"schedule": map[string]any{"start": "2027-01-01", "blocks": []any{map[string]any{"at": "20:00", "count": 1}}}})
	if code != 200 || ch["schedule"] == nil {
		t.Fatalf("jellyfin: %d %s", code, raw)
	}
	_, items, raw := h.do("GET", "/admin/api/channels/1.4/items", nil)
	if l, _ := items["items"].([]any); len(l) != 2 || fmt.Sprint(l[0]) != "map[episode:1 n:1 season:1 seconds:1320 subtitle:Space Pilot 3000 title:Futurama]" {
		t.Errorf("items: %s", raw)
	}
	_, sched, raw := h.do("GET", "/admin/api/channels/1.4/schedule", nil)
	if progs, _ := sched["programs"].([]any); len(progs) != 1 || progs[0].(map[string]any)["offAir"] != true ||
		progs[0].(map[string]any)["subtitle"] != "Starts Fri, Jan 1 at 8:00 PM with Futurama, S1 E1." {
		t.Errorf("schedule: %s", raw)
	}
	if now := h.channel("1.4")["now"].(map[string]any); now["title"] != "Off air" {
		t.Errorf("now: %v", now)
	}

	if code, _, raw := h.do("GET", "/admin/api/channels/9.9/items", nil); code != 404 || !strings.Contains(raw, "no such channel") {
		t.Errorf("no channel: %d %s", code, raw)
	}
	wx := h.withWeather()
	if code, _, raw := put(wx.Number(), map[string]any{"kind": "weather", "number": wx.Number(), "name": "Weather",
		"schedule": map[string]any{"start": "2026-10-04"}}); code != 400 || !strings.Contains(raw, "airs around the clock") {
		t.Errorf("weather: %d %s", code, raw)
	}
	if code, _, raw := h.do("GET", "/admin/api/channels/"+wx.Number()+"/items", nil); code != 404 || !strings.Contains(raw, "doesn't play its videos in order") {
		t.Errorf("weather items: %d %s", code, raw)
	}
}
