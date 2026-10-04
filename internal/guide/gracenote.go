// Package guide loads TV listings for a ZIP code's over-the-air lineup from
// the Gracenote grid service that backs tvlistings.gracenote.com.
//
// The endpoint is public but undocumented and intended for personal use.
// Schedules Direct is the supported alternative for anything more.
package guide

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"airwaves/internal/web"
)

const (
	gridURL = "https://tvlistings.gracenote.com/api/grid"
	// chunk is the largest timespan the grid endpoint accepts per request.
	chunk = 6 * time.Hour
)

// Channel is one virtual channel in the lineup.
type Channel struct {
	ID       string `json:"id"`       // Gracenote station ID
	Number   string `json:"number"`   // "2.1"
	CallSign string `json:"callSign"` // "KWGNDT"
	Network  string `json:"network"`
	Logo     string `json:"logo"`
}

// Program is one airing.
type Program struct {
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	Title        string    `json:"title"`
	EpisodeTitle string    `json:"episodeTitle,omitempty"`
	Description  string    `json:"description,omitempty"`
	Season       string    `json:"season,omitempty"`
	Episode      string    `json:"episode,omitempty"`
	Rating       string    `json:"rating,omitempty"`
	Year         string    `json:"year,omitempty"`
	Flags        []string  `json:"flags,omitempty"` // New, Live, Premiere, Finale
	Tags         []string  `json:"tags,omitempty"`  // CC, Stereo, HD...
	Genres       []string  `json:"genres,omitempty"`
	Image        string    `json:"image,omitempty"`
	ProgramID    string    `json:"programId,omitempty"`
	SeriesID     string    `json:"seriesId,omitempty"`
	// AudioLang is the sound's language as tagged ("eng", "jpn") where a
	// custom channel knows it.
	AudioLang string `json:"audioLang,omitempty"`
}

// Guide is a lineup with listings, keyed by channel ID.
type Guide struct {
	Lineup   string               `json:"lineup"`
	Start    time.Time            `json:"start"`
	End      time.Time            `json:"end"`
	Fetched  time.Time            `json:"fetched"`
	Channels []Channel            `json:"channels"`
	Programs map[string][]Program `json:"programs"`
}

// LineupID is the Gracenote over-the-air lineup for a ZIP code.
func LineupID(zip string) string { return "USA-OTA" + zip + "-DEFAULT" }

// Fetch loads listings for zip from start for the given number of hours.
func Fetch(ctx context.Context, c *http.Client, zip string, start time.Time, hours int) (*Guide, error) {
	start = start.Truncate(30 * time.Minute)
	end := start.Add(time.Duration(hours) * time.Hour)
	g := &Guide{
		Lineup:   LineupID(zip),
		Start:    start,
		End:      end,
		Fetched:  time.Now(),
		Programs: make(map[string][]Program),
	}

	var mu sync.Mutex
	seen := make(map[string]bool)
	eg, ctx := errgroup.WithContext(ctx)
	eg.SetLimit(4)
	for t := start; t.Before(end); t = t.Add(chunk) {
		eg.Go(func() error {
			resp, err := fetchChunk(ctx, c, zip, t)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			g.merge(resp, seen)
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	slices.SortFunc(g.Channels, func(a, b Channel) int { return CompareNumbers(a.Number, b.Number) })
	for id := range g.Programs {
		slices.SortFunc(g.Programs[id], func(a, b Program) int { return a.Start.Compare(b.Start) })
	}
	return g, nil
}

type gridResponse struct {
	Channels []struct {
		CallSign      string      `json:"callSign"`
		AffiliateName string      `json:"affiliateName"`
		ChannelID     string      `json:"channelId"`
		ChannelNo     string      `json:"channelNo"`
		Thumbnail     string      `json:"thumbnail"`
		Events        []gridEvent `json:"events"`
	} `json:"channels"`
}

type gridEvent struct {
	StartTime time.Time  `json:"startTime"`
	EndTime   time.Time  `json:"endTime"`
	Thumbnail string     `json:"thumbnail"`
	Filter    []string   `json:"filter"`
	Flag      []string   `json:"flag"`
	Tags      []string   `json:"tags"`
	Rating    flexString `json:"rating"`
	SeriesID  string     `json:"seriesId"`
	Program   struct {
		Title        string     `json:"title"`
		ID           string     `json:"id"`
		ShortDesc    flexString `json:"shortDesc"`
		Season       flexString `json:"season"`
		Episode      flexString `json:"episode"`
		EpisodeTitle flexString `json:"episodeTitle"`
		ReleaseYear  flexString `json:"releaseYear"`
	} `json:"program"`
}

// flexString accepts JSON strings, numbers or null.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	*f = flexString(strings.Trim(string(b), `"`))
	return nil
}

func fetchChunk(ctx context.Context, c *http.Client, zip string, t time.Time) (*gridResponse, error) {
	q := url.Values{
		"lineupId":     {LineupID(zip)},
		"timespan":     {strconv.Itoa(int(chunk.Hours()))},
		"headendId":    {"lineupId"},
		"country":      {"USA"},
		"timezone":     {""},
		"device":       {"-"},
		"postalCode":   {zip},
		"isOverride":   {"true"},
		"time":         {strconv.FormatInt(t.Unix(), 10)},
		"pref":         {"16,128"},
		"userId":       {"-"},
		"aid":          {"orbebb"},
		"languagecode": {"en-us"},
	}
	body, err := web.Get(ctx, c, gridURL+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("gracenote grid: %w", err)
	}
	var resp gridResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode gracenote grid: %w", err)
	}
	return &resp, nil
}

func (g *Guide) merge(resp *gridResponse, seen map[string]bool) {
	for _, ch := range resp.Channels {
		if !seen[ch.ChannelID] {
			seen[ch.ChannelID] = true
			g.Channels = append(g.Channels, Channel{
				ID:       ch.ChannelID,
				Number:   ch.ChannelNo,
				CallSign: ch.CallSign,
				Network:  networkName(ch.AffiliateName),
				Logo:     imageURL(ch.Thumbnail),
			})
		}
		for _, e := range ch.Events {
			key := ch.ChannelID + "@" + e.StartTime.Format(time.RFC3339)
			if seen[key] {
				continue
			}
			seen[key] = true
			p := Program{
				Start:        e.StartTime,
				End:          e.EndTime,
				Title:        e.Program.Title,
				EpisodeTitle: string(e.Program.EpisodeTitle),
				Description:  string(e.Program.ShortDesc),
				Season:       string(e.Program.Season),
				Episode:      string(e.Program.Episode),
				Rating:       string(e.Rating),
				Year:         string(e.Program.ReleaseYear),
				Flags:        e.Flag,
				Tags:         e.Tags,
				ProgramID:    e.Program.ID,
				SeriesID:     e.SeriesID,
			}
			for _, f := range e.Filter {
				p.Genres = append(p.Genres, strings.TrimPrefix(f, "filter-"))
			}
			if e.Thumbnail != "" {
				p.Image = "https://zpmc.tmsimg.com/assets/" + e.Thumbnail + ".jpg?w=360"
			}
			g.Programs[ch.ChannelID] = append(g.Programs[ch.ChannelID], p)
		}
	}
}

func imageURL(u string) string {
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	return strings.Replace(u, "?w=55", "?w=110", 1)
}

// networkNames shortens Gracenote's formal affiliate names.
var networkNames = map[string]string{
	"AMERICAN BROADCASTING COMPANY":   "ABC",
	"CBS TELEVISION NETWORK":          "CBS",
	"NATIONAL BROADCASTING COMPANY":   "NBC",
	"FOX ENTERTAINMENT":               "FOX",
	"THE CW TELEVISION NETWORK":       "The CW",
	"PUBLIC BROADCASTING SERVICE":     "PBS",
	"ION: INDEPENDENT TELEVISION":     "ION",
	"MYNETWORKTV":                     "MyNetworkTV",
	"TRINITY BROADCASTING NETWORK":    "TBN",
	"ETERNAL WORD TELEVISION NETWORK": "EWTN",
	"ME TV NETWORK":                   "MeTV",
	"START TV NETWORK":                "Start TV",
	"DABL NETWORK":                    "Dabl",
	"HEROES & ICONS NETWORK":          "Heroes & Icons",
	"HOME SHOPPING NETWORK":           "HSN",
	"DAYSTAR TELEVISION NETWORK":      "Daystar",
	"CHRISTIAN TELEVISION NETWORK":    "CTN",
	"SONLIFE BROADCASTING NETWORK":    "SonLife",
}

// acronyms stay upper case when title-casing affiliate names.
var acronyms = map[string]bool{
	"TV": true, "HD": true, "QVC": true, "QVC2": true, "HSN": true, "HSN2": true, "ION": true,
	"PBS": true, "LATV": true, "MTN": true, "CTN": true, "TBN": true, "JTV": true, "NHK": true,
	"LC": true, "LAFF": true, "ABC": true, "CBS": true, "NBC": true, "FOX": true, "CW": true,
}

func networkName(affiliate string) string {
	if n, ok := networkNames[affiliate]; ok {
		return n
	}
	if strings.HasPrefix(affiliate, "METV") {
		affiliate = "MeTV" + strings.TrimPrefix(affiliate, "METV")
	}
	words := strings.Fields(affiliate)
	for i, w := range words {
		if !acronyms[w] && w != "MeTV" {
			words[i] = w[:1] + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, " ")
}

// CompareNumbers orders virtual channel numbers like "2.1" < "2.10" < "11".
func CompareNumbers(a, b string) int {
	am, an := SplitNumber(a)
	bm, bn := SplitNumber(b)
	if am != bm {
		return am - bm
	}
	return an - bn
}

// SplitNumber parses "7.2" into (7, 2) and "11" into (11, 0).
func SplitNumber(s string) (major, minor int) {
	ma, mi, _ := strings.Cut(s, ".")
	major, _ = strconv.Atoi(ma)
	minor, _ = strconv.Atoi(mi)
	return major, minor
}

// BaseCall reduces a Gracenote station call sign to the FCC base call:
// "KWGNDT2" → "KWGN", "KZDND10" → "KZDN", "K48MND" → "K48MN".
func BaseCall(call string) string {
	s := strings.TrimRight(strings.ToUpper(call), "0123456789")
	for _, suf := range []string{"DT", "LD", "CD", "LP", "CA", "D"} {
		if rest, ok := strings.CutSuffix(s, suf); ok && len(rest) >= 4 {
			return rest
		}
	}
	return s
}
