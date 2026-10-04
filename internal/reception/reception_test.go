package reception

import (
	"math"
	"testing"

	"airwaves/internal/terrain"
)

func flat(km, elev float64, n int) terrain.Profile {
	e := make([]float64, n)
	for i := range e {
		e[i] = elev
	}
	return terrain.Profile{DistanceKm: km, Elevations: e}
}

var rooftop = Presets[2]

func TestLineOfSightIsFreeSpace(t *testing.T) {
	// A 1000 kW UHF tower 730 m above a flat plain 21 km away, like
	// Lookout Mountain seen from downtown Denver.
	tx := Transmitter{RFChannel: 35, ERPkW: 1000, RCAMSLm: 2340}
	e := Predict(tx, flat(21, 1609, 200), rooftop)
	if !e.LineOfSight || e.DiffractionDB != 0 {
		t.Fatalf("expected a clear path, got %+v", e)
	}
	fspl := 32.45 + 20*math.Log10(21) + 20*math.Log10(599)
	if math.Abs(e.PathLossDB-fspl) > 0.1 {
		t.Errorf("path loss %.1f, want free space %.1f", e.PathLossDB, fspl)
	}
	if e.Tier != Strong {
		t.Errorf("tier %s, want strong", e.Tier)
	}
}

func TestRidgeAddsDiffraction(t *testing.T) {
	tx := Transmitter{RFChannel: 25, ERPkW: 1000, RCAMSLm: 1900}
	p := flat(100, 1600, 300)
	// A 600 m ridge two thirds of the way along the path.
	for i := 190; i < 210; i++ {
		p.Elevations[i] = 2200
	}
	clear := Predict(tx, flat(100, 1600, 300), rooftop)
	blocked := Predict(tx, p, rooftop)
	if blocked.LineOfSight {
		t.Error("ridge path reported line of sight")
	}
	if blocked.DiffractionDB < 15 {
		t.Errorf("diffraction %.1f dB, want a substantial loss", blocked.DiffractionDB)
	}
	if blocked.NoiseMarginDB >= clear.NoiseMarginDB {
		t.Errorf("ridge did not reduce margin: %.1f vs %.1f", blocked.NoiseMarginDB, clear.NoiseMarginDB)
	}
}

func TestPresetsOrderIndoorWorst(t *testing.T) {
	tx := Transmitter{RFChannel: 9, ERPkW: 45, RCAMSLm: 2360}
	p := flat(23, 1640, 200)
	in, attic, roof := Predict(tx, p, Presets[0]), Predict(tx, p, Presets[1]), Predict(tx, p, Presets[2])
	if !(in.NoiseMarginDB < attic.NoiseMarginDB && attic.NoiseMarginDB < roof.NoiseMarginDB) {
		t.Errorf("margins not ordered: indoor %.1f attic %.1f rooftop %.1f", in.NoiseMarginDB, attic.NoiseMarginDB, roof.NoiseMarginDB)
	}
}

func TestTierThresholds(t *testing.T) {
	for m, want := range map[float64]Tier{25: Strong, 20: Strong, 12: Good, 5: Fair, 0: Weak, -6: Unlikely} {
		if got := tierFor(m); got != want {
			t.Errorf("tierFor(%v) = %s, want %s", m, got, want)
		}
	}
	if !Fair.Receivable() || Weak.Receivable() {
		t.Error("Receivable cutoff should sit between fair and weak")
	}
}

func TestKnifeEdge(t *testing.T) {
	if knifeEdgeDB(-1) != 0 {
		t.Error("clear path should have no loss")
	}
	// Grazing incidence is about 6 dB.
	if l := knifeEdgeDB(0); math.Abs(l-6) > 0.1 {
		t.Errorf("J(0) = %.2f, want about 6", l)
	}
}
