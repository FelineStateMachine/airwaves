package lineuptest

import (
	"airwaves/internal/hdhr"
	"airwaves/internal/ts/tstest"
)

// Lineup is what an HDHomeRun's scan found around downtown Denver: 38
// channels on 8 RF channels, among them KWGN's 2.1 on KDVR's, and KUSA's
// 9.4 on KTVD's.
func Lineup() []hdhr.Channel {
	return []hdhr.Channel{
		{Number: "53.1", Name: "KETD-HD", FrequencyHz: 479_000_000, Program: 1, TSID: 447, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "53.2", Name: "E-NEWS", FrequencyHz: 479_000_000, Program: 2, TSID: 447, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "53.3", Name: "Confess", FrequencyHz: 479_000_000, Program: 3, TSID: 447, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "53.4", Name: "JTV", FrequencyHz: 479_000_000, Program: 4, TSID: 447, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "53.5", Name: "BUZZR", FrequencyHz: 479_000_000, Program: 5, TSID: 447, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "50.1", Name: "UniMas", FrequencyHz: 557_000_000, Program: 1, TSID: 471, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "50.2", Name: "LATV", FrequencyHz: 557_000_000, Program: 2, TSID: 471, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "25.1", Name: "KDEN-DT", FrequencyHz: 563_000_000, Program: 3, TSID: 491, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "25.2", Name: "Exitos", FrequencyHz: 563_000_000, Program: 4, TSID: 491, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "25.3", Name: "COZI", FrequencyHz: 563_000_000, Program: 5, TSID: 491, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "25.4", Name: "CRIMES", FrequencyHz: 563_000_000, Program: 6, TSID: 491, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "25.5", Name: "Oxygen", FrequencyHz: 563_000_000, Program: 7, TSID: 491, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "25.6", Name: "COZI 2", FrequencyHz: 563_000_000, Program: 8, TSID: 491, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "20.1", Name: "KTVD-HD", FrequencyHz: 575_000_000, Program: 3, TSID: 465, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "20.2", Name: "H & I", FrequencyHz: 575_000_000, Program: 4, TSID: 465, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "9.4", Name: "KUSA-HD", FrequencyHz: 575_000_000, Program: 5, TSID: 465, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "9.7", Name: "TBD", FrequencyHz: 575_000_000, Program: 6, TSID: 465, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "20.3", Name: "ShopLC", FrequencyHz: 575_000_000, Program: 7, TSID: 465, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "20.4", Name: "OUTLAW", FrequencyHz: 575_000_000, Program: 8, TSID: 465, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "14.1", Name: "Univision Denver", FrequencyHz: 581_000_000, Program: 1, TSID: 443, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "14.2", Name: "MovieSphere Gold by Lionsgate", FrequencyHz: 581_000_000, Program: 2, TSID: 443, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "14.3", Name: "GREAT", FrequencyHz: 581_000_000, Program: 3, TSID: 443, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "14.4", Name: "Better Tomorrows, Today", FrequencyHz: 581_000_000, Program: 4, TSID: 443, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "14.5", Name: "ShopLC", FrequencyHz: 581_000_000, Program: 5, TSID: 443, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "6.1", Name: "KRMADT1", FrequencyHz: 587_000_000, Program: 1, TSID: 459, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "6.2", Name: "PBSKIDS", FrequencyHz: 587_000_000, Program: 2, TSID: 459, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "6.3", Name: "Create", FrequencyHz: 587_000_000, Program: 3, TSID: 459, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "6.4", Name: "World", FrequencyHz: 587_000_000, Program: 4, TSID: 459, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "4.1", Name: "KCNC-TV", FrequencyHz: 599_000_000, Program: 1, TSID: 457, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "4.2", Name: "StartTV", FrequencyHz: 599_000_000, Program: 2, TSID: 457, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "4.3", Name: "Dabl", FrequencyHz: 599_000_000, Program: 3, TSID: 457, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "4.4", Name: "MeTV", FrequencyHz: 599_000_000, Program: 4, TSID: 457, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "4.5", Name: "Catchy", FrequencyHz: 599_000_000, Program: 5, TSID: 457, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "4.6", Name: "Story", FrequencyHz: 599_000_000, Program: 6, TSID: 457, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "31.1", Name: "KDVR-DT", FrequencyHz: 605_000_000, Program: 3, TSID: 467, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "31.2", Name: "Antenna", FrequencyHz: 605_000_000, Program: 4, TSID: 467, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "31.3", Name: "ROAR-TV", FrequencyHz: 605_000_000, Program: 5, TSID: 467, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
		{Number: "2.1", Name: "KWGN-DT", FrequencyHz: 605_000_000, Program: 6, TSID: 467, Modulation: "8vsb", VideoCodec: "MPEG2", AudioCodec: "AC3"},
	}
}

// Muxes is what each RF channel of the lineup carries, for a fake
// HDHomeRun: its programs, each with an English audio track (and a
// Spanish one on 50.1, and a described video track on 4.1).
func Muxes() map[int64][]byte {
	byRF := map[int64]*tstest.Mux{}
	for _, c := range Lineup() {
		m := byRF[c.FrequencyHz]
		if m == nil {
			m = &tstest.Mux{TSID: c.TSID}
			byRF[c.FrequencyHz] = m
		}
		pid := 0x30 + 0x10*len(m.Programs)
		p := tstest.Program{Number: c.Program, PMTPID: pid, VideoPID: pid + 1, Audio: []tstest.Audio{{PID: pid + 4, Lang: "eng"}}, Name: c.Name}
		switch c.Number {
		case "50.1":
			p.Audio = append(p.Audio, tstest.Audio{PID: pid + 5, Lang: "spa"})
		case "4.1":
			p.Audio = append(p.Audio, tstest.Audio{PID: pid + 5, Lang: "eng", Described: true})
		}
		m.Programs = append(m.Programs, p)
	}
	out := map[int64][]byte{}
	for f, m := range byRF {
		out[f] = m.Packets(60 * len(m.Programs))
	}
	return out
}
