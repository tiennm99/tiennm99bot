package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// forecastFixture mirrors a live Open-Meteo response for Ho Chi Minh City.
const forecastFixture = `{
	"current":{"time":"2026-10-01T10:30","temperature_2m":29.0,"apparent_temperature":35.7,"relative_humidity_2m":78,"weather_code":3,"wind_speed_10m":3.2},
	"hourly":{
		"time":["2026-10-01T10:00","2026-10-01T11:00","2026-10-01T12:00","2026-10-01T13:00","2026-10-01T14:00","2026-10-01T15:00","2026-10-01T16:00"],
		"temperature_2m":[30.4,31.4,32.1,32.1,32.2,32.7,31.9],
		"apparent_temperature":[38.1,40.2,41.3,40.9,40.2,38.2,36.6],
		"weather_code":[3,3,3,3,51,1,2],
		"precipitation_probability":[2,2,4,10,23,41,55],
		"precipitation":[0.00,0.00,0.00,0.00,0.10,0.00,0.00],
		"relative_humidity_2m":[73,71,68,68,64,57,60],
		"wind_speed_10m":[3.0,2.4,1.6,1.1,1.4,2.8,5.7]
	},
	"daily":{
		"time":["2026-10-01","2026-10-02","2026-10-03","2026-10-04","2026-10-05","2026-10-06","2026-10-07"],
		"weather_code":[80,80,81,95,95,95,53],
		"temperature_2m_max":[32.7,31.8,32.0,32.2,32.5,31.1,31.1],
		"temperature_2m_min":[24.3,23.9,24.2,23.7,24.5,24.0,23.9],
		"precipitation_sum":[5.30,7.90,7.00,5.70,13.90,4.00,7.60],
		"precipitation_probability_max":[70,88,85,92,98,100,89],
		"uv_index_max":[8.90,8.20,8.60,8.40,8.85,7.80,8.70],
		"sunrise":["2026-10-01T05:42","2026-10-02T05:42","2026-10-03T05:42","2026-10-04T05:42","2026-10-05T05:42","2026-10-06T05:42","2026-10-07T05:42"],
		"sunset":["2026-10-01T17:44","2026-10-02T17:43","2026-10-03T17:42","2026-10-04T17:42","2026-10-05T17:41","2026-10-06T17:40","2026-10-07T17:40"]
	}
}`

const daLatGeocodeFixture = `{"results":[
	{"name":"Ðà Lạt","latitude":11.94646,"longitude":108.44193,"country_code":"VN","country":"Việt Nam","admin1":"Lam Dong"}
]}`

// fakeOpenMeteo serves both Open-Meteo endpoints for the duration of t and
// records the geocoding queries it received.
type fakeOpenMeteo struct {
	geocodeBody    string
	forecastBody   string
	forecastStatus int
	geocodeQueries []string
	forecastCalls  atomic.Int32
	lastForecastQ  map[string]string
}

func stubOpenMeteo(t *testing.T, f *fakeOpenMeteo) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			f.geocodeQueries = append(f.geocodeQueries, r.URL.Query().Get("name"))
			_, _ = w.Write([]byte(f.geocodeBody))
		case "/forecast":
			f.forecastCalls.Add(1)
			f.lastForecastQ = map[string]string{}
			for k := range r.URL.Query() {
				f.lastForecastQ[k] = r.URL.Query().Get(k)
			}
			if f.forecastStatus != 0 {
				http.Error(w, "unavailable", f.forecastStatus)
				return
			}
			_, _ = w.Write([]byte(f.forecastBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	origGeocode, origForecast := geocodeURL, forecastURL
	geocodeURL, forecastURL = server.URL+"/search", server.URL+"/forecast"
	t.Cleanup(func() { geocodeURL, forecastURL = origGeocode, origForecast })
}

func installWeather(t *testing.T) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	mod := New(modules.Deps{Store: storage.NewMemoryProvider().Collection(CollectionName)})
	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: CollectionName, Commands: mod.Commands}},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range mod.Commands {
		reg.AllCommands[c.Name] = c
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

func send(rb *testutil.RecordingBot, text string) string {
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, text))
	return rb.LastSent().Text()
}

func TestCommands_RegistrationAndParameters(t *testing.T) {
	mod := New(modules.Deps{Store: storage.NewMemoryProvider().Collection(CollectionName)})
	want := []struct{ name, parameters string }{
		{"thoitiet", "[location... | lat,long]"},
		{"thoitiethomnay", "[location... | lat,long]"},
		{"thoitietngaymai", "[location... | lat,long]"},
		{"thoitiettuannay", "[location... | lat,long]"},
		{"thuyvan", ""},
		{"thuyvan_subscribe", ""},
		{"thuyvan_unsubscribe", ""},
	}
	if len(mod.Commands) != len(want) {
		t.Fatalf("commands = %d, want %d", len(mod.Commands), len(want))
	}
	for i, c := range mod.Commands {
		if c.Name != want[i].name {
			t.Errorf("commands[%d] = %q, want %q", i, c.Name, want[i].name)
		}
		if c.Parameters != want[i].parameters {
			t.Errorf("/%s parameters = %q, want %q", c.Name, c.Parameters, want[i].parameters)
		}
		if c.Visibility != modules.VisibilityPublic {
			t.Errorf("/%s is not public", c.Name)
		}
	}
	if len(mod.Crons) != 2 || mod.Crons[0].Schedule != "30 3 * * *" || mod.Crons[1].Schedule != "30 5 * * *" {
		t.Errorf("crons = %+v, want the 10:30 ICT flood push and its 12:30 retry", mod.Crons)
	}
}

func TestToday_DefaultsToTanThuanWithoutGeocoding(t *testing.T) {
	f := &fakeOpenMeteo{forecastBody: forecastFixture}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	want := "🌦️ Thời tiết hôm nay 01/10 — Tân Thuận, Thành phố Hồ Chí Minh\n" +
		"Hiện tại: 29°C (cảm giác 36°C), Nhiều mây ☁️\n" +
		"Độ ẩm 78%, gió 3,2 km/h\n" +
		"Cả ngày: 24–33°C, Mưa rào nhẹ 🌦️\n" +
		"Khả năng mưa 70% (5,3 mm), UV 8,9\n" +
		"Mặt trời mọc 05:42, lặn 17:44\n" +
		"Nguồn: Open-Meteo"
	if got := send(rb, "/thoitiethomnay"); got != want {
		t.Errorf("reply =\n%s\nwant\n%s", got, want)
	}
	if len(f.geocodeQueries) != 0 {
		t.Errorf("geocode called with %v, want no calls", f.geocodeQueries)
	}
	if f.lastForecastQ["latitude"] != "10.74111" || f.lastForecastQ["longitude"] != "106.71806" ||
		f.lastForecastQ["timezone"] != "auto" ||
		f.lastForecastQ["forecast_days"] != "7" || f.lastForecastQ["forecast_hours"] != "7" {
		t.Errorf("forecast query = %v", f.lastForecastQ)
	}
}

func TestToday_HCMAliasSkipsGeocoding(t *testing.T) {
	f := &fakeOpenMeteo{forecastBody: forecastFixture}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	for _, cmd := range []string{"/thoitiethomnay hcm", "/thoitiethomnay Sài Gòn"} {
		got := send(rb, cmd)
		if !strings.HasPrefix(got, "🌦️ Thời tiết hôm nay 01/10 — Thành phố Hồ Chí Minh\n") {
			t.Errorf("%s reply header = %q", cmd, strings.SplitN(got, "\n", 2)[0])
		}
		if f.lastForecastQ["latitude"] != "10.82302" {
			t.Errorf("%s forecast latitude = %q", cmd, f.lastForecastQ["latitude"])
		}
	}
	if len(f.geocodeQueries) != 0 {
		t.Errorf("geocode called with %v, want no calls", f.geocodeQueries)
	}
}

func TestCoordinatesSkipGeocoding(t *testing.T) {
	f := &fakeOpenMeteo{forecastBody: forecastFixture}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	got := send(rb, "/thoitiet 10.7769, 106.7009")
	if !strings.HasPrefix(got, "🕐 Thời tiết 6 giờ tới — 10.7769, 106.7009\n") {
		t.Errorf("reply header = %q", strings.SplitN(got, "\n", 2)[0])
	}
	if f.lastForecastQ["latitude"] != "10.7769" || f.lastForecastQ["longitude"] != "106.7009" {
		t.Errorf("forecast query = %v", f.lastForecastQ)
	}
	if len(f.geocodeQueries) != 0 {
		t.Errorf("geocode called with %v, want no calls", f.geocodeQueries)
	}
}

func TestHourly_ListsNextSixHours(t *testing.T) {
	f := &fakeOpenMeteo{forecastBody: forecastFixture}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	want := "🕐 Thời tiết 6 giờ tới — Tân Thuận, Thành phố Hồ Chí Minh\n" +
		"Hiện tại 10:30: 29°C (cảm giác 36°C), Nhiều mây ☁️\n" +
		"\n11:00 ☁️ Nhiều mây, 31°C (cảm giác 40°C)\n" +
		"Mưa 2% (0,0 mm), độ ẩm 71%, gió 2,4 km/h\n" +
		"\n12:00 ☁️ Nhiều mây, 32°C (cảm giác 41°C)\n" +
		"Mưa 4% (0,0 mm), độ ẩm 68%, gió 1,6 km/h\n" +
		"\n13:00 ☁️ Nhiều mây, 32°C (cảm giác 41°C)\n" +
		"Mưa 10% (0,0 mm), độ ẩm 68%, gió 1,1 km/h\n" +
		"\n14:00 🌦️ Mưa phùn nhẹ, 32°C (cảm giác 40°C)\n" +
		"Mưa 23% (0,1 mm), độ ẩm 64%, gió 1,4 km/h\n" +
		"\n15:00 🌤️ Ít mây, 33°C (cảm giác 38°C)\n" +
		"Mưa 41% (0,0 mm), độ ẩm 57%, gió 2,8 km/h\n" +
		"\n16:00 ⛅ Mây rải rác, 32°C (cảm giác 37°C)\n" +
		"Mưa 55% (0,0 mm), độ ẩm 60%, gió 5,7 km/h\n" +
		"\nNguồn: Open-Meteo"
	if got := send(rb, "/thoitiet"); got != want {
		t.Errorf("reply =\n%s\nwant\n%s", got, want)
	}
}

func TestHourly_NoUpcomingHoursRepliesError(t *testing.T) {
	stale := strings.Replace(forecastFixture, `"time":"2026-10-01T10:30"`, `"time":"2026-10-01T16:30"`, 1)
	stubOpenMeteo(t, &fakeOpenMeteo{forecastBody: stale})
	rb := installWeather(t)

	if got := send(rb, "/thoitiet"); got != fetchErrorText {
		t.Errorf("reply = %q, want %q", got, fetchErrorText)
	}
}

func TestTomorrow_GeocodesDiacriticLocation(t *testing.T) {
	f := &fakeOpenMeteo{geocodeBody: daLatGeocodeFixture, forecastBody: forecastFixture}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	got := send(rb, "/thoitietngaymai  Đà   Lạt ")
	want := "🌦️ Thời tiết ngày mai T6 02/10 — Ðà Lạt, Lam Dong\n" +
		"Dự báo: 24–32°C, Mưa rào nhẹ 🌦️\n" +
		"Khả năng mưa 88% (7,9 mm), UV 8,2\n" +
		"Mặt trời mọc 05:42, lặn 17:43\n" +
		"Nguồn: Open-Meteo"
	if got != want {
		t.Errorf("reply =\n%s\nwant\n%s", got, want)
	}
	if len(f.geocodeQueries) != 1 || f.geocodeQueries[0] != "da lat" {
		t.Errorf("geocode queries = %q, want [\"da lat\"]", f.geocodeQueries)
	}
	if f.lastForecastQ["latitude"] != "11.94646" {
		t.Errorf("forecast latitude = %q", f.lastForecastQ["latitude"])
	}
}

func TestWeek_ListsSevenDays(t *testing.T) {
	f := &fakeOpenMeteo{forecastBody: forecastFixture}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	want := "📅 Thời tiết 7 ngày tới — Tân Thuận, Thành phố Hồ Chí Minh\n" +
		"T5 01/10: 24–33°C 🌦️ Mưa rào nhẹ, mưa 70%\n" +
		"T6 02/10: 24–32°C 🌦️ Mưa rào nhẹ, mưa 88%\n" +
		"T7 03/10: 24–32°C 🌦️ Mưa rào, mưa 85%\n" +
		"CN 04/10: 24–32°C ⛈️ Dông, mưa 92%\n" +
		"T2 05/10: 25–33°C ⛈️ Dông, mưa 98%\n" +
		"T3 06/10: 24–31°C ⛈️ Dông, mưa 100%\n" +
		"T4 07/10: 24–31°C 🌦️ Mưa phùn, mưa 89%\n" +
		"Nguồn: Open-Meteo"
	if got := send(rb, "/thoitiettuannay"); got != want {
		t.Errorf("reply =\n%s\nwant\n%s", got, want)
	}
}

func TestAliasExpansionAndForeignPlace(t *testing.T) {
	f := &fakeOpenMeteo{
		geocodeBody: `{"results":[
			{"name":"Tokyo","latitude":35.6895,"longitude":139.69171,"country_code":"JP","country":"Nhật Bản","admin1":"Tokyo"}
		]}`,
		forecastBody: forecastFixture,
	}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	got := send(rb, "/thoitiettuannay Tokyo")
	if !strings.HasPrefix(got, "📅 Thời tiết 7 ngày tới — Tokyo, Nhật Bản\n") {
		t.Errorf("reply header = %q", strings.SplitN(got, "\n", 2)[0])
	}
	send(rb, "/thoitiet HN")
	if want := []string{"tokyo", "Ha Noi"}; strings.Join(f.geocodeQueries, "|") != strings.Join(want, "|") {
		t.Errorf("geocode queries = %q, want %q", f.geocodeQueries, want)
	}
}

func TestUnknownLocation(t *testing.T) {
	f := &fakeOpenMeteo{geocodeBody: `{"generationtime_ms":0.3}`}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	if got, want := send(rb, "/thoitiet xyzzy"), `Không tìm thấy địa điểm "xyzzy".`; got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	if n := f.forecastCalls.Load(); n != 0 {
		t.Errorf("forecast calls = %d, want 0", n)
	}
}

func TestUpstreamFailureRepliesError(t *testing.T) {
	cases := map[string]*fakeOpenMeteo{
		"forecast non-200":  {forecastStatus: http.StatusServiceUnavailable},
		"forecast bad json": {forecastBody: "<html>"},
		"no daily rows":     {forecastBody: `{"daily":{"time":[]}}`},
		"geocode bad json":  {geocodeBody: "<html>"},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			stubOpenMeteo(t, f)
			rb := installWeather(t)
			cmd := "/thoitiet"
			if f.geocodeBody != "" {
				cmd = "/thoitiet Hue"
			}
			if got := send(rb, cmd); got != fetchErrorText {
				t.Errorf("reply = %q, want %q", got, fetchErrorText)
			}
		})
	}
}

func TestTomorrow_NeedsTwoDailyRows(t *testing.T) {
	oneDay := strings.NewReplacer(
		`,"2026-10-02","2026-10-03","2026-10-04","2026-10-05","2026-10-06","2026-10-07"`, "",
	).Replace(forecastFixture)
	f := &fakeOpenMeteo{forecastBody: oneDay}
	stubOpenMeteo(t, f)
	rb := installWeather(t)

	if got := send(rb, "/thoitietngaymai"); got != fetchErrorText {
		t.Errorf("reply = %q, want %q", got, fetchErrorText)
	}
	if got := send(rb, "/thoitiettuannay"); !strings.Contains(got, "Thời tiết 1 ngày tới") {
		t.Errorf("week reply = %q, want a one-day list", got)
	}
}
