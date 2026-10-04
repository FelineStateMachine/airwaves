// Package fcc queries the FCC TV Query service for licensed broadcast
// transmitters near a location.
package fcc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"airwaves/internal/geo"
	"airwaves/internal/web"
)

const tvqURL = "https://transition.fcc.gov/fcc-bin/tvq"

// Facility is one licensed TV transmitter.
type Facility struct {
	CallSign string `json:"callSign"` // e.g. "KWGN-TV"
	BaseCall string `json:"baseCall"` // e.g. "KWGN"
	// Service is the FCC service code: DTV (full power), DCA (class A),
	// LPD (low power), DRT (replacement translator), LPT, DTS, ...
	Service        string    `json:"service"`
	RFChannel      int       `json:"rfChannel"`
	VirtualChannel int       `json:"virtualChannel"` // PSIP major number, 0 if unknown
	Status         string    `json:"status"`
	City           string    `json:"city"`
	State          string    `json:"state"`
	FacilityID     int       `json:"facilityId"`
	Licensee       string    `json:"licensee"`
	ERPkW          float64   `json:"erpKw"`
	HAATm          float64   `json:"haatM"`   // height above average terrain
	RCAMSLm        float64   `json:"rcamslM"` // radiation center above mean sea level
	RCAGLm         float64   `json:"rcaglM"`  // radiation center above ground
	Directional    bool      `json:"directional"`
	Point          geo.Point `json:"point"`
	DistanceKm     float64   `json:"distanceKm"`
	BearingDeg     float64   `json:"bearingDeg"` // from the search point to the tower
}

// Band names the broadcast band an RF channel falls in.
func Band(rf int) string {
	switch {
	case rf >= 2 && rf <= 6:
		return "VHF-Lo"
	case rf >= 7 && rf <= 13:
		return "VHF-Hi"
	default:
		return "UHF"
	}
}

// CenterMHz returns the center frequency of a US TV RF channel.
func CenterMHz(rf int) float64 {
	switch {
	case rf >= 2 && rf <= 4:
		return 57 + 6*float64(rf-2)
	case rf >= 5 && rf <= 6:
		return 79 + 6*float64(rf-5)
	case rf >= 7 && rf <= 13:
		return 177 + 6*float64(rf-7)
	default:
		return 473 + 6*float64(rf-14)
	}
}

// Query returns licensed transmitters within radiusKm of p.
func Query(ctx context.Context, c *http.Client, p geo.Point, radiusKm float64) ([]Facility, error) {
	latD, latM, latS, ns := dms(p.Lat, "N", "S")
	lonD, lonM, lonS, ew := dms(p.Lon, "E", "W")
	q := url.Values{
		"call": {""}, "city": {""}, "state": {""}, "arn": {""}, "serv": {""},
		"vac": {""}, "freq": {"0.0"}, "fre2": {"999"}, "facid": {""},
		"emailaddr": {""}, "class": {""}, "list": {"4"}, "size": {"9"},
		"dist":  {strconv.FormatFloat(radiusKm, 'f', 0, 64)},
		"dlat2": {strconv.Itoa(latD)}, "mlat2": {strconv.Itoa(latM)}, "slat2": {strconv.FormatFloat(latS, 'f', 1, 64)}, "NS": {ns},
		"dlon2": {strconv.Itoa(lonD)}, "mlon2": {strconv.Itoa(lonM)}, "slon2": {strconv.FormatFloat(lonS, 'f', 1, 64)}, "EW": {ew},
	}
	body, err := web.Get(ctx, c, tvqURL+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("fcc tv query: %w", err)
	}
	all, err := Parse(body)
	if err != nil {
		return nil, err
	}
	return Licensed(all), nil
}

// Licensed keeps licensed facilities, dropping duplicate records for the
// same transmitter.
func Licensed(all []Facility) []Facility {
	type key struct {
		id, rf   int
		lat, lon float64
	}
	seen := make(map[key]bool)
	var out []Facility
	for _, f := range all {
		if f.Status != "LIC" {
			continue
		}
		k := key{f.FacilityID, f.RFChannel, f.Point.Lat, f.Point.Lon}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}

// Parse decodes the pipe-delimited TV Query "list=4" format.
func Parse(body []byte) ([]Facility, error) {
	var out []Facility
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "|") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 39 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		fac := Facility{
			CallSign:       f[1],
			BaseCall:       BaseCall(f[1]),
			Service:        f[3],
			RFChannel:      atoi(f[4]),
			Directional:    f[5] == "DA",
			Status:         f[9],
			City:           titleCase(f[10]),
			State:          f[11],
			ERPkW:          num(f[14]),
			HAATm:          num(f[16]),
			FacilityID:     atoi(f[18]),
			Licensee:       f[27],
			DistanceKm:     num(f[28]),
			BearingDeg:     num(f[30]),
			RCAMSLm:        num(f[31]),
			RCAGLm:         num(f[36]),
			VirtualChannel: atoi(f[38]),
			Point: geo.Point{
				Lat: fromDMS(f[19], f[20], f[21], f[22]),
				Lon: fromDMS(f[23], f[24], f[25], f[26]),
			},
		}
		if fac.CallSign == "" || fac.RFChannel == 0 {
			continue
		}
		out = append(out, fac)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("parse tv query: %w", err)
	}
	return out, nil
}

// BaseCall strips the service suffix from an FCC call sign: "KWGN-TV" and
// "K11QJ-D" become "KWGN" and "K11QJ".
func BaseCall(call string) string {
	base, _, _ := strings.Cut(strings.ToUpper(call), "-")
	return base
}

func dms(v float64, pos, neg string) (int, int, float64, string) {
	hemi := pos
	if v < 0 {
		hemi = neg
		v = -v
	}
	d := math.Floor(v)
	m := math.Floor((v - d) * 60)
	s := ((v-d)*60 - m) * 60
	return int(d), int(m), s, hemi
}

func fromDMS(hemi, d, m, s string) float64 {
	v := num(d) + num(m)/60 + num(s)/3600
	if hemi == "S" || hemi == "W" {
		v = -v
	}
	return v
}

// num parses the leading number of a field such as "1000.  kW" or
// "20.73 km"; placeholders like "-" yield 0.
func num(s string) float64 {
	end := 0
	for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.' || s[end] == '-' && end == 0) {
		end++
	}
	v, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0
	}
	return v
}

func atoi(s string) int { return int(num(s)) }

func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
