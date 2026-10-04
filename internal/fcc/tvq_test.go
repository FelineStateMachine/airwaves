package fcc

import (
	"math"
	"os"
	"testing"
)

func TestParseAndLicensed(t *testing.T) {
	raw, err := os.ReadFile("testdata/tvq.txt")
	if err != nil {
		t.Fatal(err)
	}
	all, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("parsed %d records, want 5", len(all))
	}
	lic := Licensed(all)
	got := make(map[string]Facility)
	for _, f := range lic {
		got[f.CallSign] = f
		if f.Status != "LIC" {
			t.Errorf("%s: status %q kept", f.CallSign, f.Status)
		}
	}
	if len(lic) != 3 {
		t.Fatalf("licensed = %d records, want 3 (KBRO-LD, KWGN-TV, KCNC-TV)", len(lic))
	}

	k := got["KWGN-TV"]
	checks := []struct {
		name      string
		got, want float64
	}{
		{"rf", float64(k.RFChannel), 34},
		{"virtual", float64(k.VirtualChannel), 2},
		{"erp", k.ERPkW, 1000},
		{"haat", k.HAATm, 336},
		{"rcamsl", k.RCAMSLm, 2340.6},
		{"distance", k.DistanceKm, 21.06},
		{"bearing", k.BearingDeg, 267.89},
		{"lat", k.Point.Lat, 39 + 43.0/60 + 58.0/3600},
		{"lon", k.Point.Lon, -(105 + 14.0/60 + 10.0/3600)},
	}
	for _, c := range checks {
		if math.Abs(c.got-c.want) > 1e-6 {
			t.Errorf("KWGN-TV %s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if k.BaseCall != "KWGN" || k.City != "Denver" || k.Service != "DTV" {
		t.Errorf("KWGN-TV identity = %q %q %q", k.BaseCall, k.City, k.Service)
	}
	if got["KCNC-TV"].ERPkW != 1000 {
		t.Errorf("KCNC-TV kept the STA record (ERP %v), want the licensed 1000 kW", got["KCNC-TV"].ERPkW)
	}
}

func TestBandAndFrequency(t *testing.T) {
	tests := []struct {
		rf   int
		band string
		mhz  float64
	}{
		{2, "VHF-Lo", 57}, {6, "VHF-Lo", 85}, {7, "VHF-Hi", 177}, {13, "VHF-Hi", 213}, {14, "UHF", 473}, {36, "UHF", 605},
	}
	for _, tt := range tests {
		if b := Band(tt.rf); b != tt.band {
			t.Errorf("Band(%d) = %s, want %s", tt.rf, b, tt.band)
		}
		if f := CenterMHz(tt.rf); f != tt.mhz {
			t.Errorf("CenterMHz(%d) = %v, want %v", tt.rf, f, tt.mhz)
		}
	}
}

func TestBaseCall(t *testing.T) {
	for in, want := range map[string]string{"KWGN-TV": "KWGN", "K11QJ-D": "K11QJ", "KUSA": "KUSA", "kbro-ld": "KBRO"} {
		if got := BaseCall(in); got != want {
			t.Errorf("BaseCall(%q) = %q, want %q", in, got, want)
		}
	}
}
