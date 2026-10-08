package hdhr

import (
	"bytes"
	"fmt"
	"net"
	"testing"
)

func TestBroadcastAddr(t *testing.T) {
	for _, tc := range []struct {
		cidr string
		want string // "" for none
	}{
		{"169.254.93.183/16", "169.254.255.255"}, // a tuner's link-local port
		{"192.168.1.20/24", "192.168.1.255"},
		{"10.1.2.3/8", "10.255.255.255"},
		{"100.64.1.2/32", ""}, // a tailnet address
		{"10.0.0.1/31", ""},
		{"127.0.0.1/8", ""},
		{"fe80::1/64", ""},
	} {
		ip, n, err := net.ParseCIDR(tc.cidr)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		got, ok := broadcastAddr(n)
		if tc.want == "" {
			if ok {
				t.Errorf("%s: broadcast %s, want none", tc.cidr, got)
			}
			continue
		}
		if !ok || got.String() != tc.want {
			t.Errorf("%s: broadcast %v %v, want %s", tc.cidr, got, ok, tc.want)
		}
	}
	// Interface addresses can come as 16-byte IPs with 16-byte masks.
	n := &net.IPNet{IP: net.ParseIP("192.168.1.20"), Mask: net.CIDRMask(120, 128)}
	if got, ok := broadcastAddr(n); !ok || got.String() != "192.168.1.255" {
		t.Errorf("16-byte form: %v %v", got, ok)
	}
	if targets := broadcastTargets(); len(targets) == 0 || !targets[0].Equal(net.IPv4bcast) {
		t.Errorf("targets %v, want 255.255.255.255 first", targets)
	}
}

func TestDiscoveryClient(t *testing.T) {
	// The emulated device answers the search the client sends.
	if !parseRequest(searchPacket(), DeviceIDFor("nas")) {
		t.Error("the search isn't one a device answers")
	}

	id := DeviceIDFor("tuner")
	from := net.ParseIP("169.254.88.116")
	f, ok := parseReply(reply(id, 2, "http://169.254.88.116"), from)
	if !ok || f.ID != id || f.Tuners != 2 || f.BaseURL != "http://169.254.88.116" {
		t.Errorf("reply: %+v %v", f, ok)
	}
	// A name the network may not resolve gives way to the address it
	// answered from; a port stays.
	f, _ = parseReply(reply(id, 4, "http://hdhr-10afffff.local"), from)
	if f.BaseURL != "http://169.254.88.116" {
		t.Errorf("named base URL: %q", f.BaseURL)
	}
	f, _ = parseReply(reply(id, 2, "http://airwaves.local:5004"), from)
	if f.BaseURL != "http://169.254.88.116:5004" {
		t.Errorf("named base URL with a port: %q", f.BaseURL)
	}

	bad := reply(id, 2, "http://169.254.88.116")
	bad[len(bad)-1] ^= 0xFF
	if _, ok := parseReply(bad, from); ok {
		t.Error("a reply with a bad CRC was read")
	}
	if _, ok := parseReply(searchPacket(), from); ok {
		t.Error("a request was read as a reply")
	}
	// Other kinds of device (a record engine) aren't tuners.
	var b bytes.Buffer
	tlv(&b, tagDeviceType, u32(5))
	tlv(&b, tagDeviceID, u32(id))
	if _, ok := parseReply(packet(typeDiscoverRp, b.Bytes()), from); ok {
		t.Error("a storage device was taken for a tuner")
	}
}

func TestLongTag(t *testing.T) {
	long := bytes.Repeat([]byte("x"), 300)
	var b bytes.Buffer
	tlv(&b, tagBaseURL, long)
	tlv(&b, tagTunerCount, []byte{4})
	var got [][]byte
	if !eachTag(b.Bytes(), func(_ byte, v []byte) { got = append(got, v) }) {
		t.Fatal("not well formed")
	}
	if len(got) != 2 || !bytes.Equal(got[0], long) || !bytes.Equal(got[1], []byte{4}) {
		t.Errorf("values %q", got)
	}
	if eachTag(b.Bytes()[:b.Len()-1], func(byte, []byte) {}) {
		t.Error("a truncated payload was well formed")
	}
}

func TestDeviceIDOrAddress(t *testing.T) {
	id := DeviceIDFor("tuner")
	if got, ok := deviceID(fmt.Sprintf("%08X", id)); !ok || got != id {
		t.Errorf("device ID: %08X %v", got, ok)
	}
	for _, s := range []string{"", "nas.local", "192.168.1.30", fmt.Sprintf("%08X", id^1)} {
		if _, ok := deviceID(s); ok {
			t.Errorf("%q taken for a device ID", s)
		}
	}
}

func TestURLs(t *testing.T) {
	for _, tc := range []struct{ in, api, stream string }{
		{"192.168.1.30", "http://192.168.1.30", "http://192.168.1.30:5004"},
		{"http://hdhomerun.local/", "http://hdhomerun.local", "http://hdhomerun.local:5004"},
		{" http://127.0.0.1:8080/discover.json", "http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"[fe80::1]", "http://[fe80::1]", "http://[fe80::1]:5004"},
		// As a discovery reply gives it.
		{"http://169.254.1.2:80", "http://169.254.1.2", "http://169.254.1.2:5004"},
	} {
		api, err := apiURL(tc.in)
		if err != nil || api != tc.api || streamURL(api) != tc.stream {
			t.Errorf("%q: %q %q %v, want %q %q", tc.in, api, streamURL(api), err, tc.api, tc.stream)
		}
	}
	if _, err := apiURL(" "); err == nil {
		t.Error("an empty address was accepted")
	}
}
