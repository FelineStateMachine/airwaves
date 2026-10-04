package guide

import (
	"slices"
	"testing"
)

func TestBaseCall(t *testing.T) {
	for in, want := range map[string]string{
		"KWGNDT": "KWGN", "KWGNDT2": "KWGN", "KZDND10": "KZDN", "K48MND": "K48MN",
		"K11QJ": "K11QJ", "KQDKCA": "KQDK", "KZCOLP": "KZCO", "KMASLD3": "KMAS", "KTVD": "KTVD",
	} {
		if got := BaseCall(in); got != want {
			t.Errorf("BaseCall(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompareNumbers(t *testing.T) {
	got := []string{"11", "2.10", "2.2", "27", "2.1", "9.4"}
	slices.SortFunc(got, CompareNumbers)
	want := []string{"2.1", "2.2", "2.10", "9.4", "11", "27"}
	if !slices.Equal(got, want) {
		t.Errorf("sorted = %v, want %v", got, want)
	}
}

func TestNetworkName(t *testing.T) {
	for in, want := range map[string]string{
		"AMERICAN BROADCASTING COMPANY": "ABC",
		"THE NEST":                      "The Nest",
		"COZI TV":                       "Cozi TV",
		"METV TOONS":                    "MeTV Toons",
		"SHOP LC":                       "Shop LC",
	} {
		if got := networkName(in); got != want {
			t.Errorf("networkName(%q) = %q, want %q", in, got, want)
		}
	}
}
