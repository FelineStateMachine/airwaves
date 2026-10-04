// Package lineuptest holds a report and listings for downtown Denver, to
// match against the tuner lineup in internal/tvh/tvhtest: the stations on
// Lookout Mountain whose RF channels that tuner's scan found, and some it
// didn't (KMGH on VHF RF 7, KUSA's main channel, KWGN's ATSC 3.0 signal).
package lineuptest

import (
	"time"

	"airwaves/internal/atsc3"
	"airwaves/internal/fcc"
	"airwaves/internal/geo"
	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/reception"
)

// Point is downtown Denver.
var Point = geo.Point{Lat: 39.74, Lon: -104.99}

// lookout is the Lookout Mountain antenna farm.
var lookout = geo.Point{Lat: 39.7299, Lon: -105.2361}

func station(call string, fac, rf, virtual int, kw float64) lineup.Station {
	return lineup.Station{
		Facility: fcc.Facility{
			CallSign: call, BaseCall: fcc.BaseCall(call), Service: "DTV", RFChannel: rf, VirtualChannel: virtual, Status: "LIC",
			City: "Denver", State: "CO", FacilityID: fac, ERPkW: kw, HAATm: 330, Point: lookout, DistanceKm: 21.1, BearingDeg: 267.9,
		},
		Band:   fcc.Band(rf),
		Signal: map[string]reception.Estimate{},
	}
}

// Report is what is on record around downtown Denver, without estimates.
func Report() *lineup.Report {
	rep := &lineup.Report{
		Generated: time.Now(),
		Place:     geo.Place{ZIP: "80302", Name: "Boulder", State: "CO", Point: geo.Point{Lat: 40.04, Lon: -105.28}},
		Point:     Point,
		RadiusKm:  160,
		Stations: []lineup.Station{
			station("KMGH-TV", 40875, 7, 7, 30),
			station("KETD", 83289, 15, 53, 400),
			station("KUSA", 23074, 16, 9, 1000),
			station("KTFD-DT", 68695, 28, 50, 500),
			station("KDEN-TV", 10178, 29, 25, 475),
			station("KTVD", 68581, 31, 20, 1000),
			station("KCEC", 125, 32, 14, 450),
			station("KRMA-TV", 14040, 33, 6, 1000),
			station("KWGN-TV", 35883, 34, 2, 1000),
			station("KCNC-TV", 47903, 35, 4, 1000),
			station("KDVR", 126, 36, 31, 1000),
		},
		ATSC3: []atsc3.Host{{
			Market: "Denver, Colorado", CallSign: "KWGN-TV", FacilityID: 35883, RF: "34", Launched: "2020-07-14", Simulcast: "KDVR",
			Services: []atsc3.Service{
				{Display: "02-1", Major: 2, Minor: 1, Network: "CW", Name: "KWGN", ATSC1Call: "KDVR", ATSC1Display: "02-1"},
				{Display: "31-1", Major: 31, Minor: 1, Network: "FOX", Name: "KDVR", ATSC1Call: "KDVR", ATSC1Display: "31-1"},
				{Display: "04-1", Major: 4, Minor: 1, Network: "CBS", Name: "KCNC", ATSC1Call: "KCNC-TV", ATSC1Display: "04-1"},
			},
		}},
		Sources:  []lineup.Source{{Name: "FCC TV Query", URL: "https://www.fcc.gov/media/television/tv-query", Use: "Licensed transmitters"}},
		Warnings: []string{},
	}
	// A low-power station on KTVD's RF channel, nearer but on another
	// virtual channel: not where 20.x comes from.
	lp := station("K31AB-D", 999001, 31, 0, 3)
	lp.Service, lp.DistanceKm, lp.BearingDeg = "LPD", 8, 90
	rep.Stations = append(rep.Stations, lp)
	return rep
}

// Guide is the listings: the main channels, and some subchannels.
func Guide() *guide.Guide {
	now := time.Now().Truncate(time.Hour)
	ch := func(id, number, call, network string) guide.Channel {
		return guide.Channel{ID: id, Number: number, CallSign: call, Network: network, Logo: "https://example.com/" + id + ".png"}
	}
	g := &guide.Guide{
		Lineup: guide.LineupID("80302"), Start: now, End: now.Add(6 * time.Hour), Fetched: now,
		Channels: []guide.Channel{
			ch("g2", "2.1", "KWGNDT", "CW"),
			ch("g4", "4.1", "KCNCDT", "CBS"),
			ch("g6", "6.1", "KRMADT", "PBS"),
			ch("g7", "7.1", "KMGHDT", "ABC"),
			ch("g9", "9.1", "KUSADT", "NBC"),
			ch("g94", "9.4", "KUSADT4", "NBC"),
			ch("g14", "14.1", "KCECDT", "Univision"),
			ch("g20", "20.1", "KTVDDT", "MyNetworkTV"),
			ch("g31", "31.1", "KDVRDT", "FOX"),
		},
		Programs: map[string][]guide.Program{},
	}
	for _, c := range g.Channels {
		g.Programs[c.ID] = []guide.Program{
			{Start: now, End: now.Add(time.Hour), Title: c.CallSign + " News", ProgramID: "EP" + c.ID},
			{Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), Title: c.Network + " Movie", ProgramID: "MV" + c.ID},
		}
	}
	return g
}
