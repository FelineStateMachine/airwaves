package hdhr

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"hash/fnv"
	"log"
	"net"
	"strconv"
)

// HDHomeRun discovery protocol (libhdhomerun hdhomerun_pkt.h).
const (
	discoverPort   = 65001
	typeDiscoverRq = 0x0002
	typeDiscoverRp = 0x0003
	tagDeviceType  = 0x01
	tagDeviceID    = 0x02
	tagTunerCount  = 0x10
	tagLineupURL   = 0x27
	tagBaseURL     = 0x2A
	deviceTuner    = 0x00000001
	wildcard       = 0xFFFFFFFF
)

// checksumTable is libhdhomerun's device ID check digit table.
var checksumTable = [16]uint32{0xA, 0x5, 0xF, 0x6, 0x7, 0xC, 0x1, 0xB, 0x9, 0x2, 0x8, 0xD, 0x4, 0x3, 0xE, 0x0}

// ValidDeviceID reports whether id carries a correct check digit; apps
// using libhdhomerun ignore devices that fail it.
func ValidDeviceID(id uint32) bool { return checkDigit(id) == id&0xF }

func checkDigit(id uint32) uint32 {
	var c uint32
	c ^= checksumTable[(id>>28)&0xF]
	c ^= (id >> 24) & 0xF
	c ^= checksumTable[(id>>20)&0xF]
	c ^= (id >> 16) & 0xF
	c ^= checksumTable[(id>>12)&0xF]
	c ^= (id >> 8) & 0xF
	c ^= checksumTable[(id>>4)&0xF]
	return c
}

// DeviceIDFor derives a stable, valid device ID from a seed such as the
// server name.
func DeviceIDFor(seed string) uint32 {
	h := fnv.New32a()
	h.Write([]byte("airwaves:" + seed))
	id := h.Sum32() &^ 0xF
	return id | checkDigit(id)
}

func packet(typ uint16, payload []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, typ)
	_ = binary.Write(&b, binary.BigEndian, uint16(len(payload)))
	b.Write(payload)
	_ = binary.Write(&b, binary.LittleEndian, crc32.ChecksumIEEE(b.Bytes()))
	return b.Bytes()
}

func tlv(b *bytes.Buffer, tag byte, value []byte) {
	b.WriteByte(tag)
	if n := len(value); n < 128 {
		b.WriteByte(byte(n))
	} else {
		b.WriteByte(byte(n&0x7F | 0x80))
		b.WriteByte(byte(n >> 7))
	}
	b.Write(value)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// parseRequest reports whether pkt is a discover request this device should
// answer (a tuner or wildcard search, for our ID or any ID).
func parseRequest(pkt []byte, id uint32) bool {
	if len(pkt) < 8 || binary.BigEndian.Uint16(pkt) != typeDiscoverRq {
		return false
	}
	n := int(binary.BigEndian.Uint16(pkt[2:]))
	if 4+n+4 > len(pkt) || crc32.ChecksumIEEE(pkt[:4+n]) != binary.LittleEndian.Uint32(pkt[4+n:]) {
		return false
	}
	p := pkt[4 : 4+n]
	for len(p) >= 2 {
		tag, l := p[0], int(p[1])
		if len(p) < 2+l {
			return false
		}
		v := p[2 : 2+l]
		if l == 4 {
			x := binary.BigEndian.Uint32(v)
			if tag == tagDeviceType && x != wildcard && x != deviceTuner {
				return false
			}
			if tag == tagDeviceID && x != wildcard && x != id {
				return false
			}
		}
		p = p[2+l:]
	}
	return true
}

func reply(id uint32, tuners int, base string) []byte {
	var b bytes.Buffer
	tlv(&b, tagDeviceType, u32(deviceTuner))
	tlv(&b, tagDeviceID, u32(id))
	tlv(&b, tagTunerCount, []byte{byte(tuners)})
	tlv(&b, tagBaseURL, []byte(base))
	tlv(&b, tagLineupURL, []byte(base+"/lineup.json"))
	return packet(typeDiscoverRp, b.Bytes())
}

// Discover answers HDHomeRun discovery broadcasts until ctx ends. port is
// the HTTP port advertised in the base URL. Requests from this host's own
// addresses are ignored, so Tvheadend on the same machine never mistakes
// Airwaves for a tuner it should use.
func (s *Server) Discover(ctx context.Context, port int) error {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: discoverPort})
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if isLocal(from.IP) || !parseRequest(buf[:n], s.DeviceID) {
			continue
		}
		local := localIPFor(from.IP)
		if local == nil {
			continue
		}
		base := "http://" + net.JoinHostPort(local.String(), strconv.Itoa(port))
		if _, err := conn.WriteToUDP(reply(s.DeviceID, s.Tuners(), base), from); err != nil {
			log.Printf("hdhr discovery reply: %v", err)
		}
	}
}

func isLocal(ip net.IP) bool {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return ip.IsLoopback()
}

// localIPFor finds this host's address on the same subnet as peer.
func localIPFor(peer net.IP) net.IP {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.Contains(peer) {
			return n.IP
		}
	}
	return nil
}
