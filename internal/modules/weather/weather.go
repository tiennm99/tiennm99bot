// Package weather is the weather and flood module. /thoitiet shows the next 6
// hours hour by hour, and /thoitiethomnay, /thoitietngaymai, and
// /thoitiettuannay show today's, tomorrow's, and the next 7 days' forecast for
// a location, Ho Chi Minh City by default; that data comes from Open-Meteo,
// which needs no API key. /thuyvan shows the flood risk at Tân Thuận (Quận 7)
// from the KTTV Nam Bộ tide bulletin, the Open-Meteo rain forecast and VNDMS
// river gauges, and /thuyvan_subscribe opts a chat into a daily alert sent
// only when a tide or rain threshold is forecast.
package weather

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/subscription"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	locationParameter = "[location...]"
	fetchErrorText    = "Không lấy được dữ liệu thời tiết. Thử lại sau nhé."
)

// errPlaceNotFound means the geocoder had no match for the user's location.
var errPlaceNotFound = errors.New("place not found")

// view is one forecast command's renderer plus a check that the response
// carries the rows it reads.
type view struct {
	command string
	ready   func(forecast) bool
	render  func(place, forecast) string
}

func hasDays(n int) func(forecast) bool {
	return func(f forecast) bool { return f.Daily.days() >= n }
}

var (
	hourlyView = view{command: "thoitiet", render: formatHourly,
		ready: func(f forecast) bool { return len(f.upcomingHours(hourlyViewHours)) > 0 }}
	todayView    = view{command: "thoitiethomnay", ready: hasDays(1), render: formatToday}
	tomorrowView = view{command: "thoitietngaymai", ready: hasDays(2), render: formatTomorrow}
	weekView     = view{command: "thoitiettuannay", ready: hasDays(1), render: formatWeek}
)

// CollectionName is the module's registry key and storage collection, which
// holds the /thuyvan alert subscribers.
const CollectionName = "weather"

// New is the weather module Factory. Only the flood alert keeps state: its
// subscriber list and last-push date.
func New(deps modules.Deps) modules.Module {
	client := &http.Client{Timeout: httpTimeout}
	fl := &flood{
		client:      &http.Client{Timeout: floodFetchTimeout},
		subscribers: storage.Typed[subscription.Doc](deps.Store),
		pushDate:    storage.Typed[subscription.DayDoc](deps.Store),
	}
	command := func(name, description string, v view) modules.Command {
		return modules.Command{
			Name:        name,
			Visibility:  modules.VisibilityPublic,
			Description: description,
			Parameters:  locationParameter,
			Handler:     handler(client, v),
		}
	}
	return modules.Module{
		Commands: append([]modules.Command{
			command("thoitiethomnay", "Thời tiết hôm nay (mặc định TP.HCM)", todayView),
			command("thoitiet", "Thời tiết từng giờ trong 6 giờ tới (mặc định TP.HCM)", hourlyView),
			command("thoitietngaymai", "Thời tiết ngày mai (mặc định TP.HCM)", tomorrowView),
			command("thoitiettuannay", "Thời tiết 7 ngày tới (mặc định TP.HCM)", weekView),
		}, fl.commands()...),
		Crons: fl.crons(),
	}
}

func handler(client *http.Client, v view) modules.CommandHandler {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) error {
		msg := update.Message
		if msg == nil {
			return nil
		}
		query := chathelper.ArgAfterCommand(msg.Text)
		fetchCtx, cancel := chathelper.FetchContext(ctx)
		p, f, err := lookup(fetchCtx, client, query)
		cancel()
		if errors.Is(err, errPlaceNotFound) {
			return chathelper.Reply(ctx, b, msg, fmt.Sprintf("Không tìm thấy địa điểm %q.", query))
		}
		if err == nil && !v.ready(f) {
			err = fmt.Errorf("forecast: missing rows for /%s", v.command)
		}
		if err != nil {
			log.Error("weather fetch failed", "module", "weather", "command", v.command, "err", err)
			return chathelper.Reply(ctx, b, msg, fetchErrorText)
		}
		return chathelper.Reply(ctx, b, msg, v.render(p, f))
	}
}

// lookup resolves the user's location and fetches its forecast. An empty
// query or a Ho Chi Minh City alias skips geocoding.
func lookup(ctx context.Context, client *http.Client, query string) (place, forecast, error) {
	p, err := resolvePlace(ctx, client, query)
	if err != nil {
		return place{}, forecast{}, err
	}
	f, err := fetchForecast(ctx, client, p)
	return p, f, err
}

func resolvePlace(ctx context.Context, client *http.Client, query string) (place, error) {
	q := normalizeQuery(query)
	if q == "" || hcmAliases[q] {
		return hcmPlace, nil
	}
	if expanded, ok := queryAliases[q]; ok {
		q = expanded
	}
	results, err := geocode(ctx, client, q)
	if err != nil {
		return place{}, err
	}
	p, ok := pickPlace(results)
	if !ok {
		return place{}, errPlaceNotFound
	}
	return p, nil
}
