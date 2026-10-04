package atsc3

import (
	"os"
	"testing"
)

func TestParseDenver(t *testing.T) {
	raw, err := os.ReadFile("testdata/denver.html")
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 3 {
		t.Fatalf("got %d hosts, want 3", len(hosts))
	}
	k := hosts[0]
	if k.CallSign != "KWGN-TV" || k.RF != "34" || k.Market != "Denver, Colorado" || k.Simulcast != "KDVR" {
		t.Errorf("first host = %+v", k)
	}
	if k.FacilityID == 0 {
		t.Error("facility ID not parsed from the station link")
	}
	if len(k.Services) != 4 {
		t.Fatalf("KWGN-TV services = %d, want 4", len(k.Services))
	}
	cw := k.Services[0]
	if cw.Major != 2 || cw.Minor != 1 || cw.Network != "CW" || cw.Name != "KWGN" || cw.ATSC1Call != "KDVR" || cw.ATSC1Display != "02-1" {
		t.Errorf("CW service = %+v", cw)
	}
	if kxdp := hosts[1]; kxdp.CallSign != "KXDP-LD" || len(kxdp.Services) != 1 || kxdp.Services[0].ATSC1Call != "" {
		t.Errorf("KXDP-LD = %+v", kxdp)
	}
}
