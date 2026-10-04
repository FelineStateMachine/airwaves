package admin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// backupHarness is a harness that keeps backups, making one delay after
// changes through the API.
func backupHarness(t *testing.T, delay time.Duration) *harness {
	return newHarness(t, func(s *Server) {
		s.Backups = filepath.Join(t.TempDir(), "backups")
		s.Version = "9.9.9"
		s.bk.delay = delay
	})
}

// write writes a file under root, making its folder.
func write(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// entry is a file in a test archive.
type entry struct {
	name string
	body string
	typ  byte // tar.TypeReg when 0
	link string
}

var legacyTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)

// tarball makes a tar.gz of entries.
func tarball(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link, ModTime: legacyTime}
		if hdr.Typeflag == 0 {
			hdr.Typeflag = tar.TypeReg
		}
		if hdr.Typeflag != tar.TypeReg {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type unpacked struct {
	mode int64
	typ  byte
	body string
}

// untar lists a tar.gz's entries by name.
func untar(t *testing.T, data []byte) (map[string]unpacked, []string) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]unpacked{}
	var order []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[hdr.Name] = unpacked{hdr.Mode, hdr.Typeflag, string(body)}
		order = append(order, hdr.Name)
	}
	return out, order
}

// backups lists the harness's backups.
func (h *harness) backups() []map[string]any {
	h.t.Helper()
	code, res, raw := h.do("GET", "/admin/api/backups", nil)
	if code != 200 {
		h.t.Fatalf("backups: %d %s", code, raw)
	}
	var out []map[string]any
	for _, b := range res["backups"].([]any) {
		out = append(out, b.(map[string]any))
	}
	return out
}

// restore asks to restore a backup.
func (h *harness) restore(file string, req map[string]any) (int, restorePlan, string) {
	h.t.Helper()
	code, _, raw := h.do("POST", "/admin/api/backups/"+file+"/restore", req)
	var p restorePlan
	_ = json.Unmarshal([]byte(raw), &p)
	return code, p, raw
}

func item(t *testing.T, p restorePlan, key string) *restoreItem {
	t.Helper()
	for _, x := range p.Items {
		if x.Key == key {
			return x
		}
	}
	t.Fatalf("no %q in %+v", key, p.Items)
	return nil
}

func TestBackupContents(t *testing.T) {
	h := backupHarness(t, time.Hour)
	root := h.root
	write(t, root, "1.2 Cat Calming/Birds [abc].mp4", "video")
	write(t, root, "1.2 Cat Calming/Birds [abc].info.json", `{"title": "Birds"}`)
	write(t, root, "1.2 Cat Calming/channel.json", `{"callSign": "CATS"}`)
	write(t, root, "1.2 Cat Calming/logo.png", string(pngLogo))
	write(t, root, "1.3 Fireplace/fire.mkv", "video")
	write(t, root, "1.4 Cartoons/jellyfin.json", `{"series": ["Futurama (1999)"], "order": "shuffle"}`)
	write(t, root, "1.8 Northernlion/youtube.json", `{"channels": ["https://www.youtube.com/@Northernlion"]}`)
	write(t, root, "1.8 Northernlion/.catalog.json", `{"sources": []}`)
	write(t, root, "1.8 Northernlion/.playout.json", `{"airings": []}`)
	write(t, root, "1.8 Northernlion/.playout.json.tmp", `{}`)
	write(t, root, "1.8 Northernlion/.tmp-42", `{}`)
	write(t, root, "1.8 Northernlion/logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	write(t, root, ".trash/jellyfin.json", `{}`)
	write(t, root, ".jellyfin.json", `{"server": "http://127.0.0.1:1", "user": "viewer", "password": "pw"}`)
	write(t, root, ".weather.json", `{"number": "1.1", "callSign": "WX"}`)
	write(t, root, ".weather-logo.png", string(pngLogo))
	write(t, root, "notes.txt", "hi")
	write(t, root, "extra.json", "{}")

	code, b, raw := h.do("POST", "/admin/api/backups", map[string]string{"label": "  before\tthe   move "})
	if code != 201 || b["reason"] != "manual" || b["label"] != "before the move" || b["auto"] != false || b["airwavesd"] != "9.9.9" ||
		b["account"] != true || b["weather"] != true || b["schedules"] != true {
		t.Fatalf("backup: %d %s", code, raw)
	}
	file := b["file"].(string)
	if !strings.HasPrefix(file, "airwaves-") || !strings.HasSuffix(file, "-manual.tar.gz") {
		t.Errorf("file: %s", file)
	}
	if info, err := os.Stat(h.admin.Backups); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("backups folder: %v %v", info.Mode(), err)
	}
	path := filepath.Join(h.admin.Backups, file)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("backup file: %v %v", info.Mode(), err)
	}
	if strings.Contains(raw, "pw") {
		t.Error("the listing gave the password away")
	}

	data, _ := os.ReadFile(path)
	got, order := untar(t, data)
	want := []string{
		manifestFile, ".jellyfin.json", ".weather-logo.png", ".weather.json",
		"1.2 Cat Calming/", "1.2 Cat Calming/channel.json", "1.2 Cat Calming/logo.png",
		"1.3 Fireplace/",
		"1.4 Cartoons/", "1.4 Cartoons/jellyfin.json",
		"1.8 Northernlion/", "1.8 Northernlion/.playout.json", "1.8 Northernlion/logo.svg", "1.8 Northernlion/youtube.json",
	}
	if !slices.Equal(order, want) {
		t.Errorf("entries:\n%q\nwant\n%q", order, want)
	}
	for name, mode := range map[string]int64{".jellyfin.json": 0o600, "1.4 Cartoons/jellyfin.json": 0o600, "1.8 Northernlion/youtube.json": 0o644, "1.2 Cat Calming/": 0o755} {
		if got[name].mode != mode {
			t.Errorf("%s: mode %o", name, got[name].mode)
		}
	}
	if got["1.4 Cartoons/jellyfin.json"].body != `{"series": ["Futurama (1999)"], "order": "shuffle"}` || got["1.3 Fireplace/"].typ != tar.TypeDir {
		t.Errorf("files: %+v", got)
	}
	var m manifest
	if err := json.Unmarshal([]byte(got[manifestFile].body), &m); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, f := range m.Folders {
		kinds[f.Folder] = f.Kind
	}
	if m.Version != 1 || m.Airwavesd != "9.9.9" || m.Reason != "manual" || m.Label != "before the move" || time.Since(m.Created) > time.Minute ||
		!m.Account || !m.Weather || !m.Schedules ||
		!maps.Equal(kinds, map[string]string{"1.2 Cat Calming": "folder", "1.3 Fireplace": "folder", "1.4 Cartoons": "jellyfin", "1.8 Northernlion": "youtube"}) {
		t.Errorf("manifest: %s", got[manifestFile].body)
	}

	// The download is the file.
	resp, body := h.get("/admin/api/backups/" + file)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/gzip" || body != string(data) ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `filename=`+file) {
		t.Errorf("download: %d %v", resp.StatusCode, resp.Header)
	}

	// A second backup by hand is made even with nothing changed; an
	// automatic one isn't.
	if code, b2, _ := h.do("POST", "/admin/api/backups", nil); code != 201 || b2["file"] == file {
		t.Errorf("second: %d %v", code, b2)
	}
	if b, made, err := h.admin.backUp(daily, ""); err != nil || made || b.Reason != "manual" {
		t.Errorf("daily with nothing changed: %v %v %+v", made, err, b)
	}
	// A schedule moving on isn't a change either.
	write(t, root, "1.8 Northernlion/.playout.json", `{"airings": [], "basis": "later"}`)
	if _, made, _ := h.admin.backUp(daily, ""); made {
		t.Error("a schedule moving on made a backup")
	}
	write(t, root, "1.5 New/video.mp4", "video")
	if b, made, err := h.admin.backUp(daily, ""); err != nil || !made || b.Reason != "daily" || !b.Auto || len(b.Folders) != 5 {
		t.Errorf("daily after a change by hand: %v %v %+v", made, err, b)
	}
	list := h.backups()
	if len(list) != 3 || list[0]["reason"] != "daily" {
		t.Errorf("list: %v", list)
	}
}

func TestBackupEndpoints(t *testing.T) {
	off := newHarness(t)
	if code, _, raw := off.do("GET", "/admin/api/backups", nil); code != 503 || !strings.Contains(raw, "backups are off") {
		t.Errorf("off: %d %s", code, raw)
	}

	h := backupHarness(t, time.Hour)
	if list := h.backups(); len(list) != 0 {
		t.Errorf("none yet: %v", list)
	}
	if code, _, raw := h.do("POST", "/admin/api/backups", map[string]string{"label": strings.Repeat("x", 61)}); code != 400 || !strings.Contains(raw, "up to 60") {
		t.Errorf("long label: %d %s", code, raw)
	}
	_, b, _ := h.do("POST", "/admin/api/backups", map[string]string{"label": "one"})
	file := b["file"].(string)
	_, res, _ := h.do("GET", "/admin/api/backups", nil)
	if res["folder"] != h.admin.Backups || res["pending"] != false {
		t.Errorf("list: %v", res)
	}
	for _, bad := range []string{"..%2F..%2Fetc%2Fpasswd", "nope.tar.gz", ".hidden.tar.gz", "x.zip"} {
		if resp, _ := h.get("/admin/api/backups/" + bad); resp.StatusCode != 404 {
			t.Errorf("GET %s: %d", bad, resp.StatusCode)
		}
		if code, _, _ := h.do("DELETE", "/admin/api/backups/"+bad, nil); code != 404 {
			t.Errorf("DELETE %s: %d", bad, code)
		}
		if code, _, _ := h.restore(bad, map[string]any{"preview": true}); code != 404 {
			t.Errorf("restore %s: %d", bad, code)
		}
	}
	if code, _, raw := h.restore(file, map[string]any{"preview": true, "channels": []string{"9.9 Nothing"}}); code != 400 || !strings.Contains(raw, "9.9 Nothing") {
		t.Errorf("unknown channel: %d %s", code, raw)
	}
	if code, _, raw := h.send("POST", "/admin/api/backups/upload?name=junk.tar.gz", "application/gzip", []byte("not a backup")); code != 400 ||
		!strings.Contains(raw, "isn't an Airwaves backup") {
		t.Errorf("junk: %d %s", code, raw)
	}
	if code, _, raw := h.send("POST", "/admin/api/backups/upload", "application/gzip", nil); code != 400 {
		t.Errorf("nothing: %d %s", code, raw)
	}
	if code, _, _ := h.do("DELETE", "/admin/api/backups/"+file, nil); code != 204 {
		t.Errorf("delete: %d", code)
	}
	if code, _, _ := h.do("DELETE", "/admin/api/backups/"+file, nil); code != 404 {
		t.Errorf("delete again: %d", code)
	}
	if list := h.backups(); len(list) != 0 {
		t.Errorf("deleted: %v", list)
	}
	// A file that isn't a backup is listed, so it can be deleted, but not
	// restored or pruned.
	write(t, h.admin.Backups, "broken.tar.gz", "garbage")
	list := h.backups()
	if len(list) != 1 || list[0]["error"] == nil || list[0]["auto"] != false {
		t.Errorf("broken: %v", list)
	}
	if code, _, _ := h.restore("broken.tar.gz", map[string]any{"preview": true}); code != 422 {
		t.Errorf("restore broken: %d", code)
	}
}

func TestBackupAfterChanges(t *testing.T) {
	h := backupHarness(t, 150*time.Millisecond)
	cat := func(callSign string) (int, string) {
		code, _, raw := h.do("PUT", "/admin/api/channels/1.2", map[string]any{"kind": "folder", "number": "1.2", "name": "Cat Calming", "callSign": callSign})
		return code, raw
	}
	pending := func() bool {
		_, res, _ := h.do("GET", "/admin/api/backups", nil)
		return res["pending"] == true
	}
	wait := func(n int) []map[string]any {
		t.Helper()
		for range 100 {
			if list := h.backups(); len(list) >= n && !pending() {
				time.Sleep(300 * time.Millisecond) // and no more come
				return h.backups()
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("no backup %d: %v", n, h.backups())
		return nil
	}

	// Reading changes nothing.
	h.do("GET", "/admin/api/state", nil)
	if pending() {
		t.Error("a GET made a backup wait")
	}
	// A failed change changes nothing.
	if code, _ := cat("NOT A CALL SIGN AT ALL"); code != 400 || pending() {
		t.Errorf("failed change: %d %v", code, pending())
	}
	// A burst of changes makes one backup.
	for _, cs := range []string{"C1", "C2", "C3"} {
		if code, raw := cat(cs); code != 200 {
			t.Fatalf("%s: %s", cs, raw)
		}
	}
	if !pending() {
		t.Error("nothing waiting after changes")
	}
	list := wait(1)
	if len(list) != 1 || list[0]["reason"] != "change" || list[0]["auto"] != true {
		t.Fatalf("after a burst: %v", list)
	}
	data, _ := os.ReadFile(filepath.Join(h.admin.Backups, list[0]["file"].(string)))
	if got, _ := untar(t, data); !strings.Contains(got["1.2 Cat Calming/channel.json"].body, `"C3"`) {
		t.Errorf("backed up: %+v", got)
	}
	// Saving the same again makes none.
	cat("C3")
	if list := wait(1); len(list) != 1 {
		t.Errorf("unchanged: %v", list)
	}
	// Nor do the backups' own requests.
	h.do("POST", "/admin/api/backups", nil)
	if pending() {
		t.Error("a backup by hand made another wait")
	}
	// An agent's change is backed up too.
	ok(t, h.agent(), "set_channel_details", map[string]any{"number": "1.2", "callSign": "C9"})
	if list := wait(3); len(list) != 3 || list[0]["reason"] != "change" {
		t.Errorf("after an agent's change: %v", list)
	}
}

func TestBackupWhenStopping(t *testing.T) {
	h := backupHarness(t, time.Hour)
	h.do("PUT", "/admin/api/channels/1.2", map[string]any{"kind": "folder", "number": "1.2", "name": "Cat Calming", "callSign": "CATS"})
	if !h.admin.backupWaiting() {
		t.Fatal("nothing waiting")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.admin.Run(ctx)
		close(done)
	}()
	cancel()
	<-done
	if list := h.backups(); len(list) != 1 || list[0]["reason"] != "change" {
		t.Errorf("the waiting change wasn't backed up: %v", list)
	}
	// Then no more are made by themselves.
	h.do("PUT", "/admin/api/channels/1.2", map[string]any{"kind": "folder", "number": "1.2", "name": "Cat Calming", "callSign": "MEOW"})
	if h.admin.backupWaiting() {
		t.Error("a backup waits after stopping")
	}
}

func TestBackupRetention(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	var list []backupJSON
	// Five automatic backups a day for 20 days, newest first, a backup by
	// hand each day, and one that can't be read.
	for d := range 20 {
		for i := range 5 {
			at := now.AddDate(0, 0, -d).Add(-time.Duration(i) * time.Hour)
			list = append(list, backupJSON{File: at.Format("0102-15"), Created: at, Reason: afterChange, Auto: true})
		}
		at := now.AddDate(0, 0, -d).Add(-30 * time.Minute)
		list = append(list, backupJSON{File: at.Format("0102-1504") + "-manual", Created: at, Reason: byHand})
	}
	list = append(list, backupJSON{File: "broken", Created: now.AddDate(-1, 0, 0), Reason: daily, Auto: true, Error: "damaged"})
	slices.SortStableFunc(list, func(a, b backupJSON) int { return b.Created.Compare(a.Created) })

	drop := map[string]bool{}
	for _, b := range prune(list, now) {
		if !b.Auto {
			t.Errorf("dropped %s, made by hand", b.File)
		}
		drop[b.File] = true
	}
	// Days 0 to 5 are the newest 30; days 6 to 13 keep their newest; the
	// rest go.
	if len(drop) != 100-30-8 {
		t.Errorf("dropped %d", len(drop))
	}
	for file, gone := range map[string]bool{
		"1020-12": false, "1015-08": false, // the 30th newest
		"1014-12": false, "1014-11": true, // day 6: its newest stays
		"1007-12": false, "1007-11": true, // day 13, the last
		"1006-12": true, "1001-12": true, // older
		"broken": false,
	} {
		if drop[file] != gone {
			t.Errorf("%s: dropped %v", file, drop[file])
		}
	}

	// On disk: the newest 30 stay, and those made by hand.
	h := backupHarness(t, time.Hour)
	st, _ := readSetup(h.root)
	start := time.Now().Add(-40 * time.Second)
	for i := range 35 {
		if _, err := h.admin.saveBackup(st.manifest(afterChange, "", "", start.Add(time.Duration(i)*time.Second)), st); err != nil {
			t.Fatal(err)
		}
	}
	h.admin.saveBackup(st.manifest(byHand, "keep", "", start.Add(-time.Hour)), st)
	h.admin.pruneBackups()
	left := h.backups()
	if len(left) != 31 || left[30]["reason"] != "manual" || left[0]["created"].(string) < left[29]["created"].(string) {
		t.Errorf("left %d: %v", len(left), left)
	}
}

// seedSetup gives the harness a setup to back up: the account, a
// Jellyfin, a YouTube, a folder and a video-only channel, and the weather
// channel.
func seedSetup(t *testing.T, h *harness) {
	t.Helper()
	h.withWeather()
	write(t, h.root, ".weather.json", `{"number": "1.1", "name": "Airwaves Weather", "callSign": "WX"}`)
	if code, _, raw := h.do("PUT", "/admin/api/account", map[string]string{"server": h.jf.URL, "user": "viewer", "password": "s3cret-pw"}); code != 200 {
		t.Fatal(raw)
	}
	if code, _, raw := h.do("PUT", "/admin/api/channels/new", map[string]any{"number": "1.4", "name": "Cartoons", "series": []string{"Futurama (1999)"}, "callSign": "TOON"}); code != 200 {
		t.Fatal(raw)
	}
	write(t, h.root, "1.8 Northernlion/youtube.json", `{"channels": ["https://www.youtube.com/@Northernlion", "https://www.youtube.com/@TheLibraryofLetourneau"]}`)
	write(t, h.root, "1.8 Northernlion/.playout.json", `{"basis": "backed up", "airings": []}`)
	write(t, h.root, "1.8 Northernlion/.catalog.json", `{"sources": []}`)
	write(t, h.root, "1.2 Cat Calming/channel.json", `{"callSign": "CATS", "category": "Pets"}`)
	write(t, h.root, "1.2 Cat Calming/birds.mp4", "video")
	write(t, h.root, "1.3 Fireplace/fire.mkv", "video")
}

func TestRestore(t *testing.T) {
	h := backupHarness(t, time.Hour)
	seedSetup(t, h)
	root := h.root
	account := read(t, filepath.Join(root, accountFile))
	cartoons := read(t, filepath.Join(root, "1.4 Cartoons", channelFile))
	_, b, _ := h.do("POST", "/admin/api/backups", map[string]string{"label": "good"})
	file := b["file"].(string)

	// Then things go wrong: a channel lost, one renamed, one's details
	// gone and a logo added, a video-only folder lost, the weather
	// channel moved, the account changed, and a channel made.
	os.RemoveAll(filepath.Join(root, "1.4 Cartoons"))
	if err := os.Rename(filepath.Join(root, "1.8 Northernlion"), filepath.Join(root, "1.9 NL")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "1.9 NL/.playout.json", `{"basis": "mine", "airings": []}`)
	os.Remove(filepath.Join(root, "1.2 Cat Calming", "channel.json"))
	write(t, root, "1.2 Cat Calming/logo.png", string(pngLogo))
	os.RemoveAll(filepath.Join(root, "1.3 Fireplace"))
	write(t, root, ".weather.json", `{"number": "1.7", "name": "Weather", "callSign": "WTHR"}`)
	write(t, root, accountFile, `{"server": "http://127.0.0.1:1", "user": "someone", "password": "x"}`)
	write(t, root, "1.6 New/youtube.json", `{"channels": ["https://www.youtube.com/@Someone"]}`)
	h.lib.Rescan()

	code, p, raw := h.restore(file, map[string]any{"preview": true})
	if code != 200 || !p.Preview || !p.Schedules || p.Errors != 0 || p.Before != "" {
		t.Fatalf("preview: %d %s", code, raw)
	}
	if strings.Contains(raw, "s3cret-pw") {
		t.Error("the preview gave the password away")
	}
	cat, fire, cart, nl, wx := item(t, p, "1.2 Cat Calming"), item(t, p, "1.3 Fireplace"), item(t, p, "1.4 Cartoons"), item(t, p, "1.8 Northernlion"), item(t, p, weatherKey)
	if cat.Action != "change" || !slices.Equal(cat.Changes, []string{"details", "logo"}) || cat.Match != "name" || cat.Now.Number != "1.2" {
		t.Errorf("cat: %+v", cat)
	}
	if fire.Action != "add" || !fire.Empty || fire.Kind != "folder" || fire.Now != nil {
		t.Errorf("fireplace: %+v", fire)
	}
	if cart.Action != "add" || cart.Kind != "jellyfin" || !slices.Equal(cart.Changes, []string{"settings", "details"}) {
		t.Errorf("cartoons: %+v", cart)
	}
	// Found by its YouTube channels, under its new name; its schedule is
	// running, so it stays.
	if nl.Action != "change" || nl.Match != "source" || nl.Now.Folder != "1.9 NL" || nl.Now.Number != "1.9" || !slices.Equal(nl.Changes, []string{"rename"}) {
		t.Errorf("northernlion: %+v %+v", nl, nl.Now)
	}
	if wx.Action != "change" || wx.Number != "1.1" || wx.Now.Number != "1.7" || !slices.Equal(wx.Changes, []string{"details"}) {
		t.Errorf("weather: %+v %+v", wx, wx.Now)
	}
	if len(p.Others) != 1 || p.Others[0].Folder != "1.6 New" {
		t.Errorf("others: %+v", p.Others)
	}
	if a := p.Account; !a.InBackup || a.Same || a.Restore || a.Server != h.jf.URL || a.User != "viewer" {
		t.Errorf("account: %+v", a)
	}
	if exists(filepath.Join(root, "1.4 Cartoons")) || len(h.backups()) != 1 {
		t.Error("the preview changed something")
	}

	code, p, raw = h.restore(file, map[string]any{})
	if code != 200 || p.Preview || p.Errors != 0 || !strings.HasSuffix(p.Before, "-restore.tar.gz") {
		t.Fatalf("restore: %d %s", code, raw)
	}
	for _, x := range p.Items {
		if x.Done != (x.Action != "same") {
			t.Errorf("%s: %+v", x.Key, x)
		}
	}
	// Made again.
	if got := read(t, filepath.Join(root, "1.4 Cartoons", channelFile)); got != cartoons {
		t.Errorf("cartoons: %s", got)
	}
	if info, _ := os.Stat(filepath.Join(root, "1.4 Cartoons", channelFile)); info.Mode().Perm() != 0o600 {
		t.Errorf("cartoons' settings: %v", info.Mode())
	}
	if entries, err := os.ReadDir(filepath.Join(root, "1.3 Fireplace")); err != nil || len(entries) != 0 {
		t.Errorf("fireplace: %v %v", entries, err)
	}
	// Renamed back, with its catalog and its running schedule.
	if exists(filepath.Join(root, "1.9 NL")) || read(t, filepath.Join(root, "1.8 Northernlion", playoutFile)) != `{"basis": "mine", "airings": []}` ||
		!exists(filepath.Join(root, "1.8 Northernlion", ".catalog.json")) {
		t.Error("northernlion wasn't renamed back as it was")
	}
	// Details back, the logo added since gone, the videos still there.
	if record(t, filepath.Join(root, "1.2 Cat Calming", "channel.json"))["callSign"] != "CATS" || exists(filepath.Join(root, "1.2 Cat Calming", "logo.png")) ||
		read(t, filepath.Join(root, "1.2 Cat Calming", "birds.mp4")) != "video" {
		t.Error("cat calming wasn't put back as it was")
	}
	if record(t, filepath.Join(root, ".weather.json"))["number"] != "1.1" {
		t.Error("the weather channel wasn't put back")
	}
	// Left alone: the account (not asked for) and the new channel.
	if !strings.Contains(read(t, filepath.Join(root, accountFile)), "someone") || !exists(filepath.Join(root, "1.6 New", "youtube.json")) {
		t.Error("the restore touched what it shouldn't")
	}
	numbers := map[string]string{}
	for _, c := range h.do2State() {
		numbers[c["number"].(string)] = c["name"].(string)
	}
	if !maps.Equal(numbers, map[string]string{"1.1": "Airwaves Weather", "1.2": "Cat Calming", "1.3": "Fireplace", "1.4": "Cartoons", "1.6": "New", "1.8": "Northernlion"}) {
		t.Errorf("channels after: %v", numbers)
	}

	// The setup before was backed up first, the account and all.
	_, pre, _ := h.restore(p.Before, map[string]any{"preview": true})
	if pre.Backup.Reason != "restore" || item(t, pre, "1.9 NL").Match != "source" || pre.Account.Server != "http://127.0.0.1:1" {
		t.Errorf("before: %+v", pre)
	}

	// The account, on its own: no channels chosen.
	_, p, raw = h.restore(file, map[string]any{"channels": []string{}, "account": true})
	if !p.Account.Done || read(t, filepath.Join(root, accountFile)) != account {
		t.Errorf("account: %s", raw)
	}
	for _, x := range p.Items {
		if x.Chosen || x.Done {
			t.Errorf("%s restored, not chosen", x.Key)
		}
	}
	if info, _ := os.Stat(filepath.Join(root, accountFile)); info.Mode().Perm() != 0o600 {
		t.Errorf("account file: %v", info.Mode())
	}

	// A YouTube channel made again gets its schedule back, unless asked
	// not to.
	h.settle("1.8")
	os.RemoveAll(filepath.Join(root, "1.8 Northernlion"))
	h.lib.Rescan()
	_, p, raw = h.restore(file, map[string]any{"channels": []string{"1.8 Northernlion"}, "schedules": false})
	if x := item(t, p, "1.8 Northernlion"); !x.Done || x.Action != "add" || exists(filepath.Join(root, "1.8 Northernlion", playoutFile)) {
		t.Errorf("without schedules: %s", raw)
	}
	h.settle("1.8")
	os.RemoveAll(filepath.Join(root, "1.8 Northernlion"))
	h.lib.Rescan()
	_, p, raw = h.restore(file, map[string]any{"channels": []string{"1.8 Northernlion"}})
	if x := item(t, p, "1.8 Northernlion"); !x.Done || !slices.Contains(x.Changes, "schedule") ||
		read(t, filepath.Join(root, "1.8 Northernlion", playoutFile)) != `{"basis": "backed up", "airings": []}` {
		t.Errorf("with schedules: %s", raw)
	}

	// Restoring what's there already changes nothing, and backs nothing up.
	before := len(h.backups())
	_, p, _ = h.restore(file, map[string]any{"channels": []string{"1.2 Cat Calming"}})
	if x := item(t, p, "1.2 Cat Calming"); x.Action != "same" || x.Done || p.Before != "" || len(h.backups()) != before {
		t.Errorf("same: %+v %s", x, p.Before)
	}
}

// do2State lists the channels in the state.
func (h *harness) do2State() []map[string]any {
	h.t.Helper()
	_, st, _ := h.do("GET", "/admin/api/state", nil)
	var out []map[string]any
	for _, c := range st["channels"].([]any) {
		out = append(out, c.(map[string]any))
	}
	return out
}

func TestRestoreNumberTaken(t *testing.T) {
	h := backupHarness(t, time.Hour)
	wx := h.withWeather()
	write(t, h.root, ".weather.json", `{"number": "1.5", "name": "Weather"}`)
	write(t, h.root, "2.0 Foo/youtube.json", `{"channels": ["@Foo"]}`)
	write(t, h.root, "3.0 Kept/youtube.json", `{"channels": ["@Kept"]}`)
	_, b, _ := h.do("POST", "/admin/api/backups", nil)
	file := b["file"].(string)

	// Foo is lost and Bar takes its number; the weather channel moves
	// and a folder takes its old number; Kept moves to 4.0, and Other to
	// its old number.
	os.RemoveAll(filepath.Join(h.root, "2.0 Foo"))
	write(t, h.root, "2.0 Bar/youtube.json", `{"channels": ["@Bar"]}`)
	write(t, h.root, ".weather.json", `{"number": "1.1", "name": "Weather"}`)
	write(t, h.root, "1.5 Kids/video.mp4", "video")
	os.Rename(filepath.Join(h.root, "3.0 Kept"), filepath.Join(h.root, "4.0 Kept"))
	write(t, h.root, "3.0 Other/video.mp4", "video")
	h.lib.Rescan()
	if wx.Number() != "1.1" {
		t.Fatalf("weather at %s", wx.Number())
	}

	_, p, raw := h.restore(file, map[string]any{"preview": true})
	foo, w, kept := item(t, p, "2.0 Foo"), item(t, p, weatherKey), item(t, p, "3.0 Kept")
	if foo.Action != "skip" || foo.Reason != "2.0 is taken by 2.0 Bar now: give that channel another number first, or leave this one out" {
		t.Errorf("foo: %+v", foo)
	}
	if w.Action != "skip" || !strings.HasPrefix(w.Reason, "1.5 is taken by 1.5 Kids") {
		t.Errorf("weather: %+v", w)
	}
	if kept.Action != "skip" || kept.Match != "source" || !strings.HasPrefix(kept.Reason, "3.0 is taken by 3.0 Other") {
		t.Errorf("kept: %+v", kept)
	}
	_, p, raw = h.restore(file, map[string]any{})
	if p.Before != "" || exists(filepath.Join(h.root, "2.0 Foo")) || !exists(filepath.Join(h.root, "4.0 Kept")) {
		t.Errorf("restored what's taken: %s", raw)
	}

	// Once the other channel moves, it's restored.
	os.Rename(filepath.Join(h.root, "3.0 Other"), filepath.Join(h.root, "3.1 Other"))
	h.lib.Rescan()
	_, p, raw = h.restore(file, map[string]any{"channels": []string{"3.0 Kept"}})
	if x := item(t, p, "3.0 Kept"); !x.Done || !exists(filepath.Join(h.root, "3.0 Kept", "youtube.json")) || exists(filepath.Join(h.root, "4.0 Kept")) {
		t.Errorf("kept: %s", raw)
	}
}

func TestRestoreRefusesBadBackups(t *testing.T) {
	h := backupHarness(t, time.Hour)
	outside := filepath.Dir(h.root)
	zeros := strings.Repeat("\x00", 65<<20)
	many := make([]entry, 0, maxBackupFiles+1)
	for i := range maxBackupFiles + 1 {
		many = append(many, entry{name: "1.2 Cat/" + strings.Repeat("v", i%50) + ".mp4", body: "x"})
	}
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"dot dot":     {tarball(t, entry{name: "../evil.json", body: "{}"}), "outside the channels folder"},
		"deep dot":    {tarball(t, entry{name: "1.2 Cat/../../evil.json", body: "{}"}), "outside the channels folder"},
		"absolute":    {tarball(t, entry{name: "/etc/jellyfin.json", body: "{}"}), "outside the channels folder"},
		"backslash":   {tarball(t, entry{name: `..\evil.json`, body: "{}"}), "outside the channels folder"},
		"symlink":     {tarball(t, entry{name: "1.2 Cat/logo.png", typ: tar.TypeSymlink, link: "/etc/passwd"}), "link or device"},
		"hard link":   {tarball(t, entry{name: "1.2 Cat/channel.json", typ: tar.TypeLink, link: "../../x"}), "link or device"},
		"device":      {tarball(t, entry{name: "1.2 Cat/jellyfin.json", typ: tar.TypeChar}), "link or device"},
		"bad JSON":    {tarball(t, entry{name: "1.2 Cat/jellyfin.json", body: "{nope"}), "1.2 Cat/jellyfin.json in the backup is damaged"},
		"not a logo":  {tarball(t, entry{name: "1.2 Cat/logo.png", body: "#!/bin/sh"}), "isn't an image"},
		"newer":       {tarball(t, entry{name: manifestFile, body: `{"version": 99}`}, entry{name: "1.2 Cat/", typ: tar.TypeDir}), "newer Airwaves"},
		"empty":       {tarball(t, entry{name: "notes.txt", body: "hi"}), "no channels"},
		"too big":     {tarball(t, entry{name: "1.2 Cat/video.mp4", body: zeros}), "unpacks to over 64 MB"},
		"many":        {tarball(t, many...), "more than 10000 files"},
		"not gzipped": {[]byte("PK\x03\x04 a zip"), "isn't an Airwaves backup"},
	} {
		code, _, raw := h.send("POST", "/admin/api/backups/upload?name=x.tar.gz", "application/gzip", c.data)
		if code != 400 || !strings.Contains(raw, c.want) {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if exists(filepath.Join(outside, "evil.json")) || exists(filepath.Join(h.root, "1.2 Cat")) || len(h.backups()) != 0 {
		t.Error("a bad backup left something behind")
	}

	// The same dropped into the backups folder by hand can't be restored.
	write(t, h.admin.Backups, "evil.tar.gz", string(tarball(t, entry{name: "../evil.json", body: "{}"})))
	if code, _, raw := h.restore("evil.tar.gz", map[string]any{}); code != 422 || exists(filepath.Join(outside, "evil.json")) {
		t.Errorf("evil: %d %s", code, raw)
	}
}

func TestRestoreOlderBackups(t *testing.T) {
	h := backupHarness(t, time.Hour)
	// As made by hand on a server before Airwaves made its own: the channels
	// folder's *.json but catalogs and yt-dlp's, with no manifest.
	legacy := tarball(t,
		entry{name: "./", typ: tar.TypeDir},
		entry{name: "./.jellyfin.json", body: `{"server": "http://127.0.0.1:1", "user": "viewer", "password": "pw"}`},
		entry{name: "./105.0 Gameshows/jellyfin.json", body: `{"series": ["Family Feud (1976)"]}`},
		entry{name: "./202.0 Boiler Room/.playout.json", body: `{"airings": []}`},
		entry{name: "./202.0 Boiler Room/youtube.json", body: `{"channels": ["https://www.youtube.com/@boilerroom"]}`},
		entry{name: "./1.2 Cat Calming/Birds [x].info.json", body: `{}`},
		entry{name: "./notes.json", body: `{}`},
	)
	code, res, raw := h.send("POST", "/admin/api/backups/upload?name=channels-20260101-1200.tar.gz", "application/gzip", legacy)
	if code != 201 {
		t.Fatalf("upload: %d %s", code, raw)
	}
	b := res["backup"].(map[string]any)
	created, _ := time.Parse(time.RFC3339, b["created"].(string))
	if b["reason"] != "uploaded" || b["label"] != "channels-20260101-1200" || !created.Equal(legacyTime) || b["auto"] != false || b["account"] != true ||
		b["schedules"] != true || len(b["folders"].([]any)) != 2 || b["file"] != "airwaves-20260101-120000-uploaded.tar.gz" {
		t.Errorf("uploaded: %s", raw)
	}
	if skipped := res["skipped"].([]any); len(skipped) != 2 || skipped[0] != "1.2 Cat Calming/Birds [x].info.json" {
		t.Errorf("skipped: %v", skipped)
	}
	// It's kept as an Airwaves backup, with a manifest.
	data, _ := os.ReadFile(filepath.Join(h.admin.Backups, b["file"].(string)))
	if got, order := untar(t, data); order[0] != manifestFile || got["202.0 Boiler Room/youtube.json"].body == "" {
		t.Errorf("kept as %q", order)
	}
	// Uploading it again doesn't make another.
	if code, res, _ := h.send("POST", "/admin/api/backups/upload?name=again.tar.gz", "application/gzip", legacy); code != 200 || res["existing"] != true || len(h.backups()) != 1 {
		t.Errorf("again: %d %v", code, res)
	}

	_, p, raw := h.restore(b["file"].(string), map[string]any{"account": true})
	if p.Errors != 0 || item(t, p, "105.0 Gameshows").Action != "add" || !item(t, p, "202.0 Boiler Room").Done || !p.Account.Done {
		t.Fatalf("restore: %s", raw)
	}
	if read(t, filepath.Join(h.root, "202.0 Boiler Room", playoutFile)) != `{"airings": []}` ||
		!exists(filepath.Join(h.root, "105.0 Gameshows", channelFile)) || !strings.Contains(read(t, filepath.Join(h.root, accountFile)), "127.0.0.1:1") {
		t.Error("not restored")
	}
	if len(p.Others) != 1 || p.Others[0].Folder != "1.2 Cat Calming" || exists(filepath.Join(h.root, "notes.json")) {
		t.Errorf("others: %+v", p.Others)
	}

	// One copied into the backups folder as it is is listed, and restores.
	write(t, h.admin.Backups, "channels-20260101-1200.tar.gz", string(legacy))
	list := h.backups()
	var older map[string]any
	for _, b := range list {
		if b["file"] == "channels-20260101-1200.tar.gz" {
			older = b
		}
	}
	if older == nil || older["older"] != true || older["reason"] != "manual" || older["auto"] != false {
		t.Fatalf("listed: %v", list)
	}
	if code, p, raw := h.restore("channels-20260101-1200.tar.gz", map[string]any{"preview": true}); code != 200 || item(t, p, "105.0 Gameshows").Action != "same" {
		t.Errorf("older: %d %s", code, raw)
	}
}

func TestRestoreKindAndLogos(t *testing.T) {
	h := backupHarness(t, time.Hour)
	write(t, h.root, "5.0 Mixed/youtube.json", `{"channels": ["@Mixed"]}`)
	write(t, h.root, "5.0 Mixed/logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`)
	write(t, h.root, "5.0 Mixed/channel.json", `{"logo": "logo.svg"}`)
	_, b, _ := h.do("POST", "/admin/api/backups", nil)
	// It became a Jellyfin channel by hand, with another logo.
	os.Remove(filepath.Join(h.root, "5.0 Mixed", "youtube.json"))
	os.Remove(filepath.Join(h.root, "5.0 Mixed", "logo.svg"))
	write(t, h.root, "5.0 Mixed/jellyfin.json", `{"series": ["Futurama (1999)"]}`)
	write(t, h.root, "5.0 Mixed/logo.png", string(pngLogo))
	write(t, h.root, "5.0 Mixed/channel.json", `{"logo": "logo.png"}`)
	h.lib.Rescan()
	_, p, raw := h.restore(b["file"].(string), map[string]any{})
	x := item(t, p, "5.0 Mixed")
	if !x.Done || !slices.Equal(x.Changes, []string{"kind", "settings", "details", "logo"}) {
		t.Fatalf("%s", raw)
	}
	entries, _ := os.ReadDir(filepath.Join(h.root, "5.0 Mixed"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"channel.json", "logo.svg", "youtube.json"}) {
		t.Errorf("files: %v", names)
	}
}

func TestReadArchiveSkipsAndFolders(t *testing.T) {
	a, err := readArchive(bytes.NewReader(tarball(t,
		entry{name: manifestFile, body: `{"version": 1, "folders": [{"folder": "1.3 Fireplace", "kind": "folder"}, {"folder": "../x", "kind": "folder"}]}`},
		entry{name: "1.2 Cat/", typ: tar.TypeDir},
		entry{name: "1.2 Cat/sub/", typ: tar.TypeDir},
		entry{name: "1.2 Cat/sub/jellyfin.json", body: "{}"},
		entry{name: ".hidden/jellyfin.json", body: "{}"},
		entry{name: "1.2 Cat/.catalog.json", body: "{}"},
	)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.setup.folders, []string{"1.2 Cat", "1.3 Fireplace"}) || len(a.setup.files) != 0 ||
		!slices.Equal(a.skipped, []string{"1.2 Cat/sub/", "1.2 Cat/sub/jellyfin.json", ".hidden/jellyfin.json", "1.2 Cat/.catalog.json"}) {
		t.Errorf("folders %q, files %v, skipped %q", a.setup.folders, a.setup.files, a.skipped)
	}
	if _, err := readArchive(bytes.NewReader(nil)); !errors.Is(err, errNotBackup) {
		t.Errorf("nothing: %v", err)
	}
}

func TestAgentBackups(t *testing.T) {
	h := backupHarness(t, time.Hour)
	s := h.agent()
	if l := list(ok(t, s, "list_backups", nil)["backups"]); len(l) != 0 {
		t.Errorf("none yet: %v", l)
	}
	got := ok(t, s, "create_backup", map[string]any{"label": "before the move"})
	b := got["backup"].(map[string]any)
	if b["reason"] != "manual" || b["label"] != "before the move" || b["kept"] != "until deleted" {
		t.Errorf("created: %v", got)
	}
	l := list(ok(t, s, "list_backups", nil)["backups"])
	if len(l) != 1 || list(l[0].(map[string]any)["channels"])[0] != "1.2 Cat Calming (folder)" {
		t.Errorf("listed: %v", l)
	}
	fails(t, s, "create_backup", map[string]any{"label": strings.Repeat("x", 61)}, "the label is up to 60 characters")
	fails(t, newHarness(t).agent(), "list_backups", nil, "backups are off")
}
