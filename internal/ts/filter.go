package ts

import "io"

// flushAt is how much output a Filter gathers before writing it: seven
// packets, as an HDHomeRun sends them over UDP.
const flushAt = 7 * PacketSize

// Filter writes one program of a multiplex as a transport stream of its
// own, as players and recordings expect: a PAT that lists only that
// program, then the program's PMT, its elementary streams and its PCR.
// Other programs, PSIP and null packets are left out. Nothing goes out
// until the program's PMT has been read, and then the output starts with
// the PAT and a PMT from its first packet, so it starts clean. A new PAT
// or PMT is followed.
type Filter struct {
	w       io.Writer
	program int
	d       *Demux
	gen     int // d.gen when pass was set
	pass    [8192]bool
	passed  []int // the PIDs set in pass
	pmtPID  int   // -1 until the program's PMT is known
	started bool  // a PMT has gone out
	patCC   byte
	pat     [PacketSize]byte
	buf     []byte
	err     error
}

// NewFilter returns a Filter writing program to w.
func NewFilter(w io.Writer, program int) *Filter {
	return &Filter{w: w, program: program, d: NewDemux(), gen: -1, pmtPID: -1}
}

// Packet takes the multiplex's next packet. Output is written in batches
// of a few packets; Flush writes what is held. A write error is returned
// from then on.
func (f *Filter) Packet(pkt []byte) error {
	if f.err != nil {
		return f.err
	}
	f.d.Packet(pkt)
	if f.d.gen != f.gen {
		f.update()
	}
	if f.pmtPID < 0 {
		return nil
	}
	pid := PID(pkt)
	switch {
	case pid == 0:
		if pkt[1]&0x40 != 0 {
			f.writePAT() // ours, each time the multiplex sends its own
		}
	case pid == f.pmtPID:
		if !f.started && pkt[1]&0x40 == 0 {
			return nil
		}
		f.started = true
		f.write(pkt)
	case f.pass[pid] && f.started:
		f.write(pkt)
	}
	return f.err
}

// Flush writes the output held back.
func (f *Filter) Flush() error {
	if f.err != nil || len(f.buf) == 0 {
		return f.err
	}
	_, f.err = f.w.Write(f.buf)
	f.buf = f.buf[:0]
	return f.err
}

// update follows a new PAT or PMT.
func (f *Filter) update() {
	f.gen = f.d.gen
	for _, pid := range f.passed {
		f.pass[pid] = false
	}
	f.passed = f.passed[:0]
	p, ok := f.d.Program(f.program)
	if !ok {
		f.pmtPID, f.started = -1, false
		return
	}
	for _, s := range p.Streams {
		f.allow(s.PID)
	}
	f.allow(p.PCRPID)
	if p.PMTPID != f.pmtPID {
		// A PMT that moved, or the first: start on its next section.
		f.pmtPID, f.started = p.PMTPID, false
		f.writePAT()
	}
}

func (f *Filter) allow(pid int) {
	if pid > 0 && pid < nullPID && !f.pass[pid] {
		f.pass[pid] = true
		f.passed = append(f.passed, pid)
	}
}

// writePAT writes a PAT listing only the program.
func (f *Filter) writePAT() {
	tsid, _ := f.d.TSID()
	s := []byte{
		0x00, 0xb0, 13, byte(tsid >> 8), byte(tsid), 0xc1 | byte(f.d.patVer&0x1f)<<1, 0, 0,
		byte(f.program >> 8), byte(f.program), 0xe0 | byte(f.pmtPID>>8), byte(f.pmtPID),
	}
	crc := CRC32(s)
	s = append(s, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	p := f.pat[:]
	p[0], p[1], p[2], p[3], p[4] = syncByte, 0x40, 0x00, 0x10|f.patCC, 0
	f.patCC = (f.patCC + 1) & 0x0f
	n := 5 + copy(p[5:], s)
	for i := n; i < PacketSize; i++ {
		p[i] = 0xff
	}
	f.write(p)
}

func (f *Filter) write(pkt []byte) {
	f.buf = append(f.buf, pkt...)
	if len(f.buf) >= flushAt {
		f.Flush()
	}
}
