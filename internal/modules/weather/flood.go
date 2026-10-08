package weather

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// floodDays is the window the flood view and alert cover, starting today;
	// it matches the tide bulletin's 5-day forecast.
	floodDays = 5
	// nearbyRadiusKm bounds the live gauge list around Tân Thuận. Phú An
	// (2.9 km) and Nhà Bè (6.8 km) are the gauges that matter; 30 km adds
	// the upstream Sài Gòn and Đồng Nai stations.
	nearbyRadiusKm = 30
	// heavyRainMm is the daily rain forecast that triggers an alert even at
	// low tide.
	heavyRainMm = 50
	// floodFetchTimeout covers the bulletin chain (homepage, article, PDF).
	floodFetchTimeout = 15 * time.Second
	// maxBulletinAge is how old the newest linked bulletin may be. Before the
	// day's bulletin is out the homepage still links yesterday's; anything
	// older means the site stopped publishing, and its dates no longer cover
	// the window.
	maxBulletinAge = 24 * time.Hour

	floodSourceLine = "Nguồn: Đài KTTV Nam Bộ, VNDMS, Open-Meteo"
)

// tideAlarmLevels are the báo động I/II/III thresholds, in metres, that KTTV
// sets for both Phú An and Nhà Bè. The bulletin prints them and VNDMS
// returns the same values.
var tideAlarmLevels = [alarmTiers]float64{1.40, 1.50, 1.60}

// tideTier maps a water level to its báo động tier, 0 below BĐ I.
func tideTier(height float64) int {
	tier := 0
	for i, level := range tideAlarmLevels {
		if height >= level {
			tier = i + 1
		}
	}
	return tier
}

var tierNames = [...]string{"", "BĐ I", "BĐ II", "BĐ III"}

// floodReport gathers the three sources. Each fails on its own, so the view
// can still show what it has.
type floodReport struct {
	Today       time.Time // start of today, ICT
	Bulletin    tideBulletin
	BulletinErr error
	Rain        dailyWeather
	RainErr     error
	Stations    []nearbyStation
	StationsErr error
}

// fetchFloodReport queries the tide bulletin, the Tân Thuận rain forecast and
// the live gauges concurrently.
func fetchFloodReport(ctx context.Context, client *http.Client, now time.Time) floodReport {
	r := floodReport{Today: startOfDay(now.In(ictZone))}
	var wg sync.WaitGroup
	wg.Go(func() {
		r.Bulletin, r.BulletinErr = fetchTideBulletin(ctx, client)
		if r.BulletinErr == nil && r.Today.Sub(r.Bulletin.Issued) > maxBulletinAge {
			r.BulletinErr = fmt.Errorf("tide bulletin: newest is from %s", r.Bulletin.Issued.Format("2006-01-02"))
		}
	})
	wg.Go(func() {
		var f forecast
		f, r.RainErr = fetchForecast(ctx, client, tanThuanPlace)
		r.Rain = f.Daily
	})
	wg.Go(func() {
		var all []gaugeStation
		if all, r.StationsErr = fetchGaugeStations(ctx, client); r.StationsErr == nil {
			r.Stations = stationsNear(all, tanThuanPlace, nearbyRadiusKm)
		}
	})
	wg.Wait()
	return r
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// floodDay is one day of the flood view.
type floodDay struct {
	Date    time.Time
	Peaks   []tidePeak // highest tide per bulletin station: Phú An, Nhà Bè
	HasTide bool
	Tier    int // báo động tier of the higher peak
	RainMm  float64
	HasRain bool
}

// atRisk reports whether the day should raise an alert.
func (d floodDay) atRisk() bool { return d.Tier > 0 || (d.HasRain && d.RainMm >= heavyRainMm) }

// days builds the floodDays-day window from today, joining the bulletin's
// Phú An and Nhà Bè peaks with the rain forecast by date. A day either
// source lacks is still listed, with that part missing.
func (r floodReport) days() []floodDay {
	out := make([]floodDay, floodDays)
	for i := range out {
		d := floodDay{Date: r.Today.AddDate(0, 0, i)}
		if r.BulletinErr == nil {
			d.Peaks, d.HasTide = r.Bulletin.peaksOn(d.Date)
			for _, p := range d.Peaks {
				d.Tier = max(d.Tier, tideTier(p.Height))
			}
		}
		if r.RainErr == nil {
			d.RainMm, d.HasRain = r.Rain.precipitationOn(d.Date)
		}
		out[i] = d
	}
	return out
}

// peaksOn returns the day's highest tide at Phú An and at Nhà Bè.
func (b tideBulletin) peaksOn(date time.Time) ([]tidePeak, bool) {
	var peaks []tidePeak
	for _, station := range b.Stations[:2] {
		for _, d := range station {
			if !d.Date.Equal(date) {
				continue
			}
			if p, ok := d.maxPeak(); ok {
				peaks = append(peaks, p)
			}
		}
	}
	return peaks, len(peaks) > 0
}

// precipitationOn returns the forecast rain total for date.
func (d dailyWeather) precipitationOn(date time.Time) (float64, bool) {
	key := date.Format("2006-01-02")
	for i := range min(len(d.Time), len(d.PrecipitationSum)) {
		if d.Time[i] == key {
			return d.PrecipitationSum[i], true
		}
	}
	return 0, false
}

// anySource reports whether at least one source answered.
func (r floodReport) anySource() bool {
	return r.BulletinErr == nil || r.RainErr == nil || r.StationsErr == nil
}

// formatFloodReport renders /thuyvan.
func formatFloodReport(r floodReport) string {
	var b strings.Builder
	b.WriteString("🌊 Thuỷ văn — Tân Thuận (Q.7)\n")
	switch {
	case r.BulletinErr == nil && r.RainErr != nil:
		fmt.Fprintf(&b, "Chưa lấy được dự báo mưa.\nĐỉnh triều dự báo, Phú An / Nhà Bè (bản tin %s):\n", r.Bulletin.Issued.Format("02/01"))
	case r.BulletinErr == nil:
		fmt.Fprintf(&b, "Đỉnh triều dự báo, Phú An / Nhà Bè (bản tin %s):\n", r.Bulletin.Issued.Format("02/01"))
	case r.RainErr == nil:
		b.WriteString("Chưa lấy được bản tin dự báo triều.\nMưa dự báo:\n")
	default:
		b.WriteString("Chưa lấy được dự báo triều và mưa.\n")
	}
	if r.BulletinErr == nil || r.RainErr == nil {
		for _, d := range r.days() {
			b.WriteString(formatFloodDay(d) + "\n")
		}
	}
	switch {
	case r.StationsErr != nil:
		b.WriteString("Chưa lấy được mực nước hiện tại.\n")
	case len(r.Stations) > 0:
		b.WriteString("Mực nước hiện tại:\n")
		for _, s := range r.Stations {
			b.WriteString(formatNearbyStation(s) + "\n")
		}
	}
	fmt.Fprintf(&b, "Báo động I/II/III tại Phú An, Nhà Bè: %s / %s / %s m\n",
		formatMetres(tideAlarmLevels[0]), formatMetres(tideAlarmLevels[1]), formatMetres(tideAlarmLevels[2]))
	b.WriteString(floodSourceLine)
	return b.String()
}

// formatFloodAlert renders the push for the at-risk days, or "" when no day
// is at risk.
func formatFloodAlert(r floodReport) string {
	var lines []string
	for _, d := range r.days() {
		if d.atRisk() {
			lines = append(lines, formatFloodDay(d))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "⚠️ Cảnh báo ngập — Tân Thuận (Q.7)\n" + strings.Join(lines, "\n") +
		"\nXem chi tiết: /thuyvan\n" + floodSourceLine
}

// formatFloodDay renders one day, for example
// "⚠️ 29/09: triều 1.60 / 1.58 m (17:00), mưa 62 mm — BĐ III, mưa lớn".
func formatFloodDay(d floodDay) string {
	var parts []string
	if d.HasTide {
		heights := make([]string, len(d.Peaks))
		top := d.Peaks[0]
		for i, p := range d.Peaks {
			heights[i] = formatMetres(p.Height)
			if p.Height > top.Height {
				top = p
			}
		}
		parts = append(parts, fmt.Sprintf("triều %s m (%s)", strings.Join(heights, " / "), top.Time))
	}
	if d.HasRain {
		parts = append(parts, "mưa "+decimal(d.RainMm)+" mm")
	}
	if len(parts) == 0 {
		parts = append(parts, "chưa có số liệu")
	}
	var warnings []string
	if d.Tier > 0 {
		warnings = append(warnings, tierNames[d.Tier])
	}
	if d.HasRain && d.RainMm >= heavyRainMm {
		warnings = append(warnings, "mưa lớn")
	}
	line := d.Date.Format("02/01") + ": " + strings.Join(parts, ", ")
	if len(warnings) > 0 {
		line = "⚠️ " + line + " — " + strings.Join(warnings, ", ")
	}
	return line
}

// formatNearbyStation renders one live gauge, for example
// "⚠️ Thủ Dầu Một (Sài Gòn, 26.1 km): 1.56 m lúc 7h 02/10 — BĐ II".
func formatNearbyStation(s nearbyStation) string {
	line := fmt.Sprintf("%s (%s, %s km): %s m lúc %dh %02d/%02d",
		s.Name, s.River, decimal(s.DistanceKm), formatMetres(s.Level), s.Hour, s.Day, s.Month)
	if s.Tier > 0 {
		line = "⚠️ " + line + " — " + tierNames[s.Tier]
	}
	return line
}

// formatMetres renders a water level to the centimetre with the Vietnamese
// comma separator, as KTTV publishes it.
func formatMetres(m float64) string { return strings.Replace(fmt.Sprintf("%.2f", m), ".", ",", 1) }
