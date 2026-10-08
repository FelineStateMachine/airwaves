package ts_test

import (
	"reflect"
	"testing"

	"airwaves/internal/ts"
	"airwaves/internal/ts/tstest"
)

func demux(b []byte) *ts.Demux {
	d := ts.NewDemux()
	for _, p := range packets(b) {
		d.Packet(p)
	}
	return d
}

type stream struct {
	PID       int
	Codec     string
	Lang      string
	Described bool
}

func streams(p ts.Program) []stream {
	var out []stream
	for _, s := range p.Streams {
		out = append(out, stream{s.PID, s.Codec(), s.Lang, s.Described})
	}
	return out
}

func TestDemux(t *testing.T) {
	d := demux(denver.Packets(40))
	if !d.Ready() {
		t.Fatal("not ready after the tables")
	}
	if tsid, ok := d.TSID(); !ok || tsid != 467 {
		t.Errorf("TSID = %d, %v", tsid, ok)
	}
	progs := d.Programs()
	if len(progs) != 3 {
		t.Fatalf("%d programs, want 3", len(progs))
	}
	want := map[int][]stream{
		6: {{0x31, "MPEG2VIDEO", "", false}, {0x34, "AC3", "eng", false}, {0x35, "AC3", "spa", false}, {0x36, "AC3", "eng", true}},
		7: {{0x41, "H264", "", false}, {0x44, "EAC3", "eng", false}, {0x45, "AAC", "spa", true}},
		8: {{0x51, "MPEG2VIDEO", "", false}, {0x54, "AC3", "", false}},
	}
	for _, p := range progs {
		if got := streams(p); !reflect.DeepEqual(got, want[p.Number]) {
			t.Errorf("program %d streams = %+v, want %+v", p.Number, got, want[p.Number])
		}
	}
	p, _ := d.Program(6)
	if p.PMTPID != 0x30 || p.PCRPID != 0x31 || len(p.Audio()) != 3 || p.Audio()[0].Lang != "eng" {
		t.Errorf("program 6 = %+v", p)
	}
	chans := d.Channels()
	wantChans := []ts.VirtualChannel{
		{Major: 2, Minor: 1, Name: "KWGN-DT", Program: 6, TSID: 467, ServiceType: 2},
		{Major: 2, Minor: 2, Name: "Antenna", Program: 7, TSID: 467, ServiceType: 2},
		{Major: 2, Minor: 3, Name: "Court", Program: 8, TSID: 467, ServiceType: 2},
	}
	if !reflect.DeepEqual(chans, wantChans) {
		t.Errorf("channels = %+v", chans)
	}
}

func TestDemuxNotReady(t *testing.T) {
	d := ts.NewDemux()
	if d.Ready() || d.Channels() != nil {
		t.Error("ready, or with channels, before any packet")
	}
	if _, ok := d.TSID(); ok {
		t.Error("a TSID before the PAT")
	}
	// The PAT alone: the PMTs it lists are still to come.
	d = demux(tstest.Packetize(0, patSection(1, 0, [][2]int{{6, 0x30}})))
	if d.Ready() {
		t.Error("ready without the PMT")
	}
}

// TestDemuxBigVCT reads a VCT spanning packets.
func TestDemuxBigVCT(t *testing.T) {
	m := tstest.Mux{TSID: 1}
	for i := range 9 {
		m.Programs = append(m.Programs, tstest.Program{Number: i + 1, PMTPID: 0x100 + i, VideoPID: 0x200 + i, Major: 50, Minor: i + 1, Name: "KTFD"})
	}
	chans := demux(m.Packets(60)).Channels()
	if len(chans) != 9 || chans[8].Minor != 9 || chans[8].Program != 9 {
		t.Errorf("channels = %+v", chans)
	}
}

func patSection(tsid, version int, progs [][2]int) []byte {
	s := []byte{0x00, 0xb0, 0, byte(tsid >> 8), byte(tsid), 0xc1 | byte(version)<<1, 0, 0}
	for _, p := range progs {
		s = append(s, byte(p[0]>>8), byte(p[0]), 0xe0|byte(p[1]>>8), byte(p[1]))
	}
	return tstest.Section(s)
}

// pmtSection is a PMT whose streams are stream_type, PID and descriptors.
func pmtSection(number, version, pcr int, es ...[]byte) []byte {
	s := []byte{0x02, 0xb0, 0, byte(number >> 8), byte(number), 0xc1 | byte(version)<<1, 0, 0, 0xe0 | byte(pcr>>8), byte(pcr), 0xf0, 0}
	for _, e := range es {
		s = append(s, e...)
	}
	return tstest.Section(s)
}

func es(typ byte, pid int, desc ...byte) []byte {
	return append([]byte{typ, 0xe0 | byte(pid>>8), byte(pid), 0xf0, byte(len(desc))}, desc...)
}

func TestDemuxDescriptors(t *testing.T) {
	pmt := pmtSection(3, 0, 0x31,
		es(0x02, 0x31),
		// AC-3 descriptor, 1+1 mode (a second langcod), visually impaired,
		// with 3 bytes of text and then the language.
		es(0x81, 0x34, 0x81, 14, 0x08, 0x38, 2<<5|0<<1|1, 0xff, 0xff, 0x00, 3<<1|1, 'A', 'B', 'C', 0xbf, 'S', 'P', 'A'),
		// AC-3 descriptor with no language, and an ISO 639 one.
		es(0x81, 0x35, 0x81, 3, 0x08, 0x38, 0x05, 0x0a, 4, 'f', 'r', 'a', 0),
		// Private data: DVB's AC-4, E-AC-3 and AC-3 descriptors, and
		// registration.
		es(0x06, 0x36, 0x7f, 2, 0x15, 0x00),
		es(0x06, 0x37, 0x7a, 1, 0x00),
		es(0x06, 0x38, 0x05, 4, 'A', 'C', '-', '3'),
		es(0x06, 0x39), // data
		es(0x24, 0x3a),
	)
	b := append(tstest.Packetize(0, patSection(1, 0, [][2]int{{3, 0x30}})), tstest.Packetize(0x30, pmt)...)
	p, ok := demux(b).Program(3)
	if !ok {
		t.Fatal("no PMT")
	}
	want := []stream{
		{0x31, "MPEG2VIDEO", "", false},
		{0x34, "AC3", "spa", true},
		{0x35, "AC3", "fra", false},
		{0x36, "AC4", "", false},
		{0x37, "EAC3", "", false},
		{0x38, "AC3", "", false},
		{0x39, "", "", false},
		{0x3a, "HEVC", "", false},
	}
	if got := streams(p); !reflect.DeepEqual(got, want) {
		t.Errorf("streams = %+v\nwant %+v", got, want)
	}
	if !p.Streams[0].Video() || p.Streams[0].Audio() || !p.Streams[3].Audio() || p.Streams[6].Audio() || p.Streams[6].Video() {
		t.Error("Video and Audio")
	}
	if len(p.Audio()) != 5 {
		t.Errorf("%d audio streams, want 5", len(p.Audio()))
	}
}

// TestDemuxSections reads sections that share packets and cross from one
// packet to the next, found by the pointer field.
func TestDemuxSections(t *testing.T) {
	big := func(n, pid int) []byte {
		var e [][]byte
		for i := range 20 {
			e = append(e, es(0x81, pid+i, 0x0a, 4, 'e', 'n', 'g', 0))
		}
		return pmtSection(n, 0, pid, e...)
	}
	// Two programs' PMTs on one PID, back to back: the first ends, and the
	// second starts, partway into the second packet.
	b := append(tstest.Packetize(0, patSection(1, 0, [][2]int{{1, 0x30}, {2, 0x30}})),
		tstest.Packetize(0x30, big(1, 0x100), big(2, 0x200))...)
	d := demux(b)
	p1, ok1 := d.Program(1)
	p2, ok2 := d.Program(2)
	if !ok1 || !ok2 || len(p1.Streams) != 20 || len(p2.Streams) != 20 || p2.Streams[19].PID != 0x213 || !d.Ready() {
		t.Errorf("programs %v %v: %d and %d streams", ok1, ok2, len(p1.Streams), len(p2.Streams))
	}
}

func TestDemuxVersions(t *testing.T) {
	d := ts.NewDemux()
	next := map[int]byte{} // continuity counters, carried on between sections
	feed := func(pid int, s []byte) {
		for _, p := range packets(tstest.Packetize(pid, s)) {
			p[3] = p[3]&0xf0 | next[pid]
			next[pid] = (next[pid] + 1) & 0x0f
			d.Packet(p)
		}
	}
	feed(0, patSection(1, 0, [][2]int{{1, 0x30}, {2, 0x40}}))
	feed(0x30, pmtSection(1, 0, 0x31, es(0x02, 0x31)))
	feed(0x40, pmtSection(2, 0, 0x41, es(0x02, 0x41)))
	if len(d.Programs()) != 2 {
		t.Fatalf("%d programs", len(d.Programs()))
	}
	// A new PMT version adds a stream.
	feed(0x30, pmtSection(1, 1, 0x31, es(0x02, 0x31), es(0x81, 0x34)))
	if p, _ := d.Program(1); len(p.Streams) != 2 {
		t.Errorf("program 1 after its new PMT: %+v", p)
	}
	// A new PAT drops program 2.
	feed(0, patSection(1, 1, [][2]int{{1, 0x30}}))
	if progs := d.Programs(); len(progs) != 1 || progs[0].Number != 1 {
		t.Errorf("programs after the new PAT: %+v", progs)
	}
	if !d.Ready() {
		t.Error("not ready")
	}
}

func TestDemuxBadCRC(t *testing.T) {
	pmt := pmtSection(1, 0, 0x31, es(0x02, 0x31))
	pmt[len(pmt)-1] ^= 1
	b := append(tstest.Packetize(0, patSection(1, 0, [][2]int{{1, 0x30}})), tstest.Packetize(0x30, pmt)...)
	if _, ok := demux(b).Program(1); ok {
		t.Error("read a PMT with a bad CRC")
	}
	// A damaged packet is skipped.
	b = append(tstest.Packetize(0, patSection(1, 0, [][2]int{{1, 0x30}})), tstest.Packetize(0x30, pmtSection(1, 0, 0x31))...)
	tstest.SetTEI(b, 1)
	if _, ok := demux(b).Program(1); ok {
		t.Error("read a PMT from a flagged packet")
	}
}
