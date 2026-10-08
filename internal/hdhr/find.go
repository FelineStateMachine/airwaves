package hdhr

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned by Find when no HDHomeRun answers discovery.
var ErrNotFound = errors.New("no HDHomeRun answered discovery")

// Found is a tuner that answered discovery.
type Found struct {
	ID     uint32
	Tuners int
	// BaseURL is where its HTTP API is: the address it answered from,
	// with the port it gave, if any.
	BaseURL string
}

// Find locates a device: at addr when that's an address ("192.168.1.30",
// "hdhomerun.local", "http://..."), by discovery when it's a device ID
// ("1099B035"), and otherwise the first device discovery finds, never the
// one with ID skip (the emulated device of this server).
func Find(ctx context.Context, addr string, skip uint32, c *http.Client) (*Device, error) {
	want, byID := deviceID(addr)
	if addr != "" && !byID {
		return Open(ctx, addr, c)
	}
	found, err := Discover(ctx, 2*time.Second)
	if err != nil {
		return nil, err
	}
	for _, f := range found {
		if f.ID == skip || byID && f.ID != want {
			continue
		}
		return Open(ctx, f.BaseURL, c)
	}
	if byID {
		return nil, fmt.Errorf("no HDHomeRun %08X answered discovery", want)
	}
	return nil, ErrNotFound
}

// deviceID reports whether s is a device ID: eight hex digits with a
// correct check digit.
func deviceID(s string) (uint32, bool) {
	if len(s) != 8 {
		return 0, false
	}
	id, err := strconv.ParseUint(s, 16, 32)
	if err != nil || !ValidDeviceID(uint32(id)) {
		return 0, false
	}
	return uint32(id), true
}

// Discover broadcasts a discovery request on every IPv4 interface and
// lists the tuners that answer within wait, by ID. The request goes to
// each interface's own broadcast address as well as 255.255.255.255, so a
// tuner on a link-local port of its own is found too.
func Discover(ctx context.Context, wait time.Duration) ([]Found, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	req := searchPacket()
	send := func() {
		for _, ip := range broadcastTargets() {
			_, _ = conn.WriteToUDP(req, &net.UDPAddr{IP: ip, Port: discoverPort})
		}
	}
	send()
	// Once more halfway, in case the first was lost.
	resend := time.AfterFunc(wait/2, send)
	defer resend.Stop()

	var out []Found
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // the deadline, or ctx ended
		}
		f, ok := parseReply(buf[:n], from.IP)
		if ok && !slices.ContainsFunc(out, func(x Found) bool { return x.ID == f.ID }) {
			out = append(out, f)
		}
	}
	if len(out) == 0 && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	slices.SortFunc(out, func(a, b Found) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// searchPacket asks every tuner to answer.
func searchPacket() []byte {
	var b bytes.Buffer
	tlv(&b, tagDeviceType, u32(deviceTuner))
	tlv(&b, tagDeviceID, u32(wildcard))
	return packet(typeDiscoverRq, b.Bytes())
}

// parseReply reads a tuner's discovery reply, received from the address
// from; ok is false for anything else.
func parseReply(pkt []byte, from net.IP) (Found, bool) {
	typ, p, ok := parsePacket(pkt)
	if !ok || typ != typeDiscoverRp {
		return Found{}, false
	}
	var f Found
	tuner, hasID := true, false
	ok = eachTag(p, func(tag byte, v []byte) {
		switch tag {
		case tagDeviceType:
			tuner = len(v) == 4 && binary.BigEndian.Uint32(v) == deviceTuner
		case tagDeviceID:
			if len(v) == 4 {
				f.ID, hasID = binary.BigEndian.Uint32(v), true
			}
		case tagTunerCount:
			if len(v) == 1 {
				f.Tuners = int(v[0])
			}
		case tagBaseURL:
			f.BaseURL = strings.TrimRight(string(v), "\x00")
		}
	})
	if !ok || !tuner || !hasID {
		return Found{}, false
	}
	f.BaseURL = reachable(f.BaseURL, from)
	return f, true
}

// reachable is a device's base URL with its host the address it answered
// from, when it gave a name (newer firmware gives "hdhr-1099b035.local",
// which a container may not resolve) or nothing.
func reachable(base string, from net.IP) string {
	if from == nil {
		return base
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "http://" + from.String()
	}
	if net.ParseIP(u.Hostname()) != nil {
		return base
	}
	host := from.String()
	if p := u.Port(); p != "" {
		host = net.JoinHostPort(host, p)
	}
	return u.Scheme + "://" + host
}

// broadcastTargets lists where a discovery request goes: 255.255.255.255,
// and the broadcast address of each IPv4 network this host is on.
func broadcastTargets() []net.IP {
	out := []net.IP{net.IPv4bcast}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if b, ok := broadcastAddr(n); ok && !slices.ContainsFunc(out, b.Equal) {
				out = append(out, b)
			}
		}
	}
	return out
}

// broadcastAddr is an IPv4 network's broadcast address; ok is false for
// IPv6, loopback, and /31 and /32 networks, which have none.
func broadcastAddr(n *net.IPNet) (net.IP, bool) {
	ip := n.IP.To4()
	if ip == nil || ip.IsLoopback() {
		return nil, false
	}
	mask := n.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if len(mask) != net.IPv4len {
		return nil, false
	}
	if ones, _ := mask.Size(); ones >= 31 {
		return nil, false
	}
	b := make(net.IP, net.IPv4len)
	for i := range b {
		b[i] = ip[i] | ^mask[i]
	}
	return b, true
}
