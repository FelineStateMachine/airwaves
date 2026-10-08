package ts_test

import (
	"bytes"
	"testing"

	"airwaves/internal/ts"
	"airwaves/internal/ts/tstest"
)

// denver is a multiplex like KWGN's: an HD main channel with English,
// Spanish and described audio, and two subchannels.
var denver = tstest.Mux{TSID: 467, Programs: []tstest.Program{
	{Number: 6, PMTPID: 0x30, VideoPID: 0x31, Major: 2, Minor: 1, Name: "KWGN-DT", Audio: []tstest.Audio{
		{PID: 0x34, Lang: "eng"}, {PID: 0x35, Lang: "spa"}, {PID: 0x36, Lang: "eng", Described: true},
	}},
	{Number: 7, PMTPID: 0x40, VideoPID: 0x41, VideoType: 0x1b, Major: 2, Minor: 2, Name: "Antenna", Audio: []tstest.Audio{
		{PID: 0x44, Type: 0x87, Lang: "eng"}, {PID: 0x45, Type: 0x0f, Lang: "spa", Described: true},
	}},
	{Number: 8, PMTPID: 0x50, VideoPID: 0x51, Major: 2, Minor: 3, Name: "Court", Audio: []tstest.Audio{{PID: 0x54}}},
}}

func packets(b []byte) [][]byte {
	var out [][]byte
	for i := 0; i+ts.PacketSize <= len(b); i += ts.PacketSize {
		out = append(out, b[i:i+ts.PacketSize])
	}
	return out
}

func TestSplitter(t *testing.T) {
	src := denver.Packets(60)
	for _, size := range []int{1, 7, 100, 187, 188, 189, 1000, len(src)} {
		var s ts.Splitter
		var got []byte
		for i := 0; i < len(src); i += size {
			s.Write(src[i:min(i+size, len(src))], func(pkt []byte) { got = append(got, pkt...) })
		}
		if !bytes.Equal(got, src) || s.Resyncs() != 0 {
			t.Errorf("chunks of %d: got %d bytes with %d resyncs, want the %d sent and none", size, len(got), s.Resyncs(), len(src))
		}
	}
}

func TestSplitterResync(t *testing.T) {
	src := denver.Packets(20)
	// Half a packet lost, and garbage with a stray sync byte in it.
	var in []byte
	in = append(in, src[:5*ts.PacketSize]...)
	in = append(in, src[5*ts.PacketSize:5*ts.PacketSize+90]...)
	in = append(in, 0x00, 0x47, 0x12, 0x47, 0x00)
	in = append(in, src[6*ts.PacketSize:]...)
	for _, size := range []int{13, 188, len(in)} {
		var s ts.Splitter
		var got []byte
		for i := 0; i < len(in); i += size {
			s.Write(in[i:min(i+size, len(in))], func(pkt []byte) { got = append(got, pkt...) })
		}
		// Every whole packet comes out. Written all at once, the cut one is
		// seen for what it is; in pieces, it may go out before the bytes
		// after it show it was cut, taking the start of the next with it.
		want := len(src) - ts.PacketSize
		head, tail := src[:5*ts.PacketSize], src[7*ts.PacketSize:]
		ok := bytes.HasPrefix(got, head) && bytes.HasSuffix(got, tail) && len(got) >= want-ts.PacketSize && len(got) <= want
		if size == len(in) {
			ok = bytes.Equal(got, append(append([]byte{}, head...), src[6*ts.PacketSize:]...))
		}
		if !ok || s.Resyncs() != 1 {
			t.Errorf("chunks of %d: got %d packets with %d resyncs, want %d with 1", size, len(got)/ts.PacketSize, s.Resyncs(), want/ts.PacketSize)
		}
	}
}

func TestCounter(t *testing.T) {
	dup := func(b []byte, i, times int) []byte {
		p := b[i*ts.PacketSize : (i+1)*ts.PacketSize]
		var out []byte
		out = append(out, b[:(i+1)*ts.PacketSize]...)
		for range times {
			out = append(out, p...)
		}
		return append(out, b[(i+1)*ts.PacketSize:]...)
	}
	// A packet of PID 0x100 with the given continuity counter, with or
	// without a payload, and with the discontinuity indicator set.
	pkt := func(cc byte, payload, disc bool) []byte {
		p := make([]byte, ts.PacketSize)
		p[0], p[1], p[2] = 0x47, 0x01, 0x00
		p[3] = 0x20 | cc
		if payload {
			p[3] |= 0x10
		}
		p[4] = 1
		if disc {
			p[5] = 0x80
		}
		return p
	}
	null := make([]byte, ts.PacketSize)
	null[0], null[1], null[2], null[3] = 0x47, 0x1f, 0xff, 0x10
	tests := []struct {
		name    string
		in      func() []byte
		tei, cc int64
	}{
		{"clean", func() []byte { return denver.Packets(200) }, 0, 0},
		{"flagged", func() []byte { b := denver.Packets(200); tstest.SetTEI(b, 50); tstest.SetTEI(b, 51); return b }, 2, 0},
		{"lost", func() []byte { b := denver.Packets(200); tstest.SkipCC(b, 50); return b }, 0, 1},
		{"duplicate", func() []byte { return dup(denver.Packets(200), 50, 1) }, 0, 0},
		{"two duplicates", func() []byte { return dup(denver.Packets(200), 50, 2) }, 0, 1},
		{"discontinuity", func() []byte {
			return bytes.Join([][]byte{pkt(0, true, false), pkt(1, true, false), pkt(7, true, true), pkt(8, true, false)}, nil)
		}, 0, 0},
		{"no payload", func() []byte {
			return bytes.Join([][]byte{pkt(3, true, false), pkt(3, false, false), pkt(4, true, false)}, nil)
		}, 0, 0},
		{"null packets", func() []byte { return bytes.Join([][]byte{null, null, null}, nil) }, 0, 0},
	}
	for _, tt := range tests {
		var c ts.Counter
		in := tt.in()
		for _, p := range packets(in) {
			c.Packet(p)
		}
		if c.TEI != tt.tei || c.CC != tt.cc || c.Errors() != tt.tei+tt.cc || c.Packets != int64(len(in)/ts.PacketSize) {
			t.Errorf("%s: TEI %d, CC %d, %d packets; want %d, %d, %d", tt.name, c.TEI, c.CC, c.Packets, tt.tei, tt.cc, len(in)/ts.PacketSize)
		}
	}
}

func TestCRC32(t *testing.T) {
	// The check value of CRC-32/MPEG-2.
	if got := ts.CRC32([]byte("123456789")); got != 0x0376e6e7 {
		t.Errorf("CRC32 = %#x, want 0x376e6e7", got)
	}
}
