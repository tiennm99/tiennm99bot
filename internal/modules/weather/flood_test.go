package weather

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// floodNow is 2026-10-02 12:00 ICT, the day of the bulletin fixture.
func floodNow() time.Time { return time.Date(2026, 10, 2, 5, 0, 0, 0, time.UTC) }

// fakeFloodUpstreams serves KTTV Nam Bộ, VNDMS and Open-Meteo from one
// httptest server. A non-zero status fails that upstream.
type fakeFloodUpstreams struct {
	forecastBody   string
	bulletin       string // issue date YYYYMMDD of the served PDF; default 20261002
	kttvnbStatus   int
	vndmsStatus    int
	forecastStatus int
	vndmsReferers  []string
	mu             sync.Mutex
}

func stubFloodUpstreams(t *testing.T, f *fakeFloodUpstreams) {
	t.Helper()
	if f.bulletin == "" {
		f.bulletin = "20261002"
	}
	pdfData, err := os.ReadFile("testdata/HCMC_TVHN_" + f.bulletin + ".pdf")
	if err != nil {
		t.Fatal(err)
	}
	lv0, _ := os.ReadFile("testdata/vndms_lv0.json")
	lv2, _ := os.ReadFile("testdata/vndms_lv2.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail := func(status int) bool {
			if status != 0 {
				http.Error(w, "unavailable", status)
				return true
			}
			return false
		}
		switch {
		case r.URL.Path == "/kttvnb/":
			if !fail(f.kttvnbStatus) {
				d := f.bulletin
				_, _ = w.Write([]byte(`<a href="/kttvnb/index.php/thuy-van/29194-ba-n-tin-da-ba-o-tha-y-v-n-tphcm-ra-nga-y-` +
					d[6:8] + "-" + d[4:6] + "-" + d[0:4] + `">`))
			}
		case strings.HasPrefix(r.URL.Path, "/kttvnb/index.php/"):
			_, _ = w.Write([]byte(`<a class="at_url" href="/kttvnb/attachments/article/29194/HCMC_TVHN_` + f.bulletin + `.pdf">`))
		case strings.HasSuffix(r.URL.Path, ".pdf"):
			_, _ = w.Write(pdfData)
		case r.URL.Path == "/vndms/water_level":
			f.mu.Lock()
			f.vndmsReferers = append(f.vndmsReferers, r.Header.Get("Referer"))
			f.mu.Unlock()
			if fail(f.vndmsStatus) {
				return
			}
			switch r.URL.Query().Get("lv") {
			case "0":
				_, _ = w.Write(lv0)
			case "2":
				_, _ = w.Write(lv2)
			default:
				_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
			}
		case r.URL.Path == "/forecast":
			if !fail(f.forecastStatus) {
				_, _ = w.Write([]byte(f.forecastBody))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	origK, origV, origF := kttvnbURL, vndmsURL, forecastURL
	kttvnbURL, vndmsURL, forecastURL = server.URL+"/kttvnb", server.URL+"/vndms", server.URL+"/forecast"
	t.Cleanup(func() { kttvnbURL, vndmsURL, forecastURL = origK, origV, origF })
}

func newTestFlood() *flood {
	col := storage.NewMemoryProvider().Collection(CollectionName)
	return &flood{
		client:      http.DefaultClient,
		subscribers: storage.Typed[subscription.Doc](col),
		pushDate:    storage.Typed[subscription.DayDoc](col),
		nowFn:       floodNow,
	}
}

func installFlood(t *testing.T, f *flood) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: CollectionName, Commands: f.commands()}},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range f.commands() {
		reg.AllCommands[c.Name] = c
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

func TestThuyvan_RendersAllSources(t *testing.T) {
	up := &fakeFloodUpstreams{forecastBody: forecastFixture}
	stubFloodUpstreams(t, up)
	rb := installFlood(t, newTestFlood())

	want := "🌊 Thuỷ văn — Tân Thuận (Q.7)\n" +
		"Đỉnh triều dự báo, Phú An / Nhà Bè (bản tin 02/10):\n" +
		"02/10: triều 1,33 / 1,34 m (06:00), mưa 7,9 mm\n" +
		"03/10: triều 1,18 / 1,19 m (07:00), mưa 7,0 mm\n" +
		"04/10: triều 1,01 / 1,03 m (21:30), mưa 5,7 mm\n" +
		"05/10: triều 0,84 / 0,88 m (22:30), mưa 13,9 mm\n" +
		"06/10: triều 1,13 / 0,70 m (01:30), mưa 4,0 mm\n" +
		"Mực nước hiện tại:\n" +
		"Phú An (Sài Gòn, 2,9 km): 1,33 m lúc 7h 02/10\n" +
		"Nhà Bè (Đồng Điền, 6,8 km): 1,18 m lúc 7h 02/10\n" +
		"Biên Hòa (Đồng Nai, 25,0 km): 1,74 m lúc 7h 02/10\n" +
		"⚠️ Thủ Dầu Một (Sài Gòn, 26,1 km): 1,56 m lúc 7h 02/10 — BĐ II\n" +
		"Báo động I/II/III tại Phú An, Nhà Bè: 1,40 / 1,50 / 1,60 m\n" +
		floodSourceLine
	if got := send(rb, "/thuyvan"); got != want {
		t.Errorf("reply:\n%s\nwant:\n%s", got, want)
	}
	for _, ref := range up.vndmsReferers {
		if ref != vndmsURL+"/" {
			t.Errorf("VNDMS Referer = %q, want %q", ref, vndmsURL+"/")
		}
	}
}

func TestThuyvan_PartialFailureKeepsOtherSources(t *testing.T) {
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastBody: forecastFixture, kttvnbStatus: http.StatusBadGateway, vndmsStatus: http.StatusForbidden})
	rb := installFlood(t, newTestFlood())

	got := send(rb, "/thuyvan")
	for _, part := range []string{"Chưa lấy được bản tin dự báo triều.", "02/10: mưa 7,9 mm", "Chưa lấy được mực nước hiện tại."} {
		if !strings.Contains(got, part) {
			t.Errorf("reply missing %q:\n%s", part, got)
		}
	}
	if strings.Contains(got, "triều 1,") {
		t.Errorf("reply shows tide numbers without a bulletin:\n%s", got)
	}
}

func TestThuyvan_AllSourcesDown(t *testing.T) {
	stubFloodUpstreams(t, &fakeFloodUpstreams{kttvnbStatus: 500, vndmsStatus: 500, forecastStatus: 500})
	rb := installFlood(t, newTestFlood())
	if got := send(rb, "/thuyvan"); got != floodFetchErrorText {
		t.Errorf("reply = %q, want %q", got, floodFetchErrorText)
	}
}

func TestThuyvanSubscribe_Idempotent(t *testing.T) {
	f := newTestFlood()
	rb := installFlood(t, f)
	if got := send(rb, "/thuyvan_subscribe"); !strings.Contains(got, "Đã đăng ký cảnh báo ngập") {
		t.Errorf("first subscribe = %q", got)
	}
	if got := send(rb, "/thuyvan_subscribe"); got != "Chat này đã đăng ký rồi." {
		t.Errorf("second subscribe = %q", got)
	}
	subs, _ := subscription.List(context.Background(), f.subscribers)
	if len(subs) != 1 || subs[0].ChatID != 7 {
		t.Errorf("subscribers = %+v, want chat 7", subs)
	}
	if got := send(rb, "/thuyvan_unsubscribe"); got != "Đã huỷ cảnh báo ngập cho chat này." {
		t.Errorf("unsubscribe = %q", got)
	}
	if got := send(rb, "/thuyvan_unsubscribe"); got != "Chat này chưa đăng ký." {
		t.Errorf("second unsubscribe = %q", got)
	}
}

type recordingSender struct {
	calls []bot.SendMessageParams
	err   error
}

func (s *recordingSender) SendMessage(_ context.Context, p *bot.SendMessageParams) (*models.Message, error) {
	s.calls = append(s.calls, *p)
	return &models.Message{}, s.err
}

func TestRunFloodPush_SkipsWithoutRisk(t *testing.T) {
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastBody: forecastFixture})
	f := newTestFlood()
	_, _ = subscription.Add(context.Background(), f.subscribers, 7, 0)
	sender := &recordingSender{}
	if err := runFloodPush(context.Background(), f, sender); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("pushed %d messages on a day without risk", len(sender.calls))
	}
	if _, _, err := f.pushDate.Get(context.Background(), floodPushDateKey); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a no-risk day must not claim the push date: err=%v", err)
	}
}

func TestRunFloodPush_HeavyRainAlertsOncePerDay(t *testing.T) {
	// 62 mm forecast for 03/10.
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastBody: strings.Replace(forecastFixture,
		`"precipitation_sum":[5.30,7.90,7.00`, `"precipitation_sum":[5.30,7.90,62.00`, 1)})
	f := newTestFlood()
	ctx := context.Background()
	_, _ = subscription.Add(ctx, f.subscribers, 7, 0)
	_, _ = subscription.Add(ctx, f.subscribers, 8, 3)
	sender := &recordingSender{}
	if err := runFloodPush(ctx, f, sender); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 2 {
		t.Fatalf("pushed %d messages, want 2", len(sender.calls))
	}
	want := "⚠️ Cảnh báo ngập — Tân Thuận (Q.7)\n" +
		"⚠️ 03/10: triều 1,18 / 1,19 m (07:00), mưa 62,0 mm — mưa lớn\n" +
		"Xem chi tiết: /thuyvan\n" + floodSourceLine
	if got := sender.calls[0].Text; got != want {
		t.Errorf("push:\n%s\nwant:\n%s", got, want)
	}
	if sender.calls[1].ChatID != int64(8) || sender.calls[1].MessageThreadID != 3 {
		t.Errorf("second push target = %v/%d, want 8/3", sender.calls[1].ChatID, sender.calls[1].MessageThreadID)
	}
	if err := runFloodPush(ctx, f, sender); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 2 {
		t.Errorf("second run the same day pushed again: %d messages", len(sender.calls))
	}
}

func TestRunFloodPush_PrunesBlockedChats(t *testing.T) {
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastBody: strings.Replace(forecastFixture,
		`"precipitation_sum":[5.30,7.90`, `"precipitation_sum":[5.30,80.00`, 1)})
	f := newTestFlood()
	ctx := context.Background()
	_, _ = subscription.Add(ctx, f.subscribers, 7, 0)
	sender := &recordingSender{err: errors.New("Forbidden: bot was blocked by the user")}
	if err := runFloodPush(ctx, f, sender); err != nil {
		t.Fatal(err)
	}
	if subs, _ := subscription.List(ctx, f.subscribers); len(subs) != 0 {
		t.Errorf("blocked chat not pruned: %+v", subs)
	}
}

func TestRunFloodPush_FailsWhenNoForecast(t *testing.T) {
	stubFloodUpstreams(t, &fakeFloodUpstreams{kttvnbStatus: 500, forecastStatus: 500})
	f := newTestFlood()
	_, _ = subscription.Add(context.Background(), f.subscribers, 7, 0)
	if err := runFloodPush(context.Background(), f, &recordingSender{}); err == nil {
		t.Error("want an error when neither tide nor rain forecast is available")
	}
}

func TestThuyvan_RainFailureIsNoted(t *testing.T) {
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastStatus: http.StatusBadGateway})
	rb := installFlood(t, newTestFlood())
	got := send(rb, "/thuyvan")
	for _, part := range []string{"Chưa lấy được dự báo mưa.", "02/10: triều 1,33 / 1,34 m (06:00)\n"} {
		if !strings.Contains(got, part) {
			t.Errorf("reply missing %q:\n%s", part, got)
		}
	}
}

func TestThuyvan_StaleBulletinIsNotUsed(t *testing.T) {
	// The newest linked bulletin is from 10/09, three weeks before today.
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastBody: forecastFixture, bulletin: "20260910"})
	rb := installFlood(t, newTestFlood())
	got := send(rb, "/thuyvan")
	if !strings.Contains(got, "Chưa lấy được bản tin dự báo triều.") || strings.Contains(got, "bản tin 10/09") {
		t.Errorf("stale bulletin used:\n%s", got)
	}
}

func TestRunFloodPush_TideAlert(t *testing.T) {
	// The 10/09 bulletin forecasts báo động I–II at Phú An and Nhà Bè from
	// 11/09; the rain fixture's dates do not overlap, so tide alone triggers.
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastBody: forecastFixture, bulletin: "20260910"})
	f := newTestFlood()
	f.nowFn = func() time.Time { return time.Date(2026, 9, 10, 4, 0, 0, 0, time.UTC) }
	_, _ = subscription.Add(context.Background(), f.subscribers, 7, 0)
	sender := &recordingSender{}
	if err := runFloodPush(context.Background(), f, sender); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("pushed %d messages, want 1", len(sender.calls))
	}
	got := sender.calls[0].Text
	for _, part := range []string{"⚠️ 11/09: triều 1,37 / 1,41 m (03:30) — BĐ I", "⚠️ 12/09: triều 1,45 / 1,46 m (16:00) — BĐ I"} {
		if !strings.Contains(got, part) {
			t.Errorf("push missing %q:\n%s", part, got)
		}
	}
	if strings.Contains(got, "10/09") {
		t.Errorf("a day below báo động I is in the push:\n%s", got)
	}
}

func TestRunFloodPush_FailsWithoutBulletinWhenRainIsQuiet(t *testing.T) {
	stubFloodUpstreams(t, &fakeFloodUpstreams{forecastBody: forecastFixture, kttvnbStatus: 500})
	f := newTestFlood()
	_, _ = subscription.Add(context.Background(), f.subscribers, 7, 0)
	sender := &recordingSender{}
	if err := runFloodPush(context.Background(), f, sender); err == nil {
		t.Error("want an error so the missing tide forecast is not read as no risk")
	}
	if len(sender.calls) != 0 {
		t.Errorf("pushed %d messages", len(sender.calls))
	}
}

func TestFormatFloodDay_TideTiers(t *testing.T) {
	cases := []struct {
		height float64
		want   string
	}{
		{1.39, "29/09: triều 1,39 m (17:00)"},
		{1.40, "⚠️ 29/09: triều 1,40 m (17:00) — BĐ I"},
		{1.55, "⚠️ 29/09: triều 1,55 m (17:00) — BĐ II"},
		{1.60, "⚠️ 29/09: triều 1,60 m (17:00) — BĐ III"},
	}
	for _, c := range cases {
		d := floodDay{Date: time.Date(2026, 9, 29, 0, 0, 0, 0, ictZone), Peaks: []tidePeak{{c.height, "17:00"}},
			HasTide: true, Tier: tideTier(c.height)}
		if got := formatFloodDay(d); got != c.want {
			t.Errorf("height %.2f: %q, want %q", c.height, got, c.want)
		}
	}
}
