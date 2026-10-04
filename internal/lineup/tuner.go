package lineup

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"airwaves/internal/fcc"
	"airwaves/internal/guide"
	"airwaves/internal/reception"
)

// Scanned is a channel a tuner's scan found.
type Scanned struct {
	Number string // "20.1"
	// Name is the broadcast's own name for it (its PSIP short name, as
	// Tvheadend names the service): "KTVD-HD", "H & I".
	Name string
	// RF is the RF channel of the multiplex it came in on; 0 when unknown.
	RF int
}

// TunerChannel is a channel the tuner receives, with what the FCC records,
// the listings and the ATSC 3.0 list say about it. Those only describe it:
// every TunerChannel is one the tuner found.
type TunerChannel struct {
	Number string `json:"number"`
	Major  int    `json:"major"`
	Minor  int    `json:"minor"`
	// Name is the broadcast's own name for the channel (its PSIP short
	// name): "KTVD-HD", "H & I", "StartTV".
	Name string `json:"name"`
	// CallSign is the station's call sign to show, "KWGN", the same for
	// all its subchannels: from the listings, else the station whose
	// virtual channel it is, else the broadcast's name. BaseCall is the
	// same call sign as a key ("" when the name isn't a call sign).
	CallSign string `json:"callSign"`
	BaseCall string `json:"baseCall"`
	// GuideID, GuideCallSign ("KWGNDT", "KCNCDT2"), Network ("CW",
	// "Start TV") and Logo come from the listings, when they have the
	// channel.
	GuideID       string `json:"guideId,omitempty"`
	GuideCallSign string `json:"guideCallSign,omitempty"`
	Network       string `json:"network"`
	Logo          string `json:"logo,omitempty"`
	// RF is the RF channel the tuner receives it on; 0 when unknown.
	RF int `json:"rf"`
	// FacilityID and Transmitter are the licensed transmitter on RF that
	// the channel comes from: 0 and "" when none in the FCC's records
	// within the search radius is on RF.
	FacilityID  int    `json:"facilityId"`
	Transmitter string `json:"transmitter,omitempty"`
	// Via is the transmitter's call sign when it is another station's
	// (common after a station moves to ATSC 3.0 and another hosts its
	// ATSC 1.0 signal).
	Via string `json:"via,omitempty"`
	// NextGen is where the station also broadcasts this channel in ATSC
	// 3.0, a fact about the station; nil when it doesn't.
	NextGen *NextGen `json:"atsc3,omitempty"`
}

// NextGen is a channel's ATSC 3.0 broadcast.
type NextGen struct {
	HostCall   string `json:"hostCall"`
	FacilityID int    `json:"facilityId"`
	RF         string `json:"rf"`
	Display    string `json:"display"` // "02-1"
}

// nameBase is a broadcast name's call sign base, for names that are call
// signs: "KTVD-HD" and "KRMADT1" give "KTVD" and "KRMA"; "" for names that
// aren't ("H & I", "StartTV").
func nameBase(name string) string {
	b := guide.BaseCall(fcc.BaseCall(strings.TrimSpace(name)))
	if !callSign.MatchString(b) {
		return ""
	}
	return b
}

// callSign matches a US broadcast call sign's base: "KWGN", "WGN", and a
// low-power station's "K14QW".
var callSign = regexp.MustCompile(`^(?:[KW][A-Z]{2,3}|[KW]\d{2}[A-Z]{2})$`)

// Match describes the tuner's channels from rep's transmitters and ATSC 3.0
// hosts and from the listings g (nil without them), in channel order.
func Match(rep *Report, g *guide.Guide, found []Scanned) []TunerChannel {
	// What each multiplex carries decides which transmitter it is: the
	// one on its RF channel whose virtual channel or call sign is there.
	majors := map[int]map[int]bool{}
	bases := map[int]map[string]bool{}
	note := func(rf int, major int, base string) {
		if majors[rf] == nil {
			majors[rf], bases[rf] = map[int]bool{}, map[string]bool{}
		}
		majors[rf][major] = true
		if base != "" {
			bases[rf][base] = true
		}
	}
	listed := map[string][]guide.Channel{}
	if g != nil {
		for _, gc := range g.Channels {
			listed[gc.Number] = append(listed[gc.Number], gc)
		}
	}
	for _, f := range found {
		major, _ := guide.SplitNumber(f.Number)
		note(f.RF, major, nameBase(f.Name))
		for _, gc := range listed[f.Number] {
			note(f.RF, major, guide.BaseCall(gc.CallSign))
		}
	}
	tx := map[int]*Station{}
	for rf := range majors {
		var best *Station
		score := func(s *Station) int {
			n := 0
			if majors[rf][s.VirtualChannel] {
				n += 2
			}
			if bases[rf][s.BaseCall] {
				n++
			}
			return n
		}
		for i := range rep.Stations {
			s := &rep.Stations[i]
			if rf == 0 || s.RFChannel != rf {
				continue
			}
			if best == nil || score(s) > score(best) || score(s) == score(best) && s.DistanceKm < best.DistanceKm {
				best = s
			}
		}
		tx[rf] = best
	}
	// A station by its virtual channel, for channels the listings lack.
	byVirtual := map[int]*Station{}
	for i := range rep.Stations {
		s := &rep.Stations[i]
		if o := byVirtual[s.VirtualChannel]; s.VirtualChannel > 0 && (o == nil || s.DistanceKm < o.DistanceKm) {
			byVirtual[s.VirtualChannel] = s
		}
	}

	out := make([]TunerChannel, 0, len(found))
	for _, f := range found {
		major, minor := guide.SplitNumber(f.Number)
		ch := TunerChannel{Number: f.Number, Major: major, Minor: minor, Name: strings.TrimSpace(f.Name), RF: f.RF}
		t := tx[f.RF]
		if t != nil {
			ch.FacilityID, ch.Transmitter = t.FacilityID, t.CallSign
		}
		if gc, ok := pickListing(listed[f.Number], nameBase(f.Name), t); ok {
			ch.GuideID, ch.GuideCallSign, ch.BaseCall, ch.Network, ch.Logo = gc.ID, gc.CallSign, guide.BaseCall(gc.CallSign), gc.Network, gc.Logo
		} else if t != nil && t.VirtualChannel == major {
			ch.BaseCall = t.BaseCall
		} else if s := byVirtual[major]; s != nil {
			ch.BaseCall = s.BaseCall
		} else {
			ch.BaseCall = nameBase(f.Name)
		}
		ch.CallSign = cmp.Or(ch.BaseCall, ch.Name)
		if t != nil && t.BaseCall != ch.BaseCall && t.VirtualChannel != major {
			ch.Via = t.CallSign
		}
		ch.NextGen = nextGen(rep, ch, t)
		out = append(out, ch)
	}
	slices.SortStableFunc(out, func(a, b TunerChannel) int {
		return cmp.Or(cmp.Compare(a.Major, b.Major), cmp.Compare(a.Minor, b.Minor))
	})
	return out
}

// pickListing chooses among the listings' channels with one number: the
// one named like the broadcast or its transmitter, else the first.
func pickListing(cands []guide.Channel, base string, t *Station) (guide.Channel, bool) {
	if len(cands) == 0 {
		return guide.Channel{}, false
	}
	for _, gc := range cands {
		b := guide.BaseCall(gc.CallSign)
		if b == base || t != nil && b == t.BaseCall {
			return gc, true
		}
	}
	return cands[0], true
}

// nextGen finds the channel among the ATSC 3.0 hosts' services: the same
// number, from the same station.
func nextGen(rep *Report, ch TunerChannel, t *Station) *NextGen {
	for _, h := range rep.ATSC3 {
		for _, svc := range h.Services {
			if svc.Major != ch.Major || svc.Minor != ch.Minor {
				continue
			}
			same := fcc.BaseCall(svc.Name) == ch.BaseCall ||
				t != nil && svc.ATSC1Call != "" && fcc.BaseCall(svc.ATSC1Call) == t.BaseCall
			if same {
				return &NextGen{HostCall: h.CallSign, FacilityID: h.FacilityID, RF: h.RF, Display: svc.Display}
			}
		}
	}
	return nil
}

// ForTuner is rep for an app whose lineup is the tuner's: the same
// records, with no estimates (no presets, and each station's Signal
// empty), each station's Carries the channels the tuner receives from it
// and the ATSC 3.0 services it hosts, and no Channels (the caller has
// them).
func ForTuner(rep *Report, chans []TunerChannel) *Report {
	out := *rep
	out.Presets = nil
	out.Channels = []Channel{}
	carried := map[[2]int][]string{}
	for _, c := range chans {
		if c.FacilityID != 0 {
			k := [2]int{c.FacilityID, c.RF}
			carried[k] = append(carried[k], c.Number)
		}
	}
	hosted := map[int][]string{}
	for _, h := range rep.ATSC3 {
		for _, svc := range h.Services {
			hosted[h.FacilityID] = append(hosted[h.FacilityID], fmt.Sprintf("%d.%d (3.0)", svc.Major, svc.Minor))
		}
	}
	out.Stations = make([]Station, len(rep.Stations))
	for i, s := range rep.Stations {
		s.Signal = map[string]reception.Estimate{}
		s.Carries = append(slices.Clone(carried[[2]int{s.FacilityID, s.RFChannel}]), hosted[s.FacilityID]...)
		if s.Carries == nil {
			s.Carries = []string{}
		}
		out.Stations[i] = s
	}
	slices.SortStableFunc(out.Stations, func(a, b Station) int { return cmp.Compare(a.DistanceKm, b.DistanceKm) })
	return &out
}
