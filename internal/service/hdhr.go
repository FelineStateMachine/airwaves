package service

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/geo"
	"airwaves/internal/guide"
	"airwaves/internal/hdhr"
	"airwaves/internal/lineup"
	"airwaves/internal/sd"
	"airwaves/internal/tuner"
	"airwaves/internal/vchan"
)

// HDHR adapts the service to the emulated HDHomeRun: the tuner's channels,
// as the app has them, plus the custom channels.
func (s *Service) HDHR() hdhr.Backend { return hdhrBackend{s} }

// TunerCount is what the emulated HDHomeRun reports: the server's real
// tuners (two until they're found). Custom channels need no tuner.
func (s *Service) TunerCount() int {
	if s.tuners == nil {
		return 2
	}
	return cmp.Or(s.tuners.Count(), 2)
}

type hdhrBackend struct{ s *Service }

// antenna is the tuner's channels described from the records, by number,
// and the listings.
func (b hdhrBackend) antenna(ctx context.Context) (map[string]lineup.TunerChannel, *guide.Guide, error) {
	b.s.mu.Lock()
	rep, g := b.s.report, b.s.guide
	b.s.mu.Unlock()
	chans, _, err := b.s.matched(ctx, rep, g)
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]lineup.TunerChannel, len(chans))
	for _, c := range chans {
		out[c.Number] = c
	}
	return out, g, nil
}

// Lineup implements hdhr.Backend. Custom channels keep the group
// "Airwaves" rather than their categories, so players list them together,
// apart from the antenna's; the categories are in the guide.
func (b hdhrBackend) Lineup(ctx context.Context) ([]hdhr.Entry, error) {
	var out []hdhr.Entry
	custom := map[string]bool{}
	for _, v := range b.s.customChannels() {
		d := v.Details()
		custom[vchan.NumberKey(d.Number)] = true
		out = append(out, hdhr.Entry{Number: d.Number, Name: d.Name, HD: true, Group: "Airwaves", Logo: logoPath(d)})
	}
	chans, _, err := b.antenna(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range chans {
		if custom[vchan.NumberKey(c.Number)] {
			continue
		}
		// Players match guide data by the listings' call signs, one per
		// subchannel ("KTVDDT2"); without listings, the broadcast's name.
		out = append(out, hdhr.Entry{Number: c.Number, Name: cmp.Or(c.GuideCallSign, c.Name), HD: true, Group: "Antenna", Logo: c.Logo})
	}
	slices.SortStableFunc(out, func(a, b hdhr.Entry) int { return guide.CompareNumbers(a.Number, b.Number) })
	return out, nil
}

// Stream implements hdhr.Backend: a custom channel, or an antenna channel
// passed through as broadcast, on a tuner shared like the app's.
func (b hdhrBackend) Stream(ctx context.Context, number string, w io.Writer) error {
	if v := b.s.customChannel(number); v != nil {
		return v.Stream(ctx, w)
	}
	if b.s.tuners == nil {
		return fmt.Errorf("no channel %s: %w", number, ErrNoAntenna)
	}
	freq, program, err := b.s.tuning(ctx, number)
	if err != nil {
		return err
	}
	sub, err := b.s.tuners.Open(ctx, tuner.Request{FrequencyHz: freq, Program: program, Priority: tuner.Watching, Who: number + " for an HDHomeRun app"})
	if err != nil {
		return err
	}
	defer sub.Close()
	wait, cancel := context.WithTimeout(ctx, b.s.timing.noLock)
	_, err = sub.Wait(wait)
	cancel()
	if err != nil {
		return err
	}
	log.Printf("hdhr: streaming %s", number)
	defer log.Printf("hdhr: stopped streaming %s", number)
	return sub.Copy(ctx, w)
}

type xmltvDoc struct {
	XMLName    xml.Name         `xml:"tv"`
	Generator  string           `xml:"generator-info-name,attr"`
	Channels   []xmltvChannel   `xml:"channel"`
	Programmes []xmltvProgramme `xml:"programme"`
}

type xmltvChannel struct {
	ID    string     `xml:"id,attr"`
	Names []string   `xml:"display-name"`
	Icon  *xmltvIcon `xml:"icon,omitempty"`
	LCN   string     `xml:"lcn,omitempty"`
}

type xmltvIcon struct {
	Src string `xml:"src,attr"`
}

// xmltvProgramme is one airing, its elements in the order XMLTV's DTD
// has them, with series-id and live (newer than the DTD) last.
type xmltvProgramme struct {
	Start      string          `xml:"start,attr"`
	Stop       string          `xml:"stop,attr"`
	Channel    string          `xml:"channel,attr"`
	Title      string          `xml:"title"`
	SubTitle   string          `xml:"sub-title,omitempty"`
	Desc       string          `xml:"desc,omitempty"`
	Credits    *xmltvCredits   `xml:"credits,omitempty"`
	Date       string          `xml:"date,omitempty"`
	Category   []string        `xml:"category,omitempty"`
	Length     *xmltvLength    `xml:"length,omitempty"`
	Icon       *xmltvIcon      `xml:"icon,omitempty"`
	Episodes   []xmltvSystem   `xml:"episode-num,omitempty"`
	Video      *xmltvVideo     `xml:"video,omitempty"`
	Previously *xmltvPrevious  `xml:"previously-shown,omitempty"`
	Premiere   *struct{}       `xml:"premiere,omitempty"`
	New        *struct{}       `xml:"new,omitempty"`
	Subtitles  *xmltvSubtitles `xml:"subtitles,omitempty"`
	Rating     *xmltvRating    `xml:"rating,omitempty"`
	Stars      *xmltvRating    `xml:"star-rating,omitempty"`
	SeriesID   *xmltvSystem    `xml:"series-id,omitempty"`
	Live       *struct{}       `xml:"live,omitempty"`
}

// xmltvCredits are plain names: Jellyfin 10.11's reader fails a channel
// whose credits hold anything else.
type xmltvCredits struct {
	Actors []xmltvActor `xml:"actor"`
}

type xmltvActor struct {
	Role string `xml:"role,attr,omitempty"`
	Name string `xml:",chardata"`
}

type xmltvLength struct {
	Units string `xml:"units,attr"`
	Value int    `xml:",chardata"`
}

type xmltvSystem struct {
	System string `xml:"system,attr"`
	Value  string `xml:",chardata"`
}

type xmltvVideo struct {
	Quality string `xml:"quality"`
}

type xmltvPrevious struct {
	Start string `xml:"start,attr,omitempty"`
}

type xmltvSubtitles struct {
	Type string `xml:"type,attr"`
}

type xmltvRating struct {
	System string `xml:"system,attr,omitempty"`
	Value  string `xml:"value"`
}

const xmltvTime = "20060102150405 -0700"

// customProgram is a custom channel's program as the guide has it.
func customProgram(p vchan.Program) guide.Program {
	gp := guide.Program{
		Start: p.Start, End: p.End, Title: p.Title, EpisodeTitle: p.Subtitle, Description: p.Description,
		Season: p.Season, Episode: p.Episode, Image: p.Image, AudioLang: p.AudioLang,
		OriginalAirDate: p.Aired, Cast: p.Cast, Rating: p.Rating, Genres: slices.Clone(p.Genres),
	}
	if len(p.Date) >= 4 {
		gp.Year = p.Date[:4]
	}
	if p.New {
		gp.Flags = []string{"New"}
	}
	if p.Captions {
		gp.Tags = []string{"CC"}
	}
	return gp
}

// typeWords are the guide's genres (its own filter keys, any case) that
// xmltvCategories says with a type word.
var typeWords = []string{"movie", "movies", "sports", "sports event", "news", "kids", "children"}

// xmltvCategories leads with the words players file by (Jellyfin's
// defaults, and Channels DVR's): Movie, Series, Sports event and Sports,
// News, Children; then the program's own genres.
func xmltvCategories(p guide.Program) []string {
	has := func(g string) bool {
		return slices.ContainsFunc(p.Genres, func(x string) bool { return strings.EqualFold(x, g) })
	}
	movie := has("movie") || has("movies") || strings.HasPrefix(p.ProgramID, "MV")
	var out []string
	if movie {
		out = append(out, "Movie")
	} else if p.Season != "" || p.EpisodeTitle != "" || strings.HasPrefix(p.ProgramID, "EP") {
		out = append(out, "Series")
	}
	if has("sports") || has("sports event") || strings.HasPrefix(p.ProgramID, "SP") {
		out = append(out, "Sports event", "Sports")
	}
	if has("news") {
		out = append(out, "News")
	}
	if has("kids") || has("children") {
		out = append(out, "Children")
	}
	for _, g := range p.Genres {
		if g == "" || slices.Contains(typeWords, strings.ToLower(g)) {
			continue
		}
		if g == strings.ToLower(g) {
			g = strings.ToUpper(g[:1]) + g[1:] // a filter key, as a genre
		}
		if !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, g) }) {
			out = append(out, g)
		}
	}
	return out
}

// xmltvProgram is one airing for the XMLTV guide, with all that players
// take from it; abs makes a path on this server an address, and
// captions says how a custom channel's captions come ("" for an antenna
// channel's, from its tags).
func xmltvProgram(p guide.Program, channel string, abs func(string) string, captions string) xmltvProgramme {
	x := xmltvProgramme{
		Start: p.Start.Format(xmltvTime), Stop: p.End.Format(xmltvTime), Channel: channel,
		Title: p.Title, SubTitle: p.EpisodeTitle, Desc: p.Description, Category: xmltvCategories(p),
	}
	if len(p.Cast) > 0 {
		x.Credits = &xmltvCredits{}
		for _, c := range p.Cast {
			name, role := c, ""
			if i := strings.LastIndex(c, " ("); i > 0 && strings.HasSuffix(c, ")") {
				name, role = c[:i], c[i+2:len(c)-1]
			}
			x.Credits.Actors = append(x.Credits.Actors, xmltvActor{Name: name, Role: role})
		}
	}
	movie := slices.Contains(x.Category, "Movie")
	switch {
	case p.OriginalAirDate != "" && !movie:
		x.Date = strings.ReplaceAll(p.OriginalAirDate, "-", "")
	case p.Year != "":
		x.Date = p.Year
	}
	if p.Runtime > 0 {
		x.Length = &xmltvLength{Units: "minutes", Value: p.Runtime}
	}
	if src := p.Image; src != "" {
		if strings.HasPrefix(src, "/") && abs != nil {
			src = abs(src)
		}
		x.Icon = &xmltvIcon{Src: src}
	}
	if p.Season != "" && p.Episode != "" {
		s, _ := strconv.Atoi(p.Season)
		e, _ := strconv.Atoi(p.Episode)
		if s > 0 && e > 0 {
			x.Episodes = append(x.Episodes,
				xmltvSystem{System: "xmltv_ns", Value: fmt.Sprintf("%d.%d.", s-1, e-1)},
				xmltvSystem{System: "onscreen", Value: fmt.Sprintf("S%02dE%02d", s, e)})
		}
	}
	// One ID per program, so players tell episodes apart (and an airing
	// of the same one twice is a repeat): Gracenote's, or one made from
	// what the program is.
	if id := p.ProgramID; len(id) == 14 {
		x.Episodes = append(x.Episodes, xmltvSystem{System: "dd_progid", Value: id[:10] + "." + id[10:]})
	} else {
		h := fnv.New64a()
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", p.Title, p.EpisodeTitle, p.Season, p.Episode)
		x.Episodes = append(x.Episodes, xmltvSystem{System: "dd_progid", Value: fmt.Sprintf("AW%08x.%04x", h.Sum64()>>32, h.Sum64()&0xffff)})
	}
	if slices.Contains(p.Tags, "HD") {
		x.Video = &xmltvVideo{Quality: "HDTV"}
	}
	isNew, live := slices.Contains(p.Flags, "New"), slices.Contains(p.Flags, "Live")
	if !isNew && !live {
		// A repeat, with when it first aired when that's another day.
		x.Previously = &xmltvPrevious{}
		if d := strings.ReplaceAll(p.OriginalAirDate, "-", ""); d != "" && d != p.Start.Format("20060102") {
			x.Previously.Start = d
		}
	}
	if slices.Contains(p.Flags, "Premiere") {
		x.Premiere = &struct{}{}
	}
	if isNew {
		x.New = &struct{}{}
	}
	switch {
	case captions != "":
		x.Subtitles = &xmltvSubtitles{Type: captions}
	case slices.Contains(p.Tags, "CC"):
		x.Subtitles = &xmltvSubtitles{Type: "teletext"}
	}
	if p.Rating != "" {
		sys := "MPAA" // a film's, as TV's start "TV-"
		if strings.HasPrefix(p.Rating, "TV-") {
			sys = "VCHIP"
		}
		x.Rating = &xmltvRating{System: sys, Value: p.Rating}
	}
	if p.Stars > 0 {
		x.Stars = &xmltvRating{Value: strconv.FormatFloat(p.Stars, 'f', -1, 64) + "/4"}
	}
	if p.SeriesID != "" {
		x.SeriesID = &xmltvSystem{System: "airwaves", Value: p.SeriesID}
	} else if !movie && (p.EpisodeTitle != "" || p.Season != "") {
		h := fnv.New32a()
		h.Write([]byte(p.Title))
		x.SeriesID = &xmltvSystem{System: "airwaves", Value: fmt.Sprintf("AW%08x", h.Sum32())}
	}
	if live {
		x.Live = &struct{}{}
	}
	return x
}

// XMLTV implements hdhr.Backend: every channel, with its programs as far
// as the listings go (two days of Gracenote's grid, two weeks from
// Schedules Direct), each with all that players take from it. A custom
// channel's call sign is one of its display names.
func (b hdhrBackend) XMLTV(ctx context.Context, w io.Writer, abs func(path string) string) error {
	entries, err := b.Lineup(ctx)
	if err != nil {
		return err
	}
	antenna, g, err := b.antenna(ctx)
	if err != nil {
		return err
	}
	custom := map[string]vchan.Channel{}
	for _, v := range b.s.customChannels() {
		custom[v.Number()] = v
	}
	from, to := time.Now().Add(-time.Hour), time.Now().Add(sd.Days*24*time.Hour)
	doc := xmltvDoc{Generator: "Airwaves"}
	for _, e := range entries {
		id := hdhr.ChannelID(e.Number)
		v := custom[e.Number]
		var d vchan.Details
		names := []string{e.Number + " " + e.Name, e.Name}
		if v != nil {
			if d = v.Details(); d.CallSign != "" && d.CallSign != e.Name {
				names = append(names, d.CallSign)
			}
		}
		// The number last: Jellyfin matches channels by it.
		ch := xmltvChannel{ID: id, Names: append(names, e.Number), LCN: e.Number}
		if e.Logo != "" {
			src := e.Logo
			if strings.HasPrefix(src, "/") && abs != nil {
				src = abs(src)
			}
			ch.Icon = &xmltvIcon{Src: src}
		}
		doc.Channels = append(doc.Channels, ch)

		if v != nil {
			for _, p := range v.Programs(from, to) {
				// Captions for sound not in English are burned in for
				// HDHomeRun clients, which can't be told to show them.
				captions := ""
				if p.Captions {
					captions = "teletext"
					if cc.Foreign(p.AudioLang) {
						captions = "onscreen"
					}
				}
				// Filed under the program's category, else the channel's.
				gp := customProgram(p)
				if c := cmp.Or(p.Category, d.Category); c != "" && c != "Other" {
					gp.Genres = append([]string{c}, gp.Genres...)
				}
				doc.Programmes = append(doc.Programmes, xmltvProgram(gp, id, abs, captions))
			}
			continue
		}
		ac, ok := antenna[e.Number]
		if !ok || ac.GuideID == "" || g == nil {
			continue
		}
		for _, p := range g.Programs[ac.GuideID] {
			if p.End.Before(from) || p.Start.After(to) {
				continue
			}
			doc.Programmes = append(doc.Programmes, xmltvProgram(p, id, abs, ""))
		}
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", " ")
	return enc.Encode(doc)
}

// weatherStarRev versions the branded WeatherStar display (see
// deploy/weatherstar).
const weatherStarRev = "3"

// weatherStarURL is the WeatherStar 4000+ kiosk URL for the server's
// location, on the display at base ("http://host:port").
func (s *Service) weatherStarURL(ctx context.Context, base string, music bool) (string, error) {
	snap, err := s.snapshot(ctx, false)
	if err != nil {
		return "", err
	}
	p := snap.Report.Point
	if p == (geo.Point{}) {
		return "", ErrNoLocation
	}
	q := []string{
		"latLon=" + url.QueryEscape(fmt.Sprintf(`{"lat":%.4f,"lon":%.4f}`, p.Lat, p.Lon)),
		"kiosk=true", "wide=true",
		// Changes when the branded display changes, so web views that
		// cached an older page load the new one.
		"airwaves=" + weatherStarRev,
	}
	// The display remembers its music setting in the browser, so say "off"
	// outright rather than leaving it out.
	if music {
		q = append(q, "mediaPlaying=true", "mediaVolume=0.6")
	} else {
		q = append(q, "mediaPlaying=false")
	}
	return base + "/?" + strings.Join(q, "&"), nil
}
