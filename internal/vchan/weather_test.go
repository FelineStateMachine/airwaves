package vchan

import (
	"strings"
	"testing"
	"time"

	"airwaves/internal/weather"
)

func TestForecastProgram(t *testing.T) {
	base := time.Date(2026, 10, 3, 18, 0, 0, 0, time.UTC)
	rep := &weather.Report{
		Hourly: []weather.Hour{{Start: base, TempF: 71, Short: "Sunny"}, {Start: base.Add(time.Hour), TempF: 66, Short: "Clear"}},
		Periods: []weather.Period{
			{Name: "This Afternoon", Start: base.Add(-6 * time.Hour), Detailed: "Sunny, with a high near 82."},
			{Name: "Tonight", Start: base.Add(30 * time.Minute), Detailed: "Mostly clear, with a low around 52."},
		},
	}
	p := ForecastProgram(rep, base.Add(time.Hour))
	if p.Title != "Local Forecast" || p.Subtitle != "66° and Clear" || !strings.HasPrefix(p.Description, "Tonight: Mostly clear") {
		t.Fatalf("program = %+v", p)
	}
	if p.End.Sub(p.Start) != time.Hour {
		t.Errorf("block is %v, want an hour", p.End.Sub(p.Start))
	}

	rep.Alerts = []weather.Alert{{Event: "Red Flag Warning", Ends: base.Add(3 * time.Hour)}}
	if p := ForecastProgram(rep, base); !strings.HasPrefix(p.Subtitle, "Red Flag Warning. 71°") {
		t.Errorf("alert not called out: %q", p.Subtitle)
	}
	if p := ForecastProgram(nil, base); p.Title != "Local Forecast" || p.Subtitle != "" {
		t.Errorf("without a forecast: %+v", p)
	}
}
