package weather

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
)

const (
	floodFetchErrorText = "Không lấy được dữ liệu thuỷ văn. Thử lại sau nhé."

	// floodPushCronName is the cron's registry and scheduler key; it must be
	// unique across all modules' crons.
	floodPushCronName = "thuyvan_flood_push"
	// floodPushSchedule is UTC: 03:30 UTC == 10:30 ICT, after the tide
	// bulletin is published (about 09:20 ICT).
	floodPushSchedule = "30 3 * * *"
	// floodRetryCronName and floodRetrySchedule rerun the push at 12:30 ICT.
	// The day is claimed only when an alert goes out, so the retry sends
	// nothing after a successful 10:30 push and covers a 10:30 run that
	// failed or found the day's bulletin not yet published.
	floodRetryCronName = "thuyvan_flood_push_retry"
	floodRetrySchedule = "30 5 * * *"
	// floodPushDateKey records the ICT date of the last claimed push, so a
	// double fire (a rolling deploy briefly running two containers) sends
	// once.
	floodPushDateKey = "flood_push:last_date"
)

// flood is the /thuyvan state: its HTTP client and the alert subscribers.
type flood struct {
	client      *http.Client
	subscribers subscription.Store
	pushDate    subscription.DayStore
	// subscribersMu serializes Get→mutate→Put on the single subscriber slot.
	subscribersMu sync.Mutex
	// nowFn lets tests pin the clock; nil means time.Now.
	nowFn func() time.Time
}

func (f *flood) now() time.Time {
	if f.nowFn != nil {
		return f.nowFn()
	}
	return time.Now()
}

func (f *flood) commands() []modules.Command {
	return []modules.Command{
		{
			Name:        "thuyvan",
			Visibility:  modules.VisibilityPublic,
			Description: "Dự báo triều cường, mưa và mực nước gần Tân Thuận (Q.7)",
			Handler:     f.handleReport,
		},
		{
			Name:        "thuyvan_subscribe",
			Visibility:  modules.VisibilityPublic,
			Description: "Nhận cảnh báo ngập Tân Thuận lúc 10:30 khi có triều cường hoặc mưa lớn",
			Handler:     f.handleSubscribe,
		},
		{
			Name:        "thuyvan_unsubscribe",
			Visibility:  modules.VisibilityPublic,
			Description: "Ngừng nhận cảnh báo ngập Tân Thuận",
			Handler:     f.handleUnsubscribe,
		},
	}
}

func (f *flood) crons() []modules.Cron {
	return []modules.Cron{
		{Name: floodPushCronName, Schedule: floodPushSchedule, Handler: f.pushHandler},
		{Name: floodRetryCronName, Schedule: floodRetrySchedule, Handler: f.pushHandler},
	}
}

func (f *flood) handleReport(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	fetchCtx, cancelFetch := chathelper.FetchContext(ctx)
	fetchCtx, cancel := context.WithTimeout(fetchCtx, floodFetchTimeout)
	r := fetchFloodReport(fetchCtx, f.client, f.now())
	cancel()
	cancelFetch()
	logFloodErrors("/thuyvan", r)
	if !r.anySource() {
		return chathelper.Reply(ctx, b, msg, floodFetchErrorText)
	}
	return chathelper.Reply(ctx, b, msg, formatFloodReport(r))
}

func (f *flood) handleSubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	f.subscribersMu.Lock()
	defer f.subscribersMu.Unlock()
	added, err := subscription.Add(ctx, f.subscribers, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		return err
	}
	if added {
		return chathelper.Reply(ctx, b, msg, "✅ Đã đăng ký cảnh báo ngập Tân Thuận cho "+scopeVI(msg)+
			". Bot nhắn lúc 10:30 khi 5 ngày tới có triều cường từ báo động I hoặc mưa từ 50 mm.")
	}
	return chathelper.Reply(ctx, b, msg, capitalizeScopeVI(msg)+" đã đăng ký rồi.")
}

func (f *flood) handleUnsubscribe(ctx context.Context, b *bot.Bot, update *models.Update) error {
	msg := update.Message
	if msg == nil {
		return nil
	}
	f.subscribersMu.Lock()
	defer f.subscribersMu.Unlock()
	removed, err := subscription.Remove(ctx, f.subscribers, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		return err
	}
	if removed {
		return chathelper.Reply(ctx, b, msg, "Đã huỷ cảnh báo ngập cho "+scopeVI(msg)+".")
	}
	return chathelper.Reply(ctx, b, msg, capitalizeScopeVI(msg)+" chưa đăng ký.")
}

// scopeVI names where a subscription applies, in Vietnamese.
func scopeVI(msg *models.Message) string {
	if msg.MessageThreadID != 0 {
		return "topic này"
	}
	return "chat này"
}

func capitalizeScopeVI(msg *models.Message) string {
	if msg.MessageThreadID != 0 {
		return "Topic này"
	}
	return "Chat này"
}

func (f *flood) pushHandler(ctx context.Context, deps modules.Deps) error {
	if deps.Bot == nil {
		return errors.New("flood push: deps.Bot is nil (BuildOptions.Bot not wired)")
	}
	return runFloodPush(ctx, f, deps.Bot)
}

// runFloodPush sends the alert to every subscriber when a day in the window
// is at risk, and sends nothing otherwise. The day is claimed only after the
// fetch, so a failed fetch leaves it unclaimed.
func runFloodPush(ctx context.Context, f *flood, sender subscription.Sender) error {
	subs, err := subscription.List(ctx, f.subscribers)
	if err != nil {
		return fmt.Errorf("flood push: list subscribers: %w", err)
	}
	if len(subs) == 0 {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, floodFetchTimeout)
	r := fetchFloodReport(fetchCtx, f.client, f.now())
	cancel()
	logFloodErrors("flood push", r)
	if r.BulletinErr != nil && r.RainErr != nil {
		return fmt.Errorf("flood push: no tide or rain forecast: %w", errors.Join(r.BulletinErr, r.RainErr))
	}
	text := formatFloodAlert(r)
	if text == "" {
		// Tide is the main flood signal here; without the bulletin a quiet
		// rain forecast proves nothing, so fail loudly and let the retry
		// run try again.
		if r.BulletinErr != nil {
			return fmt.Errorf("flood push: no tide forecast, rain alone shows no risk: %w", r.BulletinErr)
		}
		log.Info("flood push: no risk in the next days, skipping")
		return nil
	}
	day := r.Today.Format("2006-01-02")
	won, err := subscription.ClaimDay(ctx, f.pushDate, floodPushDateKey, day)
	if err != nil {
		return fmt.Errorf("flood push: claim date: %w", err)
	}
	if !won {
		log.Info("flood push: already pushed today, skipping", "date", day)
		return nil
	}
	res, err := subscription.Fanout(ctx, "flood", f.subscribers, &f.subscribersMu, subs, sender, bot.SendMessageParams{Text: text})
	if err != nil {
		return err
	}
	log.Info("flood push complete", "subscribers", len(subs),
		"sent", res.Sent, "failed", res.Failed, "pruned", res.Pruned, "throttled", res.Throttled)
	return nil
}

func logFloodErrors(where string, r floodReport) {
	for source, err := range map[string]error{"tide bulletin": r.BulletinErr, "rain": r.RainErr, "gauges": r.StationsErr} {
		if err != nil {
			log.Warn("flood source failed", "module", "weather", "where", where, "source", source, "err", err)
		}
	}
}
