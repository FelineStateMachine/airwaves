// Package geo resolves ZIP codes to coordinates and does great-circle math.
package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"airwaves/internal/web"
)

const earthRadiusKm = 6371.0

// Point is a WGS84 coordinate in decimal degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Place is a resolved postal code.
type Place struct {
	ZIP   string `json:"zip"`
	Name  string `json:"name"`
	State string `json:"state"`
	Point Point  `json:"point"`
}

// LookupZIP resolves a US ZIP code to its centroid using zippopotam.us.
func LookupZIP(ctx context.Context, c *http.Client, zip string) (Place, error) {
	raw, err := web.Get(ctx, c, "https://api.zippopotam.us/us/"+zip)
	if err != nil {
		return Place{}, fmt.Errorf("lookup zip %s: %w", zip, err)
	}
	var body struct {
		Places []struct {
			Name  string `json:"place name"`
			State string `json:"state abbreviation"`
			Lat   string `json:"latitude"`
			Lon   string `json:"longitude"`
		} `json:"places"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Place{}, fmt.Errorf("decode zip %s: %w", zip, err)
	}
	if len(body.Places) == 0 {
		return Place{}, fmt.Errorf("zip %s not found", zip)
	}
	p := body.Places[0]
	lat, err := strconv.ParseFloat(p.Lat, 64)
	if err != nil {
		return Place{}, fmt.Errorf("zip %s latitude: %w", zip, err)
	}
	lon, err := strconv.ParseFloat(p.Lon, 64)
	if err != nil {
		return Place{}, fmt.Errorf("zip %s longitude: %w", zip, err)
	}
	return Place{ZIP: zip, Name: p.Name, State: p.State, Point: Point{Lat: lat, Lon: lon}}, nil
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// DistanceKm returns the great-circle distance between a and b.
func DistanceKm(a, b Point) float64 {
	dLat := rad(b.Lat - a.Lat)
	dLon := rad(b.Lon - a.Lon)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(h)))
}

// BearingDeg returns the initial bearing from a to b, clockwise from true
// north.
func BearingDeg(a, b Point) float64 {
	φ1, φ2 := rad(a.Lat), rad(b.Lat)
	Δλ := rad(b.Lon - a.Lon)
	y := math.Sin(Δλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(Δλ)
	return math.Mod(deg(math.Atan2(y, x))+360, 360)
}

// Intermediate returns the point a fraction f of the way from a to b along
// the great circle.
func Intermediate(a, b Point, f float64) Point {
	φ1, λ1 := rad(a.Lat), rad(a.Lon)
	φ2, λ2 := rad(b.Lat), rad(b.Lon)
	δ := DistanceKm(a, b) / earthRadiusKm
	if δ == 0 {
		return a
	}
	sa := math.Sin((1-f)*δ) / math.Sin(δ)
	sb := math.Sin(f*δ) / math.Sin(δ)
	x := sa*math.Cos(φ1)*math.Cos(λ1) + sb*math.Cos(φ2)*math.Cos(λ2)
	y := sa*math.Cos(φ1)*math.Sin(λ1) + sb*math.Cos(φ2)*math.Sin(λ2)
	z := sa*math.Sin(φ1) + sb*math.Sin(φ2)
	return Point{Lat: deg(math.Atan2(z, math.Hypot(x, y))), Lon: deg(math.Atan2(y, x))}
}
