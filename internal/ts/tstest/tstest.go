// Package tstest builds synthetic MPEG transport streams for tests: a
// multiplex's tables (PAT, PMTs and ATSC's VCT) and packets of its
// programs' streams, with correct continuity counters.
package tstest

import (
	"cmp"
	"unicode/utf16"

	"airwaves/internal/ts"
)

// Audio is an audio stream of a program.
type Audio struct {
	PID int
	// Type is the stream_type; zero is 0x81, ATSC's AC-3, described with
	// ATSC's AC-3 audio descriptor. Other types get an ISO 639 language
	// descriptor when they have a language or are described.
	Type      byte
	Lang      string
	Described bool
}

// Program is a program of a multiplex.
type Program struct {
	Number, PMTPID, VideoPID int
	// VideoType is the video's stream_type; zero is 0x02, MPEG-2 video.
	VideoType byte
	Audio     []Audio
	// Major and Minor are the program's virtual channel in the VCT, Name
	// its short name; it has none when Major is zero.
	Major, Minor int
	Name         string
}

// Mux is a multiplex, as one RF channel carries it.
type Mux struct {
	TSID     int
	Programs []Program
	// NoVCT leaves out the VCT, as a broadcast without PSIP would.
	NoVCT bool
}

// Packets returns n packets of the multiplex: its tables first and again
// every 20 packets or so, and between them packets of each program's
// streams in turn. Video packets carry a PCR every eighth packet.
func (m Mux) Packets(n int) []byte {
	g := &gen{cc: map[int]byte{}}
	var pids []int
	for _, p := range m.Programs {
		if p.VideoPID > 0 {
			pids = append(pids, p.VideoPID)
		}
		for _, a := range p.Audio {
			pids = append(pids, a.PID)
		}
	}
	video := map[int]bool{}
	for _, p := range m.Programs {
		video[p.VideoPID] = true
	}
	for i := 0; len(g.out) < n*ts.PacketSize; i++ {
		if i%20 == 0 || len(pids) == 0 {
			g.section(0, m.pat())
			for _, p := range m.Programs {
				g.section(p.PMTPID, pmt(p))
			}
			if !m.NoVCT {
				g.section(0x1ffb, m.vct())
			}
			continue
		}
		pid := pids[i%len(pids)]
		g.es(pid, video[pid] && i%8 == 1)
	}
	return g.out[:n*ts.PacketSize]
}

// Packetize carries sections, back to back, in packets of pid: a packet
// where a section starts has the payload unit start flag and a pointer to
// it, and the last packet is padded with stuffing. Continuity counters
// start at 0.
func Packetize(pid int, sections ...[]byte) []byte {
	g := &gen{cc: map[int]byte{}}
	g.section(pid, sections...)
	return g.out
}

// Section finishes a PSI section: it sets the section length from len(s)
// and appends the CRC. s holds the table ID, two bytes for the flags and
// the length, and the rest of the section without its CRC.
func Section(s []byte) []byte {
	n := len(s) + 4 - 3
	s[1] = s[1]&0xf0 | byte(n>>8)&0x0f
	s[2] = byte(n)
	crc := ts.CRC32(s)
	return append(s, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// SetTEI sets the transport error indicator of packet i, as a demodulator
// does for a packet it couldn't correct.
func SetTEI(b []byte, i int) { b[i*ts.PacketSize+1] |= 0x80 }

// SkipCC advances the continuity counter of packet i, and of every later
// packet of its PID that carries a payload, by one: as if a packet had been
// lost just before packet i, which is one gap.
func SkipCC(b []byte, i int) {
	pid := ts.PID(b[i*ts.PacketSize:])
	for j := i * ts.PacketSize; j+ts.PacketSize <= len(b); j += ts.PacketSize {
		p := b[j : j+ts.PacketSize]
		if ts.PID(p) == pid && p[3]&0x10 != 0 {
			p[3] = p[3]&0xf0 | (p[3]+1)&0x0f
		}
	}
}

type gen struct {
	out []byte
	cc  map[int]byte
	pcr uint64
}

func (g *gen) packet(pid int, start bool, af, data []byte) {
	p := make([]byte, ts.PacketSize)
	p[0], p[1], p[2] = 0x47, byte(pid>>8)&0x1f, byte(pid)
	if start {
		p[1] |= 0x40
	}
	p[3] = 0x10 | g.cc[pid]
	g.cc[pid] = (g.cc[pid] + 1) & 0x0f
	i := 4
	if af != nil {
		p[3] |= 0x20
		p[4] = byte(len(af))
		i += 1 + copy(p[5:], af)
	}
	i += copy(p[i:], data)
	for ; i < ts.PacketSize; i++ {
		p[i] = 0xff
	}
	g.out = append(g.out, p...)
}

func (g *gen) section(pid int, sections ...[]byte) {
	var data []byte
	starts := map[int]bool{}
	for _, s := range sections {
		starts[len(data)] = true
		data = append(data, s...)
	}
	for pos := 0; pos < len(data); {
		next := len(data) // the next section start at or after pos
		for k := pos; k < len(data); k++ {
			if starts[k] {
				next = k
				break
			}
		}
		if next < pos+ts.PacketSize-5 {
			end := min(pos+ts.PacketSize-5, len(data))
			g.packet(pid, true, nil, append([]byte{byte(next - pos)}, data[pos:end]...))
			pos = end
			continue
		}
		// No section starts in this packet; one starting right after it
		// begins the next, with stuffing between.
		end := min(pos+ts.PacketSize-4, next)
		g.packet(pid, false, nil, data[pos:end])
		pos = end
	}
}

// es writes a packet of an elementary stream: filler, with a PCR in its
// adaptation field when pcr is set.
func (g *gen) es(pid int, pcr bool) {
	var af []byte
	if pcr {
		g.pcr += 90 * 40 // 40 ms on the 90 kHz clock
		b := g.pcr
		af = []byte{0x10, byte(b >> 25), byte(b >> 17), byte(b >> 9), byte(b >> 1), byte(b&1)<<7 | 0x7e, 0}
	}
	data := make([]byte, ts.PacketSize)
	for i := range data {
		if data[i] = byte(pid) ^ byte(i); data[i] == 0x47 {
			data[i] = 0 // no sync bytes in the filler, for tests of resyncing
		}
	}
	g.packet(pid, false, af, data)
}

func (m Mux) pat() []byte {
	s := []byte{0x00, 0xb0, 0, byte(m.TSID >> 8), byte(m.TSID), 0xc1, 0, 0}
	for _, p := range m.Programs {
		s = append(s, byte(p.Number>>8), byte(p.Number), 0xe0|byte(p.PMTPID>>8), byte(p.PMTPID))
	}
	return Section(s)
}

func pmt(p Program) []byte {
	pcr := p.VideoPID
	if pcr == 0 {
		pcr = 0x1fff
	}
	s := []byte{0x02, 0xb0, 0, byte(p.Number >> 8), byte(p.Number), 0xc1, 0, 0, 0xe0 | byte(pcr>>8), byte(pcr), 0xf0, 0}
	es := func(typ byte, pid int, desc []byte) {
		s = append(s, typ, 0xe0|byte(pid>>8), byte(pid), 0xf0|byte(len(desc)>>8), byte(len(desc)))
		s = append(s, desc...)
	}
	if p.VideoPID > 0 {
		es(cmp.Or(p.VideoType, 0x02), p.VideoPID, nil)
	}
	for _, a := range p.Audio {
		typ := cmp.Or(a.Type, 0x81)
		var desc []byte
		switch {
		case typ == 0x81:
			bsmod := byte(0)
			if a.Described {
				bsmod = 2
			}
			// 48 kHz, 384 kb/s, 2/0, then langcod, mainid, no text.
			d := []byte{0x08, 0x1c << 1, bsmod<<5 | 0x02<<1 | 1, 0xff, 0x00, 0x01}
			if a.Lang != "" {
				d = append(d, 0xbf)
				d = append(d, a.Lang...)
			}
			desc = append([]byte{0x81, byte(len(d))}, d...)
		case a.Lang != "" || a.Described:
			kind := byte(0)
			if a.Described {
				kind = 3
			}
			lang := a.Lang
			if lang == "" {
				lang = "und"
			}
			desc = append([]byte{0x0a, 4}, append([]byte(lang), kind)...)
		}
		es(typ, a.PID, desc)
	}
	return Section(s)
}

func (m Mux) vct() []byte {
	var chans []Program
	for _, p := range m.Programs {
		if p.Major > 0 {
			chans = append(chans, p)
		}
	}
	s := []byte{0xc8, 0xf0, 0, byte(m.TSID >> 8), byte(m.TSID), 0xc1, 0, 0, 0, byte(len(chans))}
	for _, p := range chans {
		name := make([]byte, 14)
		for i, c := range utf16.Encode([]rune(p.Name)) {
			if i < 7 {
				name[2*i], name[2*i+1] = byte(c>>8), byte(c)
			}
		}
		s = append(s, name...)
		s = append(s,
			0xf0|byte(p.Major>>6), byte(p.Major&0x3f)<<2|byte(p.Minor>>8), byte(p.Minor), 0x04, // 8VSB
			0, 0, 0, 0, // carrier frequency, no longer used
			byte(m.TSID>>8), byte(m.TSID), byte(p.Number>>8), byte(p.Number),
			0x0d, 0xc0|0x02, // not hidden; digital TV
			byte(p.Number>>8), byte(p.Number), // source ID
			0xfc, 0x00, // no descriptors
		)
	}
	s = append(s, 0xfc, 0x00) // no additional descriptors
	return Section(s)
}
