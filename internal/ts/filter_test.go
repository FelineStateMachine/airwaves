package ts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"airwaves/internal/ts"
	"airwaves/internal/ts/tstest"
)

func filter(t *testing.T, in []byte, program int) []byte {
	t.Helper()
	var out bytes.Buffer
	f := ts.NewFilter(&out, program)
	for _, p := range packets(in) {
		if err := f.Packet(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestFilter(t *testing.T) {
	in := denver.Packets(500)
	out := filter(t, in, 7)
	if len(out) == 0 || len(out)%ts.PacketSize != 0 {
		t.Fatalf("%d bytes out", len(out))
	}
	pkts := packets(out)
	// It starts clean: our PAT, then the PMT from its first packet.
	if ts.PID(pkts[0]) != 0 || ts.PID(pkts[1]) != 0x40 || pkts[1][1]&0x40 == 0 {
		t.Errorf("starts with PIDs %#x, %#x", ts.PID(pkts[0]), ts.PID(pkts[1]))
	}
	allowed := []int{0, 0x40, 0x41, 0x44, 0x45}
	var c ts.Counter
	for _, p := range pkts {
		if !slices.Contains(allowed, ts.PID(p)) {
			t.Fatalf("PID %#x in program 7's stream", ts.PID(p))
		}
		c.Packet(p)
	}
	if c.Errors() != 0 {
		t.Errorf("%d continuity errors in the output", c.Errors())
	}
	d := demux(out)
	progs := d.Programs()
	want, _ := demux(in).Program(7)
	if len(progs) != 1 || !reflect.DeepEqual(progs[0], want) || !d.Ready() {
		t.Errorf("output programs = %+v, want only %+v", progs, want)
	}
	if tsid, _ := d.TSID(); tsid != 467 || d.Channels() != nil {
		t.Errorf("TSID %d, channels %v", tsid, d.Channels())
	}
	// Every packet of the program's streams after the start went out.
	start := slices.IndexFunc(packets(in), func(p []byte) bool { return ts.PID(p) == 0x40 && p[1]&0x40 != 0 })
	n := 0
	for _, p := range packets(in)[start:] {
		if slices.Contains(allowed[1:], ts.PID(p)) {
			n++
		}
	}
	if got := len(pkts) - countPID(pkts, 0); got != n {
		t.Errorf("%d packets of the program out, want %d", got, n)
	}
}

func countPID(pkts [][]byte, pid int) int {
	n := 0
	for _, p := range pkts {
		if ts.PID(p) == pid {
			n++
		}
	}
	return n
}

// TestFilterWaits sends nothing until the program's PMT has been read.
func TestFilterWaits(t *testing.T) {
	var noTables []byte
	for _, p := range packets(denver.Packets(200)) {
		if pid := ts.PID(p); pid != 0 && pid != 0x30 && pid != 0x40 && pid != 0x50 && pid != 0x1ffb {
			noTables = append(noTables, p...)
		}
	}
	if out := filter(t, noTables, 6); len(out) != 0 {
		t.Errorf("%d bytes out before the tables", len(out))
	}
	if out := filter(t, denver.Packets(200), 99); len(out) != 0 {
		t.Errorf("%d bytes out for a program that isn't there", len(out))
	}
	out := filter(t, append(noTables, denver.Packets(100)...), 6)
	if len(out) == 0 || ts.PID(out) != 0 {
		t.Error("no output once the tables came")
	}
}

// TestFilterFollows keeps up with a PMT that moves.
func TestFilterFollows(t *testing.T) {
	moved := denver
	moved.Programs = slices.Clone(denver.Programs)
	moved.Programs[0].PMTPID = 0x60
	pat := patSection(467, 1, [][2]int{{6, 0x60}, {7, 0x40}, {8, 0x50}})
	in := slices.Concat(denver.Packets(100), tstest.Packetize(0, pat), moved.Packets(100))
	out := filter(t, in, 6)
	pkts := packets(out)
	if countPID(pkts, 0x30) == 0 || countPID(pkts, 0x60) == 0 {
		t.Fatalf("PMTs on %d and %d packets", countPID(pkts, 0x30), countPID(pkts, 0x60))
	}
	// After the move, the output's PAT points at the new PMT.
	d := demux(out)
	if p, ok := d.Program(6); !ok || p.PMTPID != 0x60 || len(d.Programs()) != 1 {
		t.Errorf("program 6 = %+v", p)
	}
}

type failing struct{ n int }

func (f *failing) Write(p []byte) (int, error) {
	f.n++
	return 0, errors.New("pipe closed")
}

func TestFilterWriteError(t *testing.T) {
	w := &failing{}
	f := ts.NewFilter(w, 6)
	var err error
	for _, p := range packets(denver.Packets(300)) {
		if err = f.Packet(p); err != nil {
			break
		}
	}
	if err == nil || f.Flush() == nil || f.Packet(denver.Packets(1)) == nil || w.n != 1 {
		t.Errorf("err %v after %d writes; want it kept, and no more writes", err, w.n)
	}
}

// TestFilterFFprobe checks that ffprobe sees one program in the output.
func TestFilterFFprobe(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	path := filepath.Join(t.TempDir(), "out.ts")
	if err := os.WriteFile(path, filter(t, denver.Packets(2000), 6), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command(ffprobe, "-v", "quiet", "-show_programs", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var probe struct {
		Programs []struct {
			ProgramNum int `json:"program_num"`
			PMTPID     int `json:"pmt_pid"`
			NbStreams  int `json:"nb_streams"`
		} `json:"programs"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if len(probe.Programs) != 1 || probe.Programs[0].ProgramNum != 6 || probe.Programs[0].PMTPID != 0x30 {
		t.Errorf("ffprobe found %+v", probe.Programs)
	}
}
