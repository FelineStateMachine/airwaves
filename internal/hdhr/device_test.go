package hdhr_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"airwaves/internal/hdhr"
	"airwaves/internal/hdhr/hdhrtest"
)

var lineup = []hdhr.Channel{
	{Number: "2.1", Name: "KWGN-DT", FrequencyHz: 605_000_000, Program: 6, TSID: 467, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3", HD: true, SignalStrength: 96, SignalQuality: 100},
	{Number: "31.1", Name: "KDVR-HD", FrequencyHz: 605_000_000, Program: 3, TSID: 467, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3", HD: true, SignalStrength: 96, SignalQuality: 100},
	{Number: "14.2", Name: "KCEC-2", FrequencyHz: 563_000_000, Program: 4, TSID: 1, Modulation: "8vsb", VideoCodec: "H264", AudioCodec: "AC3", DRM: true, SignalStrength: 82, SignalQuality: 87},
}

// mux is a stand-in multiplex: a few null packets.
var mux = bytes.Repeat(append([]byte{0x47, 0x1f, 0xff, 0x10}, make([]byte, 184)...), 7)

func fake(t *testing.T, d hdhrtest.Device) (*hdhrtest.Server, *hdhr.Device) {
	t.Helper()
	if d.Lineup == nil {
		d.Lineup = lineup
	}
	if d.Mux == nil {
		d.Mux = func(freq int64) []byte {
			if freq == 509_000_000 {
				return nil // nothing there
			}
			return mux
		}
	}
	s := hdhrtest.New(t, d)
	dev, err := hdhr.Open(context.Background(), s.URL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, dev
}

func TestOpen(t *testing.T) {
	s, d := fake(t, hdhrtest.Device{})
	if d.Model != "HDFX-2US" || d.FriendlyName != "HDHomeRun FLEX DUO" || d.Tuners != 2 || d.Version != "20250623" || d.ID == "" {
		t.Errorf("device %+v", d)
	}
	if d.BaseURL != s.URL() || d.StreamURL != s.URL() || d.ATSC3() {
		t.Errorf("URLs %q %q, ATSC 3.0 %v", d.BaseURL, d.StreamURL, d.ATSC3())
	}
	_, d4k := fake(t, hdhrtest.Device{Model: "HDFX-4K", Firmware: "hdhomerun_dvr_atsc3", Tuners: 4})
	if !d4k.ATSC3() || d4k.Tuners != 4 {
		t.Errorf("4K: %+v", d4k)
	}
	if _, err := hdhr.Open(context.Background(), "", nil); err == nil {
		t.Error("opened no address")
	}
}

func TestLineup(t *testing.T) {
	_, d := fake(t, hdhrtest.Device{})
	got, err := d.Lineup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, lineup) {
		t.Errorf("lineup\n%+v\nwant\n%+v", got, lineup)
	}
}

// TestLenient reads what devices write in other ways: numbers as strings,
// flags as booleans, a decimal place, and fields it doesn't know.
func TestLenient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discover.json":
			io.WriteString(w, `{"DeviceID":"1099B035","ModelNumber":"HDFX-2US","FirmwareVersion":20250623,"TunerCount":"2","Legacy":0}`)
		case "/lineup.json":
			io.WriteString(w, `[{"GuideNumber":7.1,"GuideName":" KMGH-HD ","Frequency":"177000000","ProgramNumber":3,"HD":true,"DRM":false,"Tags":"favorite"}]`)
		case "/status.json":
			io.WriteString(w, `[{"Resource":"tuner0","Frequency":177000000,"SignalStrengthPercent":"83.6","SignalQualityPercent":90.2,"SymbolQualityPercent":null},{"Resource":"tuner1"}]`)
		}
	}))
	defer srv.Close()
	d, err := hdhr.Open(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "20250623" || d.Tuners != 2 {
		t.Errorf("device %+v", d)
	}
	l, err := d.Lineup(context.Background())
	if err != nil || len(l) != 1 || l[0].Number != "7.1" || l[0].Name != "KMGH-HD" || l[0].FrequencyHz != 177_000_000 || !l[0].HD || l[0].DRM {
		t.Errorf("lineup %+v %v", l, err)
	}
	st, err := d.Status(context.Background())
	if err != nil || len(st) != 2 {
		t.Fatalf("status %+v %v", st, err)
	}
	if st[0].StrengthPct == nil || *st[0].StrengthPct != 84 || *st[0].QualityPct != 90 || st[0].SymbolPct != nil || !st[0].Tuned() {
		t.Errorf("tuner 0 %+v", st[0])
	}
	if st[1].Tuner != 1 || st[1].Tuned() || st[1].StrengthPct != nil {
		t.Errorf("tuner 1 %+v", st[1])
	}
}

func TestOpenRF(t *testing.T) {
	s, d := fake(t, hdhrtest.Device{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := d.OpenRF(ctx, -1, 605_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if st.Tuner != 0 {
		t.Errorf("tuner %d", st.Tuner)
	}
	buf := make([]byte, 4*len(mux))
	if _, err := io.ReadFull(st, buf); err != nil || buf[0] != 0x47 {
		t.Fatalf("read: %v", err)
	}
	status, err := d.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status[0].Tuned() || status[0].FrequencyHz != 605_000_000 || status[0].QualityPct == nil || *status[0].QualityPct != 95 || status[0].NetworkRate == 0 {
		t.Errorf("tuner 0 %+v", status[0])
	}
	if status[1].Tuned() {
		t.Errorf("tuner 1 %+v", status[1])
	}

	// A tuner asked for by number.
	st1, err := d.OpenRF(ctx, 1, 563_000_000)
	if err != nil || st1.Tuner != 1 {
		t.Fatalf("tuner 1: %+v %v", st1, err)
	}
	st1.Close()

	// Closing frees the tuner.
	st.Close()
	waitFor(t, func() bool { return s.Streams() == 0 })
	status, _ = d.Status(ctx)
	if status[0].Tuned() || status[0].StrengthPct != nil {
		t.Errorf("tuner 0 after closing %+v", status[0])
	}
}

func TestBusy(t *testing.T) {
	s, d := fake(t, hdhrtest.Device{})
	ctx := context.Background()
	s.Busy(0)
	s.Busy(1)
	_, err := d.OpenRF(ctx, -1, 605_000_000)
	var he *hdhr.Error
	if !errors.As(err, &he) || he.Code != 805 || he.Reason != "All Tuners In Use" || !he.Busy() {
		t.Errorf("all busy: %v", err)
	}
	_, err = d.OpenRF(ctx, 1, 605_000_000)
	if !errors.As(err, &he) || he.Code != 804 || !he.Busy() {
		t.Errorf("tuner 1 busy: %v", err)
	}
	status, _ := d.Status(ctx)
	if status[0].FrequencyHz != hdhrtest.OtherFrequency || status[0].TargetIP != hdhrtest.OtherTarget {
		t.Errorf("busy tuner %+v", status[0])
	}
	s.Free(1)
	st, err := d.OpenRF(ctx, -1, 605_000_000)
	if err != nil || st.Tuner != 1 {
		t.Fatalf("after freeing tuner 1: %v", err)
	}
	st.Close()

	if _, err := d.OpenRF(ctx, 7, 605_000_000); !errors.As(err, &he) || he.Busy() || he.Status != http.StatusNotFound {
		t.Errorf("no tuner 7: %v", err)
	}
	if e := (&hdhr.Error{Status: 503}); !e.Busy() || e.Error() != "HDHomeRun: HTTP 503" {
		t.Errorf("bare 503: %v busy %v", e, e.Busy())
	}
	if e := (&hdhr.Error{Status: 503, Code: 807, Reason: "No Video Data"}); e.Busy() || e.Error() != "HDHomeRun: 807 No Video Data" {
		t.Errorf("807: %v busy %v", e, e.Busy())
	}
}

// TestNoLock: an RF channel the tuner can't lock gets no answer, while
// status.json shows the tuner on it with what it measures.
func TestNoLock(t *testing.T) {
	s, d := fake(t, hdhrtest.Device{})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := d.OpenRF(ctx, -1, 509_000_000)
		done <- err
	}()
	waitFor(t, func() bool {
		st, err := d.Status(context.Background())
		return err == nil && st[0].FrequencyHz == 509_000_000
	})
	st, _ := d.Status(context.Background())
	if st[0].StrengthPct == nil || *st[0].StrengthPct != 40 || *st[0].QualityPct != 0 || st[0].NetworkRate != 0 {
		t.Errorf("tuner 0 %+v", st[0])
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("open: %v, want the deadline", err)
	}
	waitFor(t, func() bool { return s.Streams() == 0 })
}

func TestScan(t *testing.T) {
	rescan := append([]hdhr.Channel{{Number: "9.1", Name: "KUSA-HD", FrequencyHz: 189_000_000, Program: 3, Modulation: "8vsb", HD: true}}, lineup...)
	s, d := fake(t, hdhrtest.Device{Rescan: rescan, ScanTime: 100 * time.Millisecond})
	ctx := context.Background()
	st, err := d.ScanStatus(ctx)
	if err != nil || st.InProgress || !st.Possible || st.Source != "Antenna" {
		t.Fatalf("before: %+v %v", st, err)
	}
	if err := d.StartScan(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := d.ScanStatus(ctx); !st.InProgress {
		t.Errorf("during: %+v", st)
	}
	var he *hdhr.Error
	if _, err := d.OpenRF(ctx, -1, 605_000_000); !errors.As(err, &he) || he.Code != 803 || !he.Busy() {
		t.Errorf("stream during a scan: %v", err)
	}
	waitFor(t, func() bool {
		st, err := d.ScanStatus(ctx)
		return err == nil && !st.InProgress
	})
	got, err := d.Lineup(ctx)
	if err != nil || len(got) != len(rescan) || got[0].Number != "9.1" {
		t.Errorf("after: %+v %v", got, err)
	}
	if s.Scans() != 1 {
		t.Errorf("%d scans", s.Scans())
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
