// Package reception estimates over-the-air signal strength from transmitter
// parameters and a terrain profile.
//
// The model is free-space loss plus Deygout multiple knife-edge diffraction
// over the terrain profile on a 4/3 effective earth. It captures line of
// sight and ridge blockage, which dominate reception in mountainous areas,
// but not multipath, foliage, buildings or directional transmit patterns.
// Treat results as a ranking and a rough margin, not a guarantee.
package reception

import (
	"math"

	"airwaves/internal/fcc"
	"airwaves/internal/terrain"
)

// Tier buckets a noise margin into a reception expectation.
type Tier string

// Tiers from best to worst.
const (
	Strong   Tier = "strong"
	Good     Tier = "good"
	Fair     Tier = "fair"
	Weak     Tier = "weak"
	Unlikely Tier = "unlikely"
	Unknown  Tier = "unknown"
)

// Receivable reports whether a tier should decode reliably.
func (t Tier) Receivable() bool { return t == Strong || t == Good || t == Fair }

const (
	// effectiveEarthRadiusM is 4/3 of the earth's radius, the standard
	// allowance for atmospheric refraction.
	effectiveEarthRadiusM = 8_495_000.0
	// thermalNoiseDBm is kTB for a 6 MHz channel plus a 5 dB tuner noise
	// figure.
	thermalNoiseDBm = -106.2 + 5
	// requiredCNRDB is the ATSC 1.0 (8-VSB) decoding threshold. Robust
	// ATSC 3.0 modes decode a few dB lower.
	requiredCNRDB = 15.2
	// dipoleDBi converts ERP (relative to a dipole) to EIRP.
	dipoleDBi = 2.15
)

// Preset describes a receive antenna installation.
type Preset struct {
	Name    string             `json:"name"`
	Label   string             `json:"label"`
	HeightM float64            `json:"heightM"`
	GainDBi map[string]float64 `json:"gainDbi"` // by band, see fcc.Band
	LossDB  float64            `json:"lossDb"`  // walls, roof, cable
}

// Presets are the installations every transmitter is evaluated against.
var Presets = []Preset{
	{
		Name: "indoor", Label: "Indoor antenna", HeightM: 3, LossDB: 12,
		GainDBi: map[string]float64{"UHF": 2, "VHF-Hi": -2, "VHF-Lo": -6},
	},
	{
		Name: "attic", Label: "Attic antenna", HeightM: 6, LossDB: 7,
		GainDBi: map[string]float64{"UHF": 6, "VHF-Hi": 3, "VHF-Lo": -2},
	},
	{
		Name: "rooftop", Label: "Rooftop antenna, 30 ft", HeightM: 9.1, LossDB: 2,
		GainDBi: map[string]float64{"UHF": 9, "VHF-Hi": 6, "VHF-Lo": 2},
	},
}

// manMadeNoiseDB is ambient electrical noise above thermal, by band. Low VHF
// suffers most from household interference.
var manMadeNoiseDB = map[string]float64{"UHF": 0, "VHF-Hi": 4, "VHF-Lo": 12}

// Transmitter is the subset of facility data the model needs.
type Transmitter struct {
	RFChannel int
	ERPkW     float64
	RCAMSLm   float64
	RCAGLm    float64
	HAATm     float64
}

// FromFacility extracts model inputs from an FCC record.
func FromFacility(f fcc.Facility) Transmitter {
	return Transmitter{RFChannel: f.RFChannel, ERPkW: f.ERPkW, RCAMSLm: f.RCAMSLm, RCAGLm: f.RCAGLm, HAATm: f.HAATm}
}

// Estimate is the predicted signal for one transmitter and preset.
type Estimate struct {
	Preset        string  `json:"preset"`
	PowerDBm      float64 `json:"powerDbm"` // at the tuner input
	NoiseMarginDB float64 `json:"noiseMarginDb"`
	FieldDBuVm    float64 `json:"fieldDbuVm"` // field strength at the antenna
	PathLossDB    float64 `json:"pathLossDb"`
	DiffractionDB float64 `json:"diffractionDb"`
	LineOfSight   bool    `json:"lineOfSight"`
	Tier          Tier    `json:"tier"`
}

// Predict estimates reception of tx over prof with antenna preset p.
func Predict(tx Transmitter, prof terrain.Profile, p Preset) Estimate {
	n := len(prof.Elevations)
	if n < 3 || tx.ERPkW <= 0 || prof.DistanceKm <= 0 {
		return Estimate{Preset: p.Name, Tier: Unknown}
	}
	fMHz := fcc.CenterMHz(tx.RFChannel)
	band := fcc.Band(tx.RFChannel)
	lambda := 299.792458 / fMHz
	dM := prof.DistanceKm * 1000

	pos := make([]float64, n)
	z := make([]float64, n)
	for i, e := range prof.Elevations {
		pos[i] = dM * float64(i) / float64(n-1)
		// Raise terrain by the earth bulge relative to the straight chord.
		z[i] = e + pos[i]*(dM-pos[i])/(2*effectiveEarthRadiusM)
	}
	rxH := prof.Elevations[0] + p.HeightM
	txH := txHeightAMSL(tx, prof.Elevations[n-1])

	_, vMain := worstEdge(pos, z, 0, n-1, rxH, txH, lambda)
	diff := deygout(pos, z, 0, n-1, rxH, txH, lambda, 2)
	fspl := 32.45 + 20*math.Log10(prof.DistanceKm) + 20*math.Log10(fMHz)
	isoDBm := 60 + 10*math.Log10(tx.ERPkW) + dipoleDBi - fspl - diff
	power := isoDBm + p.GainDBi[band] - p.LossDB
	margin := power - (thermalNoiseDBm + manMadeNoiseDB[band] + requiredCNRDB)

	return Estimate{
		Preset:        p.Name,
		PowerDBm:      round1(power),
		NoiseMarginDB: round1(margin),
		FieldDBuVm:    round1(isoDBm + 20*math.Log10(fMHz) + 77.2),
		PathLossDB:    round1(fspl + diff),
		DiffractionDB: round1(diff),
		LineOfSight:   vMain < 0,
		Tier:          tierFor(margin),
	}
}

func tierFor(margin float64) Tier {
	switch {
	case margin >= 20:
		return Strong
	case margin >= 10:
		return Good
	case margin >= 3:
		return Fair
	case margin >= -5:
		return Weak
	default:
		return Unlikely
	}
}

func txHeightAMSL(tx Transmitter, ground float64) float64 {
	h := tx.RCAMSLm
	if h <= 0 {
		agl := tx.RCAGLm
		if agl <= 0 {
			agl = max(tx.HAATm, 30)
		}
		h = ground + agl
	}
	// Guard against records whose height sits below the sampled ground.
	return max(h, ground+10)
}

// worstEdge finds the terrain point with the largest Fresnel-Kirchhoff
// parameter between indexes i and j.
func worstEdge(pos, z []float64, i, j int, hi, hj, lambda float64) (int, float64) {
	k, v := -1, math.Inf(-1)
	span := pos[j] - pos[i]
	for m := i + 1; m < j; m++ {
		d1, d2 := pos[m]-pos[i], pos[j]-pos[m]
		los := hi + (hj-hi)*d1/span
		vm := (z[m] - los) * math.Sqrt(2*span/(lambda*d1*d2))
		if vm > v {
			k, v = m, vm
		}
	}
	return k, v
}

// deygout sums knife-edge losses: the dominant edge, then recursively the
// dominant edges of the sub-paths on either side of it.
func deygout(pos, z []float64, i, j int, hi, hj, lambda float64, depth int) float64 {
	if j-i < 2 {
		return 0
	}
	k, v := worstEdge(pos, z, i, j, hi, hj, lambda)
	if k < 0 || v <= -0.78 {
		return 0
	}
	loss := knifeEdgeDB(v)
	if depth > 0 {
		loss += deygout(pos, z, i, k, hi, z[k], lambda, depth-1)
		loss += deygout(pos, z, k, j, z[k], hj, lambda, depth-1)
	}
	return loss
}

// knifeEdgeDB is the ITU-R P.526 approximation of single knife-edge loss.
func knifeEdgeDB(v float64) float64 {
	if v <= -0.78 {
		return 0
	}
	return 6.9 + 20*math.Log10(math.Sqrt((v-0.1)*(v-0.1)+1)+v-0.1)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
