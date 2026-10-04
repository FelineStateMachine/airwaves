// Package weather builds a local weather report from public US sources:
// the National Weather Service (forecast, hourly, observations, alerts,
// radar), NOAA GOES satellite imagery, and Open-Meteo (air quality, sun).
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"airwaves/internal/geo"
)

// userAgent identifies the app to the NWS API, which requires one.
const userAgent = "Airwaves home TV (personal use)"

// Report is everything the weather page and alert crawl need.
type Report struct {
	Updated time.Time `json:"updated"`
	Now     *Now      `json:"now,omitempty"`
	Periods []Period  `json:"periods"` // 7-day, day and night
	Hourly  []Hour    `json:"hourly"`  // next 48 hours
	Alerts  []Alert   `json:"alerts"`
	Air     *Air      `json:"air,omitempty"`
	Sun     *Sun      `json:"sun,omitempty"`
	// Radar and Satellite are server paths serving the latest images.
	Radar     string   `json:"radar,omitempty"`
	Satellite string   `json:"satellite,omitempty"`
	Errors    []string `json:"errors,omitempty"` // sources that failed this time
}

// Now is the latest observation, in US units.
type Now struct {
	Station      string    `json:"station"`
	Observed     time.Time `json:"observed"`
	Description  string    `json:"description"`
	TempF        *float64  `json:"tempF"`
	FeelsLikeF   *float64  `json:"feelsLikeF"`
	DewpointF    *float64  `json:"dewpointF"`
	Humidity     *float64  `json:"humidity"`
	WindMph      *float64  `json:"windMph"`
	GustMph      *float64  `json:"gustMph"`
	WindDir      string    `json:"windDir"`
	PressureInHg *float64  `json:"pressureInHg"`
	VisibilityMi *float64  `json:"visibilityMi"`
}

// Period is one part of the 7-day forecast ("Tonight", "Monday").
type Period struct {
	Name     string    `json:"name"`
	Start    time.Time `json:"start"`
	IsDay    bool      `json:"isDay"`
	TempF    int       `json:"tempF"`
	Short    string    `json:"short"`
	Detailed string    `json:"detailed"`
	Precip   int       `json:"precip"` // chance, percent
	Wind     string    `json:"wind"`
}

// Hour is one hour of the hourly forecast.
type Hour struct {
	Start  time.Time `json:"start"`
	TempF  int       `json:"tempF"`
	Precip int       `json:"precip"`
	Short  string    `json:"short"`
	Wind   string    `json:"wind"`
	IsDay  bool      `json:"isDay"`
}

// Alert is an active NWS watch, warning or advisory.
type Alert struct {
	Event       string    `json:"event"`
	Headline    string    `json:"headline"`
	Severity    string    `json:"severity"` // Extreme, Severe, Moderate, Minor
	Urgency     string    `json:"urgency"`
	Onset       time.Time `json:"onset"`
	Ends        time.Time `json:"ends"`
	Description string    `json:"description"`
	Instruction string    `json:"instruction"`
}

// Air is current air quality.
type Air struct {
	AQI      int     `json:"aqi"` // US AQI
	Category string  `json:"category"`
	PM25     float64 `json:"pm25"`
	Ozone    float64 `json:"ozone"`
}

// Sun is today's sunrise and sunset.
type Sun struct {
	Rise time.Time `json:"rise"`
	Set  time.Time `json:"set"`
}

// Source fetches and caches weather for one location.
type Source struct {
	HTTP *http.Client

	mu     sync.Mutex
	point  geo.Point
	meta   *pointMeta
	cache  map[string]cached
	images map[string]image
}

type cached struct {
	at   time.Time
	data any
}

type image struct {
	at   time.Time
	kind string
	data []byte
}

// pointMeta is what NWS knows about a location.
type pointMeta struct {
	Forecast string
	Hourly   string
	Stations string
	Radar    string // radar site, e.g. KFTG
}

// NewSource returns a weather source.
func NewSource(c *http.Client) *Source {
	return &Source{HTTP: c, cache: map[string]cached{}, images: map[string]image{}}
}

// Report gathers the weather at p. Each part is cached on its own schedule
// and a failed part keeps its last good value.
func (s *Source) Report(ctx context.Context, p geo.Point) (*Report, error) {
	s.mu.Lock()
	if s.point != p {
		s.point, s.meta = p, nil
		clear(s.cache)
		clear(s.images)
	}
	s.mu.Unlock()
	meta, err := s.pointMeta(ctx, p)
	if err != nil {
		return nil, err
	}
	r := &Report{Updated: time.Now(), Radar: "/wximg/radar.gif", Satellite: "/wximg/satellite.jpg"}
	part := func(key string, ttl time.Duration, fetch func(context.Context) (any, error)) any {
		v, err := s.cached(ctx, key, ttl, fetch)
		if err != nil {
			r.Errors = append(r.Errors, key+": "+err.Error())
		}
		return v
	}
	if v, ok := part("now", 10*time.Minute, func(ctx context.Context) (any, error) { return s.observation(ctx, meta) }).(*Now); ok {
		r.Now = v
	}
	if v, ok := part("periods", 20*time.Minute, func(ctx context.Context) (any, error) { return s.periods(ctx, meta) }).([]Period); ok {
		r.Periods = v
	}
	if v, ok := part("hourly", 20*time.Minute, func(ctx context.Context) (any, error) { return s.hourly(ctx, meta) }).([]Hour); ok {
		r.Hourly = v
	}
	if v, ok := part("alerts", 2*time.Minute, func(ctx context.Context) (any, error) { return s.alerts(ctx, p) }).([]Alert); ok {
		r.Alerts = v
	}
	if v, ok := part("air", 30*time.Minute, func(ctx context.Context) (any, error) { return s.air(ctx, p) }).(*Air); ok {
		r.Air = v
	}
	if v, ok := part("sun", 6*time.Hour, func(ctx context.Context) (any, error) { return s.sun(ctx, p) }).(*Sun); ok {
		r.Sun = v
	}
	// Drop hours already past.
	cut := time.Now().Add(-time.Hour)
	for len(r.Hourly) > 0 && r.Hourly[0].Start.Before(cut) {
		r.Hourly = r.Hourly[1:]
	}
	return r, nil
}

func (s *Source) cached(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) (any, error)) (any, error) {
	s.mu.Lock()
	c, ok := s.cache[key]
	s.mu.Unlock()
	if ok && time.Since(c.at) < ttl {
		return c.data, nil
	}
	v, err := fetch(ctx)
	if err != nil {
		if ok {
			return c.data, err
		}
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = cached{at: time.Now(), data: v}
	s.mu.Unlock()
	return v, nil
}

func (s *Source) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/geo+json, application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *Source) pointMeta(ctx context.Context, p geo.Point) (*pointMeta, error) {
	s.mu.Lock()
	m := s.meta
	s.mu.Unlock()
	if m != nil {
		return m, nil
	}
	var body struct {
		Properties struct {
			Forecast            string `json:"forecast"`
			ForecastHourly      string `json:"forecastHourly"`
			ObservationStations string `json:"observationStations"`
			RadarStation        string `json:"radarStation"`
		} `json:"properties"`
	}
	if err := s.getJSON(ctx, fmt.Sprintf("https://api.weather.gov/points/%.4f,%.4f", p.Lat, p.Lon), &body); err != nil {
		return nil, fmt.Errorf("nws point: %w", err)
	}
	pr := body.Properties
	m = &pointMeta{Forecast: pr.Forecast, Hourly: pr.ForecastHourly, Stations: pr.ObservationStations, Radar: pr.RadarStation}
	s.mu.Lock()
	s.meta = m
	s.mu.Unlock()
	return m, nil
}

type quantity struct {
	Value *float64 `json:"value"`
}

func (s *Source) observation(ctx context.Context, m *pointMeta) (*Now, error) {
	var stations struct {
		Features []struct {
			Properties struct {
				ID   string `json:"stationIdentifier"`
				Name string `json:"name"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := s.getJSON(ctx, m.Stations, &stations); err != nil {
		return nil, err
	}
	// The nearest station sometimes has no recent report; try a few.
	for _, f := range stations.Features[:min(3, len(stations.Features))] {
		var obs struct {
			Properties struct {
				Timestamp          time.Time `json:"timestamp"`
				TextDescription    string    `json:"textDescription"`
				Temperature        quantity  `json:"temperature"`
				Dewpoint           quantity  `json:"dewpoint"`
				WindDirection      quantity  `json:"windDirection"`
				WindSpeed          quantity  `json:"windSpeed"`
				WindGust           quantity  `json:"windGust"`
				BarometricPressure quantity  `json:"barometricPressure"`
				Visibility         quantity  `json:"visibility"`
				RelativeHumidity   quantity  `json:"relativeHumidity"`
				WindChill          quantity  `json:"windChill"`
				HeatIndex          quantity  `json:"heatIndex"`
			} `json:"properties"`
		}
		if err := s.getJSON(ctx, "https://api.weather.gov/stations/"+f.Properties.ID+"/observations/latest", &obs); err != nil {
			continue
		}
		o := obs.Properties
		if o.Temperature.Value == nil || time.Since(o.Timestamp) > 3*time.Hour {
			continue
		}
		now := &Now{
			Station: f.Properties.Name, Observed: o.Timestamp, Description: o.TextDescription,
			TempF: cToF(o.Temperature.Value), DewpointF: cToF(o.Dewpoint.Value), Humidity: round(o.RelativeHumidity.Value, 0),
			WindMph: scale(o.WindSpeed.Value, 0.621371), GustMph: scale(o.WindGust.Value, 0.621371),
			WindDir:      compass(o.WindDirection.Value),
			PressureInHg: scale(o.BarometricPressure.Value, 0.0002953), VisibilityMi: scale(o.Visibility.Value, 0.000621371),
		}
		switch {
		case o.HeatIndex.Value != nil:
			now.FeelsLikeF = cToF(o.HeatIndex.Value)
		case o.WindChill.Value != nil:
			now.FeelsLikeF = cToF(o.WindChill.Value)
		}
		return now, nil
	}
	return nil, fmt.Errorf("no recent observation near here")
}

type nwsPeriod struct {
	Name              string    `json:"name"`
	StartTime         time.Time `json:"startTime"`
	IsDaytime         bool      `json:"isDaytime"`
	Temperature       int       `json:"temperature"`
	WindSpeed         string    `json:"windSpeed"`
	WindDirection     string    `json:"windDirection"`
	ShortForecast     string    `json:"shortForecast"`
	DetailedForecast  string    `json:"detailedForecast"`
	ProbabilityPrecip quantity  `json:"probabilityOfPrecipitation"`
}

func (s *Source) forecast(ctx context.Context, u string) ([]nwsPeriod, error) {
	var body struct {
		Properties struct {
			Periods []nwsPeriod `json:"periods"`
		} `json:"properties"`
	}
	if err := s.getJSON(ctx, u, &body); err != nil {
		return nil, err
	}
	return body.Properties.Periods, nil
}

func (s *Source) periods(ctx context.Context, m *pointMeta) ([]Period, error) {
	ps, err := s.forecast(ctx, m.Forecast)
	if err != nil {
		return nil, err
	}
	out := make([]Period, 0, len(ps))
	for _, p := range ps {
		out = append(out, Period{
			Name: p.Name, Start: p.StartTime, IsDay: p.IsDaytime, TempF: p.Temperature,
			Short: p.ShortForecast, Detailed: p.DetailedForecast, Precip: pct(p.ProbabilityPrecip.Value),
			Wind: strings.TrimSpace(p.WindDirection + " " + p.WindSpeed),
		})
	}
	return out, nil
}

func (s *Source) hourly(ctx context.Context, m *pointMeta) ([]Hour, error) {
	ps, err := s.forecast(ctx, m.Hourly)
	if err != nil {
		return nil, err
	}
	out := make([]Hour, 0, 48)
	for _, p := range ps[:min(48, len(ps))] {
		out = append(out, Hour{
			Start: p.StartTime, TempF: p.Temperature, Precip: pct(p.ProbabilityPrecip.Value),
			Short: p.ShortForecast, Wind: strings.TrimSpace(p.WindDirection + " " + p.WindSpeed), IsDay: p.IsDaytime,
		})
	}
	return out, nil
}

func (s *Source) alerts(ctx context.Context, p geo.Point) ([]Alert, error) {
	var body struct {
		Features []struct {
			Properties struct {
				Event       string    `json:"event"`
				Headline    string    `json:"headline"`
				Severity    string    `json:"severity"`
				Urgency     string    `json:"urgency"`
				Onset       time.Time `json:"onset"`
				Ends        time.Time `json:"ends"`
				Expires     time.Time `json:"expires"`
				Description string    `json:"description"`
				Instruction string    `json:"instruction"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := s.getJSON(ctx, fmt.Sprintf("https://api.weather.gov/alerts/active?point=%.4f,%.4f", p.Lat, p.Lon), &body); err != nil {
		return nil, err
	}
	out := []Alert{}
	for _, f := range body.Features {
		a := f.Properties
		ends := a.Ends
		if ends.IsZero() {
			ends = a.Expires
		}
		out = append(out, Alert{
			Event: a.Event, Headline: a.Headline, Severity: a.Severity, Urgency: a.Urgency,
			Onset: a.Onset, Ends: ends, Description: a.Description, Instruction: a.Instruction,
		})
	}
	return out, nil
}

func (s *Source) air(ctx context.Context, p geo.Point) (*Air, error) {
	var body struct {
		Current struct {
			AQI   *float64 `json:"us_aqi"`
			PM25  float64  `json:"pm2_5"`
			Ozone float64  `json:"ozone"`
		} `json:"current"`
	}
	q := url.Values{
		"latitude": {fmt.Sprintf("%.4f", p.Lat)}, "longitude": {fmt.Sprintf("%.4f", p.Lon)},
		"current": {"us_aqi,pm2_5,ozone"},
	}
	if err := s.getJSON(ctx, "https://air-quality-api.open-meteo.com/v1/air-quality?"+q.Encode(), &body); err != nil {
		return nil, err
	}
	if body.Current.AQI == nil {
		return nil, fmt.Errorf("no air quality reading")
	}
	aqi := int(math.Round(*body.Current.AQI))
	return &Air{AQI: aqi, Category: aqiCategory(aqi), PM25: body.Current.PM25, Ozone: body.Current.Ozone}, nil
}

func (s *Source) sun(ctx context.Context, p geo.Point) (*Sun, error) {
	var body struct {
		Timezone string `json:"timezone"`
		Daily    struct {
			Sunrise []string `json:"sunrise"`
			Sunset  []string `json:"sunset"`
		} `json:"daily"`
	}
	q := url.Values{
		"latitude": {fmt.Sprintf("%.4f", p.Lat)}, "longitude": {fmt.Sprintf("%.4f", p.Lon)},
		"daily": {"sunrise,sunset"}, "timezone": {"auto"}, "forecast_days": {"1"},
	}
	if err := s.getJSON(ctx, "https://api.open-meteo.com/v1/forecast?"+q.Encode(), &body); err != nil {
		return nil, err
	}
	if len(body.Daily.Sunrise) == 0 || len(body.Daily.Sunset) == 0 {
		return nil, fmt.Errorf("no sun times")
	}
	loc, err := time.LoadLocation(body.Timezone)
	if err != nil {
		loc = time.Local
	}
	rise, err1 := time.ParseInLocation("2006-01-02T15:04", body.Daily.Sunrise[0], loc)
	set, err2 := time.ParseInLocation("2006-01-02T15:04", body.Daily.Sunset[0], loc)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("parse sun times")
	}
	return &Sun{Rise: rise, Set: set}, nil
}

// Image returns the latest radar loop ("radar.gif") or satellite still
// ("satellite.jpg") for the location, cached for a few minutes.
func (s *Source) Image(ctx context.Context, p geo.Point, name string) ([]byte, string, error) {
	meta, err := s.pointMeta(ctx, p)
	if err != nil {
		return nil, "", err
	}
	var u, kind string
	var ttl time.Duration
	switch name {
	case "radar.gif":
		u, kind, ttl = "https://radar.weather.gov/ridge/standard/"+meta.Radar+"_loop.gif", "image/gif", 4*time.Minute
	case "satellite.jpg":
		sat, sector := satelliteSector(p)
		u = fmt.Sprintf("https://cdn.star.nesdis.noaa.gov/%s/ABI/SECTOR/%s/GEOCOLOR/600x600.jpg", sat, sector)
		kind, ttl = "image/jpeg", 10*time.Minute
	default:
		return nil, "", fmt.Errorf("no image %q", name)
	}
	s.mu.Lock()
	img, ok := s.images[name]
	s.mu.Unlock()
	if ok && time.Since(img.at) < ttl {
		return img.data, img.kind, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		if ok {
			return img.data, img.kind, nil
		}
		return nil, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		if ok {
			return img.data, img.kind, nil
		}
		return nil, "", fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	s.mu.Lock()
	s.images[name] = image{at: time.Now(), kind: kind, data: data}
	s.mu.Unlock()
	return data, kind, nil
}

// satelliteSector picks the GOES regional view centered nearest p.
func satelliteSector(p geo.Point) (satellite, sector string) {
	sectors := []struct {
		sat, code string
		at        geo.Point
	}{
		{"GOES18", "pnw", geo.Point{Lat: 45.5, Lon: -121}},
		{"GOES18", "psw", geo.Point{Lat: 36, Lon: -119}},
		{"GOES19", "nr", geo.Point{Lat: 45, Lon: -109}},
		{"GOES19", "sr", geo.Point{Lat: 37.5, Lon: -107}},
		{"GOES19", "sp", geo.Point{Lat: 33, Lon: -99}},
		{"GOES19", "umv", geo.Point{Lat: 44, Lon: -93}},
		{"GOES19", "smv", geo.Point{Lat: 33, Lon: -91}},
		{"GOES19", "cgl", geo.Point{Lat: 43, Lon: -84}},
		{"GOES19", "ne", geo.Point{Lat: 43, Lon: -73}},
		{"GOES19", "se", geo.Point{Lat: 31, Lon: -83}},
	}
	best := sectors[0]
	for _, s := range sectors[1:] {
		if geo.DistanceKm(p, s.at) < geo.DistanceKm(p, best.at) {
			best = s
		}
	}
	return best.sat, best.code
}

func aqiCategory(aqi int) string {
	switch {
	case aqi <= 50:
		return "Good"
	case aqi <= 100:
		return "Moderate"
	case aqi <= 150:
		return "Unhealthy for sensitive groups"
	case aqi <= 200:
		return "Unhealthy"
	case aqi <= 300:
		return "Very unhealthy"
	default:
		return "Hazardous"
	}
}

func cToF(c *float64) *float64 {
	if c == nil {
		return nil
	}
	f := math.Round(*c*9/5 + 32)
	return &f
}

func scale(v *float64, k float64) *float64 {
	if v == nil {
		return nil
	}
	x := math.Round(*v*k*10) / 10
	return &x
}

func round(v *float64, places int) *float64 {
	if v == nil {
		return nil
	}
	p := math.Pow(10, float64(places))
	x := math.Round(*v*p) / p
	return &x
}

func pct(v *float64) int {
	if v == nil {
		return 0
	}
	return int(math.Round(*v))
}

func compass(deg *float64) string {
	if deg == nil {
		return ""
	}
	dirs := []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}
	return dirs[int(math.Round(*deg/22.5))%16]
}
