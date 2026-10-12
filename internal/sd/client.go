package sd

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// API is the Schedules Direct JSON API.
const API = "https://json.schedulesdirect.org/20141201/"

// Request limits.
const (
	maxStations = 5000 // per /schedules and /schedules/md5
	maxPrograms = 5000 // per /programs
)

// StopError is a refusal that ends updating until the account is
// changed: a wrong password, a locked, expired or disabled account, too
// many logins, or a request Schedules Direct calls a client bug. Trying
// again would only count against the account.
type StopError struct {
	Code    int
	Message string
}

func (e *StopError) Error() string {
	if e.Code == 0 {
		return e.Message
	}
	return fmt.Sprintf("%s (Schedules Direct code %d)", e.Message, e.Code)
}

// apiError is any other error response.
type apiError struct {
	Code     int
	Response string
	Message  string
	Status   int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Schedules Direct: %s (code %d, HTTP %d)", cmp.Or(e.Message, e.Response), e.Code, e.Status)
}

// stopCodes end updating until the account changes.
var stopCodes = map[int]string{
	4001: "The Schedules Direct account has expired",
	4002: "Schedules Direct didn't take the password's form",
	4003: "Schedules Direct doesn't know that user name and password",
	4004: "The Schedules Direct account is locked for a while after failed logins",
	4005: "Schedules Direct has turned off this account's access; open a ticket with them",
	4007: "Schedules Direct has turned off this application",
	4008: "The Schedules Direct account isn't active",
	4009: "Too many logins to Schedules Direct today",
	4010: "Schedules Direct saw this account used from too many places",
	1003: "Schedules Direct wants a User-Agent",
	1011: "Schedules Direct wants JSON",
	2055: "Schedules Direct refused a debug request",
	9999: "Schedules Direct reported an internal failure",
}

// client is one update's conversation with Schedules Direct.
type client struct {
	base    string // API, or a test server's
	http    *http.Client
	ua      string
	account Account
	st      *state
	// relogged is set once a request has been retried with a new token.
	relogged bool
}

// envelope is the code every response object may carry.
type envelope struct {
	Code     *int   `json:"code"`
	Response string `json:"response"`
	Message  string `json:"message"`
}

// do makes a request with JSON in and out. Errors in the response, by its
// code, become *StopError or *apiError; an expired token is renewed once.
func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, cmp.Or(c.base, API)+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.ua)
	if in != nil || method == http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	if path != "token" {
		req.Header.Set("Token", c.st.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Schedules Direct: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if err != nil {
		return fmt.Errorf("Schedules Direct: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' && trimmed[0] != '[' {
		// Not JSON at all: a load balancer's refusal, or worse. Stop
		// rather than knock again.
		return &StopError{Message: fmt.Sprintf("Schedules Direct answered HTTP %d with something other than JSON; this server may be blocked", resp.StatusCode)}
	}
	if trimmed[0] == '{' {
		var env envelope
		if err := json.Unmarshal(trimmed, &env); err == nil && env.Code != nil && *env.Code != 0 {
			code := *env.Code
			if code == 4006 && path != "token" && !c.relogged {
				// The token ran out: one new one, one retry.
				c.relogged = true
				c.st.Token = ""
				if err := c.login(ctx, true); err != nil {
					return err
				}
				return c.do(ctx, method, path, in, out)
			}
			if msg, ok := stopCodes[code]; ok {
				return &StopError{Code: code, Message: msg}
			}
			return &apiError{Code: code, Response: env.Response, Message: env.Message, Status: resp.StatusCode}
		}
	}
	if resp.StatusCode >= 400 {
		return &apiError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(trimmed, out); err != nil {
		return fmt.Errorf("Schedules Direct %s: %w", path, err)
	}
	return nil
}

// login gets a token, unless the saved one has more than an hour left. A
// token lasts a day from its first use, and asking again within that day
// gives the same one; fresh asks for a new one.
func (c *client) login(ctx context.Context, fresh bool) error {
	if !fresh && c.st.Token != "" && time.Until(c.st.TokenExpires) > time.Hour {
		return nil
	}
	in := map[string]any{"username": c.account.User, "password": c.account.PasswordSHA1}
	if fresh || c.st.Token != "" {
		in["newToken"] = true
	}
	var out struct {
		Token        string `json:"token"`
		TokenExpires int64  `json:"tokenExpires"`
		ServerTime   int64  `json:"serverTime"`
	}
	if err := c.do(ctx, http.MethodPost, "token", in, &out); err != nil {
		return err
	}
	if out.Token == "" || out.TokenExpires == 0 {
		return errors.New("Schedules Direct gave no token; it may be offline")
	}
	// The expiry by the server's clock, carried over to ours.
	life := time.Duration(out.TokenExpires-cmp.Or(out.ServerTime, time.Now().Unix())) * time.Second
	c.st.Token, c.st.TokenExpires = out.Token, time.Now().Add(life)
	return nil
}

// update brings the state up to date: the account, the antenna lineup of
// zip, the schedules that changed and the programs they air.
func (c *client) update(ctx context.Context, zip string, now time.Time) error {
	if err := c.login(ctx, false); err != nil {
		return err
	}
	id := "USA-OTA-" + zip
	modified, err := c.status(ctx, id)
	if err != nil {
		return err
	}
	if modified == "" {
		if err := c.addLineup(ctx, id); err != nil {
			return err
		}
		modified = "added"
	}
	if c.st.Lineup != id || c.st.LineupModified != modified || len(c.st.Stations) == 0 {
		if err := c.lineup(ctx, id); err != nil {
			return err
		}
		c.st.LineupModified = modified
	}
	if err := c.schedules(ctx, now); err != nil {
		return err
	}
	if err := c.programs(ctx); err != nil {
		return err
	}
	c.st.prune(now)
	return nil
}

// status reads the account: when it expires, whether the service is up,
// and when the lineup id last changed ("" when the account hasn't got it).
func (c *client) status(ctx context.Context, id string) (string, error) {
	var out struct {
		Account struct {
			Expires    string `json:"expires"`
			MaxLineups int    `json:"maxLineups"`
		} `json:"account"`
		Lineups []struct {
			Lineup    string `json:"lineup"`
			ID        string `json:"ID"`
			Modified  string `json:"modified"`
			IsDeleted bool   `json:"isDeleted"`
		} `json:"lineups"`
		SystemStatus []json.RawMessage `json:"systemStatus"`
	}
	if err := c.do(ctx, http.MethodGet, "status", nil, &out); err != nil {
		return "", err
	}
	if t, err := time.Parse(time.RFC3339, out.Account.Expires); err == nil {
		c.st.Expires = t
	}
	for _, raw := range out.SystemStatus {
		var sys struct {
			Status, Message string
		}
		if json.Unmarshal(raw, &sys) == nil && strings.EqualFold(sys.Status, "Offline") {
			return "", fmt.Errorf("Schedules Direct is offline: %s", sys.Message)
		}
		break // the first entry is the current one
	}
	for _, l := range out.Lineups {
		if cmp.Or(l.Lineup, l.ID) == id && !l.IsDeleted {
			return cmp.Or(l.Modified, "unknown"), nil
		}
	}
	return "", nil
}

// addLineup adds the antenna lineup to the account, once: Schedules
// Direct allows a few changes a day.
func (c *client) addLineup(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodPut, "lineups/"+id, nil, nil)
	var ae *apiError
	switch {
	case errors.As(err, &ae) && ae.Code == 2100: // already there
		return nil
	case errors.As(err, &ae) && ae.Code == 2107:
		return errors.New("Schedules Direct is preparing this ZIP code's antenna lineup; trying again later")
	case errors.As(err, &ae) && (ae.Code == 4100 || ae.Code == 4101):
		return &StopError{Code: ae.Code, Message: "The Schedules Direct account can't take another lineup (" + ae.Message + "); remove one on schedulesdirect.org"}
	case errors.As(err, &ae) && ae.Code >= 2101 && ae.Code <= 2106:
		return &StopError{Code: ae.Code, Message: "Schedules Direct has no antenna lineup for ZIP code " + strings.TrimPrefix(id, "USA-OTA-")}
	}
	return err
}

// lineup reads the stations of lineup id and their channel numbers.
func (c *client) lineup(ctx context.Context, id string) error {
	var out struct {
		Map []struct {
			StationID string          `json:"stationID"`
			Major     int             `json:"atscMajor"`
			Minor     int             `json:"atscMinor"`
			RF        int             `json:"uhfVhf"`
			Channel   json.RawMessage `json:"channel"`
		} `json:"map"`
		Stations []struct {
			StationID string `json:"stationID"`
			Name      string `json:"name"`
			CallSign  string `json:"callsign"`
			Affiliate string `json:"affiliate"`
			Logos     []struct {
				URL      string `json:"URL"`
				Category string `json:"category"`
			} `json:"stationLogo"`
			Logo *struct {
				URL string `json:"URL"`
			} `json:"logo"`
		} `json:"stations"`
	}
	if err := c.do(ctx, http.MethodGet, "lineups/"+id, nil, &out); err != nil {
		return err
	}
	number := map[string]string{}
	for _, m := range out.Map {
		switch {
		case m.Major > 0:
			number[m.StationID] = fmt.Sprintf("%d.%d", m.Major, m.Minor)
		default:
			var ch string
			if json.Unmarshal(m.Channel, &ch) == nil && ch != "" {
				number[m.StationID] = strings.TrimLeft(ch, "0")
			}
		}
	}
	var stations []station
	for _, s := range out.Stations {
		n := number[s.StationID]
		if n == "" {
			continue
		}
		sta := station{ID: s.StationID, Number: n, CallSign: s.CallSign, Name: s.Name, Affiliate: s.Affiliate}
		// The logo for a light background (the guide's logo chips), else
		// any.
		for _, want := range []string{"dark", "gray", ""} {
			for _, l := range s.Logos {
				if sta.Logo == "" && (want == "" || l.Category == want) {
					sta.Logo = l.URL
				}
			}
		}
		if sta.Logo == "" && s.Logo != nil {
			sta.Logo = s.Logo.URL
		}
		stations = append(stations, sta)
	}
	if len(stations) == 0 {
		return fmt.Errorf("Schedules Direct's lineup %s has no stations", id)
	}
	c.st.Lineup, c.st.Stations = id, stations
	if c.st.Days == nil {
		c.st.Days = map[string]map[string]day{}
	}
	keep := map[string]bool{}
	for _, s := range stations {
		keep[s.ID] = true
	}
	for id := range c.st.Days {
		if !keep[id] {
			delete(c.st.Days, id)
		}
	}
	return nil
}

// dates are the UTC days to keep: yesterday (US evenings fall on the next
// UTC day) and Days from today.
func dates(now time.Time) []string {
	out := []string{}
	d := now.UTC().AddDate(0, 0, -1)
	for range Days + 1 {
		out = append(out, d.Format(time.DateOnly))
		d = d.AddDate(0, 0, 1)
	}
	return out
}

type stationDates struct {
	StationID string   `json:"stationID"`
	Date      []string `json:"date"`
}

// schedules fetches the station-days whose checksum changed since the
// last update, and only those.
func (c *client) schedules(ctx context.Context, now time.Time) error {
	ds := dates(now)
	var ask []stationDates
	for _, s := range c.st.Stations {
		ask = append(ask, stationDates{StationID: s.ID, Date: ds})
	}
	changed := map[string][]string{}
	for chunk := range slices.Chunk(ask, maxStations) {
		var out map[string]json.RawMessage
		if err := c.do(ctx, http.MethodPost, "schedules/md5", chunk, &out); err != nil {
			return err
		}
		for id, raw := range out {
			// A station with nothing comes as [] rather than {}.
			var days map[string]struct {
				Code int    `json:"code"`
				MD5  string `json:"md5"`
			}
			if json.Unmarshal(raw, &days) != nil {
				continue
			}
			for date, d := range days {
				if d.Code == 0 && d.MD5 != "" && c.st.Days[id][date].MD5 != d.MD5 {
					changed[id] = append(changed[id], date)
				}
			}
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var get []stationDates
	for id, ds := range changed {
		slices.Sort(ds)
		get = append(get, stationDates{StationID: id, Date: ds})
	}
	slices.SortFunc(get, func(a, b stationDates) int { return strings.Compare(a.StationID, b.StationID) })
	for chunk := range slices.Chunk(get, maxStations) {
		var out []json.RawMessage
		if err := c.do(ctx, http.MethodPost, "schedules", chunk, &out); err != nil {
			return err
		}
		for _, raw := range out {
			c.schedule(raw)
		}
	}
	return nil
}

// schedule keeps one station-day of a /schedules response; errors for
// one station-day (deleted, queued, out of range) skip it.
func (c *client) schedule(raw json.RawMessage) {
	var s struct {
		StationID string `json:"stationID"`
		Code      int    `json:"code"`
		Programs  []struct {
			ProgramID          string   `json:"programID"`
			AirDateTime        string   `json:"airDateTime"`
			Duration           int      `json:"duration"`
			MD5                string   `json:"md5"`
			New                bool     `json:"new"`
			Premiere           bool     `json:"premiere"`
			LiveTapeDelay      string   `json:"liveTapeDelay"`
			IsPremiereOrFinale string   `json:"isPremiereOrFinale"`
			AudioProperties    []string `json:"audioProperties"`
			VideoProperties    []string `json:"videoProperties"`
			Ratings            []struct {
				Body string `json:"body"`
				Code string `json:"code"`
			} `json:"ratings"`
		} `json:"programs"`
		Metadata struct {
			MD5       string `json:"md5"`
			StartDate string `json:"startDate"`
			Code      int    `json:"code"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &s) != nil || s.StationID == "" {
		return
	}
	if s.Code == 2201 || s.Metadata.Code == 2201 { // the station is gone
		delete(c.st.Days, s.StationID)
		c.st.LineupModified = "" // read the lineup again next time
		return
	}
	if s.Code != 0 || s.Metadata.StartDate == "" {
		return
	}
	d := day{MD5: s.Metadata.MD5}
	for _, p := range s.Programs {
		start, err := time.Parse(time.RFC3339, p.AirDateTime)
		if err != nil || p.ProgramID == "" || p.Duration <= 0 {
			continue
		}
		a := airing{
			ProgramID: p.ProgramID, Start: start, Seconds: p.Duration, MD5: p.MD5,
			New: p.New, Live: p.LiveTapeDelay == "Live",
			Premiere: p.Premiere || strings.Contains(p.IsPremiereOrFinale, "Premiere"),
			Finale:   strings.Contains(p.IsPremiereOrFinale, "Finale"),
		}
		for _, prop := range slices.Concat(p.AudioProperties, p.VideoProperties) {
			if tag := tags[strings.ToLower(prop)]; tag != "" && !slices.Contains(a.Tags, tag) {
				a.Tags = append(a.Tags, tag)
			}
		}
		for _, r := range p.Ratings {
			if a.Rating == "" || strings.Contains(r.Body, "USA") {
				a.Rating = ratingCode(r.Code)
			}
		}
		d.Airings = append(d.Airings, a)
	}
	if c.st.Days[s.StationID] == nil {
		c.st.Days[s.StationID] = map[string]day{}
	}
	c.st.Days[s.StationID][s.Metadata.StartDate] = d
}

// tags are the guide's names for an airing's sound and picture.
var tags = map[string]string{
	"cc": "CC", "stereo": "Stereo", "dvs": "DVS", "sap": "SAP", "dd 5.1": "5.1", "surround": "5.1",
	"subtitled": "Subtitled", "hdtv": "HD",
}

// ratingCode writes a rating as the guide does: "TV14" as "TV-14".
func ratingCode(code string) string {
	if rest, ok := strings.CutPrefix(code, "TV"); ok && rest != "" && rest[0] != '-' {
		return "TV-" + rest
	}
	return code
}

// programs fetches the programs aired whose details changed (or were
// never fetched).
func (c *client) programs(ctx context.Context) error {
	want := map[string]bool{}
	for _, days := range c.st.Days {
		for _, d := range days {
			for _, a := range d.Airings {
				if p, ok := c.st.Programs[a.ProgramID]; !ok || p.MD5 != a.MD5 {
					want[a.ProgramID] = true
				}
			}
		}
	}
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range want {
			if !yield(id) {
				return
			}
		}
	})
	for chunk := range slices.Chunk(ids, maxPrograms) {
		var out []json.RawMessage
		if err := c.do(ctx, http.MethodPost, "programs", chunk, &out); err != nil {
			return err
		}
		for _, raw := range out {
			if id, p, ok := parseProgram(raw); ok {
				c.st.Programs[id] = p
			}
		}
	}
	return nil
}

// sdProgram is the part of a /programs entry kept.
type sdProgram struct {
	ProgramID string `json:"programID"`
	Code      int    `json:"code"`
	MD5       string `json:"md5"`
	Titles    []struct {
		Title120 string `json:"title120"`
	} `json:"titles"`
	EpisodeTitle150 string `json:"episodeTitle150"`
	Descriptions    struct {
		Short []sdDescription `json:"description100"`
		Long  []sdDescription `json:"description1000"`
	} `json:"descriptions"`
	OriginalAirDate string   `json:"originalAirDate"`
	Genres          []string `json:"genres"`
	EntityType      string   `json:"entityType"`
	Cast            []struct {
		BillingOrder  string `json:"billingOrder"`
		Role          string `json:"role"`
		Name          string `json:"name"`
		CharacterName string `json:"characterName"`
	} `json:"cast"`
	Metadata []map[string]struct {
		Season  int `json:"season"`
		Episode int `json:"episode"`
	} `json:"metadata"`
	ContentRating []struct {
		Body string `json:"body"`
		Code string `json:"code"`
	} `json:"contentRating"`
	Movie *struct {
		Year          flexInt `json:"year"`
		Duration      int     `json:"duration"`
		QualityRating []struct {
			RatingsBody string `json:"ratingsBody"`
			Rating      string `json:"rating"`
		} `json:"qualityRating"`
	} `json:"movie"`
	EventDetails *struct {
		Teams []struct {
			Name   string `json:"name"`
			IsHome bool   `json:"isHome"`
		} `json:"teams"`
	} `json:"eventDetails"`
	HasImageArtwork bool `json:"hasImageArtwork"`
}

type sdDescription struct {
	Language    string `json:"descriptionLanguage"`
	Description string `json:"description"`
}

// flexInt reads a number that may come as a string.
type flexInt int

func (n *flexInt) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(string(raw), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	*n = flexInt(v)
	return err
}

// parseProgram turns a /programs entry into what's kept; ok is false for
// an entry that's an error (queued, unknown).
func parseProgram(raw json.RawMessage) (string, program, bool) {
	var sp sdProgram
	if json.Unmarshal(raw, &sp) != nil || sp.ProgramID == "" || sp.Code != 0 || sp.MD5 == "" {
		return "", program{}, false
	}
	p := program{
		MD5: sp.MD5, EpisodeTitle: sp.EpisodeTitle150, OriginalAirDate: sp.OriginalAirDate,
		Genres: sp.Genres, Kind: sp.EntityType, Art: sp.HasImageArtwork,
		Description: cmp.Or(english(sp.Descriptions.Long), english(sp.Descriptions.Short)),
	}
	if len(sp.Titles) > 0 {
		p.Title = sp.Titles[0].Title120
	}
	for _, m := range sp.Metadata {
		if g, ok := m["Gracenote"]; ok {
			p.Season, p.Episode = g.Season, g.Episode
		}
	}
	for _, r := range sp.ContentRating {
		if p.Rating == "" || strings.Contains(r.Body, "USA") || strings.Contains(r.Body, "Motion Picture") {
			p.Rating = ratingCode(r.Code)
		}
	}
	cast := slices.Clone(sp.Cast)
	slices.SortStableFunc(cast, func(a, b struct {
		BillingOrder  string `json:"billingOrder"`
		Role          string `json:"role"`
		Name          string `json:"name"`
		CharacterName string `json:"characterName"`
	}) int {
		return strings.Compare(a.BillingOrder, b.BillingOrder)
	})
	for _, m := range cast {
		if len(p.Cast) == 6 || m.Name == "" {
			continue
		}
		if m.CharacterName != "" {
			p.Cast = append(p.Cast, m.Name+" ("+m.CharacterName+")")
		} else {
			p.Cast = append(p.Cast, m.Name)
		}
	}
	if sp.Movie != nil {
		p.Year, p.Runtime = int(sp.Movie.Year), sp.Movie.Duration/60
		for _, q := range sp.Movie.QualityRating {
			if v, err := strconv.ParseFloat(q.Rating, 64); err == nil && strings.EqualFold(q.RatingsBody, "Gracenote") {
				p.Stars = v
			}
		}
	}
	if sp.EventDetails != nil {
		for _, t := range sp.EventDetails.Teams {
			if t.IsHome {
				p.HomeTeam = t.Name
			} else {
				p.AwayTeam = t.Name
			}
		}
	}
	return sp.ProgramID, p, true
}

// english picks the English description, else the first.
func english(ds []sdDescription) string {
	for _, d := range ds {
		if strings.HasPrefix(d.Language, "en") {
			return d.Description
		}
	}
	if len(ds) > 0 {
		return ds[0].Description
	}
	return ""
}
