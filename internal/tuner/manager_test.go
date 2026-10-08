package tuner

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"airwaves/internal/hdhr"
	"airwaves/internal/hdhr/hdhrtest"
	"airwaves/internal/ts"
	"airwaves/internal/ts/tstest"
)

// Three RF channels: KMGH's two programs on 177 MHz, KUSA's on 189 MHz,
// KTVD's on 557 MHz; 509 MHz has nothing to lock.
const (
	rfKMGH = 177_000_000
	rfKUSA = 189_000_000
	rfKTVD = 557_000_000
	rfNone = 509_000_000
)

var muxes = map[int64][]byte{
	rfKMGH: tstest.Mux{TSID: 1, Programs: []tstest.Program{
		{Number: 3, PMTPID: 0x30, VideoPID: 0x31, Audio: []tstest.Audio{{PID: 0x34, Lang: "eng"}, {PID: 0x35, Lang: "spa"}}, Major: 7, Minor: 1, Name: "KMGH"},
		{Number: 4, PMTPID: 0x40, VideoPID: 0x41, Audio: []tstest.Audio{{PID: 0x44, Lang: "eng"}}, Major: 7, Minor: 2, Name: "Grit"},
	}}.Packets(700),
	rfKUSA: tstest.Mux{TSID: 2, Programs: []tstest.Program{{Number: 3, PMTPID: 0x30, VideoPID: 0x31, Audio: []tstest.Audio{{PID: 0x34, Lang: "eng"}}, Major: 9, Minor: 1, Name: "KUSA"}}}.Packets(700),
	rfKTVD: tstest.Mux{TSID: 3, Programs: []tstest.Program{{Number: 1, PMTPID: 0x30, VideoPID: 0x31, Audio: []tstest.Audio{{PID: 0x34, Lang: "eng"}}, Major: 20, Minor: 1, Name: "KTVD"}}}.Packets(700),
}

func setup(t *testing.T, rate ...int) (*Manager, *hdhrtest.Server) {
	t.Helper()
	d := hdhrtest.Device{Mux: func(f int64) []byte { return muxes[f] }}
	if len(rate) > 0 {
		d.Rate = rate[0]
	}
	srv := hdhrtest.New(t, d)
	dev, err := hdhr.Open(context.Background(), srv.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	m.Linger = 0
	m.SetDevice(dev, dev.Tuners)
	return m, srv
}

func open(t *testing.T, m *Manager, r Request) *Sub {
	t.Helper()
	s, err := m.Open(context.Background(), r)
	if err != nil {
		t.Fatalf("open %+v: %v", r, err)
	}
	t.Cleanup(s.Close)
	return s
}

func wait(t *testing.T, s *Sub) ts.Program {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := s.Wait(ctx)
	if err != nil {
		t.Fatalf("wait for program %d: %v", s.program, err)
	}
	return p
}

// read copies what s receives for a moment and lists the programs in it.
func read(t *testing.T, s *Sub) []ts.Program {
	t.Helper()
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = s.Copy(ctx, &buf)
	d := ts.NewDemux()
	var split ts.Splitter
	split.Write(buf.Bytes(), d.Packet)
	return d.Programs()
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TestShareRF: two programs of one RF channel take one tuner, and each
// user gets their own program alone, with its audio tracks described.
func TestShareRF(t *testing.T) {
	m, srv := setup(t)
	a := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1"})
	b := open(t, m, Request{FrequencyHz: rfKMGH, Program: 4, Priority: Recording, Who: "Grit movie"})
	pa, pb := wait(t, a), wait(t, b)
	if a.Tuner() != b.Tuner() || srv.Streams() != 1 {
		t.Fatalf("tuners %d and %d, %d streams; want one shared", a.Tuner(), b.Tuner(), srv.Streams())
	}
	if audio, _ := AudioTracks(pa); len(audio) != 2 || audio[1].Lang != "spa" || audio[1].Map != "0:a:1" {
		t.Errorf("7.1's audio = %+v", audio)
	}
	if len(pb.Audio()) != 1 {
		t.Errorf("7.2's audio = %+v", pb.Audio())
	}
	if got := read(t, a); len(got) != 1 || got[0].Number != 3 {
		t.Errorf("7.1's stream carries %+v, want program 3 alone", got)
	}
	if got := read(t, b); len(got) != 1 || got[0].Number != 4 {
		t.Errorf("7.2's stream carries %+v, want program 4 alone", got)
	}
	tuners, err := m.Tuners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	on := tuners[a.Tuner()]
	if on.FrequencyHz != rfKMGH || len(on.Users) != 2 || on.Top != Recording || !on.Locked || on.Packets == 0 || on.Device.StrengthPct == nil {
		t.Errorf("tuner state = %+v", on)
	}
}

// TestPriorities: with every tuner taken, a viewer can't have one from
// another viewer, but a recording can: the one tuned last, with the
// least to lose. The viewer it ends learns why.
func TestPriorities(t *testing.T) {
	m, _ := setup(t)
	a := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1", Owner: "den"})
	wait(t, a)
	b := open(t, m, Request{FrequencyHz: rfKUSA, Program: 3, Priority: Watching, Who: "9.1", Owner: "kitchen"})
	wait(t, b)

	_, err := m.Open(context.Background(), Request{FrequencyHz: rfKTVD, Program: 1, Priority: Watching, Who: "20.1", Owner: "attic"})
	var busy *BusyError
	if !errors.As(err, &busy) || len(busy.Users) != 2 {
		t.Fatalf("a third viewer got %v, want every tuner in use by the other two", err)
	}

	r := open(t, m, Request{FrequencyHz: rfKTVD, Program: 1, Priority: Recording, Who: "The News"})
	wait(t, r)
	<-b.Done()
	var pre *PreemptedError
	if !errors.As(b.Err(), &pre) || pre.By != "recording The News" {
		t.Fatalf("the kitchen's viewer ended with %v", b.Err())
	}
	if a.Err() != nil {
		t.Fatalf("the den's viewer ended too: %v", a.Err())
	}
}

// TestOwnerRetunes: a viewer changing channel gets their own tuner back
// even when every tuner is busy.
func TestOwnerRetunes(t *testing.T) {
	m, srv := setup(t)
	a := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1", Owner: "den"})
	b := open(t, m, Request{FrequencyHz: rfKUSA, Program: 3, Priority: Recording, Who: "The News"})
	wait(t, a)
	wait(t, b)
	c := open(t, m, Request{FrequencyHz: rfKTVD, Program: 1, Priority: Watching, Who: "20.1", Owner: "den"})
	wait(t, c)
	if a.Err() == nil || b.Err() != nil || c.Tuner() != a.Tuner() {
		t.Fatalf("den's old tune %v, the recording %v, new tune on tuner %d", a.Err(), b.Err(), c.Tuner())
	}
	eventually(t, "two streams", func() bool { return srv.Streams() == 2 })
}

// TestMeasuringYields: measuring takes only a free tuner, and a viewer
// takes it from measuring.
func TestMeasuringYields(t *testing.T) {
	m, _ := setup(t)
	a := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1"})
	meas := open(t, m, Request{FrequencyHz: rfKUSA, Priority: Measuring, Who: "RF 9"})
	wait(t, a)
	wait(t, meas)
	if !meas.Locked() {
		t.Fatal("measuring an RF channel with a multiplex has no lock")
	}
	if _, err := m.Open(context.Background(), Request{FrequencyHz: rfKTVD, Priority: Measuring, Who: "RF 26"}); err == nil {
		t.Fatal("measuring took a viewer's tuner")
	}
	v := open(t, m, Request{FrequencyHz: rfKTVD, Program: 1, Priority: Watching, Who: "20.1"})
	wait(t, v)
	<-meas.Done()
	if a.Err() != nil {
		t.Fatalf("the first viewer ended: %v", a.Err())
	}
}

// TestNoLock: on an RF channel with nothing to lock, Wait gives up when
// its context does, and the device's reading of the tuner is there.
func TestNoLock(t *testing.T) {
	m, _ := setup(t)
	s := open(t, m, Request{FrequencyHz: rfNone, Program: 1, Priority: Watching, Who: "RF 20"})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := s.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait = %v, want the deadline", err)
	}
	if s.Locked() {
		t.Fatal("locked with nothing to lock")
	}
	tuners, err := m.Tuners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st := tuners[s.Tuner()]
	if st.FrequencyHz != rfNone || st.Locked || st.Device.StrengthPct == nil || *st.Device.StrengthPct != 40 {
		t.Fatalf("tuner state = %+v", st)
	}
}

// TestNoProgram: a program the RF channel doesn't carry fails once its
// tables are in.
func TestNoProgram(t *testing.T) {
	m, _ := setup(t)
	s := open(t, m, Request{FrequencyHz: rfKUSA, Program: 9, Priority: Watching, Who: "9.9"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Wait(ctx); !errors.Is(err, ErrNoProgram) {
		t.Fatalf("wait = %v, want ErrNoProgram", err)
	}
}

// TestLinger: a tuner stays on its RF channel for a moment after its last
// user, so coming back is instant, then lets go.
func TestLinger(t *testing.T) {
	m, srv := setup(t)
	m.Linger = 200 * time.Millisecond
	a := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1"})
	wait(t, a)
	a.Close()
	b := open(t, m, Request{FrequencyHz: rfKMGH, Program: 4, Priority: Watching, Who: "7.2"})
	wait(t, b)
	if srv.Streams() != 1 || b.Tuner() != a.Tuner() {
		t.Fatalf("%d streams after coming back, want the lingering one", srv.Streams())
	}
	b.Close()
	eventually(t, "the tuner to let go", func() bool { return srv.Streams() == 0 })
}

// TestBusyElsewhere: a tuner someone else took is skipped from then on.
func TestBusyElsewhere(t *testing.T) {
	m, srv := setup(t)
	srv.Busy(0)
	a := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var he *hdhr.Error
	if _, err := a.Wait(ctx); !errors.As(err, &he) || !he.Busy() {
		t.Fatalf("wait on a tuner taken elsewhere = %v", err)
	}
	eventually(t, "the failed tune to end", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.muxes) == 0 })
	b := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1"})
	wait(t, b)
	if b.Tuner() != 1 {
		t.Fatalf("tuned %d, want the free one", b.Tuner())
	}
	tuners, _ := m.Tuners(ctx)
	if !tuners[0].Elsewhere || tuners[0].FrequencyHz != hdhrtest.OtherFrequency {
		t.Fatalf("tuner 0 = %+v, want it shown in use elsewhere", tuners[0])
	}
}

// TestSlowUser: a user that stops reading is dropped, and doesn't hold up
// the others.
func TestSlowUser(t *testing.T) {
	m, _ := setup(t, 40<<20)
	slow := open(t, m, Request{FrequencyHz: rfKMGH, Program: 3, Priority: Watching, Who: "7.1"})
	fast := open(t, m, Request{FrequencyHz: rfKMGH, Program: 4, Priority: Watching, Who: "7.2"})
	wait(t, slow)
	wait(t, fast)
	var mu sync.Mutex
	n := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = fast.Copy(ctx, writerFunc(func(p []byte) (int, error) {
			mu.Lock()
			n += len(p)
			mu.Unlock()
			return len(p), nil
		}))
	}()
	select {
	case <-slow.Done():
		if !errors.Is(slow.Err(), errTooSlow) {
			t.Fatalf("slow user ended with %v", slow.Err())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a user that never reads wasn't dropped")
	}
	mu.Lock()
	defer mu.Unlock()
	if n == 0 || fast.Err() != nil {
		t.Fatalf("the reading user got %d bytes, ended %v", n, fast.Err())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
