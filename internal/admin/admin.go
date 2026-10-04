// Package admin serves the server owner's web page for custom channels:
// every channel's number, name and details (call sign, category,
// description, logo, and whether it's on), the Jellyfin account, which
// series, movies, collections and playlists each Jellyfin channel plays,
// and which YouTube channels' uploads each YouTube channel plays. It
// writes the same files a person would: a channel.json, jellyfin.json or
// youtube.json in each channel's folder, and .jellyfin.json for the
// account and .weather.json for the weather channel at the top of the
// channels folder. It keeps backups of all that, to restore from
// (backups.go, restore.go).
package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"airwaves/internal/guide"
	"airwaves/internal/jellyfin"
	"airwaves/internal/vchan"
)

//go:embed ui
var ui embed.FS

// Server is the admin page and its API, under /admin/.
type Server struct {
	Library *vchan.Library
	// Weather is the weather channel, nil when there's none.
	Weather *vchan.Weather
	// Antenna lists the antenna channels by number, with their names, to
	// warn of custom channels taking their numbers; optional.
	Antenna func(ctx context.Context) map[string]string
	HTTP    *http.Client
	// Backups is the folder backups of the channel setup are kept in
	// (backups.go); none are made when it's "".
	Backups string
	// Version is the server's, noted in each backup.
	Version string
	// ChannelsDir and MusicDir are where the channels and the weather
	// music folders are as the host sees them, for the page's hints.
	ChannelsDir, MusicDir string

	bk       backupState
	mu       sync.Mutex
	account  accountStatus
	checked  time.Time
	browse   map[string]browsed      // by kind
	codes    map[string]quickConnect // Quick Connect sign-ins in progress, by handle
	searches map[string]searched     // YouTube searches, by query
	lookups  map[string]lookedUp     // YouTube channels looked up, by page and "id:" ID
	ytSlots  chan struct{}           // yt-dlp runs under way
}

// quickConnect is a code sign-in waiting for the user to approve it.
type quickConnect struct {
	server, secret string
	started        time.Time
}

type browsed struct {
	items []itemJSON
	at    time.Time
}

const (
	// deviceID is how Jellyfin lists Airwaves among the user's devices.
	deviceID    = "airwaves"
	accountFile = ".jellyfin.json"
	channelFile = "jellyfin.json"
	// recheckEvery is how long an account check and a library listing are
	// trusted.
	recheckEvery = 5 * time.Minute
	// codeLife is how long a Quick Connect code waits for approval.
	codeLife = 10 * time.Minute
)

var idRe = regexp.MustCompile(`^[0-9A-Za-z-]{8,64}$`)

// Handler serves the page and its API.
func (s *Server) Handler() http.Handler {
	s.ytSlots = make(chan struct{}, ytRuns)
	mux := http.NewServeMux()
	static, _ := fs.Sub(ui, "ui")
	files := http.StripPrefix("/admin/", http.FileServerFS(static))
	mux.Handle("GET /admin/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The page changes with the server; never run a stale copy.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /admin/api/state", s.getState)
	mux.HandleFunc("PUT /admin/api/account", s.putAccount)
	mux.HandleFunc("POST /admin/api/account/quickconnect", s.startQuickConnect)
	mux.HandleFunc("GET /admin/api/account/quickconnect/{id}", s.checkQuickConnect)
	mux.HandleFunc("GET /admin/api/library/{kind}", s.getLibrary)
	mux.HandleFunc("GET /admin/api/image/{id}", s.getImage)
	mux.HandleFunc("PUT /admin/api/channels/{number}", s.putChannel)
	mux.HandleFunc("DELETE /admin/api/channels/{number}", s.deleteChannel)
	mux.HandleFunc("GET /admin/api/channels/{number}/schedule", s.getSchedule)
	mux.HandleFunc("GET /admin/api/channels/{number}/items", s.getItems)
	mux.HandleFunc("GET /admin/api/channels/{number}/logo", s.getLogo)
	mux.HandleFunc("PUT /admin/api/channels/{number}/logo", s.putLogo)
	mux.HandleFunc("POST /admin/api/channels/{number}/logo", s.putLogo)
	mux.HandleFunc("DELETE /admin/api/channels/{number}/logo", s.deleteLogo)
	mux.HandleFunc("POST /admin/api/channels/{number}/logo/source", s.logoFromSource)
	mux.HandleFunc("GET /admin/api/youtube/search", s.searchYouTube)
	mux.HandleFunc("GET /admin/api/youtube/channel", s.lookupYouTube)
	mux.HandleFunc("GET /admin/api/youtube/playlist", s.lookupPlaylist)
	s.addBackupRoutes(mux)
	// Changes through the API, the agent tools' included, are backed up.
	api := s.watch(mux)
	addAgentRoutes(mux, api) // the same, as tools for AI agents (mcp.go)
	return api
}

// ---------- account ----------

type accountStatus struct {
	Server      string `json:"server"`
	User        string `json:"user"`
	HasPassword bool   `json:"hasPassword"`
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	ServerName  string `json:"serverName,omitempty"`
}

func (s *Server) accountPath() string { return filepath.Join(s.Library.Root, accountFile) }

func (s *Server) client() (*jellyfin.Client, error) {
	acct, err := jellyfin.LoadAccount(s.accountPath())
	if err != nil || acct.Server == "" {
		return nil, errors.New("set up the Jellyfin account first")
	}
	return jellyfin.New(acct, deviceID, s.HTTP), nil
}

// accountStatus returns the account and whether it works, checking at most
// every few minutes unless force is set.
func (s *Server) accountStatus(ctx context.Context, force bool) accountStatus {
	s.mu.Lock()
	if !force && !s.checked.IsZero() && time.Since(s.checked) < recheckEvery {
		defer s.mu.Unlock()
		return s.account
	}
	s.mu.Unlock()

	acct, err := jellyfin.LoadAccount(s.accountPath())
	st := accountStatus{Server: acct.Server, User: acct.User, HasPassword: acct.Password != "" || acct.Token != ""}
	switch {
	case errors.Is(err, fs.ErrNotExist) || (err == nil && acct.Server == ""):
		st.Error = "No Jellyfin account yet"
	case err != nil:
		st.Error = "Can't read the account file: " + err.Error()
	default:
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		st.ServerName = s.serverName(ctx, acct.Server)
		if err := jellyfin.New(acct, deviceID, s.HTTP).Login(ctx); err != nil {
			st.Error = err.Error()
		} else {
			st.OK = true
		}
	}
	s.mu.Lock()
	s.account, s.checked = st, time.Now()
	s.mu.Unlock()
	return st
}

// serverName asks a Jellyfin server its name (no login needed).
func (s *Server) serverName(ctx context.Context, server string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(server, "/")+"/System/Info/Public", nil)
	if err != nil {
		return ""
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var info struct{ ServerName string }
	_ = json.NewDecoder(resp.Body).Decode(&info)
	return info.ServerName
}

func (s *Server) putAccount(w http.ResponseWriter, r *http.Request) {
	var req struct{ Server, User, Password string }
	if !decode(w, r, &req) {
		return
	}
	req.Server = strings.TrimRight(strings.TrimSpace(req.Server), "/")
	req.User = strings.TrimSpace(req.User)
	if u, err := url.Parse(req.Server); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, errors.New("the server should be a URL like https://jellyfin.example.com"))
		return
	}
	acct, _ := jellyfin.LoadAccount(s.accountPath())
	if acct.Server != req.Server || acct.User != req.User || req.Password != "" {
		// A different login: drop the old one's token.
		acct = jellyfin.Account{Server: req.Server, User: req.User, Password: cmp(req.Password, acct.Password)}
	}
	if err := s.saveAccount(acct); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, s.accountStatus(r.Context(), true))
}

func (s *Server) saveAccount(acct jellyfin.Account) error {
	raw, err := json.MarshalIndent(acct, "", "  ")
	if err != nil {
		return err
	}
	// Only this user may read the password or token.
	if err := writeFile(s.accountPath(), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	s.browse = nil
	s.mu.Unlock()
	s.backupSoon() // a Quick Connect sign-in comes by GET, which watch passes over
	return nil
}

// startQuickConnect begins a code sign-in: the page shows the code, and the
// user approves it from a Jellyfin app they're signed in to.
func (s *Server) startQuickConnect(w http.ResponseWriter, r *http.Request) {
	var req struct{ Server string }
	if !decode(w, r, &req) {
		return
	}
	server := strings.TrimRight(strings.TrimSpace(req.Server), "/")
	if u, err := url.Parse(server); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, errors.New("the server should be a URL like https://jellyfin.example.com"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if on, err := jellyfin.QuickConnectEnabled(ctx, server, s.HTTP); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	} else if !on {
		writeErr(w, http.StatusConflict, errors.New("Quick Connect is turned off on this server"))
		return
	}
	qc, err := jellyfin.StartQuickConnect(ctx, server, deviceID, s.HTTP)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s.mu.Lock()
	if s.codes == nil {
		s.codes = map[string]quickConnect{}
	}
	for k, c := range s.codes {
		if time.Since(c.started) > codeLife {
			delete(s.codes, k)
		}
	}
	s.codes[id] = quickConnect{server: server, secret: qc.Secret, started: time.Now()}
	s.mu.Unlock()
	writeJSON(w, map[string]string{"id": id, "code": qc.Code})
}

// checkQuickConnect reports whether the code was approved, and when it is,
// saves the account (a token, never a password).
func (s *Server) checkQuickConnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	c, ok := s.codes[id]
	s.mu.Unlock()
	expired := func() {
		s.mu.Lock()
		delete(s.codes, id)
		s.mu.Unlock()
		writeJSON(w, map[string]string{"state": "expired"})
	}
	if !ok || time.Since(c.started) > codeLife {
		expired()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	approved, err := jellyfin.CheckQuickConnect(ctx, c.server, c.secret, s.HTTP)
	if err != nil {
		// Most often the server has forgotten the code; start over.
		log.Printf("admin: quick connect: %v", err)
		expired()
		return
	}
	if !approved {
		writeJSON(w, map[string]string{"state": "waiting"})
		return
	}
	acct, err := jellyfin.FinishQuickConnect(ctx, c.server, c.secret, deviceID, s.HTTP)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.mu.Lock()
	delete(s.codes, id)
	s.mu.Unlock()
	if err := s.saveAccount(acct); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("admin: signed in to %s as %s with Quick Connect", acct.Server, acct.User)
	writeJSON(w, map[string]any{"state": "done", "account": s.accountStatus(r.Context(), true)})
}

// ---------- channels ----------

type channelConfig struct {
	Series      []string `json:"series"`
	Movies      []string `json:"movies"`
	Collections []string `json:"collections"`
	Playlists   []string `json:"playlists"`
	Order       string   `json:"order"`
	MaxBitrate  float64  `json:"maxBitrate"`
}

type programJSON struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle,omitempty"`
	// OffAir marks a scheduled channel's time off the air; its subtitle
	// says what's next and when.
	OffAir bool `json:"offAir,omitempty"`
}

// programOf is a guide entry as the page shows it, an off-air one's note
// as its subtitle.
func programOf(p vchan.Program) programJSON {
	out := programJSON{Start: p.Start, End: p.End, Title: p.Title, Subtitle: p.Subtitle, OffAir: p.OffAir}
	if p.OffAir {
		out.Subtitle = p.Description
	}
	return out
}

type channelJSON struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // "weather", "folder", "jellyfin" or "youtube"
	Folder string `json:"folder,omitempty"`
	// The channel's details, as guides and lineups show them.
	CallSign    string `json:"callSign"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Logo        string `json:"logo,omitempty"` // where the page gets it, with its version
	Enabled     bool   `json:"enabled"`
	// Antenna names the antenna channel with the same number, which this
	// one takes the place of in the HDHomeRun lineup.
	Antenna string `json:"antenna,omitempty"`
	// Videos counts a folder's videos, or a YouTube channel's catalog.
	Videos *int           `json:"videos,omitempty"`
	Config *channelConfig `json:"config,omitempty"`
	// Items counts the videos in rotation: a Jellyfin channel's, or those
	// that fit a YouTube channel's settings.
	Items     *int           `json:"items,omitempty"`
	Unmatched []string       `json:"unmatched,omitempty"`
	YouTube   *ytConfigJSON  `json:"youtube,omitempty"`
	Sources   []ytSourceJSON `json:"sources,omitempty"`
	Listing   bool           `json:"listing,omitempty"` // a YouTube source is being listed
	// Leveled counts the videos whose loudness is remembered from an
	// airing, when leveling is on: of a folder's or a Jellyfin channel's
	// videos with sound, of what a YouTube channel has scheduled.
	Leveled *leveledJSON `json:"leveled,omitempty"`
	// Schedule is when a channel that plays in order airs, as its
	// channel.json has it; none airs around the clock.
	Schedule *vchan.Schedule `json:"schedule,omitempty"`
	Error    string          `json:"error,omitempty"`
	Now      *programJSON    `json:"now,omitempty"`
}

type leveledJSON struct {
	Measured int `json:"measured"`
	Total    int `json:"total"`
}

// describe is a folder channel as the page shows it; antenna is the
// antenna channels by number.
func (s *Server) describe(e vchan.Entry, antenna map[string]string) channelJSON {
	c := channelJSON{Kind: vchan.KindOf(e.Channel), Folder: e.Folder}
	withDetails(&c, e.Channel.Details(), antenna)
	c.Schedule = vchan.ReadSchedule(filepath.Join(s.Library.Root, e.Folder))
	if l, ok := e.Channel.(interface{ Leveled() (int, int) }); ok {
		if m, t := l.Leveled(); t > 0 {
			c.Leveled = &leveledJSON{m, t}
		}
	}
	switch ch := e.Channel.(type) {
	case *vchan.Folder:
		n := ch.Videos()
		c.Videos = &n
	case *vchan.YouTube:
		s.describeYouTube(&c, ch)
	case *vchan.Jellyfin:
		cfg, err := readConfig(filepath.Join(s.Library.Root, e.Folder, channelFile))
		if err != nil {
			c.Error = err.Error()
		}
		c.Config = &cfg
		items, unmatched, err := ch.Status()
		c.Items, c.Unmatched = &items, unmatched
		if err != nil {
			c.Error = err.Error()
		}
		if items > 0 {
			c.Now = nowOn(ch)
		}
	}
	return c
}

// describeWeather is the weather channel as the page shows it.
func (s *Server) describeWeather(antenna map[string]string) channelJSON {
	c := channelJSON{Kind: "weather"}
	withDetails(&c, s.Weather.Details(), antenna)
	return c
}

// withDetails fills in a channel's number, name and details.
func withDetails(c *channelJSON, d vchan.Details, antenna map[string]string) {
	c.Number, c.Name, c.CallSign, c.Category, c.Description, c.Enabled = d.Number, d.Name, d.CallSign, d.Category, d.Description, d.Enabled
	if tag := vchan.LogoTag(d.Logo); d.Logo != "" && tag != "" {
		c.Logo = "/admin/api/channels/" + url.PathEscape(d.Number) + "/logo?v=" + tag
	}
	for n, name := range antenna {
		if vchan.SameNumber(n, d.Number) {
			c.Antenna = name
		}
	}
}

// antenna lists the antenna channels by number, or none when they can't
// be known.
func (s *Server) antenna(ctx context.Context) map[string]string {
	if s.Antenna == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.Antenna(ctx)
}

// describeNumber is the channel numbered number as the page shows it.
func (s *Server) describeNumber(ctx context.Context, number string) (channelJSON, bool) {
	if s.isWeather(number) {
		return s.describeWeather(s.antenna(ctx)), true
	}
	if e, ok := s.find(number); ok {
		return s.describe(e, s.antenna(ctx)), true
	}
	return channelJSON{}, false
}

func nowOn(ch vchan.Channel) *programJSON {
	now := time.Now()
	for _, p := range ch.Programs(now, now.Add(time.Second)) {
		if !p.Start.After(now) && p.End.After(now) {
			pj := programOf(p)
			return &pj
		}
	}
	return nil
}

type antennaJSON struct {
	Number string `json:"number"`
	Name   string `json:"name"`
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	antenna := s.antenna(r.Context())
	chans := []channelJSON{}
	if s.Weather != nil {
		chans = append(chans, s.describeWeather(antenna))
	}
	for _, e := range s.Library.Entries() {
		chans = append(chans, s.describe(e, antenna))
	}
	list := []antennaJSON{}
	for n, name := range antenna {
		list = append(list, antennaJSON{Number: n, Name: name})
	}
	slices.SortFunc(list, func(a, b antennaJSON) int { return guide.CompareNumbers(a.Number, b.Number) })
	writeJSON(w, map[string]any{
		"account": s.accountStatus(r.Context(), false), "channels": chans, "categories": vchan.Categories, "antenna": list,
		"channelsDir": s.ChannelsDir, "musicDir": s.MusicDir,
	})
}

func (s *Server) find(number string) (vchan.Entry, bool) {
	for _, e := range s.Library.Entries() {
		if vchan.SameNumber(e.Channel.Number(), number) {
			return e, true
		}
	}
	return vchan.Entry{}, false
}

// isWeather reports whether number is the weather channel's.
func (s *Server) isWeather(number string) bool {
	return s.Weather != nil && vchan.SameNumber(s.Weather.Number(), number)
}

// channelReq is a channel as the page saves it: its number, name and
// details, and a Jellyfin channel's settings, or a YouTube channel's under
// "youtube".
type channelReq struct {
	Kind   string `json:"kind"` // "jellyfin" (when left out), "youtube", "folder" or "weather"
	Number string `json:"number"`
	Name   string `json:"name"`
	channelConfig
	YouTube *ytReq `json:"youtube"`
	detailsReq
}

// detailsReq is a channel's details as the page saves them. Those left out
// stay as they are; empty ones go back to the defaults.
type detailsReq struct {
	CallSign    *string `json:"callSign"`
	Category    *string `json:"category"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
	// Schedule is when a channel that plays in order airs (see
	// vchan.Schedule); null takes it out, to air around the clock.
	Schedule json.RawMessage `json:"schedule"`
	schedule *vchan.Schedule // Schedule read, nil for null
}

const (
	maxCallSign    = 8
	maxDescription = 500
)

// callSignRe is a call sign: letters and digits, with a few marks ("A&E",
// "E!") and single spaces.
var callSignRe = regexp.MustCompile(`^[\p{L}\p{N}&+!.'-]+( [\p{L}\p{N}&+!.'-]+)*$`)

// check tidies the details and says what's wrong with them.
func (d *detailsReq) check() error {
	if d.CallSign != nil {
		c := strings.Join(strings.Fields(*d.CallSign), " ")
		if c != "" && (utf8.RuneCountInString(c) > maxCallSign || !callSignRe.MatchString(c)) {
			return fmt.Errorf("the call sign is a short label of up to %d letters and digits, like TOON", maxCallSign)
		}
		d.CallSign = &c
	}
	if d.Category != nil {
		c := vchan.Category(*d.Category)
		if c == "" && strings.TrimSpace(*d.Category) != "" {
			return fmt.Errorf("the category is one of %s", strings.Join(vchan.Categories, ", "))
		}
		d.Category = &c
	}
	if d.Description != nil {
		desc := strings.Join(strings.Fields(*d.Description), " ")
		if utf8.RuneCountInString(desc) > maxDescription {
			return fmt.Errorf("the description is up to %d characters", maxDescription)
		}
		d.Description = &desc
	}
	if d.Schedule != nil && !bytes.Equal(bytes.TrimSpace(d.Schedule), []byte("null")) {
		var sch vchan.Schedule
		dec := json.NewDecoder(bytes.NewReader(d.Schedule))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&sch); err != nil {
			return fmt.Errorf("the schedule should be {start, first, blocks: [{days, at, count or until}]}: %v", err)
		}
		sch.Start = strings.TrimSpace(sch.Start)
		for i := range sch.Blocks {
			b := &sch.Blocks[i]
			b.At, b.Until = strings.TrimSpace(b.At), strings.TrimSpace(b.Until)
			for k, day := range b.Days {
				b.Days[k] = strings.ToLower(strings.TrimSpace(day))
			}
		}
		if err := sch.Check(); err != nil {
			return fmt.Errorf("the schedule: %w", err)
		}
		d.schedule = &sch
	}
	return nil
}

func (d detailsReq) given() bool {
	return d.CallSign != nil || d.Category != nil || d.Description != nil || d.Enabled != nil || d.Schedule != nil
}

// apply sets the details in a record.
func (d detailsReq) apply(m map[string]any) {
	set := func(key string, v *string) {
		switch {
		case v == nil:
		case *v == "":
			delete(m, key)
		default:
			m[key] = *v
		}
	}
	set("callSign", d.CallSign)
	set("category", d.Category)
	set("description", d.Description)
	if d.Enabled != nil {
		m["enabled"] = *d.Enabled
	}
	switch {
	case d.Schedule == nil:
	case d.schedule == nil:
		delete(m, "schedule")
	default:
		m["schedule"] = d.schedule
	}
}

var kindLabel = map[string]string{"jellyfin": "Jellyfin", "youtube": "YouTube", "folder": "folder", "weather": "weather"}

func (s *Server) putChannel(w http.ResponseWriter, r *http.Request) {
	current := r.PathValue("number")
	var req channelReq
	if !decode(w, r, &req) {
		return
	}
	number, numbered := vchan.ParseNumber(req.Number)
	req.Number, req.Name = number, strings.Join(strings.Fields(req.Name), " ")
	kind := cmp(req.Kind, "jellyfin")
	switch {
	case kindLabel[kind] == "":
		writeErr(w, http.StatusBadRequest, errors.New("kind is jellyfin, youtube, folder or weather"))
		return
	case !numbered:
		writeErr(w, http.StatusBadRequest, errors.New("the number should look like 1.4 or 104.0: 1 to 999, a dot, then 0 to 999"))
		return
	case req.Name == "" || len(req.Name) > 48 || strings.ContainsAny(req.Name, `/\`) || strings.HasPrefix(req.Name, "."):
		writeErr(w, http.StatusBadRequest, errors.New("give the channel a short name without slashes"))
		return
	case kind == "jellyfin" && req.Order != "" && req.Order != "shuffle" && req.Order != "aired":
		writeErr(w, http.StatusBadRequest, errors.New("order is shuffle or aired"))
		return
	case kind == "jellyfin" && (req.MaxBitrate < 0 || req.MaxBitrate > 100):
		writeErr(w, http.StatusBadRequest, errors.New("the bitrate cap is in Mbps, up to 100"))
		return
	}
	if err := req.check(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if kind == "weather" {
		if req.schedule != nil {
			writeErr(w, http.StatusBadRequest, errors.New("the schedule: the weather channel airs around the clock"))
			return
		}
		s.putWeather(w, r, current, req)
		return
	}
	if s.isWeather(req.Number) {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s is taken by %s", req.Number, s.Weather.Name()))
		return
	}
	var existing *vchan.Entry
	if current != "new" {
		e, ok := s.find(current)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("no channel %s", current))
			return
		}
		if k := vchan.KindOf(e.Channel); k != kind {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("%s is a %s channel and can't become a %s one; make a new channel instead",
				current, kindLabel[k], kindLabel[kind]))
			return
		}
		existing = &e
	} else if kind == "folder" {
		writeErr(w, http.StatusBadRequest, errors.New("folder channels are made by adding a folder of videos to the channels folder"))
		return
	}
	// What to write in the channel's folder besides its details: nothing
	// for a folder channel.
	var file string
	var write func(path string) error
	switch kind {
	case "jellyfin":
		file, write = channelFile, func(path string) error { return writeConfig(path, req.channelConfig) }
	case "youtube":
		cur := ytConfigOf(vchan.YouTubeDefaults())
		if existing != nil {
			c, _ := vchan.ReadYouTubeConfig(filepath.Join(s.Library.Root, existing.Folder))
			cur = ytConfigOf(c)
		}
		cfg, err := ytSettings(req.YouTube, cur)
		if err == nil && req.schedule != nil && cfg.Playlist == "" {
			err = errors.New("the schedule: uploads play in no order; a channel playing a playlist can air on a schedule")
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		file, write = youtubeFile, func(path string) error { return writeYtConfig(path, cfg) }
	}
	if e, ok := s.find(req.Number); ok && (existing == nil || e.Folder != existing.Folder) {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s is taken by %s", req.Number, e.Channel.Name()))
		return
	}

	// The folder is renamed only for a new number or name, so a folder
	// without a number keeps its name when only its details change.
	folder := req.Number + " " + req.Name
	switch {
	case existing == nil:
		if err := os.Mkdir(filepath.Join(s.Library.Root, folder), 0o755); err != nil {
			writeErr(w, http.StatusConflict, fmt.Errorf("can't make the folder: %w", err))
			return
		}
	case existing.Channel.Number() == req.Number && existing.Channel.Name() == req.Name:
		folder = existing.Folder
	case existing.Folder != folder:
		if _, err := os.Stat(filepath.Join(s.Library.Root, folder)); err == nil {
			writeErr(w, http.StatusConflict, fmt.Errorf("a folder named %q already exists", folder))
			return
		}
		if err := os.Rename(filepath.Join(s.Library.Root, existing.Folder), filepath.Join(s.Library.Root, folder)); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	dir := filepath.Join(s.Library.Root, folder)
	var err error
	if write != nil {
		err = write(filepath.Join(dir, file))
	}
	if err == nil && req.given() {
		err = updateRecord(filepath.Join(dir, vchan.DetailsFile), req.apply)
	}
	if err != nil {
		if existing == nil {
			_ = os.RemoveAll(dir)
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.Library.Rescan()
	e, ok := s.find(req.Number)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("saved, but the channel didn't appear"))
		return
	}
	log.Printf("admin: saved channel %s", folder)
	writeJSON(w, s.describe(e, s.antenna(r.Context())))
}

// putWeather saves the weather channel's number, name and details in its
// record.
func (s *Server) putWeather(w http.ResponseWriter, r *http.Request, current string, req channelReq) {
	if s.Weather == nil || s.Weather.Record == "" || !s.isWeather(current) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%s isn't the weather channel", current))
		return
	}
	if e, ok := s.find(req.Number); ok {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s is taken by %s", req.Number, e.Channel.Name()))
		return
	}
	err := updateRecord(s.Weather.Record, func(m map[string]any) {
		m["number"], m["name"] = req.Number, req.Name
		req.apply(m)
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The folders give way to the weather channel's number.
	s.Library.Rescan()
	log.Printf("admin: saved the weather channel as %s %s", req.Number, req.Name)
	writeJSON(w, s.describeWeather(s.antenna(r.Context())))
}

// updateRecord changes a details file (channel.json or .weather.json),
// keeping anything the page doesn't edit.
func updateRecord(path string, change func(map[string]any)) error {
	m := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	change(m)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(raw, '\n'), 0o644)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	e, ok := s.find(r.PathValue("number"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("no such channel"))
		return
	}
	// The channel's files, the one that makes it a channel first, then
	// its details and logo.
	var files []string
	switch e.Channel.(type) {
	case *vchan.Jellyfin:
		files = []string{channelFile}
	case *vchan.YouTube:
		files = vchan.YouTubeFiles()
	default:
		writeErr(w, http.StatusBadRequest, errors.New("only Jellyfin and YouTube channels are deleted here"))
		return
	}
	dir := filepath.Join(s.Library.Root, e.Folder)
	if logo := e.Channel.Details().Logo; logo != "" {
		files = append(files, filepath.Base(logo))
	}
	files = append(files, vchan.DetailsFile)
	if err := os.Remove(filepath.Join(dir, files[0])); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, f := range files[1:] {
		_ = os.Remove(filepath.Join(dir, f))
	}
	_ = os.Remove(dir) // only if nothing else is in it
	s.Library.Rescan()
	log.Printf("admin: deleted channel %s", e.Folder)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	var ch vchan.Channel
	if s.isWeather(number) {
		ch = s.Weather
	} else if e, ok := s.find(number); ok {
		ch = e.Channel
	}
	if ch == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such channel"))
		return
	}
	now := time.Now()
	out := []programJSON{}
	for _, p := range ch.Programs(now, now.Add(24*time.Hour)) {
		if len(out) == 12 {
			break
		}
		out = append(out, programOf(p))
	}
	writeJSON(w, map[string]any{"programs": out})
}

// sequenceItemJSON is an item of a channel that plays in order, for
// choosing which its schedule begins with.
type sequenceItemJSON struct {
	N        int    `json:"n"` // from 1, as a schedule's first counts
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Season   string `json:"season,omitempty"`
	Episode  string `json:"episode,omitempty"`
	Seconds  int    `json:"seconds"`
}

// getItems lists a channel's items in the order they air, for one that
// can air on a schedule.
func (s *Server) getItems(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	e, found := s.find(number)
	if !found && !s.isWeather(number) {
		writeErr(w, http.StatusNotFound, errors.New("no such channel"))
		return
	}
	seq, ok := e.Channel.(vchan.Sequenced)
	var items []vchan.SequenceItem
	if ok {
		items, ok = seq.Sequence()
	}
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%s doesn't play its videos in order", number))
		return
	}
	out := make([]sequenceItemJSON, len(items))
	for i, it := range items {
		out[i] = sequenceItemJSON{N: i + 1, Title: it.Title, Subtitle: it.Subtitle, Season: it.Season, Episode: it.Episode, Seconds: int(it.Length.Seconds())}
	}
	writeJSON(w, map[string]any{"items": out})
}

// readConfig reads the parts of a channel's jellyfin.json the page edits.
func readConfig(path string) (channelConfig, error) {
	c := channelConfig{Order: "shuffle"}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", channelFile, err)
	}
	if c.Order == "" {
		c.Order = "shuffle"
	}
	for _, l := range []*[]string{&c.Series, &c.Movies, &c.Collections, &c.Playlists} {
		if *l == nil {
			*l = []string{}
		}
	}
	return c, nil
}

// writeConfig updates a channel's jellyfin.json, keeping any settings the
// page doesn't edit (a different server, or libraries).
func writeConfig(path string, c channelConfig) error {
	m := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s: %w", channelFile, err)
		}
	}
	set := func(key string, list []string) {
		list = slices.Compact(slices.Sorted(slices.Values(list)))
		if len(list) == 0 {
			delete(m, key)
		} else {
			m[key] = list
		}
	}
	set("series", c.Series)
	set("movies", c.Movies)
	set("collections", c.Collections)
	set("playlists", c.Playlists)
	m["order"] = cmp(c.Order, "shuffle")
	if c.MaxBitrate > 0 {
		m["maxBitrate"] = c.MaxBitrate
	} else {
		delete(m, "maxBitrate")
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// A channel file may carry its own login, so keep it private too.
	return writeFile(path, append(raw, '\n'), 0o600)
}

func cmp(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// writeFile replaces path atomically.
func writeFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---------- library ----------

type itemJSON struct {
	ID             string   `json:"id"`
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	Year           int      `json:"year,omitempty"`
	Genres         []string `json:"genres"`
	Overview       string   `json:"overview,omitempty"`
	Count          int      `json:"count,omitempty"`
	RuntimeMinutes int      `json:"runtimeMinutes,omitempty"`
	Image          string   `json:"image"`
}

func (s *Server) getLibrary(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	s.mu.Lock()
	if b, ok := s.browse[kind]; ok && time.Since(b.at) < recheckEvery {
		s.mu.Unlock()
		writeJSON(w, map[string]any{"items": b.items})
		return
	}
	s.mu.Unlock()

	c, err := s.client()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	list := map[string]func(context.Context) ([]jellyfin.Item, error){
		"series": c.Series, "movies": c.Movies, "collections": c.Collections, "playlists": c.Playlists,
	}[kind]
	if list == nil {
		writeErr(w, http.StatusNotFound, errors.New("kind is series, movies, collections or playlists"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	found, err := list(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	items := make([]itemJSON, 0, len(found))
	for _, it := range found {
		x := itemJSON{
			ID: it.ID, Key: it.Key(), Name: it.Name, Year: it.Year, Genres: it.Genres, Overview: it.Overview,
			Count: it.Count, RuntimeMinutes: int(it.Runtime.Minutes()),
		}
		if x.Genres == nil {
			x.Genres = []string{}
		}
		if it.HasImage {
			x.Image = "/admin/api/image/" + it.ID
		}
		items = append(items, x)
	}
	slices.SortFunc(items, func(a, b itemJSON) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	s.mu.Lock()
	if s.browse == nil {
		s.browse = map[string]browsed{}
	}
	s.browse[kind] = browsed{items: items, at: time.Now()}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"items": items})
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	c, err := s.client()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	data, kind, err := c.Image(r.Context(), id, 300)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = w.Write(data)
}

// ---------- helpers ----------

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
