package misc

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// petrolimexSearchURL is the CMS search endpoint behind the "Giá bán lẻ xăng
// dầu" widget on petrolimex.com.vn. It is not a published API: the site calls
// it from the browser, and it answers plain unauthenticated GETs. A variable so
// tests can point it at an httptest server.
var petrolimexSearchURL = "https://portals.petrolimex.com.vn/~apis/portals/cms.item/search"

// petrolimexPriceFilter selects the published retail fuel price records. The
// IDs identify Petrolimex's CMS system, the price repository, and its entity;
// they are kept readable here and base64-encoded into the x-request parameter
// the endpoint expects.
const petrolimexPriceFilter = `{"FilterBy":{"And":[` +
	`{"SystemID":{"Equals":"6783dc1271ff449e95b74a9520964169"}},` +
	`{"RepositoryID":{"Equals":"a95451e23b474fe5886bfb7cf843f53c"}},` +
	`{"RepositoryEntityID":{"Equals":"3801378fe1e045b1afa10de7c5776124"}},` +
	`{"Status":{"Equals":"Published"}}]},` +
	`"SortBy":{"LastModified":"Descending"},` +
	`"Pagination":{"TotalRecords":-1,"TotalPages":0,"PageSize":0,"PageNumber":0}}`

const (
	petrolimexHTTPTimeout = 5 * time.Second
	petrolimexMaxBytes    = 1 << 20

	giaxangErrorText = "Không lấy được giá xăng dầu từ Petrolimex. Thử lại sau nhé."
)

// giaxangLocation renders the update time in Vietnam time. FixedZone matches
// the rest of the repo and needs no tzdata in the container image.
var giaxangLocation = time.FixedZone("Asia/Saigon", 7*60*60)

// fuelPrice is one retail product in VND per liter for each price zone.
type fuelPrice struct {
	Title        string  `json:"Title"`
	Zone1Price   float64 `json:"Zone1Price"`
	Zone2Price   float64 `json:"Zone2Price"`
	DisplayOrder int     `json:"DIsplayOrder"` // upstream spelling
	LastModified string  `json:"LastModified"`
}

type petrolimexSearchResponse struct {
	Objects []fuelPrice `json:"Objects"`
}

// fetchFuelPrices returns the current Petrolimex retail prices in display
// order. Records without a title or a positive Zone 1 price are dropped; an
// empty result is an error so the caller never renders a blank table.
func fetchFuelPrices(ctx context.Context, client *http.Client) ([]fuelPrice, error) {
	q := url.Values{}
	q.Set("object-identity", "search")
	q.Set("x-request", base64.StdEncoding.EncodeToString([]byte(petrolimexPriceFilter)))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, petrolimexSearchURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("petrolimex: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("petrolimex: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("petrolimex: status %d", resp.StatusCode)
	}

	var body petrolimexSearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, petrolimexMaxBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("petrolimex: decode: %w", err)
	}
	prices := make([]fuelPrice, 0, len(body.Objects))
	for _, p := range body.Objects {
		p.Title = strings.TrimSpace(p.Title)
		if p.Title == "" || p.Zone1Price <= 0 {
			continue
		}
		prices = append(prices, p)
	}
	if len(prices) == 0 {
		return nil, errors.New("petrolimex: no prices in response")
	}
	slices.SortStableFunc(prices, func(a, b fuelPrice) int { return cmp.Compare(a.DisplayOrder, b.DisplayOrder) })
	return prices, nil
}

// formatFuelPrices renders the price table as Telegram HTML. The table sits in
// a <pre> block so the zone columns line up in Telegram's monospace font.
func formatFuelPrices(prices []fuelPrice) string {
	width := utf8.RuneCountInString("Mặt hàng")
	for _, p := range prices {
		width = max(width, utf8.RuneCountInString(p.Title))
	}

	var table strings.Builder
	writeRow := func(name, zone1, zone2 string) {
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(name))
		fmt.Fprintf(&table, "%s%s  %6s  %6s\n", name, pad, zone1, zone2)
	}
	writeRow("Mặt hàng", "Vùng 1", "Vùng 2")
	for _, p := range prices {
		writeRow(p.Title, formatThousands(p.Zone1Price), formatThousands(p.Zone2Price))
	}

	var sb strings.Builder
	sb.WriteString("⛽ Giá bán lẻ xăng dầu Petrolimex (đ/lít)\n")
	if updated, ok := latestUpdate(prices); ok {
		sb.WriteString("Cập nhật: " + updated.In(giaxangLocation).Format("15:04 02/01/2006") + "\n")
	}
	sb.WriteString("\n<pre>" + html.EscapeString(strings.TrimRight(table.String(), "\n")) + "</pre>")
	return sb.String()
}

// latestUpdate returns the newest LastModified across the records, which is
// when Petrolimex last published a price change.
func latestUpdate(prices []fuelPrice) (time.Time, bool) {
	var latest time.Time
	for _, p := range prices {
		t, err := time.Parse(time.RFC3339, p.LastModified)
		if err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest, !latest.IsZero()
}

// formatThousands renders a VND amount with dot thousands separators, the
// Vietnamese convention (27080 -> "27.080").
func formatThousands(n float64) string {
	s := strconv.FormatInt(int64(n+0.5), 10)
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			sb.WriteByte('.')
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func giaxangCommand() modules.Command {
	client := &http.Client{Timeout: petrolimexHTTPTimeout}
	return modules.Command{
		Name:        "giaxang",
		Visibility:  modules.VisibilityPublic,
		Description: "Giá xăng dầu hôm nay",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			fetchCtx, cancel := chathelper.FetchContext(ctx)
			prices, err := fetchFuelPrices(fetchCtx, client)
			cancel()
			if err != nil {
				log.Error("fuel price fetch failed", "module", "misc", "command", "giaxang", "err", err)
				return chathelper.Reply(ctx, b, update.Message, giaxangErrorText)
			}
			return chathelper.ReplyHTML(ctx, b, update.Message, formatFuelPrices(prices))
		},
	}
}
