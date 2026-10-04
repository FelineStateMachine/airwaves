package service

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"airwaves/internal/cc"
	"airwaves/internal/geo"
	"airwaves/internal/guide"
	"airwaves/internal/hdhr"
	"airwaves/internal/lineup"
	"airwaves/internal/vchan"
)

// HDHR adapts the service to the emulated HDHomeRun: Tvheadend's channels
// passed straight through, plus the custom channels.
func (s *Service) HDHR() hdhr.Backend { return hdhrBackend{s} }

// TunerCount is what the emulated HDHomeRun reports: the server's real
// tuners (two until any are found). Custom channels need no tuner.
func (s *Service) TunerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(s.tuners, 2)
}

type hdhrBackend struct{ s *Service }

// reportChannel finds the guide channel for a virtual number, preferring
// one with listings.
func (b hdhrBackend) reportChannel(number string) *lineup.Channel {
	_, chans := b.s.guideAndChannels()
	var found *lineup.Channel
	for i := range chans {
		if chans[i].Number == number {
			if chans[i].GuideID != "" {
				return &chans[i]
			}
			if found == nil {
				found = &chans[i]
			}
		}
	}
	return found
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
	if b.s.tvhTuner != nil {
		chans, err := b.s.tvhTuner.Channels(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range chans {
			if !c.Enabled || c.Number == "0" || len(c.Services) == 0 || custom[vchan.NumberKey(c.Number)] {
				continue
			}
			e := hdhr.Entry{Number: c.Number, Name: c.Name, HD: true, Group: "Antenna"}
			if rc := b.reportChannel(c.Number); rc != nil {
				e.Name, e.Logo = rc.CallSign, rc.Logo
			}
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b hdhr.Entry) int { return guide.CompareNumbers(a.Number, b.Number) })
	return out, nil
}

func (b hdhrBackend) Stream(ctx context.Context, number string, w io.Writer) error {
	if v := b.s.customChannel(number); v != nil {
		return v.Stream(ctx, w)
	}
	if b.s.tvhTuner == nil {
		return fmt.Errorf("no tuner")
	}
	uuid, err := b.s.tvhTuner.ChannelUUID(ctx, number)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.s.opt.Tvheadend.StreamURL(uuid), nil)
	if err != nil {
		return err
	}
	// No client timeout: a stream runs until the viewer stops.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tvheadend stream: HTTP %d", resp.StatusCode)
	}
	_, err = io.Copy(w, resp.Body)
	return err
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
	g, _ := b.s.guideAndChannels()
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
		rc := b.reportChannel(e.Number)
		if rc == nil || g == nil {
			continue
		}
		for _, p := range g.Programs[rc.GuideID] {
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
