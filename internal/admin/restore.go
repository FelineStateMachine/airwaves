package admin

// Restoring a backup puts channels back as they were in it, folder by
// folder. The rule (the page and README say it too):
//
//   - A channel in the backup is the folder of the same name now. Failing
//     that, a Jellyfin or YouTube channel is the one folder of its kind
//     with the same source (the same YouTube channels, or the same
//     Jellyfin picks) that the backup doesn't have: it was renamed since,
//     and gets its old folder name, so number and name, back. Failing
//     that, its folder is made again; a folder channel's comes back empty,
//     as videos are never in a backup.
//   - The folder gets the backup's settings, details and logo, and loses
//     those the backup doesn't have for it (a logo added since, say).
//     Videos, catalogs and anything else in it stay as they are.
//   - A YouTube channel's schedule comes back only where it has none, so a
//     channel on the air keeps its guide, and one made again picks up
//     where the backup left off.
//   - A channel is left out when its number belongs, after the restore, to
//     a channel the restore leaves alone (or to the weather channel): move
//     that one first.
//   - The weather channel's record and logo come back like a channel's.
//   - The Jellyfin account comes back only when asked.
//   - Channels the backup doesn't have are left as they are.
//
// The setup as it is is backed up first, so restoring that undoes it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"airwaves/internal/guide"
	"airwaves/internal/jellyfin"
	"airwaves/internal/vchan"
)

// weatherKey is the weather channel among a backup's channels.
const weatherKey = ".weather"

var errNoSuchChannel = errors.New("the backup has no such channel")

// restoreReq is what to restore.
type restoreReq struct {
	Channels  []string `json:"channels"`  // by key; all of them when left out
	Account   bool     `json:"account"`   // the Jellyfin account too
	Schedules *bool    `json:"schedules"` // YouTube channels' schedules, where they have none; yes when left out
	Preview   bool     `json:"preview"`   // only say what restoring would do
}

// restoreItem is a channel in a backup, and what restoring it does.
type restoreItem struct {
	Key    string      `json:"key"` // its folder, or ".weather"
	Kind   string      `json:"kind"`
	Number string      `json:"number,omitempty"` // as in the backup
	Name   string      `json:"name"`
	Chosen bool        `json:"chosen"`
	Now    *channelNow `json:"now,omitempty"`   // the channel it puts back, as it is now
	Match  string      `json:"match,omitempty"` // how that was found: "name" or "source"
	Action string      `json:"action"`          // "add", "change", "same" or "skip"
	// Changes says what changes: "rename", "kind", "settings", "details",
	// "logo" and "schedule".
	Changes []string `json:"changes,omitempty"`
	Empty   bool     `json:"empty,omitempty"`  // a folder channel made again, without its videos
	Reason  string   `json:"reason,omitempty"` // why it's left out
	Done    bool     `json:"done,omitempty"`
	Error   string   `json:"error,omitempty"` // what went wrong putting it back

	folder string            // its folder now, "" when there's none
	files  map[string][]byte // the backup's files for it, by name
	source string            // what it plays, to know it by when renamed
}

// channelNow is a channel as it is now.
type channelNow struct {
	Folder string `json:"folder,omitempty"`
	Number string `json:"number"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

// accountPlan is what restoring does to the Jellyfin account. The
// password or token never leaves the server.
type accountPlan struct {
	InBackup bool   `json:"inBackup"`
	Server   string `json:"server,omitempty"` // the backup's
	User     string `json:"user,omitempty"`
	Same     bool   `json:"same"` // as the account now
	Restore  bool   `json:"restore"`
	Done     bool   `json:"done,omitempty"`
	Error    string `json:"error,omitempty"`

	data []byte
}

// restorePlan is what restoring a backup does, or did.
type restorePlan struct {
	Backup    backupJSON     `json:"backup"`
	Preview   bool           `json:"preview"`
	Schedules bool           `json:"schedules"`
	Items     []*restoreItem `json:"items"`
	Account   accountPlan    `json:"account"`
	Others    []channelNow   `json:"others"`            // channels the backup doesn't have, left as they are
	Skipped   []string       `json:"skipped,omitempty"` // files in the backup that aren't part of a setup
	Before    string         `json:"before,omitempty"`  // the backup of the setup as it was
	Errors    int            `json:"errors"`
}

// folderNumber reads a channel folder's name as the library does: a
// number, then the name, or only a name.
func folderNumber(folder string) (number, name string) {
	if m := folderNumberRe.FindStringSubmatch(folder); m != nil {
		if n, ok := vchan.ParseNumber(m[1]); ok {
			return n, strings.TrimSpace(m[2])
		}
	}
	return "", folder
}

// weatherName is the weather channel's number and name in its record.
func weatherName(rec []byte) (number, name string) {
	var r struct{ Number, Name string }
	_ = json.Unmarshal(rec, &r)
	number, name = vchan.WeatherNumber, vchan.WeatherName
	if n, ok := vchan.ParseNumber(r.Number); ok {
		number = n
	}
	if n := strings.TrimSpace(r.Name); n != "" {
		name = n
	}
	return number, name
}

// sourceOf is what a Jellyfin or YouTube channel plays, to know it by
// when it's been renamed: its YouTube channels or playlist, or its
// Jellyfin server and picks. "" for a folder channel, or one that plays
// nothing.
func sourceOf(files map[string][]byte) string {
	switch kindOf(files) {
	case "jellyfin":
		var c struct {
			Server      string   `json:"server"`
			Series      []string `json:"series"`
			Movies      []string `json:"movies"`
			Collections []string `json:"collections"`
			Playlists   []string `json:"playlists"`
		}
		if json.Unmarshal(files[channelFile], &c) != nil {
			return ""
		}
		var picks []string
		for _, l := range []struct {
			kind  string
			names []string
		}{{"s", c.Series}, {"m", c.Movies}, {"c", c.Collections}, {"p", c.Playlists}} {
			for _, n := range l.names {
				picks = append(picks, l.kind+":"+strings.ToLower(strings.TrimSpace(n)))
			}
		}
		if len(picks) == 0 {
			return ""
		}
		slices.Sort(picks)
		return "jellyfin\x00" + strings.ToLower(strings.TrimRight(c.Server, "/")) + "\x00" + strings.Join(slices.Compact(picks), "\x00")
	case "youtube":
		var c struct {
			Channels []string `json:"channels"`
			Playlist string   `json:"playlist"`
		}
		if json.Unmarshal(files[youtubeFile], &c) != nil {
			return ""
		}
		if c.Playlist != "" {
			page, err := vchan.YouTubePlaylist(c.Playlist)
			if err != nil {
				page = strings.TrimSpace(c.Playlist)
			}
			return "youtube\x00" + page
		}
		var pages []string
		for _, ch := range c.Channels {
			page, err := youtubeChannel(ch)
			if err != nil {
				page = strings.TrimSpace(ch)
			}
			pages = append(pages, strings.ToLower(page))
		}
		if len(pages) == 0 {
			return ""
		}
		slices.Sort(pages)
		return "youtube\x00" + strings.Join(slices.Compact(pages), "\x00")
	}
	return ""
}

// differs reports whether two versions of a file differ, being there or
// not included.
func differs(a, b []byte) bool { return (a == nil) != (b == nil) || !bytes.Equal(a, b) }

// logosDiffer reports whether two sets of files have different logos.
func logosDiffer(a, b map[string][]byte, base string) bool {
	logos := func(m map[string][]byte) map[string][]byte {
		out := map[string][]byte{}
		for n, d := range m {
			if isLogo(n, base) {
				out[n] = d
			}
		}
		return out
	}
	return !maps.EqualFunc(logos(a), logos(b), bytes.Equal)
}

// planRestore works out what restoring a backup does: which channel each
// of its channels is now, and what changes.
func (s *Server) planRestore(a archive, b backupJSON, req restoreReq) (*restorePlan, error) {
	root := s.Library.Root
	cur, err := readSetup(root)
	if err != nil {
		return nil, fmt.Errorf("reading the channel setup: %w", err)
	}
	p := &restorePlan{Backup: b, Preview: req.Preview, Schedules: req.Schedules == nil || *req.Schedules,
		Items: []*restoreItem{}, Others: []channelNow{}, Skipped: a.skipped}

	// The channels as they are now, by folder.
	listed := map[string]channelNow{}
	for _, e := range s.Library.Entries() {
		listed[e.Folder] = channelNow{Folder: e.Folder, Number: e.Channel.Number(), Name: e.Channel.Name(), Kind: vchan.KindOf(e.Channel)}
	}
	now := func(folder string) *channelNow {
		if c, ok := listed[folder]; ok {
			return &c
		}
		number, name := folderNumber(folder)
		return &channelNow{Folder: folder, Number: number, Name: name, Kind: kindOf(cur.in(folder))}
	}
	isFolder := map[string]bool{}
	for _, f := range cur.folders {
		isFolder[f] = true
	}
	inBackup := map[string]bool{}
	for _, f := range a.setup.folders {
		inBackup[f] = true
	}

	// The backup's channels, each the folder of its name...
	for _, f := range a.setup.folders {
		files := a.setup.in(f)
		number, name := folderNumber(f)
		x := &restoreItem{Key: f, Kind: kindOf(files), Number: number, Name: name, files: files, source: sourceOf(files)}
		if isFolder[f] {
			x.folder, x.Match = f, "name"
		}
		p.Items = append(p.Items, x)
	}
	// ... or else the one renamed from it: the one folder the backup
	// doesn't have that plays the same, for the one channel that does.
	wants := map[string][]*restoreItem{}
	for _, x := range p.Items {
		if x.folder == "" && x.source != "" {
			wants[x.source] = append(wants[x.source], x)
		}
	}
	plays := map[string][]string{}
	for _, f := range cur.folders {
		if !inBackup[f] {
			if src := sourceOf(cur.in(f)); src != "" {
				plays[src] = append(plays[src], f)
			}
		}
	}
	for src, xs := range wants {
		if len(xs) == 1 && len(plays[src]) == 1 {
			xs[0].folder, xs[0].Match = plays[src][0], "source"
		}
	}
	if rec := a.setup.files[vchan.WeatherFile]; rec != nil {
		number, name := weatherName(rec)
		files := map[string][]byte{}
		for n, d := range a.setup.top() {
			if n == vchan.WeatherFile || isLogo(n, vchan.WeatherLogoName) {
				files[n] = d
			}
		}
		p.Items = append(p.Items, &restoreItem{Key: weatherKey, Kind: "weather", Number: number, Name: name, Match: "name", files: files})
	}

	// Which are chosen.
	chosen := map[string]bool{}
	for _, k := range req.Channels {
		chosen[k] = true
	}
	for _, x := range p.Items {
		x.Chosen = req.Channels == nil || chosen[x.Key]
		delete(chosen, x.Key)
	}
	if len(chosen) > 0 {
		return nil, fmt.Errorf("%w: %s", errNoSuchChannel, strings.Join(slices.Sorted(maps.Keys(chosen)), ", "))
	}

	// What each changes.
	curTop := cur.top()
	for _, x := range p.Items {
		switch {
		case x.Key == weatherKey:
			number, name := weatherName(curTop[vchan.WeatherFile])
			if s.Weather != nil {
				number, name = s.Weather.Number(), s.Weather.Name()
			}
			x.Now = &channelNow{Number: number, Name: name, Kind: "weather"}
			if differs(curTop[vchan.WeatherFile], x.files[vchan.WeatherFile]) {
				x.Changes = append(x.Changes, "details")
			}
			if logosDiffer(curTop, x.files, vchan.WeatherLogoName) {
				x.Changes = append(x.Changes, "logo")
			}
		case x.folder != "":
			x.Now = now(x.folder)
			x.Changes = folderChanges(x, cur.in(x.folder), p.Schedules)
		default:
			x.Empty = x.Kind == "folder"
			x.Changes = folderChanges(x, map[string][]byte{}, p.Schedules)
		}
		// Something not a folder, or a link, where its folder goes.
		if x.Key != weatherKey && x.folder != x.Key {
			if _, err := os.Lstat(filepath.Join(root, x.Key)); err == nil {
				x.Reason = fmt.Sprintf("something that isn't its folder is in the way: %q in the channels folder", x.Key)
			}
		}
	}
	s.checkNumbers(p, cur)
	for _, x := range p.Items {
		switch {
		case x.Reason != "":
			x.Action = "skip"
		case x.Key != weatherKey && x.folder == "":
			x.Action = "add"
		case len(x.Changes) > 0:
			x.Action = "change"
		default:
			x.Action = "same"
		}
	}
	slices.SortStableFunc(p.Items, func(x, y *restoreItem) int {
		switch {
		case x.Number == "" && y.Number != "":
			return 1
		case x.Number != "" && y.Number == "":
			return -1
		case x.Number != "":
			if c := guide.CompareNumbers(x.Number, y.Number); c != 0 {
				return c
			}
		}
		return strings.Compare(x.Key, y.Key)
	})

	// Channels the backup doesn't have.
	targets := map[string]bool{}
	for _, x := range p.Items {
		targets[x.folder] = true
	}
	for _, f := range cur.folders {
		if !targets[f] {
			p.Others = append(p.Others, *now(f))
		}
	}
	slices.SortFunc(p.Others, func(x, y channelNow) int { return guide.CompareNumbers(x.Number, y.Number) })

	if data := a.setup.files[accountFile]; data != nil {
		var acct jellyfin.Account
		_ = json.Unmarshal(data, &acct)
		now, err := jellyfin.LoadAccount(s.accountPath())
		p.Account = accountPlan{InBackup: true, Server: acct.Server, User: acct.User, Same: err == nil && now == acct, data: data}
		p.Account.Restore = req.Account && !p.Account.Same
	}
	return p, nil
}

// folderChanges says what restoring x into its folder, which holds has
// now, changes.
func folderChanges(x *restoreItem, has map[string][]byte, schedules bool) []string {
	var out []string
	if x.folder != "" && x.folder != x.Key {
		out = append(out, "rename")
	}
	if x.folder != "" && kindOf(has) != x.Kind {
		out = append(out, "kind")
	}
	if settings := map[string]string{"jellyfin": channelFile, "youtube": youtubeFile}[x.Kind]; settings != "" && differs(has[settings], x.files[settings]) {
		out = append(out, "settings")
	}
	if differs(has[vchan.DetailsFile], x.files[vchan.DetailsFile]) {
		out = append(out, "details")
	}
	if logosDiffer(has, x.files, vchan.LogoName) {
		out = append(out, "logo")
	}
	if schedules && x.Kind == "youtube" && x.files[playoutFile] != nil && has[playoutFile] == nil {
		out = append(out, "schedule")
	}
	return out
}

// checkNumbers leaves out the chosen channels whose number, after the
// restore, a channel the restore leaves alone (or the weather channel)
// has too, and says so of the others, were they chosen.
func (s *Server) checkNumbers(p *restorePlan, cur setup) {
	var touched map[string]bool
	var wx *restoreItem
	in := func(x *restoreItem) bool { return x.Chosen && x.Reason == "" }
	look := func() {
		touched = map[string]bool{}
		for _, x := range p.Items {
			if x.Key == weatherKey {
				wx = x
			} else if in(x) && x.folder != "" {
				touched[x.folder] = true
			}
		}
	}
	taken := func(x *restoreItem) string {
		if x.Number == "" {
			return ""
		}
		key := vchan.NumberKey(x.Number)
		if x.Key != weatherKey {
			wn := ""
			if wx != nil && in(wx) {
				wn = wx.Number
			} else if s.Weather != nil {
				wn = s.Weather.Number()
			}
			if wn != "" && vchan.NumberKey(wn) == key {
				return "the weather channel"
			}
		}
		for _, f := range cur.folders {
			if f == x.folder || touched[f] {
				continue
			}
			if n, name := folderNumber(f); n != "" && vchan.NumberKey(n) == key {
				return n + " " + name
			}
		}
		return ""
	}
	why := func(x *restoreItem, who string) string {
		return fmt.Sprintf("%s is taken by %s now: give that channel another number first, or leave this one out", x.Number, who)
	}
	for again := true; again; {
		again = false
		look()
		for _, x := range p.Items {
			if in(x) {
				if who := taken(x); who != "" {
					x.Reason, again = why(x, who), true
				}
			}
		}
	}
	look()
	for _, x := range p.Items {
		if !x.Chosen && x.Reason == "" {
			if who := taken(x); who != "" {
				x.Reason = why(x, who)
			}
		}
	}
}

// changes reports whether the plan changes anything.
func (p *restorePlan) changes() bool {
	for _, x := range p.Items {
		if x.Chosen && (x.Action == "add" || x.Action == "change") {
			return true
		}
	}
	return p.Account.Restore
}

// applyRestore carries a plan out, noting what went wrong with each
// channel.
func (s *Server) applyRestore(p *restorePlan) {
	root := s.Library.Root
	fail := func(err error) {
		for _, x := range p.Items {
			if x.Chosen && (x.Action == "add" || x.Action == "change") {
				x.Error = err.Error()
				p.Errors++
			}
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		fail(err)
		return
	}
	if p.Account.Restore {
		if err := writeFile(s.accountPath(), p.Account.data, 0o600); err != nil {
			p.Account.Error = err.Error()
			p.Errors++
		} else {
			p.Account.Done = true
			s.mu.Lock()
			s.checked, s.browse = time.Time{}, nil
			s.mu.Unlock()
		}
	}
	for _, x := range p.Items {
		if !x.Chosen || (x.Action != "add" && x.Action != "change") {
			continue
		}
		var err error
		if x.Key == weatherKey {
			err = putFiles(root, x.files, func(n string) bool { return n == vchan.WeatherFile || isLogo(n, vchan.WeatherLogoName) }, false)
		} else {
			err = putFolder(root, x, p.Schedules)
		}
		if err != nil {
			x.Error = err.Error()
			p.Errors++
		} else {
			x.Done = true
		}
	}
	s.Library.Rescan()
}

// putFolder puts a channel's folder back: made again, or renamed back,
// then its files.
func putFolder(root string, x *restoreItem, schedules bool) error {
	dir := filepath.Join(root, x.Key)
	switch {
	case x.folder == "":
		if err := os.Mkdir(dir, 0o755); err != nil {
			return fmt.Errorf("can't make its folder: %w", err)
		}
	case x.folder != x.Key:
		if _, err := os.Lstat(dir); err == nil {
			return fmt.Errorf("can't rename %q back: %q is in the way", x.folder, x.Key)
		}
		if err := os.Rename(filepath.Join(root, x.folder), dir); err != nil {
			return fmt.Errorf("can't rename its folder: %w", err)
		}
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%q isn't a folder", x.Key)
	}
	managed := func(n string) bool {
		return n == channelFile || n == youtubeFile || n == vchan.DetailsFile || isLogo(n, vchan.LogoName)
	}
	return putFiles(dir, x.files, managed, schedules)
}

// putFiles makes the setup files in dir what want has, each written
// whole or not at all: the schedule first, and only where there's none;
// then the logo and the details; then the files want doesn't have go
// (those managed says are the setup's); and last the settings, which make
// the folder a channel of their kind.
func putFiles(dir string, want map[string][]byte, managed func(string) bool, schedules bool) error {
	put := func(name string) error {
		p := filepath.Join(dir, name)
		if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, want[name]) {
			return nil // as it is, so the channel isn't stirred
		}
		perm := os.FileMode(0o644)
		if private(name) {
			perm = 0o600
		}
		return writeFile(p, want[name], perm)
	}
	settings := func(n string) bool { return n == channelFile || n == youtubeFile }
	if want[playoutFile] != nil && schedules {
		if _, err := os.Lstat(filepath.Join(dir, playoutFile)); errors.Is(err, fs.ErrNotExist) {
			if err := put(playoutFile); err != nil {
				return err
			}
		}
	}
	names := slices.Sorted(maps.Keys(want))
	// Logos before the details that name them.
	slices.SortStableFunc(names, func(a, b string) int {
		la, lb := isLogo(a, vchan.LogoName) || isLogo(a, vchan.WeatherLogoName), isLogo(b, vchan.LogoName) || isLogo(b, vchan.WeatherLogoName)
		switch {
		case la && !lb:
			return -1
		case lb && !la:
			return 1
		}
		return 0
	})
	for _, n := range names {
		if n != playoutFile && !settings(n) {
			if err := put(n); err != nil {
				return err
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && managed(n) && want[n] == nil {
			if err := os.Remove(filepath.Join(dir, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	for _, n := range names {
		if settings(n) {
			if err := put(n); err != nil {
				return err
			}
		}
	}
	return nil
}

// restoreBackup says what restoring a backup would do (with preview), or
// does it: the setup as it is is backed up first, and nothing is restored
// when that fails.
func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupsOn(w) {
		return
	}
	var req restoreReq
	if !decodeOptional(w, r, &req) {
		return
	}
	file := r.PathValue("file")
	if !req.Preview {
		s.bk.run.Lock()
		defer s.bk.run.Unlock()
	}
	a, b, err := s.openBackup(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeErr(w, http.StatusNotFound, fmt.Errorf("no backup %s", file))
		return
	case err != nil:
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	p, err := s.planRestore(a, b, req)
	switch {
	case errors.Is(err, errNoSuchChannel):
		writeErr(w, http.StatusBadRequest, err)
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.Preview || !p.changes() {
		writeJSON(w, p)
		return
	}
	before, _, err := s.backUpLocked(beforeRestore, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("nothing was restored, as the setup as it is couldn't be backed up first: %w", err))
		return
	}
	p.Before = before.File
	s.applyRestore(p)
	done := 0
	for _, x := range p.Items {
		if x.Done {
			done++
		}
	}
	log.Printf("admin: restored %d channels from backup %s (the setup before is %s), with %d errors", done, file, p.Before, p.Errors)
	s.backupSoon()
	writeJSON(w, p)
}
