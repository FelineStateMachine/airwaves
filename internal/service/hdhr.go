package service

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/geo"
	"airwaves/internal/guide"
	"airwaves/internal/hdhr"
	"airwaves/internal/lineup"
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

type xmltvProgramme struct {
	Start    string     `xml:"start,attr"`
	Stop     string     `xml:"stop,attr"`
	Channel  string     `xml:"channel,attr"`
	Title    string     `xml:"title"`
	SubTitle string     `xml:"sub-title,omitempty"`
	Desc     string     `xml:"desc,omitempty"`
	Date     string     `xml:"date,omitempty"`
	Category []string   `xml:"category,omitempty"`
	Episode  string     `xml:"episode-num,omitempty"`
	Icon     *xmltvIcon `xml:"icon,omitempty"`
	New      *struct{}  `xml:"new,omitempty"`
	// Subtitles are "teletext" for closed captions, "onscreen" for
	// subtitles burned into the picture.
	Subtitles *xmltvSubtitles `xml:"subtitles,omitempty"`
}

type xmltvSubtitles struct {
	Type string `xml:"type,attr"`
}

const xmltvTime = "20060102150405 -0700"

// XMLTV implements hdhr.Backend. A custom channel's call sign is one of
// its display names, and its category is that of each of its programmes
// that has none of its own ("Other" says nothing, so it's left out).
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
	from, to := time.Now().Add(-time.Hour), time.Now().Add(48*time.Hour)
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
				x := xmltvProgramme{
					Start: p.Start.Format(xmltvTime), Stop: p.End.Format(xmltvTime), Channel: id,
					Title: p.Title, SubTitle: p.Subtitle, Desc: p.Description, Date: p.Date,
				}
				if p.Category != "" {
					x.Category = []string{p.Category}
				} else if d.Category != "" && d.Category != "Other" {
					x.Category = []string{d.Category}
				}
				if p.Season != "" && p.Episode != "" {
					x.Episode = fmt.Sprintf("S%sE%s", p.Season, p.Episode)
				}
				if p.Image != "" {
					x.Icon = &xmltvIcon{Src: p.Image}
				}
				if p.New {
					x.New = &struct{}{}
				}
				if p.Captions {
					// Subtitles for sound not in English are burned in for
					// HDHomeRun clients, which can't be told to show captions.
					x.Subtitles = &xmltvSubtitles{Type: "teletext"}
					if cc.Foreign(p.AudioLang) {
						x.Subtitles.Type = "onscreen"
					}
				}
				doc.Programmes = append(doc.Programmes, x)
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
			x := xmltvProgramme{
				Start: p.Start.Format(xmltvTime), Stop: p.End.Format(xmltvTime), Channel: id,
				Title: p.Title, SubTitle: p.EpisodeTitle, Desc: p.Description, Category: p.Genres,
			}
			if p.Season != "" && p.Episode != "" {
				x.Episode = fmt.Sprintf("S%sE%s", p.Season, p.Episode)
			}
			if p.Image != "" {
				x.Icon = &xmltvIcon{Src: p.Image}
			}
			if slices.Contains(p.Flags, "New") {
				x.New = &struct{}{}
			}
			doc.Programmes = append(doc.Programmes, x)
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
