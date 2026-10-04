package hdhr

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceIDCheckDigit(t *testing.T) {
	for _, seed := range []string{"nas", "airwaves", "x"} {
		id := DeviceIDFor(seed)
		if !ValidDeviceID(id) {
			t.Errorf("DeviceIDFor(%q) = %08X fails the check digit", seed, id)
		}
		if ValidDeviceID(id ^ 1) {
			t.Errorf("%08X with a flipped bit still validates", id^1)
		}
	}
}

func discoverRequest(deviceType, id uint32) []byte {
	var b bytes.Buffer
	tlv(&b, tagDeviceType, u32(deviceType))
	tlv(&b, tagDeviceID, u32(id))
	return packet(typeDiscoverRq, b.Bytes())
}

func TestDiscoveryRequestAndReply(t *testing.T) {
	id := DeviceIDFor("nas")
	if !parseRequest(discoverRequest(wildcard, wildcard), id) {
		t.Error("wildcard search not answered")
	}
	if !parseRequest(discoverRequest(deviceTuner, id), id) {
		t.Error("search for our ID not answered")
	}
	if parseRequest(discoverRequest(deviceTuner, id^0x10), id) {
		t.Error("search for another device answered")
	}
	if parseRequest(discoverRequest(5, wildcard), id) {
		t.Error("search for another device type answered")
	}

	r := reply(id, 2, "http://192.168.1.20:5004")
	n := int(binary.BigEndian.Uint16(r[2:]))
	if binary.BigEndian.Uint16(r) != typeDiscoverRp || !bytes.Contains(r[4:4+n], []byte("http://192.168.1.20:5004/lineup.json")) {
		t.Errorf("reply % x", r)
	}
}

type fakeBackend struct{}

func (fakeBackend) Lineup(context.Context) ([]Entry, error) {
	return []Entry{{Number: "7.1", Name: "KMGH", HD: true, Group: "Antenna"}, {Number: "1.1", Name: "Airwaves Weather", Group: "Airwaves", Logo: "/channel-logos/1.1"}}, nil
}

func (fakeBackend) Stream(_ context.Context, number string, w io.Writer) error {
	_, err := fmt.Fprintf(w, "TS:%s", number)
	return err
}

func (fakeBackend) XMLTV(_ context.Context, w io.Writer, url func(string) string) error {
	_, err := fmt.Fprintf(w, "<tv>%s</tv>", url("/auto/v7.1"))
	return err
}

func TestHTTPAPI(t *testing.T) {
	logos := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "logo "+r.URL.Path) })
	s := &Server{Backend: fakeBackend{}, DeviceID: DeviceIDFor("nas"), FriendlyName: "Airwaves", Tuners: func() int { return 2 }, Logos: logos}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	get := func(path string) string {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	var disc map[string]any
	if err := json.Unmarshal([]byte(get("/discover.json")), &disc); err != nil {
		t.Fatal(err)
	}
	if disc["LineupURL"] != srv.URL+"/lineup.json" || disc["TunerCount"].(float64) != 2 {
		t.Errorf("discover.json = %v", disc)
	}
	var lineup []map[string]any
	if err := json.Unmarshal([]byte(get("/lineup.json")), &lineup); err != nil {
		t.Fatal(err)
	}
	if len(lineup) != 2 || lineup[0]["URL"] != srv.URL+"/auto/v7.1" || lineup[0]["HD"].(float64) != 1 {
		t.Errorf("lineup.json = %v", lineup)
	}
	if got := get("/auto/v1.1"); got != "TS:1.1" {
		t.Errorf("stream = %q", got)
	}
	if got := get("/tuner1/v7.1"); got != "TS:7.1" {
		t.Errorf("tuner stream = %q", got)
	}
	m3u := get("/channels.m3u")
	if !strings.Contains(m3u, `tvg-chno="1.1"`) || !strings.Contains(m3u, srv.URL+"/auto/v1.1") || !strings.Contains(m3u, "/xmltv.xml") ||
		!strings.Contains(m3u, `tvg-logo="`+srv.URL+`/channel-logos/1.1"`) || !strings.Contains(m3u, `tvg-logo=""`) {
		t.Errorf("m3u:\n%s", m3u)
	}
	if got := get("/channel-logos/1.1"); got != "logo /channel-logos/1.1" {
		t.Errorf("logo = %q", got)
	}
	if got := get("/xmltv.xml"); got != "<tv>"+srv.URL+"/auto/v7.1</tv>" {
		t.Errorf("xmltv = %q", got)
	}
}

// TestFromHost: without discovery, URLs use the address the client was
// given, as behind a container's port mapping, not the local address.
func TestFromHost(t *testing.T) {
	s := &Server{Backend: fakeBackend{}, DeviceID: DeviceIDFor("friend"), FriendlyName: "Airwaves", Tuners: func() int { return 2 }, FromHost: true}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/lineup.json", nil)
	req.Host = "nas.local:15004"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var lineup []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&lineup); err != nil {
		t.Fatal(err)
	}
	if len(lineup) != 2 || lineup[0]["URL"] != "http://nas.local:15004/auto/v7.1" {
		t.Errorf("lineup.json = %v", lineup)
	}
}
