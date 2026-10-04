package service

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"airwaves/internal/hdhr"
	"airwaves/internal/jellyfin/jellyfintest"
	"airwaves/internal/lineup"
	"airwaves/internal/reception"
	"airwaves/internal/vchan"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newCustomService is a service with a weather channel and a channels
// folder of Jellyfin channels, playing from a fake Jellyfin server:
// 1.4 Cartoons (TOON, Kids, with a logo) and 1.5 Off (turned off).
func newCustomService(t *testing.T) (*Service, string) {
	t.Helper()
	jf := jellyfintest.New(t, "viewer", "pw",
		jellyfintest.Item{ID: "lib", Type: "CollectionFolder", Name: "Shows"},
		jellyfintest.Item{ID: "fut", Type: "Series", Name: "Futurama", Parent: "lib", Year: 1999},
		jellyfintest.Item{ID: "fut-1-1", Type: "Episode", Name: "Space Pilot 3000", Parent: "fut", Season: 1, Episode: 1, Runtime: 22 * time.Minute},
		jellyfintest.Item{ID: "fut-1-2", Type: "Episode", Name: "The Series Has Landed", Parent: "fut", Season: 1, Episode: 2, Runtime: 22 * time.Minute},
		jellyfintest.Item{ID: "giant", Type: "Movie", Name: "The Iron Giant", Year: 1999, Runtime: 86 * time.Minute},
	)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".jellyfin.json"), fmt.Sprintf(`{"server": %q, "user": "viewer", "password": "pw"}`, jf.URL))
	writeFile(t, filepath.Join(root, "1.4 Cartoons", "jellyfin.json"), `{"series": ["Futurama"], "movies": ["The Iron Giant"], "order": "aired"}`)
	writeFile(t, filepath.Join(root, "1.4 Cartoons", vchan.DetailsFile), `{"callSign": "TOON", "category": "Kids", "description": "Saturday mornings."}`)
	writeFile(t, filepath.Join(root, "1.4 Cartoons", "logo.png"), "\x89PNG\r\n\x1a\nlogo")
	writeFile(t, filepath.Join(root, "1.5 Off", "jellyfin.json"), `{"series": ["Futurama"]}`)
	writeFile(t, filepath.Join(root, "1.5 Off", vchan.DetailsFile), `{"enabled": false}`)

	s := &Service{opt: Options{WeatherStarPort: 8080, ChannelsDir: root}, cfg: Config{ZIP: "80302"}.normalized()}
	s.wx = &vchan.Weather{Record: filepath.Join(root, vchan.WeatherFile)}
	s.wxStreams = true
	s.library = &vchan.Library{Root: root, FFmpeg: "ffmpeg", Reserved: s.reserved}
	return s, root
}

func TestSnapshotCustomChannels(t *testing.T) {
	s, root := newCustomService(t)
	custom := s.custom()
	var got []string
	for _, c := range custom {
		got = append(got, c.Number+" "+c.Kind)
	}
	if !slices.Equal(got, []string{"1.1 weather", "1.4 jellyfin"}) {
		t.Fatalf("custom = %v", got)
	}
	wx, toon := custom[0], custom[1]
	if wx.Name != "Airwaves Weather" || wx.CallSign != "WX" || wx.Category != "Weather" || wx.Description == "" || wx.Logo != "" ||
		wx.Programs == nil || len(wx.Programs) != 0 {
		t.Errorf("weather: %+v", wx)
	}
	if toon.Name != "Cartoons" || toon.CallSign != "TOON" || toon.Category != "Kids" || toon.Description != "Saturday mornings." ||
		!strings.HasPrefix(toon.Logo, "/channel-logos/1.4?v=") || len(toon.Programs) == 0 || toon.Programs[0].Title == "" {
		t.Errorf("cartoons: %+v", toon)
	}
	raw, _ := json.Marshal(wx)
	if !strings.Contains(string(raw), `"kind":"weather"`) || !strings.Contains(string(raw), `"programs":[]`) {
		t.Errorf("weather JSON: %s", raw)
	}

	// The weather channel's record moves it and can turn it off.
	writeFile(t, filepath.Join(root, vchan.WeatherFile), `{"number": "1.9", "callSign": "WTHR"}`)
	if c := s.custom(); len(c) != 2 || c[1].Number != "1.9" || c[1].CallSign != "WTHR" || c[1].Name != "Airwaves Weather" {
		t.Errorf("moved: %+v", c)
	}
	writeFile(t, filepath.Join(root, vchan.WeatherFile), `{"number": "1.9", "enabled": false}`)
	if c := s.custom(); len(c) != 1 || c[0].Number != "1.4" {
		t.Errorf("weather off: %+v", c)
	}
	// With none on, still a list: the app reads its absence as a server
	// from before the weather channel was listed here.
	writeFile(t, filepath.Join(root, "1.4 Cartoons", vchan.DetailsFile), `{"enabled": false}`)
	raw, _ = json.Marshal(&Snapshot{Custom: s.custom()})
	if !strings.Contains(string(raw), `"custom":[]`) {
		t.Errorf("none on: %s", raw)
	}
}

func TestAntennaChannels(t *testing.T) {
	s, _ := newCustomService(t)
	s.report = &lineup.Report{Channels: []lineup.Channel{
		{Number: "7.1", CallSign: "KMGH", Tier: map[string]reception.Tier{"rooftop": reception.Strong}},
		{Number: "7.1", CallSign: "KMGH-LD", Tier: map[string]reception.Tier{"rooftop": reception.Strong}},
		{Number: "1.4", CallSign: "KTOON", Tier: map[string]reception.Tier{"rooftop": reception.Good}},
		{Number: "50.1", CallSign: "KFAR", Tier: map[string]reception.Tier{"rooftop": reception.Unlikely}},
	}}
	got := s.AntennaChannels(t.Context())
	if len(got) != 2 || got["7.1"] != "KMGH" || got["1.4"] != "KTOON" {
		t.Errorf("antenna = %v", got)
	}
}

type xmltvOut struct {
	Channels []struct {
		ID    string   `xml:"id,attr"`
		Names []string `xml:"display-name"`
		Icon  struct {
			Src string `xml:"src,attr"`
		} `xml:"icon"`
	} `xml:"channel"`
	Programmes []struct {
		Channel  string   `xml:"channel,attr"`
		Title    string   `xml:"title"`
		Category []string `xml:"category"`
	} `xml:"programme"`
}

func TestHDHomeRunCustomChannels(t *testing.T) {
	s, root := newCustomService(t)
	dev := &hdhr.Server{Backend: s.HDHR(), FriendlyName: "Airwaves", Tuners: func() int { return 2 }, Logos: s.LogoHandler()}
	srv := httptest.NewServer(dev.Handler())
	defer srv.Close()
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, string(raw)
	}

	_, raw := get("/lineup.json")
	var lineup []map[string]any
	if err := json.Unmarshal([]byte(raw), &lineup); err != nil {
		t.Fatal(err)
	}
	var numbers []string
	for _, e := range lineup {
		numbers = append(numbers, e["GuideNumber"].(string)+" "+e["GuideName"].(string))
	}
	if !slices.Equal(numbers, []string{"1.1 Airwaves Weather", "1.4 Cartoons"}) {
		t.Errorf("lineup: %v", numbers)
	}

	// The playlist points at the logo on this server, and keeps custom
	// channels in their own group.
	_, m3u := get("/channels.m3u")
	logo := ""
	for _, line := range strings.Split(m3u, "\n") {
		if strings.Contains(line, `tvg-chno="1.4"`) {
			if !strings.Contains(line, `group-title="Airwaves"`) || !strings.Contains(line, `tvg-name="Cartoons"`) {
				t.Errorf("1.4: %s", line)
			}
			if i := strings.Index(line, `tvg-logo="`); i >= 0 {
				logo = line[i+len(`tvg-logo="`):]
				logo = logo[:strings.Index(logo, `"`)]
			}
		}
		if strings.Contains(line, `tvg-chno="1.1"`) && !strings.Contains(line, `tvg-logo=""`) {
			t.Errorf("weather has no logo yet: %s", line)
		}
	}
	if !strings.HasPrefix(logo, srv.URL+"/channel-logos/1.4?v=") {
		t.Fatalf("logo %q in:\n%s", logo, m3u)
	}
	resp, body := get(strings.TrimPrefix(logo, srv.URL))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || body != "\x89PNG\r\n\x1a\nlogo" ||
		!strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("logo: %d %v %q", resp.StatusCode, resp.Header, body)
	}
	if resp, _ := get("/channel-logos/1.1"); resp.StatusCode != 404 {
		t.Errorf("weather without a logo: %d", resp.StatusCode)
	}
	// Turned off, a channel's logo is still there for the admin page and
	// anything that had it.
	writeFile(t, filepath.Join(root, "1.5 Off", "logo.svg"), "<svg/>")
	if resp, _ := get("/channel-logos/1.5"); resp.StatusCode != 200 {
		t.Errorf("off channel's logo: %d", resp.StatusCode)
	}

	_, raw = get("/xmltv.xml")
	var doc xmltvOut
	if err := xml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.Channels {
		switch c.ID {
		case "airwaves.1.4":
			if !slices.Equal(c.Names, []string{"1.4 Cartoons", "Cartoons", "TOON", "1.4"}) || c.Icon.Src != logo {
				t.Errorf("1.4 channel: %+v", c)
			}
		case "airwaves.1.1":
			if !slices.Equal(c.Names, []string{"1.1 Airwaves Weather", "Airwaves Weather", "WX", "1.1"}) || c.Icon.Src != "" {
				t.Errorf("1.1 channel: %+v", c)
			}
		default:
			t.Errorf("channel %s", c.ID)
		}
	}
	kinds := map[string]bool{}
	for _, p := range doc.Programmes {
		switch {
		case p.Channel == "airwaves.1.1" && slices.Equal(p.Category, []string{"Weather"}):
		case p.Channel == "airwaves.1.4" && p.Title == "The Iron Giant" && slices.Equal(p.Category, []string{"Movie"}):
		case p.Channel == "airwaves.1.4" && p.Title == "Futurama" && slices.Equal(p.Category, []string{"Kids"}):
		default:
			t.Errorf("programme: %+v", p)
		}
		kinds[p.Channel+" "+p.Title] = true
	}
	if len(kinds) != 3 {
		t.Errorf("programmes: %v", kinds)
	}

	// Off, or not rendered, a channel leaves the lineup.
	writeFile(t, filepath.Join(root, "1.4 Cartoons", vchan.DetailsFile), `{"enabled": false}`)
	s.wxStreams = false
	if _, raw := get("/lineup.json"); raw != "[]\n" {
		t.Errorf("lineup with both off: %s", raw)
	}
}
