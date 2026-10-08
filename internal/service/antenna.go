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
	"airwaves/internal/hdhr"
	"airwaves/internal/lineup"
	"airwaves/internal/signal"
	"airwaves/internal/tuner"
)

// ErrNoTuner is returned for an antenna channel by a server that has found
// no HDHomeRun to tune it with.
var ErrNoTuner = tuner.ErrNoDevice

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
	// Ready is set when there is a tuner.
	Ready bool `json:"ready"`
	// Reason says why there are no antenna channels, when there aren't.
	Reason string `json:"reason,omitempty"`
	// Name and Model are the tuner's, "HDHomeRun FLEX DUO" and "HDFX-2US".
	Name   string `json:"name,omitempty"`
	Model  string `json:"model,omitempty"`
	Tuners int    `json:"tuners"`
	// InUse counts tuners tuned now: to a viewer, a recording or a
	// measurement.
	InUse int `json:"inUse"`
	// Standards lists what the tuners receive; ATSC3 is whether that takes
	// in ATSC 3.0 (NextGen TV).
	Standards []string `json:"standards"`
	ATSC3     bool     `json:"atsc3"`
	// Scanning is set while the tuner scans for channels.
	Scanning bool `json:"scanning"`
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
}

// MuxSignal is one RF channel: the tuner's channels on it, what the FCC
// licenses on it nearby, and what the tuners measured on it.
type MuxSignal struct {
	RF           int     `json:"rf"`
	FrequencyMHz float64 `json:"frequencyMhz"`
	Band         string  `json:"band"`
	// Tuned is set while a tuner is on it.
	Tuned bool `json:"tuned"`
	// Channels are the tuner's channels on it: what its scan found there.
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

// device is the HDHomeRun, nil until one is found.
func (s *Service) device() *hdhr.Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dev
}

// findTuner looks for the HDHomeRun until one is found, then keeps its
// details current, looking again when it stops answering.
func (s *Service) findTuner(ctx context.Context) {
	if s.tuners == nil {
		return
	}
	if dev := s.device(); dev != nil {
		fresh := *dev
		if err := fresh.Refresh(ctx); err == nil {
			s.setDevice(&fresh)
			return
		}
		log.Printf("tuner: %s stopped answering; looking for it again", dev.FriendlyName)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dev, err := hdhr.Find(ctx, s.opt.HDHomeRun, s.opt.SkipDeviceID, s.http)
	if err != nil {
		s.mu.Lock()
		changed := s.devErr != err.Error()
		s.devErr = err.Error()
		s.mu.Unlock()
		if changed {
			log.Printf("tuner: %v", err)
		}
		s.setDevice(nil)
		return
	}
	if s.device() == nil {
		log.Printf("tuner: %s (%s, %d tuners, firmware %s) at %s", dev.FriendlyName, dev.Model, dev.Tuners, dev.Version, dev.BaseURL)
	}
	s.setDevice(dev)
}

// setDevice makes dev the tuner to use; nil for none.
func (s *Service) setDevice(dev *hdhr.Device) {
	s.mu.Lock()
	same := dev != nil && s.dev != nil && dev.ID == s.dev.ID && dev.BaseURL == s.dev.BaseURL && dev.Tuners == s.dev.Tuners
	if dev == nil && s.dev == nil || same {
		if same {
			s.dev = dev
		}
		s.mu.Unlock()
		return
	}
	s.dev, s.devErr = dev, ""
	s.lineupAt = time.Time{}
	s.mu.Unlock()
	if dev == nil {
		s.tuners.SetDevice(nil, 0)
		return
	}
	s.tuners.SetDevice(dev, dev.Tuners)
}

// lineupTTL is how long the tuner's lineup is used before asking again.
const lineupTTL = time.Minute

// tunerLineup is the channels the tuner's scan found that it can show:
// not ATSC 3.0 ones, whose audio can't be decoded, nor encrypted ones, by
// number (of two with one number, the first stays). A failure keeps the
// last lineup.
func (s *Service) tunerLineup(ctx context.Context) ([]hdhr.Channel, error) {
	dev := s.device()
	if dev == nil {
		return nil, ErrNoTuner
	}
	s.mu.Lock()
	l, at := s.lineup, s.lineupAt
	s.mu.Unlock()
	if !at.IsZero() && time.Since(at) < lineupTTL {
		return l, nil
	}
	got, err := dev.Lineup(ctx)
	if err != nil {
		if !at.IsZero() {
			return l, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	l = got[:0]
	for _, c := range got {
		if c.ATSC3 || c.DRM || c.FrequencyHz <= 0 || c.Program <= 0 || seen[c.Number] {
			continue
		}
		seen[c.Number] = true
		l = append(l, c)
	}
	slices.SortStableFunc(l, func(a, b hdhr.Channel) int { return guide.CompareNumbers(a.Number, b.Number) })
	s.mu.Lock()
	s.lineup, s.lineupAt = l, time.Now()
	s.mu.Unlock()
	return l, nil
}

// tuning finds where a channel is broadcast: its RF channel and program.
func (s *Service) tuning(ctx context.Context, number string) (int64, int, error) {
	l, err := s.tunerLineup(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, c := range l {
		if c.Number == number {
			return c.FrequencyHz, c.Program, nil
		}
	}
	return 0, 0, fmt.Errorf("channel %s isn't in the tuner's lineup", number)
}

// scanStatus is the tuner's channel scan, read at most every few seconds.
func (s *Service) scanStatus(ctx context.Context) hdhr.ScanStatus {
	dev := s.device()
	if dev == nil {
		return hdhr.ScanStatus{}
	}
	s.mu.Lock()
	st, at := s.scan, s.scanAt
	s.mu.Unlock()
	if !at.IsZero() && time.Since(at) < 5*time.Second {
		return st
	}
	st, err := dev.ScanStatus(ctx)
	if err != nil {
		return hdhr.ScanStatus{}
	}
	s.mu.Lock()
	if s.scan.InProgress && !st.InProgress {
		s.lineupAt = time.Time{} // a scan just finished: read its lineup
	}
	s.scan, s.scanAt = st, time.Now()
	s.mu.Unlock()
	return st
}

// ScanChannels has the tuner scan for channels again, as after moving the
// antenna. Every tuner is taken for the few minutes it takes, ending
// whatever was watched or measured; recordings in progress make it wait.
func (s *Service) ScanChannels(ctx context.Context) (TunerInfo, error) {
	if s.opt.NoAntenna {
		return TunerInfo{}, ErrNoAntenna
	}
	dev := s.device()
	if dev == nil {
		return TunerInfo{}, ErrNoTuner
	}
	if busy := s.recordingNow(ctx); busy != "" {
		return TunerInfo{}, fmt.Errorf("not while recording %s: a scan takes every tuner", busy)
	}
	s.tuners.SetDevice(nil, 0) // let go of every tuner
	err := dev.StartScan(ctx)
	s.tuners.SetDevice(dev, dev.Tuners)
	if err != nil {
		return TunerInfo{}, err
	}
	log.Print("tuner: scanning for channels")
	s.mu.Lock()
	s.scanAt = time.Time{}
	s.mu.Unlock()
	return s.tunerInfo(ctx), nil
}

// recordingNow names a recording in progress, "" when there's none.
func (s *Service) recordingNow(ctx context.Context) string {
	tuners, err := s.tuners.Tuners(ctx)
	if err != nil {
		return ""
	}
	for _, t := range tuners {
		for _, u := range t.Users {
			if u.Priority == tuner.Recording {
				return u.Who
			}
		}
	}
	return ""
}

// tunerDevice describes the tuner for Info.
func (s *Service) tunerDevice() tuner.Device {
	if s.opt.NoAntenna {
		return tuner.Device{ID: "none", Name: "No antenna", Kind: "none", Detail: "custom channels only"}
	}
	dev := s.device()
	if dev == nil {
		return tuner.Device{ID: "none", Name: "No tuner", Kind: "none", Detail: "no HDHomeRun found on the network"}
	}
	return tuner.Device{
		ID: dev.ID, Name: dev.FriendlyName, Kind: "hdhomerun", Model: dev.Model, Tuners: dev.Tuners,
		Detail: "firmware " + dev.Version, Standards: standards(dev), ATSC3: dev.ATSC3(),
	}
}

// standards lists what dev's tuners receive.
func standards(dev *hdhr.Device) []string {
	if dev.ATSC3() {
		return []string{"ATSC 1.0", "ATSC 3.0"}
	}
	return []string{"ATSC 1.0"}
}

// tunerInfo describes the tuners and what's tuned now.
func (s *Service) tunerInfo(ctx context.Context) TunerInfo {
	if s.opt.NoAntenna {
		return TunerInfo{Reason: ErrNoAntenna.Error(), Standards: []string{}}
	}
	dev := s.device()
	if dev == nil {
		reason := ErrNoTuner.Error()
		s.mu.Lock()
		if s.devErr != "" && s.opt.HDHomeRun != "" {
			reason = "no tuner: " + s.devErr
		}
		s.mu.Unlock()
		return TunerInfo{Reason: reason, Standards: []string{}}
	}
	ti := TunerInfo{Ready: true, Name: dev.FriendlyName, Model: dev.Model, Tuners: dev.Tuners, Standards: standards(dev), ATSC3: dev.ATSC3()}
	if tuners, err := s.tuners.Tuners(ctx); err == nil {
		for _, t := range tuners {
			if t.FrequencyHz > 0 {
				ti.InUse++
			}
		}
	}
	ti.Scanning = s.scanStatus(ctx).InProgress
	if ti.Scanning {
		ti.Reason = "the tuner is scanning for channels"
	}
	return ti
}

// antenna is the tuner's lineup described from rep and g, with the
// measurements; rep and g may be nil.
func (s *Service) antenna(ctx context.Context, rep *lineup.Report, g *guide.Guide) *Antenna {
	a := &Antenna{Channels: []AntennaChannel{}, Muxes: []MuxSignal{}, Sweep: s.sweepStatus(), Tuner: s.tunerInfo(ctx)}
	if s.opt.NoAntenna {
		return a
	}
	chans, found, err := s.matched(ctx, rep, g)
	if err != nil {
		log.Printf("antenna channels: %v", err)
		if a.Tuner.Reason == "" {
			a.Tuner.Reason = "the tuner's lineup can't be read: " + err.Error()
		}
	}
	onRF := map[int64][]string{} // channel numbers by frequency
	for i, c := range chans {
		ac := AntennaChannel{TunerChannel: c}
		if m, ok := s.signals.Get(found[i].FrequencyHz); ok {
			ac.Signal, ac.Recent = m.Latest, m.Recent()
		}
		a.Channels = append(a.Channels, ac)
		onRF[found[i].FrequencyHz] = append(onRF[found[i].FrequencyHz], c.Number)
	}
	a.Muxes = s.muxSignals(ctx, rep, onRF)
	return a
}

// matched is the tuner's channels described from rep and g, with the
// tuner's own entry for each, in the same order.
func (s *Service) matched(ctx context.Context, rep *lineup.Report, g *guide.Guide) ([]lineup.TunerChannel, []hdhr.Channel, error) {
	if s.tuners == nil {
		return nil, nil, nil
	}
	l, err := s.tunerLineup(ctx)
	if errors.Is(err, ErrNoTuner) {
		return nil, nil, nil // no tuner, no channels
	}
	if err != nil {
		return nil, nil, err
	}
	found := make([]lineup.Scanned, len(l))
	for i, c := range l {
		found[i] = lineup.Scanned{Number: c.Number, Name: c.Name, RF: signal.RF(c.FrequencyHz)}
	}
	if rep == nil {
		rep = &lineup.Report{}
	}
	chans := lineup.Match(rep, g, found)
	byNumber := make(map[string]hdhr.Channel, len(l))
	for _, c := range l {
		byNumber[c.Number] = c
	}
	same := make([]hdhr.Channel, len(chans))
	for i, c := range chans {
		same[i] = byNumber[c.Number]
	}
	return chans, same, nil
}

// muxSignals lists the RF channels worth showing: those the tuner's scan
// found channels on (onRF, by frequency), those a licensed station nearby
// uses, and any measured before.
func (s *Service) muxSignals(ctx context.Context, rep *lineup.Report, onRF map[int64][]string) []MuxSignal {
	tuned := map[int64]bool{}
	if tuners, err := s.tuners.Tuners(ctx); err == nil {
		for _, t := range tuners {
			tuned[t.FrequencyHz] = t.FrequencyHz > 0
		}
	}
	freqs := map[int64]bool{}
	for f := range onRF {
		freqs[f] = true
	}
	if rep != nil {
		for _, st := range rep.Stations {
			if f := signal.Frequency(st.RFChannel); f > 0 {
				freqs[f] = true
			}
		}
	}
	for _, m := range s.signals.All() {
		freqs[m.FrequencyHz] = true
	}
	out := make([]MuxSignal, 0, len(freqs))
	for f := range freqs {
		rf := signal.RF(f)
		if rf == 0 {
			continue
		}
		ms := MuxSignal{RF: rf, FrequencyMHz: float64(f) / 1e6, Band: fcc.Band(rf), Tuned: tuned[f],
			Channels: []string{}, Stations: muxStations(rep, rf)}
		if c := onRF[f]; c != nil {
			ms.Channels = slices.SortedFunc(slices.Values(c), guide.CompareNumbers)
		}
		if m, ok := s.signals.Get(f); ok {
			ms.Signal, ms.History = m.Latest, m.History()
		}
		out = append(out, ms)
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
