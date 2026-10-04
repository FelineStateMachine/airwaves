// Package jellyfintest runs a fake Jellyfin server for tests: one account,
// a small library, and the endpoints package jellyfin uses.
package jellyfintest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Item is an entry in the fake library.
type Item struct {
	ID string
	// Type is "Series", "Season", "Episode", "Movie", "BoxSet", "Playlist",
	// or "CollectionFolder" for a library.
	Type      string
	MediaType string // "Audio" for a music playlist
	Name      string
	Parent    string   // the library, series or season it is in
	Children  []string // a collection's or playlist's entries
	Year      int
	Premiere  string // "2006-01-02"
	Overview  string
	Genres    []string
	Season    int
	Episode   int
	Runtime   time.Duration
	Silent    bool // no sound
	Image     bool // has a primary image
	Missing   bool // known to the server, with no file
	// Streams are a video's sound and subtitles, after its picture. Without
	// them it has one AAC track, unless Silent.
	Streams []Stream
}

// Stream is a sound or subtitle stream in a video's file, or a subtitle
// file beside it.
type Stream struct {
	Type            string // "Audio" or "Subtitle"
	Language        string // "eng"
	Codec           string // "aac", "subrip", "ass", "pgssub"...
	Title           string
	Default         bool
	Forced          bool
	HearingImpaired bool
	External        bool
	// SRT is a text subtitle's content, served by the subtitle endpoint.
	SRT string
}

// textSubtitle reports whether the server converts a subtitle codec to
// other text formats.
func textSubtitle(codec string) bool {
	switch codec {
	case "subrip", "srt", "ass", "ssa", "webvtt", "vtt", "mov_text", "text":
		return true
	}
	return false
}

// Request is a request the server received.
type Request struct {
	Method        string
	URL           string // path and query
	Authorization string
}

// Server is a fake Jellyfin server. User and Password sign in; APIKey, if
// set, is accepted as a token too. As on a real server, a token works only
// from the device it was issued to, and signing in again from a device ends
// its last sign-in.
type Server struct {
	*httptest.Server
	User         string
	Password     string
	APIKey       string
	QuickConnect bool // allow code sign-in

	mu       sync.Mutex
	items    []Item
	attempts int
	issued   int
	tokens   map[string]string // token to device
	codes    []*code
	requests []Request
}

type code struct {
	code, secret, device string
	approved             bool
}

// New starts a server holding items, stopped when the test ends.
func New(t testing.TB, user, password string, items ...Item) *Server {
	s := &Server{User: user, Password: password, items: items}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// SetItems replaces the library.
func (s *Server) SetItems(items ...Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
}

// Logins counts sign-in attempts, refused ones included.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

// Revoke ends every sign-in, as signing devices out does.
func (s *Server) Revoke() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.tokens)
}

// Approve approves a Quick Connect code, as the user entering it in a
// signed-in app does.
func (s *Server) Approve(c string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.codes {
		if x.code == c {
			x.approved = true
			return true
		}
	}
	return false
}

// issue signs a device in, ending its previous sign-in. s.mu is held.
func (s *Server) issue(device string) string {
	for t, d := range s.tokens {
		if d == device {
			delete(s.tokens, t)
		}
	}
	if s.tokens == nil {
		s.tokens = map[string]string{}
	}
	s.issued++
	t := fmt.Sprintf("session-%d", s.issued)
	s.tokens[t] = device
	return t
}

// Requests lists the requests so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

var (
	tokenField  = regexp.MustCompile(`Token="([^"]*)"`)
	deviceField = regexp.MustCompile(`DeviceId="([^"]+)"`)
)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth := r.Header.Get("Authorization")
	s.requests = append(s.requests, Request{Method: r.Method, URL: r.URL.String(), Authorization: auth})
	var token, device string
	if m := tokenField.FindStringSubmatch(auth); m != nil {
		token = m[1]
	}
	if m := deviceField.FindStringSubmatch(auth); m != nil {
		device = m[1]
	}
	path := r.URL.Path
	switch path {
	case "/QuickConnect/Enabled":
		writeJSON(w, s.QuickConnect)
		return
	case "/QuickConnect/Connect":
		for _, x := range s.codes {
			if x.secret == r.URL.Query().Get("secret") {
				writeJSON(w, map[string]any{"Authenticated": x.approved, "Secret": x.secret, "Code": x.code})
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(auth, "MediaBrowser ") || device == "" {
		http.Error(w, "no MediaBrowser authorization with a DeviceId", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodPost && path == "/Users/AuthenticateByName":
		var body struct{ Username, Pw string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.attempts++
		if !strings.EqualFold(body.Username, s.User) || body.Pw != s.Password {
			http.Error(w, "Invalid username or password entered.", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"AccessToken": s.issue(device), "User": map[string]any{"Id": "user-1", "Name": s.User}})
		return
	case r.Method == http.MethodPost && path == "/QuickConnect/Initiate":
		if !s.QuickConnect {
			http.Error(w, "Quick connect is disabled", http.StatusUnauthorized)
			return
		}
		x := &code{code: fmt.Sprintf("%06d", 100000+len(s.codes)), secret: fmt.Sprintf("secret-%d", len(s.codes)), device: device}
		s.codes = append(s.codes, x)
		writeJSON(w, map[string]any{"Authenticated": false, "Secret": x.secret, "Code": x.code, "DeviceId": device})
		return
	case r.Method == http.MethodPost && path == "/Users/AuthenticateWithQuickConnect":
		var body struct{ Secret string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, x := range s.codes {
			if x.secret == body.Secret && x.approved && x.device == device {
				writeJSON(w, map[string]any{"AccessToken": s.issue(device), "User": map[string]any{"Id": "user-1", "Name": s.User}})
				return
			}
		}
		http.Error(w, "Unknown or unapproved secret", http.StatusUnauthorized)
		return
	}
	if parts := strings.Split(strings.Trim(path, "/"), "/"); len(parts) == 4 && parts[0] == "Items" && parts[2] == "Images" {
		// Images load without signing in, as on a real server.
		if it, ok := s.find(parts[1]); ok && it.Image {
			// A JPEG's first bytes, then what was asked for.
			w.Header().Set("Content-Type", "image/jpeg")
			fmt.Fprintf(w, "\xff\xd8\xff\xe0jpeg %s %s %s", it.ID, parts[3], r.URL.Query().Get("maxWidth"))
			return
		}
		http.NotFound(w, r)
		return
	}
	apiKey := s.APIKey != "" && token == s.APIKey
	if d, ok := s.tokens[token]; !apiKey && (!ok || d != device) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(path, "/Videos/") && strings.Contains(path, "/Subtitles/") {
		s.subtitle(w, r)
		return
	}
	switch path {
	case "/Users/Me":
		if apiKey {
			http.Error(w, "an API key is not a user", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"Id": "user-1", "Name": s.User})
	case "/Users":
		if !apiKey {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		writeJSON(w, []map[string]any{{"Id": "user-1", "Name": s.User}})
	case "/UserViews", "/Library/MediaFolders":
		if path == "/Library/MediaFolders" && !apiKey {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var views []map[string]any
		for _, it := range s.items {
			if it.Type == "CollectionFolder" {
				views = append(views, s.dto(it, nil, true))
			}
		}
		writeJSON(w, map[string]any{"Items": views, "TotalRecordCount": len(views)})
	case "/Items":
		s.query(w, r)
	default:
		http.NotFound(w, r)
	}
}

// query answers /Items with the filters package jellyfin uses.
func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var list []Item
	switch {
	case q.Get("ids") != "":
		for id := range strings.SplitSeq(q.Get("ids"), ",") {
			if it, ok := s.find(id); ok {
				list = append(list, it)
			}
		}
	case q.Get("parentId") != "":
		p, _ := s.find(q.Get("parentId"))
		if p.Type == "BoxSet" || p.Type == "Playlist" {
			for _, id := range p.Children {
				if it, ok := s.find(id); ok {
					list = append(list, it)
				}
			}
			break
		}
		for _, it := range s.items {
			if it.Parent == p.ID || (q.Get("recursive") == "true" && s.within(it, p.ID)) {
				list = append(list, it)
			}
		}
	default:
		for _, it := range s.items {
			if it.Type != "CollectionFolder" {
				list = append(list, it)
			}
		}
	}
	types := strings.Split(q.Get("includeItemTypes"), ",")
	list = slices.DeleteFunc(list, func(it Item) bool {
		return (q.Get("includeItemTypes") != "" && !slices.Contains(types, it.Type)) ||
			(it.Missing && strings.Contains(q.Get("excludeLocationTypes"), "Virtual"))
	})
	if q.Get("sortBy") == "SortName" {
		slices.SortStableFunc(list, func(a, b Item) int { return strings.Compare(a.Name, b.Name) })
	}
	total := len(list)
	start, _ := strconv.Atoi(q.Get("startIndex"))
	list = list[min(start, len(list)):]
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n < len(list) {
		list = list[:n]
	}
	fields := strings.Split(q.Get("fields"), ",")
	out := []map[string]any{}
	for _, it := range list {
		out = append(out, s.dto(it, fields, q.Get("enableUserData") != "false"))
	}
	writeJSON(w, map[string]any{"Items": out, "TotalRecordCount": total, "StartIndex": start})
}

// subtitle serves /Videos/{id}/{source}/Subtitles/{index}[/{ticks}]/Stream.srt.
func (s *Server) subtitle(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 || parts[3] != "Subtitles" || parts[len(parts)-1] != "Stream.srt" {
		http.NotFound(w, r)
		return
	}
	it, ok := s.find(parts[1])
	index, err := strconv.Atoi(parts[4])
	if !ok || err != nil || parts[2] != "src-"+it.ID || index < 1 || index > len(it.Streams) {
		http.NotFound(w, r)
		return
	}
	st := it.Streams[index-1]
	if st.Type != "Subtitle" || !textSubtitle(st.Codec) {
		http.Error(w, "not a text subtitle", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, st.SRT)
}

func (s *Server) find(id string) (Item, bool) {
	for _, it := range s.items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// within reports whether id is one of its ancestors.
func (s *Server) within(it Item, id string) bool {
	for range 10 {
		p, ok := s.find(it.Parent)
		if !ok {
			return false
		}
		if p.ID == id {
			return true
		}
		it = p
	}
	return false
}

// dto renders an item as Jellyfin's BaseItemDto, with the optional fields
// asked for. As on a real server, a series' episode count comes only with
// user data.
func (s *Server) dto(it Item, fields []string, userData bool) map[string]any {
	m := map[string]any{"Id": it.ID, "Name": it.Name, "Type": it.Type, "LocationType": "FileSystem", "ImageTags": map[string]string{}}
	if it.MediaType != "" {
		m["MediaType"] = it.MediaType
	}
	if it.Missing {
		m["LocationType"] = "Virtual"
	}
	if it.Year > 0 {
		m["ProductionYear"] = it.Year
	}
	if it.Premiere != "" {
		m["PremiereDate"] = it.Premiere + "T00:00:00.0000000Z"
	}
	if it.Runtime > 0 {
		m["RunTimeTicks"] = int64(it.Runtime / 100)
	}
	if it.Image {
		m["ImageTags"] = map[string]string{"Primary": "tag-" + it.ID}
	}
	if slices.Contains(fields, "Overview") {
		m["Overview"] = it.Overview
	}
	if slices.Contains(fields, "Genres") {
		m["Genres"] = it.Genres
	}
	switch it.Type {
	case "Episode":
		for _, p := range s.items {
			if p.Type == "Series" && s.within(it, p.ID) {
				m["SeriesName"], m["SeriesId"] = p.Name, p.ID
				if p.Image {
					m["SeriesPrimaryImageTag"] = "tag-" + p.ID
				}
			}
		}
		if it.Season > 0 {
			m["ParentIndexNumber"] = it.Season
		}
		if it.Episode > 0 {
			m["IndexNumber"] = it.Episode
		}
	case "Series":
		if slices.Contains(fields, "RecursiveItemCount") && userData {
			n := 0
			for _, e := range s.items {
				if e.Type == "Episode" && s.within(e, it.ID) {
					n++
				}
			}
			m["RecursiveItemCount"] = n
		}
	case "BoxSet", "Playlist":
		if slices.Contains(fields, "ChildCount") {
			m["ChildCount"] = len(it.Children)
		}
	}
	if (it.Type == "Episode" || it.Type == "Movie") && !it.Missing && slices.Contains(fields, "MediaSources") {
		streams := []map[string]any{{"Type": "Video", "Codec": "h264", "Index": 0}}
		if !it.Silent && it.Streams == nil {
			streams = append(streams, map[string]any{"Type": "Audio", "Codec": "aac", "Index": 1, "IsDefault": true})
		}
		for i, st := range it.Streams {
			m := map[string]any{
				"Type": st.Type, "Index": i + 1, "Codec": st.Codec, "IsDefault": st.Default, "IsForced": st.Forced,
				"IsHearingImpaired": st.HearingImpaired, "IsExternal": st.External,
				"IsTextSubtitleStream": st.Type == "Subtitle" && textSubtitle(st.Codec),
			}
			if st.Language != "" {
				m["Language"] = st.Language
			}
			if st.Title != "" {
				m["Title"] = st.Title
			}
			streams = append(streams, m)
		}
		m["MediaSources"] = []map[string]any{{"Id": "src-" + it.ID, "RunTimeTicks": int64(it.Runtime / 100), "MediaStreams": streams}}
	}
	return m
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
