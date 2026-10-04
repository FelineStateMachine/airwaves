// Package lineup combines transmitter data, ATSC 3.0 hosting and guide
// listings into one view of what is on the air at a location: with
// terrain-based reception estimates for planning (otascan), or matched to
// what a tuner actually receives (Match) for the app.
package lineup

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"airwaves/internal/atsc3"
	"airwaves/internal/fcc"
	"airwaves/internal/geo"
	"airwaves/internal/guide"
	"airwaves/internal/reception"
	"airwaves/internal/store"
	"airwaves/internal/terrain"
)

// Station is a transmitter, with its predicted reception when the report
// was built with terrain.
type Station struct {
	fcc.Facility
	Band string `json:"band"`
	// Signal is the estimate for each antenna preset, keyed by preset name;
	// none without terrain.
	Signal map[string]reception.Estimate `json:"signal,omitempty"`
	ATSC3  bool                          `json:"atsc3"`
	// Carries lists the virtual channels this transmitter is matched to.
	Carries []string `json:"carries"`
}

// Carriage describes where a channel is broadcast in ATSC 3.0.
type Carriage struct {
	HostCall   string                    `json:"hostCall"`
	FacilityID int                       `json:"facilityId"`
	RF         string                    `json:"rf"`
	Display    string                    `json:"display"`
	Tier       map[string]reception.Tier `json:"tier"`
}

// Channel is a virtual channel a viewer can tune.
type Channel struct {
	Number   string `json:"number"`
	Major    int    `json:"major"`
	Minor    int    `json:"minor"`
	GuideID  string `json:"guideId,omitempty"`
	CallSign string `json:"callSign"`
	BaseCall string `json:"baseCall"`
	Network  string `json:"network"`
	Logo     string `json:"logo,omitempty"`
	// FacilityID is the transmitter carrying the ATSC 1.0 signal, picked as
	// the best-received facility for the station; 0 when none was found.
	FacilityID int `json:"facilityId"`
	// Via names the host when another station's transmitter carries this
	// channel's ATSC 1.0 signal (common after an ATSC 3.0 conversion).
	Via string `json:"via,omitempty"`
	// Tier is the predicted ATSC 1.0 reception per preset. For channels that
	// only exist in ATSC 3.0 it is the 3.0 prediction.
	Tier      map[string]reception.Tier `json:"tier"`
	ATSC3     *Carriage                 `json:"atsc3,omitempty"`
	ATSC3Only bool                      `json:"atsc3Only,omitempty"`
}

// Source credits a data provider.
type Source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Use  string `json:"use"`
}

// Report is everything known about over-the-air TV at a location.
type Report struct {
	Generated time.Time          `json:"generated"`
	Place     geo.Place          `json:"place"`
	Point     geo.Point          `json:"point"`
	RadiusKm  float64            `json:"radiusKm"`
	Presets   []reception.Preset `json:"presets,omitempty"` // with estimates only
	Stations  []Station          `json:"stations"`
	Channels  []Channel          `json:"channels"`
	ATSC3     []atsc3.Host       `json:"atsc3"`
	Sources   []Source           `json:"sources"`
	Warnings  []string           `json:"warnings"`
}

// Options select the location and freshness of a build.
type Options struct {
	ZIP        string
	Point      *geo.Point // overrides the ZIP centroid
	RadiusKm   float64
	GuideHours int
	Refresh    bool // bypass cached transmitter, ATSC 3.0 and guide data
}

// Builder fetches and merges the data sources.
type Builder struct {
	HTTP  *http.Client
	Cache *store.Cache
	// Terrain, when set, has every transmitter's reception estimated from
	// the terrain between it and the location. Without it the report is
	// the records alone, and its stations are ordered by distance.
	Terrain  *terrain.Source
	Progress func(string)
}

func (b *Builder) progress(format string, args ...any) {
	if b.Progress != nil {
		b.Progress(fmt.Sprintf(format, args...))
	}
}

// Build produces a report and the guide it was built from. Failures of
// optional sources (terrain, ATSC 3.0 list, listings) become warnings.
func (b *Builder) Build(ctx context.Context, opt Options) (*Report, *guide.Guide, error) {
	rep := &Report{Generated: time.Now(), RadiusKm: opt.RadiusKm}
	for _, src := range sources {
		if b.Terrain != nil || src.Name != terrainSource {
			rep.Sources = append(rep.Sources, src)
		}
	}
	if b.Terrain != nil {
		rep.Presets = reception.Presets
	}

	b.progress("Locating ZIP %s", opt.ZIP)
	place, err := store.Load(ctx, b.Cache, "zip-"+opt.ZIP, 365*24*time.Hour, false, func(ctx context.Context) (geo.Place, error) {
		return geo.LookupZIP(ctx, b.HTTP, opt.ZIP)
	})
	if err != nil {
		return nil, nil, err
	}
	rep.Place, rep.Point = place, place.Point
	if opt.Point != nil {
		rep.Point = *opt.Point
	}

	b.progress("Querying FCC for transmitters within %.0f km", opt.RadiusKm)
	facKey := fmt.Sprintf("fcc-%s-%.0f", coordKey(rep.Point), opt.RadiusKm)
	facilities, err := store.Load(ctx, b.Cache, facKey, 7*24*time.Hour, opt.Refresh, func(ctx context.Context) ([]fcc.Facility, error) {
		return fcc.Query(ctx, b.HTTP, rep.Point, opt.RadiusKm)
	})
	if err != nil {
		return nil, nil, err
	}

	rep.Stations = b.predict(ctx, rep, facilities)

	b.progress("Loading the ATSC 3.0 station list")
	hosts, err := store.Load(ctx, b.Cache, "atsc3-list", 3*24*time.Hour, opt.Refresh, func(ctx context.Context) ([]atsc3.Host, error) {
		return atsc3.Fetch(ctx, b.HTTP)
	})
	if err != nil {
		rep.Warnings = append(rep.Warnings, "ATSC 3.0 list unavailable: "+err.Error())
	}
	rep.ATSC3 = relevantHosts(hosts, rep.Stations)

	b.progress("Loading listings for lineup %s", guide.LineupID(opt.ZIP))
	guideKey := fmt.Sprintf("guide-%s-%dh", opt.ZIP, opt.GuideHours)
	g, err := store.Load(ctx, b.Cache, guideKey, 4*time.Hour, opt.Refresh, func(ctx context.Context) (*guide.Guide, error) {
		return guide.Fetch(ctx, b.HTTP, opt.ZIP, time.Now().Add(-30*time.Minute), opt.GuideHours)
	})
	if err != nil {
		rep.Warnings = append(rep.Warnings, "Listings unavailable: "+err.Error())
		g = nil
	}

	rep.Channels = buildChannels(rep, g)
	b.progress("Found %d channels from %d transmitters", len(rep.Channels), len(rep.Stations))
	return rep, g, nil
}

const terrainSource = "AWS Terrain Tiles"

var sources = []Source{
	{"FCC TV Query", "https://www.fcc.gov/media/television/tv-query", "Licensed transmitters, power, height, location"},
	{terrainSource, "https://registry.opendata.aws/terrain-tiles/", "Terrain profiles for reception estimates"},
	{"RabbitEars.Info", "https://www.rabbitears.info/market.php?request=atsc3", "ATSC 3.0 (NextGen TV) hosting"},
	{"Gracenote TV Listings", "https://tvlistings.gracenote.com/", "Over-the-air lineup and program guide"},
	{"Zippopotam.us", "https://zippopotam.us/", "Location lookup"},
}

func coordKey(p geo.Point) string {
	return strconv.FormatFloat(p.Lat, 'f', 4, 64) + "_" + strconv.FormatFloat(p.Lon, 'f', 4, 64)
}

// predict fetches a terrain profile per tower site and evaluates every
// facility against each antenna preset. Without terrain it lists the
// facilities, nearest first.
func (b *Builder) predict(ctx context.Context, rep *Report, facilities []fcc.Facility) []Station {
	if b.Terrain == nil {
		stations := make([]Station, 0, len(facilities))
		for _, f := range facilities {
			stations = append(stations, Station{Facility: f, Band: fcc.Band(f.RFChannel), Signal: map[string]reception.Estimate{}})
		}
		slices.SortStableFunc(stations, func(a, b Station) int { return cmp.Compare(a.DistanceKm, b.DistanceKm) })
		return stations
	}
	b.progress("Sampling terrain to %d transmitters", len(facilities))
	var mu sync.Mutex
	profiles := make(map[geo.Point]terrain.Profile)
	var failed int
	var eg errgroup.Group
	eg.SetLimit(8)
	sites := make(map[geo.Point]bool)
	for _, f := range facilities {
		if sites[f.Point] {
			continue
		}
		sites[f.Point] = true
		eg.Go(func() error {
			prof, err := b.Terrain.Profile(ctx, rep.Point, f.Point)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return nil
			}
			profiles[f.Point] = prof
			return nil
		})
	}
	_ = eg.Wait()
	if failed > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("Terrain unavailable for %d tower sites; their reception is unknown", failed))
	}

	stations := make([]Station, 0, len(facilities))
	for _, f := range facilities {
		s := Station{Facility: f, Band: fcc.Band(f.RFChannel), Signal: make(map[string]reception.Estimate)}
		prof, ok := profiles[f.Point]
		for _, p := range reception.Presets {
			if ok {
				s.Signal[p.Name] = reception.Predict(reception.FromFacility(f), prof, p)
			} else {
				s.Signal[p.Name] = reception.Estimate{Preset: p.Name, Tier: reception.Unknown}
			}
		}
		stations = append(stations, s)
	}
	slices.SortFunc(stations, func(a, b Station) int {
		return cmp.Compare(b.Signal["rooftop"].NoiseMarginDB, a.Signal["rooftop"].NoiseMarginDB)
	})
	return stations
}

// relevantHosts keeps ATSC 3.0 hosts that are among the nearby
// transmitters, and flags those stations.
func relevantHosts(hosts []atsc3.Host, stations []Station) []atsc3.Host {
	byID := make(map[int][]int)
	for i, s := range stations {
		byID[s.FacilityID] = append(byID[s.FacilityID], i)
	}
	var out []atsc3.Host
	for _, h := range hosts {
		idx, ok := byID[h.FacilityID]
		if !ok {
			continue
		}
		for _, i := range idx {
			stations[i].ATSC3 = true
		}
		out = append(out, h)
	}
	return out
}

func buildChannels(rep *Report, g *guide.Guide) []Channel {
	byBase := make(map[string][]*Station)
	byID := make(map[int]*Station)
	for i := range rep.Stations {
		s := &rep.Stations[i]
		byBase[s.BaseCall] = append(byBase[s.BaseCall], s)
		// Stations are sorted best first, so keep the first per facility.
		if _, ok := byID[s.FacilityID]; !ok {
			byID[s.FacilityID] = s
		}
	}

	type svcRef struct {
		host *atsc3.Host
		svc  atsc3.Service
	}
	var services []svcRef
	for i := range rep.ATSC3 {
		for _, s := range rep.ATSC3[i].Services {
			services = append(services, svcRef{&rep.ATSC3[i], s})
		}
	}
	matched := make([]bool, len(services))

	carriage := func(r svcRef) *Carriage {
		c := &Carriage{HostCall: r.host.CallSign, FacilityID: r.host.FacilityID, RF: r.host.RF, Display: r.svc.Display}
		if s := byID[r.host.FacilityID]; s != nil {
			c.Tier = tiers(s)
			s.Carries = append(s.Carries, fmt.Sprintf("%d.%d (3.0)", r.svc.Major, r.svc.Minor))
		}
		return c
	}

	var channels []Channel
	if g != nil {
		for _, gc := range g.Channels {
			major, minor := guide.SplitNumber(gc.Number)
			ch := Channel{
				Number: gc.Number, Major: major, Minor: minor, GuideID: gc.ID,
				CallSign: gc.CallSign, BaseCall: guide.BaseCall(gc.CallSign),
				Network: gc.Network, Logo: gc.Logo,
			}
			txBase := ch.BaseCall
			for i, r := range services {
				if fcc.BaseCall(r.svc.Name) != ch.BaseCall || r.svc.Major != major {
					continue
				}
				// The 3.0 list names the 1.0 host for the main channel; the
				// station's other subchannels usually move with it.
				if r.svc.ATSC1Call != "" && fcc.BaseCall(r.svc.ATSC1Call) != ch.BaseCall {
					txBase = fcc.BaseCall(r.svc.ATSC1Call)
					ch.Via = r.svc.ATSC1Call
				}
				if r.svc.Minor == minor {
					ch.ATSC3 = carriage(r)
					matched[i] = true
				}
			}
			best := bestStation(byBase[txBase])
			if best == nil && txBase != ch.BaseCall {
				best = bestStation(byBase[ch.BaseCall])
				ch.Via = ""
			}
			if best != nil {
				ch.FacilityID = best.FacilityID
				ch.Tier = tiers(best)
				best.Carries = append(best.Carries, ch.Number)
			} else {
				ch.Tier = unknownTiers()
			}
			channels = append(channels, ch)
		}
	} else {
		channels = channelsFromStations(rep.Stations)
	}

	for i, r := range services {
		if matched[i] {
			continue
		}
		c := carriage(r)
		ch := Channel{
			Number: fmt.Sprintf("%d.%d", r.svc.Major, r.svc.Minor), Major: r.svc.Major, Minor: r.svc.Minor,
			CallSign: r.svc.Name, BaseCall: fcc.BaseCall(r.svc.Name), Network: r.svc.Network,
			FacilityID: r.host.FacilityID, Tier: c.Tier, ATSC3: c, ATSC3Only: true,
		}
		if ch.Tier == nil {
			ch.Tier = unknownTiers()
		}
		channels = append(channels, ch)
	}

	slices.SortStableFunc(channels, func(a, b Channel) int {
		if c := cmp.Compare(a.Major, b.Major); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Minor, b.Minor); c != 0 {
			return c
		}
		return cmp.Compare(rank(b.Tier["rooftop"]), rank(a.Tier["rooftop"]))
	})
	return channels
}

// channelsFromStations lists one main channel per station when listings are
// unavailable.
func channelsFromStations(stations []Station) []Channel {
	seen := make(map[string]bool)
	var out []Channel
	for i := range stations {
		s := &stations[i]
		if s.VirtualChannel == 0 || seen[s.BaseCall] {
			continue
		}
		seen[s.BaseCall] = true
		n := fmt.Sprintf("%d.1", s.VirtualChannel)
		s.Carries = append(s.Carries, n)
		out = append(out, Channel{
			Number: n, Major: s.VirtualChannel, Minor: 1, CallSign: s.CallSign, BaseCall: s.BaseCall,
			FacilityID: s.FacilityID, Tier: tiers(s),
		})
	}
	return out
}

func bestStation(cands []*Station) *Station {
	var best *Station
	for _, s := range cands {
		if best == nil || s.Signal["rooftop"].NoiseMarginDB > best.Signal["rooftop"].NoiseMarginDB {
			best = s
		}
	}
	return best
}

func tiers(s *Station) map[string]reception.Tier {
	m := make(map[string]reception.Tier, len(s.Signal))
	for k, e := range s.Signal {
		m[k] = e.Tier
	}
	return m
}

func unknownTiers() map[string]reception.Tier {
	m := make(map[string]reception.Tier, len(reception.Presets))
	for _, p := range reception.Presets {
		m[p.Name] = reception.Unknown
	}
	return m
}

var tierRank = map[reception.Tier]int{
	reception.Strong: 5, reception.Good: 4, reception.Fair: 3, reception.Weak: 2, reception.Unlikely: 1,
}

func rank(t reception.Tier) int { return tierRank[t] }
