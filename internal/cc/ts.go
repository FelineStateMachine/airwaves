package cc

import (
	"bytes"
	"io"
	"math"
	"slices"
)

const (
	tsPacket  = 188
	tsPayload = 184
	tsSync    = 0x47
	// maxHeld bounds what is held back waiting for a picture to end; past
	// it the picture goes out without a caption.
	maxHeld = 8 << 20
)

// Writer adds CEA-608 captions to the H.264 picture in an MPEG-TS stream
// on its way to another writer, the way ATSC broadcasts carry them: each
// picture gets an SEI with the field 1 byte pair for its frame. Everything
// else passes through as it was. A picture is held back until the next
// begins, since only then is it known to be whole.
type Writer struct {
	w    io.Writer
	fps  int
	pair func(frame int64) (byte, byte)

	part    []byte // a packet split across writes
	out     []byte
	pmts    map[int]bool
	video   int  // the H.264 stream's PID, -1 until the PMT names it
	cc      byte // its next continuity counter
	ccSet   bool
	first   int64 // the first picture's PTS: frame 0
	started bool

	// The picture being collected: its payload and its packets' adaptation
	// fields, and every packet since it began, nil standing for its own.
	// whole is set when pes is from its start; a picture too big to hold
	// goes out in parts.
	pes   []byte
	slots []slot
	held  [][]byte
	size  int
	whole bool
	err   error
}

// slot is one of a picture's packets, minus its payload.
type slot struct {
	hdr [3]byte // the header after the sync byte
	af  []byte  // the adaptation field from its flags on, without stuffing
}

// NewWriter returns a Writer to w for video at fps frames a second.
// pair gives the field 1 byte pair, with parity, for each frame, counted
// from the first picture by presentation time.
func NewWriter(w io.Writer, fps int, pair func(frame int64) (byte, byte)) *Writer {
	return &Writer{w: w, fps: fps, pair: pair, pmts: map[int]bool{}, video: -1}
}

// Write takes MPEG-TS in pieces of any size.
func (x *Writer) Write(p []byte) (int, error) {
	if x.err != nil {
		return 0, x.err
	}
	n := len(p)
	if len(x.part) > 0 {
		k := min(tsPacket-len(x.part), len(p))
		x.part = append(x.part, p[:k]...)
		p = p[k:]
		if len(x.part) == tsPacket {
			x.packet(x.part)
			x.part = x.part[:0]
		}
	}
	for len(p) > 0 {
		if p[0] != tsSync {
			// Out of step: pass bytes through to the next sync byte.
			i := bytes.IndexByte(p, tsSync)
			if i < 0 {
				i = len(p)
			}
			x.other(p[:i])
			p = p[i:]
			continue
		}
		if len(p) < tsPacket {
			x.part = append(x.part, p...)
			break
		}
		x.packet(p[:tsPacket])
		p = p[tsPacket:]
	}
	if err := x.send(); err != nil {
		return 0, err
	}
	return n, nil
}

// Flush sends the picture held back, for the end of the stream.
func (x *Writer) Flush() error {
	if x.err != nil {
		return x.err
	}
	x.finish(true)
	if len(x.part) > 0 {
		x.out = append(x.out, x.part...)
		x.part = x.part[:0]
	}
	return x.send()
}

func (x *Writer) send() error {
	if len(x.out) == 0 {
		return nil
	}
	_, err := x.w.Write(x.out)
	x.out = x.out[:0]
	if err != nil {
		x.err = err
	}
	return err
}

// other passes on bytes that aren't the picture's, in turn.
func (x *Writer) other(b []byte) {
	if len(x.slots) == 0 {
		x.out = append(x.out, b...)
		return
	}
	x.held = append(x.held, slices.Clone(b))
	x.size += len(b)
}

func (x *Writer) packet(p []byte) {
	pid := int(p[1]&0x1f)<<8 | int(p[2])
	start := p[1]&0x40 != 0
	switch {
	case pid == 0 && start:
		x.readPAT(payload(p))
	case x.pmts[pid] && start:
		x.readPMT(payload(p))
	}
	scrambled := p[3]&0xc0 != 0
	if pid != x.video || scrambled || (!start && !x.ccSet) {
		x.other(p)
		return
	}
	if start {
		x.finish(true)
		x.whole = true
	}
	if !x.ccSet {
		x.cc, x.ccSet = p[3]&0x0f, true
	}
	s := slot{hdr: [3]byte{p[1], p[2], p[3]}}
	body := p[4:]
	if p[3]&0x20 != 0 {
		n := min(int(p[4]), tsPayload-1)
		s.af = adaptation(p[5 : 5+n])
		body = p[5+n:]
	}
	if p[3]&0x10 == 0 {
		body = nil
	}
	x.pes = append(x.pes, body...)
	x.slots = append(x.slots, s)
	x.held = append(x.held, nil)
	if x.size += tsPacket; x.size > maxHeld {
		x.finish(false)
	}
}

// payload is a packet's payload, nil if it has none.
func payload(p []byte) []byte {
	if p[3]&0x10 == 0 {
		return nil
	}
	if p[3]&0x20 == 0 {
		return p[4:]
	}
	if int(p[4]) > tsPayload-1 {
		return nil
	}
	return p[5+int(p[4]):]
}

// adaptation returns an adaptation field's contents (after its length)
// without the stuffing that pads it, nil when it holds nothing else.
func adaptation(af []byte) []byte {
	if len(af) == 0 || af[0] == 0 {
		return nil
	}
	n := 1
	flags := af[0]
	if flags&0x10 != 0 { // PCR
		n += 6
	}
	if flags&0x08 != 0 { // OPCR
		n += 6
	}
	if flags&0x04 != 0 { // splice countdown
		n++
	}
	if flags&0x02 != 0 && n < len(af) { // private data
		n += 1 + int(af[n])
	}
	if flags&0x01 != 0 && n < len(af) { // extension
		n += 1 + int(af[n])
	}
	return slices.Clone(af[:min(n, len(af))])
}

// finish sends the picture collected so far, with its caption when caption
// is set, and the packets held back with it.
func (x *Writer) finish(caption bool) {
	if len(x.slots) == 0 {
		return
	}
	pes := x.pes
	if caption && x.whole {
		pes = x.caption(pes)
	}
	pkts := x.packetize(pes)
	i := 0
	for _, b := range x.held {
		if b == nil {
			b = pkts[i]
			i++
		}
		x.out = append(x.out, b...)
	}
	x.pes, x.slots, x.held, x.size, x.whole = x.pes[:0], x.slots[:0], x.held[:0], 0, false
}

// packetize splits a picture's PES into packets in its slots, keeping each
// packet's adaptation field (the first's PCR and random access flag) where
// it was. What the caption added overflows into packets after the last.
func (x *Writer) packetize(pes []byte) [][]byte {
	out := make([][]byte, len(x.slots))
	for i, s := range x.slots {
		last := i == len(x.slots)-1
		for first := true; first || (last && len(pes) > 0); first = false {
			hdr, af := s.hdr, s.af
			if !first {
				hdr[0] &^= 0x40 // not a start
				af = nil
			}
			room := tsPayload
			if len(af) > 0 {
				room -= 1 + len(af)
			}
			n := min(room, len(pes))
			if n == 0 && len(af) == 0 {
				break
			}
			out[i] = append(out[i], x.build(hdr, af, pes[:n])...)
			pes = pes[n:]
		}
	}
	return out
}

// build makes a packet, stuffing its adaptation field to fill it.
func (x *Writer) build(hdr [3]byte, af, data []byte) []byte {
	p := make([]byte, 4, tsPacket)
	p[0], p[1], p[2] = tsSync, hdr[0], hdr[1]
	ctl := hdr[2] & 0xc0 // scrambling
	if len(data) > 0 {
		ctl |= 0x10 | x.cc
		x.cc = (x.cc + 1) & 0x0f
	} else {
		ctl |= (x.cc - 1) & 0x0f // unchanged without a payload
	}
	if fill := tsPayload - len(data); fill > 0 {
		ctl |= 0x20
		p = append(p, byte(fill-1))
		if fill > 1 {
			if len(af) == 0 {
				af = []byte{0}
			}
			p = append(p, af...)
			for len(p) < 4+fill {
				p = append(p, 0xff)
			}
		}
	}
	p[3] = ctl
	return append(p, data...)
}

// caption adds the SEI for its frame to a picture's PES, before the first
// slice, returning pes as it is when it isn't H.264 with a time.
func (x *Writer) caption(pes []byte) []byte {
	if len(pes) < 14 || pes[0] != 0 || pes[1] != 0 || pes[2] != 1 || pes[3]&0xf0 != 0xe0 || pes[7]&0x80 == 0 || pes[8] < 5 {
		return pes
	}
	es := 9 + int(pes[8])
	if es > len(pes) {
		return pes
	}
	at := firstSlice(pes[es:])
	if at < 0 {
		return pes
	}
	pts := int64(pes[9]>>1&7)<<30 | int64(pes[10])<<22 | int64(pes[11]>>1)<<15 | int64(pes[12])<<7 | int64(pes[13]>>1)
	if !x.started {
		x.first, x.started = pts, true
	}
	d := (pts - x.first) & (1<<33 - 1)
	if d >= 1<<32 {
		d -= 1 << 33 // before the first: B-frames
	}
	b1, b2 := x.pair(int64(math.Round(float64(d) * float64(x.fps) / 90000)))
	sei := captionSEI(b1, b2)
	out := make([]byte, 0, len(pes)+len(sei))
	out = append(append(append(out, pes[:es+at]...), sei...), pes[es+at:]...)
	if n := int(pes[4])<<8 | int(pes[5]); n != 0 {
		if n += len(sei); n > 0xffff {
			n = 0 // allowed for video
		}
		out[4], out[5] = byte(n>>8), byte(n)
	}
	return out
}

// firstSlice finds the start code of the first coded slice in H.264, -1
// if there is none.
func firstSlice(es []byte) int {
	for i := 0; i+3 < len(es); i++ {
		if es[i] != 0 || es[i+1] != 0 || es[i+2] != 1 {
			continue
		}
		if t := es[i+3] & 0x1f; t >= 1 && t <= 5 {
			if i > 0 && es[i-1] == 0 {
				return i - 1
			}
			return i
		}
		i += 2
	}
	return -1
}

// captionSEI is an SEI NAL unit carrying a pair of CEA-608 field 1 bytes
// (and a field 2 pair of padding) as ATSC A/53 cc_data in registered user
// data.
func captionSEI(b1, b2 byte) []byte {
	data := []byte{
		0xb5, 0x00, 0x31, // ITU-T T.35: United States, ATSC
		'G', 'A', '9', '4', 0x03, // user_identifier, user_data_type_code: cc_data
		0xc2, 0xff, // process_cc_data_flag, cc_count 2, em_data
		0xfc, b1, b2, // field 1 (CEA-608), valid
		0xfd, Padding, Padding, // field 2, padding
		0xff, // marker_bits
	}
	rbsp := append([]byte{4, byte(len(data))}, data...) // user_data_registered_itu_t_t35
	rbsp = append(rbsp, 0x80)                           // rbsp_trailing_bits
	return append([]byte{0, 0, 0, 1, 0x06}, escape(rbsp)...)
}

// escape adds H.264's emulation prevention bytes, so the NAL unit's
// payload never looks like a start code.
func escape(b []byte) []byte {
	out := make([]byte, 0, len(b)+4)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// section returns the PSI section starting in a packet's payload, nil if
// it isn't all there.
func section(p []byte) []byte {
	if len(p) < 1 || 1+int(p[0])+3 > len(p) {
		return nil
	}
	s := p[1+int(p[0]):]
	n := int(s[1]&0x0f)<<8 | int(s[2])
	if n < 9 || 3+n > len(s) {
		return nil
	}
	return s[:3+n]
}

// readPAT notes the programs' PMT PIDs.
func (x *Writer) readPAT(p []byte) {
	s := section(p)
	if s == nil || s[0] != 0 {
		return
	}
	body := s[8 : len(s)-4]
	for i := 0; i+4 <= len(body); i += 4 {
		if prog := int(body[i])<<8 | int(body[i+1]); prog != 0 {
			x.pmts[int(body[i+2]&0x1f)<<8|int(body[i+3])] = true
		}
	}
}

// readPMT finds the H.264 stream.
func (x *Writer) readPMT(p []byte) {
	s := section(p)
	if s == nil || s[0] != 2 || len(s) < 16 || x.video >= 0 {
		return
	}
	body := s[12 : len(s)-4]
	info := int(s[10]&0x0f)<<8 | int(s[11])
	if info > len(body) {
		return
	}
	for body = body[info:]; len(body) >= 5; {
		if body[0] == 0x1b { // H.264
			x.video = int(body[1]&0x1f)<<8 | int(body[2])
			return
		}
		n := 5 + (int(body[3]&0x0f)<<8 | int(body[4]))
		if n > len(body) {
			return
		}
		body = body[n:]
	}
}
