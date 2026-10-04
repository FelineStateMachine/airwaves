package cc

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
)

// muxer makes MPEG-TS for tests, the way ffmpeg does: a PES per picture,
// the first packet's adaptation field holding the PCR, the last stuffed.
type muxer struct {
	out []byte
	cc  map[int]byte
	ccs map[int][]byte // continuity counters sent, by PID
}

func (m *muxer) packet(pid int, start bool, af, data []byte) {
	if m.cc == nil {
		m.cc, m.ccs = map[int]byte{}, map[int][]byte{}
	}
	p := []byte{tsSync, byte(pid >> 8 & 0x1f), byte(pid), 0x10 | m.cc[pid]}
	if start {
		p[1] |= 0x40
	}
	m.ccs[pid] = append(m.ccs[pid], m.cc[pid])
	m.cc[pid] = (m.cc[pid] + 1) & 0x0f
	if fill := tsPayload - len(data); fill > 0 {
		p[3] |= 0x20
		p = append(p, byte(fill-1))
		if fill > 1 {
			if af == nil {
				af = []byte{0}
			}
			p = append(p, af...)
			p = append(p, bytes.Repeat([]byte{0xff}, 4+fill-len(p))...)
		}
	}
	m.out = append(m.out, append(p, data...)...)
}

// pes sends a PES, calling between after each packet but the last, to
// slip others in.
func (m *muxer) pes(pid int, pes, af []byte, between func()) {
	for first := true; len(pes) > 0; first = false {
		room := tsPayload
		var a []byte
		if first && af != nil {
			a = af
			room -= 1 + len(af)
		}
		n := min(room, len(pes))
		m.packet(pid, first, a, pes[:n])
		pes = pes[n:]
		if between != nil && len(pes) > 0 {
			between()
		}
	}
}

func (m *muxer) psi(pid int, section []byte) {
	m.packet(pid, true, nil, append(append([]byte{0}, section...), bytes.Repeat([]byte{0xff}, tsPayload-1-len(section))...))
}

const (
	videoPID = 0x100
	audioPID = 0x101
	pmtPID   = 0x1000
)

func pat() []byte {
	return []byte{0x00, 0xb0, 13, 0, 1, 0xc1, 0, 0, 0, 1, 0xe0 | pmtPID>>8, pmtPID & 0xff, 0, 0, 0, 0}
}

func pmt() []byte {
	return []byte{0x02, 0xb0, 23, 0, 1, 0xc1, 0, 0, 0xe1, 0x00, 0xf0, 0,
		0x1b, 0xe0 | videoPID>>8, videoPID & 0xff, 0xf0, 0,
		0x0f, 0xe0 | audioPID>>8, audioPID & 0xff, 0xf0, 0,
		0, 0, 0, 0}
}

func pesHeader(pts int64, length int) []byte {
	h := []byte{0, 0, 1, 0xe0, byte(length >> 8), byte(length), 0x80, 0x80, 5,
		byte(pts>>29&0x0e | 0x21), byte(pts >> 22), byte(pts>>14 | 1), byte(pts >> 7), byte(pts<<1 | 1)}
	return h
}

// picture is an access unit: an AUD, parameter sets for a keyframe, and a
// slice of n bytes.
func picture(rng *rand.Rand, key bool, n int) (es []byte, slice int) {
	es = []byte{0, 0, 0, 1, 0x09, 0xf0}
	if key {
		es = append(es, 0, 0, 0, 1, 0x67, 0x64, 0x00, 0x1f, 0xac, 0, 0, 0, 1, 0x68, 0xee, 0x3c, 0x80)
	}
	slice = len(es)
	nal := byte(0x41)
	if key {
		nal = 0x65
	}
	es = append(es, 0, 0, 1, nal)
	for range n {
		es = append(es, byte(0x10+rng.IntN(0xf0)))
	}
	return es, slice
}

type sentPicture struct {
	pes   []byte
	slice int // where the slice starts in pes
	frame int64
	pcr   []byte
}

// stream makes a TS of pictures in decode order (frames 0 3 1 2 6 4 5 ...)
// with audio between their packets.
func stream(t *testing.T, pictures int, sizes []int, lengths bool) ([]byte, []sentPicture, *muxer) {
	t.Helper()
	rng := rand.New(rand.NewPCG(7, 9))
	m := &muxer{}
	m.psi(0, pat())
	m.psi(pmtPID, pmt())
	audio := func() {
		if rng.IntN(3) == 0 {
			m.pes(audioPID, append([]byte{0, 0, 1, 0xc0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}, byte(rng.IntN(256))), nil, nil)
		}
	}
	order := []int64{0}
	for g := int64(0); len(order) < pictures; g += 3 {
		order = append(order, g+3, g+1, g+2)
	}
	var sent []sentPicture
	for i, frame := range order[:pictures] {
		key := i%10 == 0
		es, slice := picture(rng, key, sizes[i%len(sizes)])
		length := 0
		if lengths {
			length = 8 + len(es)
		}
		pes := append(pesHeader(900000+frame*3000, length), es...)
		var af []byte
		if key {
			af = []byte{0x50, 0, 0, 0x2b, 0xf2, 0x7e, 0} // random access, PCR
			af[1] = byte(i)
		}
		sent = append(sent, sentPicture{pes: pes, slice: 14 + slice, frame: frame, pcr: af})
		m.pes(videoPID, pes, af, audio)
		audio()
		if i%7 == 0 {
			m.psi(0, pat())
			m.psi(pmtPID, pmt())
		}
	}
	return m.out, sent, m
}

func pairFor(frame int64) (byte, byte) {
	return parity(byte(0x41 + frame%26)), parity(byte(0x61 + frame%26))
}

type tsPacketInfo struct {
	pid   int
	start bool
	cc    byte
	af    []byte // adaptation field contents, stuffing included
	data  []byte
	raw   []byte
}

func readTS(t *testing.T, b []byte) []tsPacketInfo {
	t.Helper()
	if len(b)%tsPacket != 0 {
		t.Fatalf("%d bytes isn't whole packets", len(b))
	}
	var out []tsPacketInfo
	for ; len(b) > 0; b = b[tsPacket:] {
		p := b[:tsPacket]
		if p[0] != tsSync {
			t.Fatalf("packet %d lacks a sync byte", len(out))
		}
		info := tsPacketInfo{pid: int(p[1]&0x1f)<<8 | int(p[2]), start: p[1]&0x40 != 0, cc: p[3] & 0x0f, raw: p}
		rest := p[4:]
		if p[3]&0x20 != 0 {
			n := int(p[4])
			if n > 183 || (p[3]&0x10 == 0 && n != 183) {
				t.Fatalf("packet %d: adaptation field of %d", len(out), n)
			}
			info.af = p[5 : 5+n]
			rest = p[5+n:]
		}
		if p[3]&0x10 != 0 {
			info.data = rest
		} else if len(rest) > 0 {
			t.Fatalf("packet %d: %d bytes beyond its adaptation field", len(out), len(rest))
		}
		out = append(out, info)
	}
	return out
}

func TestWriterAddsCaptions(t *testing.T) {
	// Slice sizes landing the PES exactly on packet boundaries, one byte
	// short of them, and in between.
	sizes := []int{2000, 140, 160, 161, 162, 165, 1, 345, 3, 5000}
	for _, lengths := range []bool{false, true} {
		in, sent, mux := stream(t, 40, sizes, lengths)
		var frames []int64
		var out bytes.Buffer
		w := NewWriter(&out, 30, func(f int64) (byte, byte) {
			frames = append(frames, f)
			return pairFor(f)
		})
		if _, err := w.Write(in); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		pkts := readTS(t, out.Bytes())

		// Everything but the video is as it was, in the same order.
		var others, othersIn []byte
		for _, p := range pkts {
			if p.pid != videoPID {
				others = append(others, p.raw...)
			}
		}
		for _, p := range readTS(t, in) {
			if p.pid != videoPID {
				othersIn = append(othersIn, p.raw...)
			}
		}
		if !bytes.Equal(others, othersIn) {
			t.Error("other streams changed")
		}

		// The video counts on unbroken from where it started.
		var want byte = mux.ccs[videoPID][0]
		var pes [][]byte
		var afs [][]byte
		for i, p := range pkts {
			if p.pid != videoPID {
				continue
			}
			if p.cc != want {
				t.Fatalf("packet %d: continuity counter %d, want %d", i, p.cc, want)
			}
			want = (want + 1) & 0x0f
			if p.start {
				pes = append(pes, nil)
				afs = append(afs, p.af)
			} else if len(p.af) > 0 && p.af[0]&0x10 != 0 {
				t.Errorf("packet %d: a PCR moved mid-picture", i)
			}
			pes[len(pes)-1] = append(pes[len(pes)-1], p.data...)
		}
		if len(pes) != len(sent) {
			t.Fatalf("%d pictures, want %d", len(pes), len(sent))
		}
		var wantFrames []int64
		for i, s := range sent {
			wantFrames = append(wantFrames, s.frame)
			sei := captionSEI(pairFor(s.frame))
			exp := slices.Concat(s.pes[:s.slice], sei, s.pes[s.slice:])
			if lengths {
				n := len(exp) - 6
				exp[4], exp[5] = byte(n>>8), byte(n)
			}
			if !bytes.Equal(pes[i], exp) {
				t.Errorf("picture %d (frame %d): PES of %d bytes differs from the %d expected", i, s.frame, len(pes[i]), len(exp))
			}
			if s.pcr != nil && !bytes.Equal(afs[i][:len(s.pcr)], s.pcr) {
				t.Errorf("picture %d: adaptation field % x, want % x", i, afs[i][:len(s.pcr)], s.pcr)
			}
		}
		if !slices.Equal(frames, wantFrames) {
			t.Errorf("frames asked for %v, want %v", frames, wantFrames)
		}
	}
}

func TestWriterTakesAnyPieces(t *testing.T) {
	in, _, _ := stream(t, 25, []int{700, 90, 3000}, false)
	var whole bytes.Buffer
	w := NewWriter(&whole, 30, pairFor)
	w.Write(in)
	w.Flush()
	rng := rand.New(rand.NewPCG(3, 4))
	for _, size := range []int{1, 7, 187, 188, 189, 4096, -1} {
		var out bytes.Buffer
		w := NewWriter(&out, 30, pairFor)
		for p := in; len(p) > 0; {
			n := size
			if n < 0 {
				n = 1 + rng.IntN(600)
			}
			n = min(n, len(p))
			if k, err := w.Write(p[:n]); err != nil || k != n {
				t.Fatalf("Write = %d, %v", k, err)
			}
			p = p[n:]
		}
		w.Flush()
		if !bytes.Equal(out.Bytes(), whole.Bytes()) {
			t.Errorf("written %d bytes at a time: output differs", size)
		}
	}
}

func TestWriterPassesOtherStreams(t *testing.T) {
	// Without a PAT and PMT naming H.264 video, nothing changes.
	m := &muxer{}
	rng := rand.New(rand.NewPCG(5, 6))
	es, _ := picture(rng, true, 900)
	m.pes(videoPID, append(pesHeader(1000, 0), es...), nil, nil)
	m.pes(audioPID, []byte{0, 0, 1, 0xc0, 0, 3, 0x80, 0, 0}, nil, nil)
	var out bytes.Buffer
	w := NewWriter(&out, 30, pairFor)
	w.Write(m.out)
	w.Flush()
	if !bytes.Equal(out.Bytes(), m.out) {
		t.Error("a stream without a PMT changed")
	}
	// Bytes out of step with packets pass through too.
	out.Reset()
	w = NewWriter(&out, 30, pairFor)
	junk := append([]byte("junk"), m.out...)
	w.Write(junk)
	w.Flush()
	if !bytes.Equal(out.Bytes(), junk) {
		t.Error("junk changed")
	}
}

func TestEscape(t *testing.T) {
	got := escape([]byte{0, 0, 0, 0, 0, 1, 0, 0, 3, 0, 0, 4})
	if want := []byte{0, 0, 3, 0, 0, 3, 0, 1, 0, 0, 3, 3, 0, 0, 4}; !bytes.Equal(got, want) {
		t.Errorf("escape = % x, want % x", got, want)
	}
}

func TestCaptionSEI(t *testing.T) {
	got := captionSEI(0x94, 0x2c)
	want := []byte{0, 0, 0, 1, 0x06, 0x04, 17, 0xb5, 0x00, 0x31, 'G', 'A', '9', '4', 0x03, 0xc2, 0xff,
		0xfc, 0x94, 0x2c, 0xfd, 0x80, 0x80, 0xff, 0x80}
	if !bytes.Equal(got, want) {
		t.Errorf("SEI\n% x, want\n% x", got, want)
	}
}
