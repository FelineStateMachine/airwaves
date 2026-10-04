// Command otascan reports which over-the-air TV channels are likely
// receivable at a US ZIP code, with ATSC 3.0 status and what is on now.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"airwaves/internal/geo"
	"airwaves/internal/guide"
	"airwaves/internal/lineup"
	"airwaves/internal/reception"
	"airwaves/internal/store"
	"airwaves/internal/terrain"
	"airwaves/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "otascan:", err)
		os.Exit(1)
	}
}

func run() error {
	zip := flag.String("zip", "", "US ZIP code (required)")
	lat := flag.Float64("lat", 0, "latitude override (use with -lon)")
	lon := flag.Float64("lon", 0, "longitude override (use with -lat)")
	radius := flag.Float64("radius", 160, "transmitter search radius in km")
	antenna := flag.String("antenna", "rooftop", "antenna preset: indoor, attic, rooftop")
	all := flag.Bool("all", false, "include channels unlikely to be received")
	asJSON := flag.Bool("json", false, "print the full report as JSON")
	refresh := flag.Bool("refresh", false, "ignore cached data")
	flag.Parse()
	if *zip == "" {
		return errors.New("give the antenna's ZIP code with -zip")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cache, err := store.NewCache("airwaves")
	if err != nil {
		return err
	}
	client := web.NewClient()
	b := &lineup.Builder{
		HTTP:     client,
		Cache:    cache,
		Terrain:  terrain.NewSource(client, filepath.Join(cache.Dir(), "terrain")),
		Progress: func(s string) { fmt.Fprintln(os.Stderr, "-", s) },
	}
	opt := lineup.Options{ZIP: *zip, RadiusKm: *radius, GuideHours: 6, Refresh: *refresh}
	if *lat != 0 && *lon != 0 {
		opt.Point = &geo.Point{Lat: *lat, Lon: *lon}
	}
	rep, g, err := b.Build(ctx, opt)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printReport(rep, g, *antenna, *all)
	return nil
}

func printReport(rep *lineup.Report, g *guide.Guide, antenna string, all bool) {
	fmt.Printf("\nOver-the-air TV near %s %s, %s (%.4f, %.4f)\n", rep.Place.ZIP, rep.Place.Name, rep.Place.State, rep.Point.Lat, rep.Point.Lon)
	fmt.Printf("Antenna preset for the channel list: %s\n", antenna)
	for _, w := range rep.Warnings {
		fmt.Println("warning:", w)
	}

	byID := make(map[int]lineup.Station)
	for _, s := range rep.Stations {
		if _, ok := byID[s.FacilityID]; !ok {
			byID[s.FacilityID] = s
		}
	}

	fmt.Println("\nTRANSMITTERS (best first; margin in dB for indoor / attic / rooftop)")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CALL\tRF\tBAND\tVIRT\tCITY\tKM\tDIR\tERP kW\tLOS\tINDOOR\tATTIC\tROOF\t3.0\tCARRIES")
	for _, s := range rep.Stations {
		roof := s.Signal["rooftop"]
		if !all && !roof.Tier.Receivable() && roof.Tier != reception.Weak {
			continue
		}
		atsc := ""
		if s.ATSC3 {
			atsc = "yes"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%s\t%.0f\t%s\t%.1f\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.CallSign, s.RFChannel, s.Band, s.VirtualChannel, s.City, s.DistanceKm, compass(s.BearingDeg), s.ERPkW,
			yesNo(roof.LineOfSight), margin(s.Signal["indoor"]), margin(s.Signal["attic"]), margin(roof), atsc,
			strings.Join(s.Carries, " "))
	}
	tw.Flush()

	if len(rep.ATSC3) > 0 {
		fmt.Println("\nATSC 3.0 / NEXTGEN TV")
		for _, h := range rep.ATSC3 {
			s := byID[h.FacilityID]
			fmt.Printf("  %s RF %s (%s market, launched %s, rooftop %s)\n", h.CallSign, h.RF, h.Market, orDash(h.Launched), margin(s.Signal["rooftop"]))
			for _, svc := range h.Services {
				src := ""
				if svc.ATSC1Call != "" {
					src = fmt.Sprintf("  1.0 simulcast on %s %s", svc.ATSC1Call, svc.ATSC1Display)
				}
				fmt.Printf("    %-6s %-10s %-10s%s\n", svc.Display, svc.Network, svc.Name, src)
			}
		}
	}

	now := time.Now()
	fmt.Printf("\nCHANNELS (%s)\n", antenna)
	tw = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CH\tCALL\tNETWORK\tSIGNAL\tVIA\t3.0\tON NOW")
	shown := 0
	for _, ch := range rep.Channels {
		t := ch.Tier[antenna]
		if !all && !t.Receivable() {
			continue
		}
		shown++
		atsc := ""
		switch {
		case ch.ATSC3Only:
			atsc = "only"
		case ch.ATSC3 != nil:
			atsc = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", ch.Number, ch.CallSign, ch.Network, t, ch.Via, atsc, onNow(g, ch.GuideID, now))
	}
	tw.Flush()
	fmt.Printf("\n%d of %d lineup channels shown. Estimates use FCC data and terrain only; local obstructions and multipath are not modeled.\n", shown, len(rep.Channels))
}

func onNow(g *guide.Guide, id string, now time.Time) string {
	if g == nil || id == "" {
		return ""
	}
	for _, p := range g.Programs[id] {
		if !p.Start.After(now) && p.End.After(now) {
			if len(p.Title) > 40 {
				return p.Title[:39] + "…"
			}
			return p.Title
		}
	}
	return ""
}

func margin(e reception.Estimate) string {
	if e.Tier == reception.Unknown || e.Tier == "" {
		return "?"
	}
	return fmt.Sprintf("%+.0f", e.NoiseMarginDB)
}

func compass(deg float64) string {
	dirs := []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}
	return dirs[int((deg+11.25)/22.5)%16]
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
