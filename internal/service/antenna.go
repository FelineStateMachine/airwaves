package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"airwaves/internal/fcc"
	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/reception"
	"airwaves/internal/signal"
	"airwaves/internal/tuner"
	"airwaves/internal/tvh"
)

// ErrNoTuner is returned for an antenna channel by a server that has no
// tuner to tune it with.
var ErrNoTuner = errors.New("no tuner: the server's Tvheadend has found no ATSC tuner")

// Antenna is what the app shows of the antenna: the tuner, the channels it
// receives and what was measured on each RF channel. Every channel is one
// the tuner's scan found; what the FCC records, the listings and the ATSC
// 3.0 list say only describes them.
type Antenna struct {
	Tuner    TunerInfo        `json:"tuner"`
	Channels []AntennaChannel `json:"channels"`
	Muxes    []MuxSignal      `json:"muxes"`
	Sweep    SweepStatus      `json:"sweep"`
}

// TunerInfo describes the server's tuners.
type TunerInfo struct {
	// Ready is set when there is a tuner with its antenna network.
	Ready bool `json:"ready"`
	// Reason says why there are no antenna channels, when there aren't.
	Reason string `json:"reason,omitempty"`
	// Model is the tuner's model as Tvheadend reports it
	// ("hdhomerun_dvr_atsc").
	Model  string `json:"model,omitempty"`
	Tuners int    `json:"tuners"`
	// InUse counts tuners tuned now: to a viewer, a recording, a
	// measurement or Tvheadend's own guide scan.
	InUse int `json:"inUse"`
	// Standards lists what the tuners receive; ATSC3 is whether that takes
	// in ATSC 3.0 (NextGen TV). Tvheadend's ATSC tuners are ATSC 1.0 only.
	Standards []string `json:"standards"`
	ATSC3     bool     `json:"atsc3"`
	// Scanning is set while Tvheadend scans RF channels for services.
	Scanning bool `json:"scanning"`
	// Demo is set while the channels are generated test patterns (demo
	// mode, before a tuner).
	Demo bool `json:"demo,omitempty"`
}

// AntennaChannel is a channel the tuner receives.
type AntennaChannel struct {
	lineup.TunerChannel
	// Signal is the latest reading of the channel's multiplex, and Recent
	// the latest stretch of readings (the last sweep, or the last while
	// it was watched) with lows and means, steadier than one reading on a
	// marginal signal; null when it has never been measured.
	Signal *signal.Reading `json:"signal"`
	Recent *signal.Period  `json:"recent"`
	// Demo marks a generated test pattern (demo mode).
	Demo bool `json:"demo,omitempty"`
}

// MuxSignal is one RF channel: what Tvheadend's scan found there, what
// the FCC licenses on it nearby, and what the tuners measured on it.
type MuxSignal struct {
	RF           int     `json:"rf"`
	FrequencyMHz float64 `json:"frequencyMhz"`
	Band         string  `json:"band"`
	// Name is Tvheadend's for the multiplex: "605MHz".
	Name string `json:"name"`
	// Scan is how Tvheadend's last scan went; null before it scanned.
	Scan     *signal.Scan `json:"scan"`
	Scanning bool         `json:"scanning"`
	// Tuned is set while a tuner is on it.
	Tuned bool `json:"tuned"`
	// Channels are the tuner's channels on it.
	Channels []string `json:"channels"`
	// Stations are the licensed transmitters on this RF channel within
	// the search radius, nearest first.
	Stations []MuxStation `json:"stations"`
	// Signal is the latest reading, History all those kept; null when
	// never measured.
	Signal  *signal.Reading `json:"signal"`
	History *signal.History `json:"history"`
}

// MuxStation is a licensed transmitter on a multiplex's RF channel, from
// the FCC's records.
type MuxStation struct {
	FacilityID     int     `json:"facilityId"`
	CallSign       string  `json:"callSign"`
	VirtualChannel int     `json:"virtualChannel"`
	Service        string  `json:"service"` // DTV, DCA, LPD...
	City           string  `json:"city"`
	State          string  `json:"state"`
	DistanceKm     float64 `json:"distanceKm"`
	BearingDeg     float64 `json:"bearingDeg"`
	ERPkW          float64 `json:"erpKw"`
	HAATm          float64 `json:"haatM"`
	// ATSC3 is set when the station broadcasts ATSC 3.0 on this RF
	// channel (per RabbitEars), which an ATSC 1.0 tuner can't decode.
	ATSC3 bool `json:"atsc3"`
}

// ChannelSignal is a channel's latest reading and stretch of readings,
// for the app to poll.
type ChannelSignal struct {
	Number string          `json:"number"`
	RF     int             `json:"rf"`
	Signal *signal.Reading `json:"signal"`
	Recent *signal.Period  `json:"recent"`
}

// SignalReport is the measurements alone, light enough to poll while
// watching or measuring.
type SignalReport struct {
	Tuner    TunerInfo       `json:"tuner"`
	Channels []ChannelSignal `json:"channels"`
	Muxes    []MuxSignal     `json:"muxes"`
	Sweep    SweepStatus     `json:"sweep"`
}

// frontendsTTL is how long a hardware lookup stands.
const frontendsTTL = time.Minute

// frontends returns the ATSC tuners Tvheadend has, looked up at most once
// a minute (or now, with fresh). A failed lookup keeps the last answer.
func (s *Service) frontends(ctx context.Context, fresh bool) ([]tvh.Frontend, error) {
	s.mu.Lock()
	fes, at, known := s.fes, s.fesAt, s.fesKnown
	s.mu.Unlock()
	if known && !fresh && time.Since(at) < frontendsTTL {
		return fes, nil
	}
	got, err := s.opt.Tvheadend.Frontends(ctx)
	if err != nil {
		if known {
			return fes, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.fes, s.fesAt, s.fesKnown = got, time.Now(), true
	s.mu.Unlock()
	return got, nil
}

// offeredNetworks are the Tvheadend networks whose channels the app,
// the emulated HDHomeRun and recording use: the antenna network while
// there's a tuner, else the demo network in demo mode, until the antenna
// network exists. Demo channels never come back once it does.
func (s *Service) offeredNetworks() []string {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	fes, err := s.frontends(ctx, false)
	if err != nil {
		return nil
	}
	if len(fes) > 0 {
		return []string{tvh.ATSCNetwork}
	}
	if !s.opt.Demo {
		return nil
	}
	if l, err := s.tvhTuner.Lineup(ctx); err != nil || l.HasNetwork(tvh.ATSCNetwork) {
		return nil
	}
	return []string{tvh.DemoNetwork}
}

// tunerInfo describes the tuners: fes, and what's tuned now.
func (s *Service) tunerInfo(ctx context.Context, l *tvh.Lineup) TunerInfo {
	if s.opt.NoAntenna {
		return TunerInfo{Reason: ErrNoAntenna.Error(), Standards: []string{}}
	}
	if s.opt.Tvheadend == nil {
		return TunerInfo{Reason: "no tuner: the server has no Tvheadend", Standards: []string{}}
	}
	ti := TunerInfo{Standards: []string{}}
	fes, err := s.frontends(ctx, false)
	if err != nil {
		ti.Reason = "Tvheadend is not reachable: " + err.Error()
		return ti
	}
	ti.Tuners = len(fes)
	for _, fe := range fes {
		ti.Model = cmp.Or(ti.Model, fe.Model)
	}
	if len(fes) > 0 {
		ti.Standards = []string{"ATSC 1.0"}
	}
	if st, ok := s.statusNow(); ok {
		ti.InUse = st.busy()
	}
	switch {
	case l == nil:
	case len(fes) > 0 && l.HasNetwork(tvh.ATSCNetwork):
		ti.Ready = true
		for _, m := range l.NetworkMuxes(tvh.ATSCNetwork) {
			ti.Scanning = ti.Scanning || bool(m.Enabled) && m.Scanning()
		}
	case len(fes) > 0:
		ti.Reason = "the tuner was just found: its antenna network is being set up"
	case s.opt.Demo && slices.Contains(s.offeredNetworks(), tvh.DemoNetwork):
		ti.Demo = true
		ti.Reason = "demo mode: the channels are test patterns until a tuner is found"
	case l.HasNetwork(tvh.ATSCNetwork):
		ti.Reason = "Tvheadend sees no tuner right now"
	default:
		ti.Reason = ErrNoTuner.Error()
	}
	return ti
}

// antenna is the tuner's lineup described from rep and g, with the
// measurements; rep and g may be nil.
func (s *Service) antenna(ctx context.Context, rep *lineup.Report, g *guide.Guide) *Antenna {
	a := &Antenna{Channels: []AntennaChannel{}, Muxes: []MuxSignal{}, Sweep: s.sweepStatus()}
	if s.opt.NoAntenna || s.opt.Tvheadend == nil {
		a.Tuner = s.tunerInfo(ctx, nil)
		return a
	}
	l, err := s.tvhTuner.Lineup(ctx)
	if err != nil {
		a.Tuner = s.tunerInfo(ctx, nil)
		a.Tuner.Reason = "Tvheadend is not reachable: " + err.Error()
		a.Muxes = s.muxSignals(nil, rep, nil)
		return a
	}
	a.Tuner = s.tunerInfo(ctx, l)
	s.noteScans(l)
	chans, tuned, err := s.matched(ctx, rep, g)
	if err != nil {
		log.Printf("antenna channels: %v", err)
	}
	onMux := map[string][]string{} // channel numbers by multiplex UUID
	for i, c := range chans {
		ac := AntennaChannel{TunerChannel: c, Demo: tuned[i].Mux.Network == tvh.DemoNetwork}
		if m, ok := s.signals.Get(tuned[i].Mux.FrequencyHz); ok {
			ac.Signal, ac.Recent = m.Latest, m.Recent()
		}
		a.Channels = append(a.Channels, ac)
		onMux[tuned[i].Mux.UUID] = append(onMux[tuned[i].Mux.UUID], c.Number)
	}
	a.Muxes = s.muxSignals(l, rep, onMux)
	return a
}

// matched is the offered channels described from rep and g, with the
// Tvheadend channel each one is, in the same order.
func (s *Service) matched(ctx context.Context, rep *lineup.Report, g *guide.Guide) ([]lineup.TunerChannel, []tuner.Channel, error) {
	if s.tvhTuner == nil {
		return nil, nil, nil
	}
	tuned, err := s.tvhTuner.Channels(ctx)
	if err != nil {
		return nil, nil, err
	}
	found := make([]lineup.Scanned, len(tuned))
	for i, c := range tuned {
		found[i] = lineup.Scanned{Number: c.Number, Name: cmp.Or(c.Service.Name, c.Name), RF: signal.RF(c.Mux.FrequencyHz)}
	}
	if rep == nil {
		rep = &lineup.Report{}
	}
	chans := lineup.Match(rep, g, found)
	byNumber := make(map[string]tuner.Channel, len(tuned))
	for _, c := range tuned {
		byNumber[c.Number] = c
	}
	same := make([]tuner.Channel, len(chans))
	for i, c := range chans {
		same[i] = byNumber[c.Number]
	}
	return chans, same, nil
}

// muxSignals lists the antenna network's multiplexes (or, without
// Tvheadend's lineup, those measured before) with what's known of each.
func (s *Service) muxSignals(l *tvh.Lineup, rep *lineup.Report, chans map[string][]string) []MuxSignal {
	st, _ := s.statusNow()
	tunedTo := map[string]bool{}
	for _, in := range st.inputs {
		if mux, net, ok := in.Mux(); ok && net == tvh.ATSCNetwork && in.InUse() {
			tunedTo[mux] = true
		}
	}
	var out []MuxSignal
	add := func(freq int64, name string) *MuxSignal {
		rf := signal.RF(freq)
		ms := MuxSignal{RF: rf, FrequencyMHz: float64(freq) / 1e6, Band: fcc.Band(rf), Name: name,
			Channels: []string{}, Stations: muxStations(rep, rf), Tuned: tunedTo[name]}
		if m, ok := s.signals.Get(freq); ok {
			ms.Scan, ms.Signal, ms.History = m.Scan, m.Latest, m.History()
		}
		out = append(out, ms)
		return &out[len(out)-1]
	}
	if l != nil {
		for _, m := range l.NetworkMuxes(tvh.ATSCNetwork) {
			if m.FrequencyHz <= 0 || !bool(m.Enabled) {
				continue
			}
			ms := add(m.FrequencyHz, m.Name)
			ms.Scanning = m.Scanning()
			if c := chans[m.UUID]; c != nil {
				slices.SortFunc(c, guide.CompareNumbers)
				ms.Channels = c
			}
		}
	} else {
		for _, m := range s.signals.All() {
			add(m.FrequencyHz, fmt.Sprintf("%dMHz", m.FrequencyHz/1_000_000))
		}
	}
	slices.SortFunc(out, func(a, b MuxSignal) int { return cmp.Compare(a.FrequencyMHz, b.FrequencyMHz) })
	return out
}

// muxStations lists rep's transmitters on RF channel rf, nearest first.
func muxStations(rep *lineup.Report, rf int) []MuxStation {
	out := []MuxStation{}
	if rep == nil || rf == 0 {
		return out
	}
	for _, st := range rep.Stations {
		if st.RFChannel != rf {
			continue
		}
		ms := MuxStation{
			FacilityID: st.FacilityID, CallSign: st.CallSign, VirtualChannel: st.VirtualChannel, Service: st.Service,
			City: st.City, State: st.State, DistanceKm: st.DistanceKm, BearingDeg: st.BearingDeg, ERPkW: st.ERPkW, HAATm: st.HAATm,
		}
		for _, h := range rep.ATSC3 {
			if h.FacilityID == st.FacilityID && hostsRF(h.RF, rf) {
				ms.ATSC3 = true
			}
		}
		out = append(out, ms)
	}
	slices.SortStableFunc(out, func(a, b MuxStation) int { return cmp.Compare(a.DistanceKm, b.DistanceKm) })
	return out
}

// hostsRF reports whether an ATSC 3.0 host's RF ("34", or "2 → 3" for a
// move) is rf, now.
func hostsRF(s string, rf int) bool {
	f := strings.FieldsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if len(f) == 0 {
		return false
	}
	n, err := strconv.Atoi(f[len(f)-1])
	return err == nil && n == rf
}

// compatChannels is the tuner's lineup in the report's older Channel
// shape, for app versions from before the measured lineup, which list a
// channel only when its "tier" reads as receivable, and key favorites by
// the listings' call sign ("KWGNDT"), as that shape had it. Its tier is measured,
// not estimated, and the same for every antenna: "good" when the latest
// reading of its multiplex (or, before any, Tvheadend's scan) locked,
// "weak" when it didn't.
func compatChannels(a *Antenna) []lineup.Channel {
	scans := map[int]*signal.Scan{}
	for _, m := range a.Muxes {
		scans[m.RF] = m.Scan
	}
	out := make([]lineup.Channel, 0, len(a.Channels))
	for _, c := range a.Channels {
		t := reception.Unknown
		switch {
		case c.Signal != nil && c.Signal.Lock:
			t = reception.Good
		case c.Signal != nil:
			t = reception.Weak
		case scans[c.RF] != nil && scans[c.RF].Lock, c.Demo:
			t = reception.Good
		}
		lc := lineup.Channel{
			Number: c.Number, Major: c.Major, Minor: c.Minor, GuideID: c.GuideID, CallSign: cmp.Or(c.GuideCallSign, c.CallSign), BaseCall: c.BaseCall,
			Network: c.Network, Logo: c.Logo, FacilityID: c.FacilityID, Via: c.Via,
			Tier: map[string]reception.Tier{"indoor": t, "attic": t, "rooftop": t},
		}
		if c.NextGen != nil {
			lc.ATSC3 = &lineup.Carriage{HostCall: c.NextGen.HostCall, FacilityID: c.NextGen.FacilityID, RF: c.NextGen.RF, Display: c.NextGen.Display}
		}
		out = append(out, lc)
	}
	return out
}

// dvrChannels is the tuner's lineup for recording: number, call sign (the
// listings', as recording rules have it) and listings.
func dvrChannels(chans []lineup.TunerChannel) []lineup.Channel {
	out := make([]lineup.Channel, 0, len(chans))
	for _, c := range chans {
		out = append(out, lineup.Channel{Number: c.Number, Major: c.Major, Minor: c.Minor, GuideID: c.GuideID, CallSign: cmp.Or(c.GuideCallSign, c.CallSign), BaseCall: c.BaseCall})
	}
	return out
}

// listedOnly keeps g's channels and listings for the tuner's channels:
// listings of stations the tuner doesn't receive aren't the app's.
func listedOnly(g *guide.Guide, chans []AntennaChannel) *guide.Guide {
	if g == nil {
		return nil
	}
	ids := map[string]bool{}
	for _, c := range chans {
		if c.GuideID != "" {
			ids[c.GuideID] = true
		}
	}
	out := *g
	out.Channels = []guide.Channel{}
	out.Programs = map[string][]guide.Program{}
	for _, c := range g.Channels {
		if ids[c.ID] {
			out.Channels = append(out.Channels, c)
		}
	}
	for id, p := range g.Programs {
		if ids[id] {
			out.Programs[id] = p
		}
	}
	return &out
}

// Signal is what was measured, for the app to poll: by multiplex, and by
// channel.
func (s *Service) Signal(ctx context.Context) (*SignalReport, error) {
	if s.opt.NoAntenna {
		return nil, ErrNoAntenna
	}
	s.mu.Lock()
	rep, g := s.report, s.guide
	s.mu.Unlock()
	a := s.antenna(ctx, rep, g)
	out := &SignalReport{Tuner: a.Tuner, Channels: make([]ChannelSignal, 0, len(a.Channels)), Muxes: a.Muxes, Sweep: a.Sweep}
	for _, c := range a.Channels {
		out.Channels = append(out.Channels, ChannelSignal{Number: c.Number, RF: c.RF, Signal: c.Signal, Recent: c.Recent})
	}
	return out, nil
}
