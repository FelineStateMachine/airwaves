package ts

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf16"
)

// maxSection bounds a section being collected; ATSC's private sections are
// at most 4096 bytes.
const maxSection = 4096

// Stream is one elementary stream of a program.
type Stream struct {
	PID int
	// Type is the PMT's stream_type: 0x02 MPEG-2 video, 0x1b H.264, 0x24
	// HEVC, 0x81 AC-3 and 0x87 E-AC-3 (ATSC), 0x0f AAC, 0x03 and 0x04
	// MPEG audio, 0x06 private data its descriptors name.
	Type byte
	// Lang is the ISO 639-2 language code ("eng", "spa") from the ISO 639
	// language descriptor or ATSC's AC-3 audio descriptor; empty when the
	// broadcast gives none.
	Lang string
	// Described marks a video description track for blind viewers.
	Described bool

	codec string // named by a descriptor, for private data
}

var codecs = map[byte]string{
	0x01: "MPEG1VIDEO", 0x02: "MPEG2VIDEO", 0x1b: "H264", 0x24: "HEVC",
	0x03: "MPEG2AUDIO", 0x04: "MPEG2AUDIO", 0x0f: "AAC", 0x11: "AAC", 0x81: "AC3", 0x87: "EAC3",
}

// Codec names the stream's coding ("MPEG2VIDEO", "H264", "HEVC", "AC3",
// "EAC3", "AAC", "MPEG2AUDIO", "AC4"); "" for data.
func (s Stream) Codec() string { return cmp.Or(s.codec, codecs[s.Type]) }

// Video reports whether the stream is a picture.
func (s Stream) Video() bool {
	switch s.Codec() {
	case "MPEG1VIDEO", "MPEG2VIDEO", "H264", "HEVC":
		return true
	}
	return false
}

// Audio reports whether the stream is sound.
func (s Stream) Audio() bool {
	switch s.Codec() {
	case "AC3", "EAC3", "AAC", "MPEG2AUDIO", "AC4":
		return true
	}
	return false
}

// Program is one program of a multiplex, as its PMT describes it.
type Program struct {
	Number  int
	PMTPID  int
	PCRPID  int
	Streams []Stream // in PMT order
}

// Audio lists the program's audio streams in PMT order: the main track
// first, as broadcasters list them.
func (p Program) Audio() []Stream {
	var out []Stream
	for _, s := range p.Streams {
		if s.Audio() {
			out = append(out, s)
		}
	}
	return out
}

// VirtualChannel is a channel of ATSC's terrestrial or cable virtual
// channel table.
type VirtualChannel struct {
	Major, Minor int
	Name         string // the short name, "KWGN-DT"
	Program      int    // its program number in the multiplex
	TSID         int    // the transport stream it's in
	Hidden       bool
	// ServiceType is 0x02 for digital TV, 0x03 audio only, 0x04 data.
	ServiceType int
}

// Demux follows a multiplex's tables: the PAT, each program's PMT and the
// VCT. Feed it every packet.
type Demux struct {
	tables map[int]*assembler // by PID
	tsid   int
	patVer int         // -1 before a PAT
	pmts   map[int]int // PMT PID by program number, from the PAT
	progs  map[int]Program
	pmtVer map[int]int
	vct    vctState
	// gen counts changes to the PAT and PMTs, for Filter.
	gen int
}

type vctState struct {
	version int // of parts, -1 for none
	parts   map[int][]VirtualChannel
	done    []VirtualChannel
	doneVer int
}

// NewDemux returns a Demux that has seen nothing yet.
func NewDemux() *Demux {
	return &Demux{
		tables: map[int]*assembler{0: newAssembler(0), psipPID: newAssembler(psipPID)},
		patVer: -1, pmts: map[int]int{}, progs: map[int]Program{}, pmtVer: map[int]int{},
		vct: vctState{version: -1, doneVer: -1},
	}
}

// Packet reads pkt's part of any table. Packets the demodulator flagged as
// damaged are skipped.
func (d *Demux) Packet(pkt []byte) {
	if len(pkt) < PacketSize || pkt[0] != syncByte || pkt[1]&0x80 != 0 {
		return
	}
	if a := d.tables[PID(pkt)]; a != nil {
		a.packet(pkt, d)
	}
}

// TSID is the multiplex's transport stream ID, from its PAT; ok is false
// before one.
func (d *Demux) TSID() (int, bool) { return d.tsid, d.patVer >= 0 }

// Program returns a program by number; ok is false until its PMT is read.
func (d *Demux) Program(number int) (Program, bool) {
	p, ok := d.progs[number]
	return p, ok
}

// Programs lists the programs whose PMT has been read, by number.
func (d *Demux) Programs() []Program {
	out := make([]Program, 0, len(d.progs))
	for _, p := range d.progs {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Program) int { return cmp.Compare(a.Number, b.Number) })
	return out
}

// Channels lists the virtual channels of the latest complete VCT; nil
// before one.
func (d *Demux) Channels() []VirtualChannel { return slices.Clone(d.vct.done) }

// Ready reports whether the PAT and every PMT it lists have been read.
func (d *Demux) Ready() bool {
	if d.patVer < 0 {
		return false
	}
	for n := range d.pmts {
		if _, ok := d.progs[n]; !ok {
			return false
		}
	}
	return true
}

// section takes a whole section that arrived on pid.
func (d *Demux) section(pid int, s []byte) {
	// The sections followed all have the long form, with a CRC.
	if len(s) < 12 || s[1]&0x80 == 0 || s[5]&1 == 0 || CRC32(s) != 0 {
		return
	}
	switch {
	case s[0] == 0x00 && pid == 0:
		d.pat(s)
	case s[0] == 0x02 && pid != 0 && pid != psipPID:
		d.pmt(pid, s)
	case (s[0] == 0xc8 || s[0] == 0xc9) && pid == psipPID:
		d.vctSection(s)
	}
}

func (d *Demux) pat(s []byte) {
	tsid, ver := int(s[3])<<8|int(s[4]), int(s[5]>>1&0x1f)
	if ver == d.patVer && tsid == d.tsid {
		return
	}
	pmts := map[int]int{}
	body := s[8 : len(s)-4]
	for i := 0; i+4 <= len(body); i += 4 {
		if n := int(body[i])<<8 | int(body[i+1]); n != 0 {
			pmts[n] = int(body[i+2]&0x1f)<<8 | int(body[i+3])
		}
	}
	d.tsid, d.patVer, d.pmts = tsid, ver, pmts
	// Programs gone, or whose PMT moved, are read again.
	for n, p := range d.progs {
		if pid, ok := pmts[n]; !ok || pid != p.PMTPID {
			delete(d.progs, n)
			delete(d.pmtVer, n)
		}
	}
	keep := map[int]bool{0: true, psipPID: true}
	for _, pid := range pmts {
		keep[pid] = true
		if d.tables[pid] == nil {
			d.tables[pid] = newAssembler(pid)
		}
	}
	for pid := range d.tables {
		if !keep[pid] {
			delete(d.tables, pid)
		}
	}
	d.gen++
}

func (d *Demux) pmt(pid int, s []byte) {
	if len(s) < 16 {
		return
	}
	number, ver := int(s[3])<<8|int(s[4]), int(s[5]>>1&0x1f)
	if d.pmts[number] != pid {
		return // not a program the PAT lists here
	}
	if v, ok := d.pmtVer[number]; ok && v == ver {
		return
	}
	p := Program{Number: number, PMTPID: pid, PCRPID: int(s[8]&0x1f)<<8 | int(s[9])}
	info := int(s[10]&0x0f)<<8 | int(s[11])
	body := s[12 : len(s)-4]
	if info > len(body) {
		return
	}
	for body = body[info:]; len(body) >= 5; {
		n := int(body[3]&0x0f)<<8 | int(body[4])
		if 5+n > len(body) {
			return
		}
		st := Stream{PID: int(body[1]&0x1f)<<8 | int(body[2]), Type: body[0]}
		describe(&st, body[5:5+n])
		p.Streams = append(p.Streams, st)
		body = body[5+n:]
	}
	d.progs[number], d.pmtVer[number] = p, ver
	d.gen++
}

// describe reads what a stream's descriptors say of its language, its
// audience and, for private data, its coding.
func describe(s *Stream, desc []byte) {
	for len(desc) >= 2 {
		tag, n := desc[0], int(desc[1])
		if 2+n > len(desc) {
			return
		}
		b := desc[2 : 2+n]
		switch tag {
		case 0x0a: // ISO 639 language
			if n >= 4 {
				s.Lang = cmp.Or(s.Lang, language(b[:3]))
				s.Described = s.Described || b[3] == 3 // visual impaired commentary
			}
		case 0x81: // ATSC AC-3 audio
			ac3(s, b)
		case 0x05: // registration
			if s.Type == 0x06 && n >= 4 {
				switch string(b[:4]) {
				case "AC-3":
					s.codec = "AC3"
				case "EAC3":
					s.codec = "EAC3"
				case "AC-4":
					s.codec = "AC4"
				}
			}
		case 0x6a, 0x7a: // DVB AC-3, E-AC-3
			if s.Type == 0x06 {
				s.codec = map[byte]string{0x6a: "AC3", 0x7a: "EAC3"}[tag]
			}
		case 0x7f: // DVB extension: AC-4
			if s.Type == 0x06 && n >= 1 && b[0] == 0x15 {
				s.codec = "AC4"
			}
		}
		desc = desc[2+n:]
	}
}

// ac3 reads ATSC's AC-3 audio descriptor (A/52 Annex A): its bit stream
// mode, where 2 is for the visually impaired, and the ISO 639 language at
// its end, past the optional fields before it.
func ac3(s *Stream, b []byte) {
	if len(b) < 3 {
		return
	}
	s.Described = s.Described || b[2]>>5 == 2
	i := 4 // past langcod
	if b[2]>>1&0x0f == 0 {
		i++ // 1+1 mode: langcod2
	}
	i++ // mainid or asvcflags
	if i >= len(b) {
		return
	}
	i += 1 + int(b[i]>>1) // textlen and the text
	if i >= len(b) {
		return
	}
	if b[i]&0x80 != 0 && i+4 <= len(b) {
		s.Lang = cmp.Or(s.Lang, language(b[i+1:i+4]))
	}
}

// language is a three-letter ISO 639 code in lower case, "" for anything
// else.
func language(b []byte) string {
	l := strings.ToLower(string(b))
	for _, r := range l {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return l
}

func (d *Demux) vctSection(s []byte) {
	if len(s) < 16 {
		return
	}
	ver, num, last := int(s[5]>>1&0x1f), int(s[6]), int(s[7])
	if ver == d.vct.doneVer {
		return
	}
	if ver != d.vct.version {
		d.vct.version, d.vct.parts = ver, map[int][]VirtualChannel{}
	}
	body := s[10 : len(s)-4]
	chans := []VirtualChannel{}
	for range int(s[9]) {
		if len(body) < 32 {
			return
		}
		n := int(body[30]&0x03)<<8 | int(body[31])
		if 32+n > len(body) {
			return
		}
		chans = append(chans, VirtualChannel{
			Name:        shortName(body[:14]),
			Major:       int(body[14]&0x0f)<<6 | int(body[15]>>2),
			Minor:       int(body[15]&0x03)<<8 | int(body[16]),
			TSID:        int(body[22])<<8 | int(body[23]),
			Program:     int(body[24])<<8 | int(body[25]),
			Hidden:      body[26]&0x10 != 0,
			ServiceType: int(body[27] & 0x3f),
		})
		body = body[32+n:]
	}
	d.vct.parts[num] = chans
	var all []VirtualChannel
	for i := 0; i <= last; i++ {
		p, ok := d.vct.parts[i]
		if !ok {
			return // more sections to come
		}
		all = append(all, p...)
	}
	d.vct.done, d.vct.doneVer = all, ver
}

// shortName decodes a VCT short name: seven UTF-16 code units, padded with
// zeros.
func shortName(b []byte) string {
	u := make([]uint16, 0, 7)
	for i := 0; i+1 < len(b); i += 2 {
		c := uint16(b[i])<<8 | uint16(b[i+1])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return strings.TrimSpace(string(utf16.Decode(u)))
}

// assembler collects one PID's sections from its packets.
type assembler struct {
	pid  int
	cc   int // the last packet's continuity counter, -1 before one
	buf  []byte
	open bool // collecting a section whose start arrived
}

func newAssembler(pid int) *assembler { return &assembler{pid: pid, cc: -1} }

func (a *assembler) packet(pkt []byte, d *Demux) {
	pl := payload(pkt)
	if len(pl) == 0 {
		return
	}
	cc := int(pkt[3] & 0x0f)
	if cc == a.cc {
		return // a duplicate
	}
	lost := a.cc >= 0 && cc != (a.cc+1)&0x0f
	a.cc = cc
	if pkt[1]&0x40 != 0 {
		ptr := int(pl[0])
		if 1+ptr > len(pl) {
			a.open, a.buf = false, a.buf[:0]
			return
		}
		if a.open && !lost {
			a.buf = append(a.buf, pl[1:1+ptr]...)
			a.drain(d)
		}
		a.buf, a.open = append(a.buf[:0], pl[1+ptr:]...), true
	} else {
		if !a.open || lost {
			a.open, a.buf = false, a.buf[:0]
			return
		}
		a.buf = append(a.buf, pl...)
	}
	a.drain(d)
}

// drain hands d each whole section collected, stopping at stuffing.
func (a *assembler) drain(d *Demux) {
	for a.open && len(a.buf) >= 3 {
		if a.buf[0] == 0xff {
			a.open, a.buf = false, a.buf[:0]
			return
		}
		n := 3 + (int(a.buf[1]&0x0f)<<8 | int(a.buf[2]))
		if n > maxSection {
			a.open, a.buf = false, a.buf[:0]
			return
		}
		if len(a.buf) < n {
			return
		}
		d.section(a.pid, a.buf[:n])
		a.buf = a.buf[:copy(a.buf, a.buf[n:])]
	}
}
