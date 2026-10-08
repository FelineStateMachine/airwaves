// Package ts reads MPEG transport streams as an HDHomeRun sends them: a
// whole RF channel's multiplex, cut into packets, with its tables followed
// (PAT, PMTs and ATSC's virtual channel table), one program taken out of it
// for each viewer or recording, and the damage that arrived counted.
package ts

// PacketSize is the size of a transport stream packet.
const PacketSize = 188

const (
	syncByte = 0x47
	nullPID  = 0x1fff
	psipPID  = 0x1ffb // ATSC's PSIP base PID, which carries the VCT
)

// PID is a packet's packet identifier.
func PID(pkt []byte) int { return int(pkt[1]&0x1f)<<8 | int(pkt[2]) }

// payload is a packet's payload, nil if it has none.
func payload(pkt []byte) []byte {
	switch pkt[3] >> 4 & 3 {
	case 1:
		return pkt[4:]
	case 3:
		if n := int(pkt[4]); n < PacketSize-5 {
			return pkt[5+n:]
		}
	}
	return nil
}

// Splitter cuts a byte stream into whole packets. When the sync byte isn't
// where the next packet should start, it looks for two sync bytes a packet
// apart and carries on from there, counting the resync.
type Splitter struct {
	buf     []byte
	lost    bool
	resyncs int64
}

// Write calls each for every whole packet in p, in order, keeping a partial
// packet for the next call. pkt is only valid during the call.
func (s *Splitter) Write(p []byte, each func(pkt []byte)) {
	s.buf = append(s.buf, p...)
	b := s.buf
	i := 0
	for len(b)-i >= PacketSize {
		// A packet is whole when the next one starts after it, where that
		// has arrived.
		next := i+PacketSize >= len(b) || b[i+PacketSize] == syncByte
		if b[i] == syncByte && next && !s.lost {
			each(b[i : i+PacketSize])
			i += PacketSize
			continue
		}
		if !s.lost {
			s.lost = true
			s.resyncs++
		}
		j := i
		for j+PacketSize < len(b) && (b[j] != syncByte || b[j+PacketSize] != syncByte) {
			j++
		}
		i = j
		if j+PacketSize >= len(b) {
			break // no confirmed start yet; wait for more
		}
		s.lost = false
	}
	s.buf = s.buf[:copy(s.buf, b[i:])]
}

// Resyncs counts the times the stream lost packet alignment.
func (s *Splitter) Resyncs() int64 { return s.resyncs }

// Counter counts packets and the damage in them: packets the demodulator
// flagged with the transport error indicator, and gaps in a PID's
// continuity counter (packets lost on the way).
type Counter struct {
	Packets, TEI, CC int64
	// state is by PID: bit 7 seen, bit 6 the last packet was a duplicate,
	// the low bits its continuity counter.
	state [8192]uint8
}

// Packet counts pkt. Only packets carrying a payload advance a continuity
// counter; one duplicate packet in a row is allowed; the adaptation
// field's discontinuity indicator starts the count over; the null PID
// isn't followed.
func (c *Counter) Packet(pkt []byte) {
	c.Packets++
	pid := PID(pkt)
	afc := pkt[3] >> 4 & 3
	tei := pkt[1]&0x80 != 0
	if tei {
		c.TEI++
	}
	if pid == nullPID || afc == 0 {
		return
	}
	cc := pkt[3] & 0x0f
	// A flagged packet arrived, so its PID's count goes on from it without
	// counting a gap twice; so does one that starts the count over.
	if tei || afc&2 != 0 && pkt[4] > 0 && pkt[5]&0x80 != 0 {
		c.state[pid] = 0x80 | cc
		return
	}
	if afc&1 == 0 {
		return
	}
	st := c.state[pid]
	last := st & 0x0f
	switch {
	case st&0x80 == 0, cc == (last+1)&0x0f:
		c.state[pid] = 0x80 | cc
	case cc == last && st&0x40 == 0:
		c.state[pid] = 0xc0 | cc
	default:
		c.CC++
		c.state[pid] = 0x80 | cc
	}
}

// Errors is the damage counted: flagged packets and continuity gaps.
func (c *Counter) Errors() int64 { return c.TEI + c.CC }

var crcTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// CRC32 is the MPEG-2 CRC that PSI sections end with. Over a whole section,
// its CRC included, it is 0 when the section arrived intact.
func CRC32(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, x := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^x]
	}
	return crc
}
