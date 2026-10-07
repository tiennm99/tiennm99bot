package amlich

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// saigonLocation pins "today" for /amlich without an argument. FixedZone (not
// LoadLocation) matches the rest of the repo and needs no tzdata in the
// runtime image; Vietnam has no DST so the fixed offset is always correct.
var saigonLocation = time.FixedZone("Asia/Saigon", 7*60*60)

const amlichUsage = `Cách dùng: /amlich [dd[/mm[/yyyy]]] — đổi dương lịch sang âm lịch, bỏ trống để xem hôm nay; thiếu tháng/năm thì dùng tháng/năm hiện tại. Ví dụ: /amlich 29/01/2025`

const duonglichUsage = `Cách dùng: /duonglich <dd[/mm[/yyyy]]> [nhuan] — đổi âm lịch sang dương lịch, thiếu tháng/năm thì dùng tháng/năm âm lịch hiện tại; thêm "nhuan" nếu là tháng nhuận. Ví dụ: /duonglich 01/01/2025`

// yearRangeMessage is shared by both commands; the bound lives in lunar.go
// next to the algorithm it protects.
var yearRangeMessage = fmt.Sprintf("Chỉ hỗ trợ các năm từ %d đến %d.", minLunarYear, maxLunarYear)

// leapHintFormat is appended to a /duonglich reply when the queried month is
// also that year's leap month and the user gave no "nhuan" flag — the input
// was ambiguous, and the answer shown is for the regular month.
const leapHintFormat = `Lưu ý: năm âm lịch %d có tháng %d nhuận — thêm "nhuan" nếu ý bạn là tháng nhuận.`

// disputedCaveat is appended when the result falls in a lunar month whose
// boundary is astronomically too close to call (see disputedMonthStarts).
const disputedCaveat = "Lưu ý: ngày này gần ranh giới tháng âm lịch chưa chắc chắn; kết quả có thể lệch 1 ngày so với lịch chính thức sau này."

func amlichCommand() modules.Command {
	return modules.Command{
		Name:        "amlich",
		Visibility:  modules.VisibilityPublic,
		Description: "Đổi ngày dương lịch sang âm lịch (bỏ trống: hôm nay)",
		Parameters:  "[date]",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			now := time.Now().In(saigonLocation)
			var day, month, year int
			arg := strings.TrimSpace(chathelper.ArgAfterCommand(update.Message.Text))
			if arg == "" {
				day, month, year = now.Day(), int(now.Month()), now.Year()
			} else {
				var ok bool
				day, month, year, ok = parseDate(arg, int(now.Month()), now.Year())
				if !ok || !isValidSolarDate(day, month, year) {
					return chathelper.Reply(ctx, b, update.Message, amlichUsage)
				}
			}
			if year < minLunarYear || year > maxLunarYear {
				return chathelper.Reply(ctx, b, update.Message, yearRangeMessage)
			}
			lunarDay, lunarMonth, lunarYear, leap := solarToLunar(day, month, year)
			text := fmt.Sprintf("Dương lịch %02d/%02d/%d là ngày %d tháng %d%s năm %s %d âm lịch.",
				day, month, year, lunarDay, lunarMonth, leapLabel(leap), canChiYear(lunarYear), lunarYear)
			if nearDisputedBoundary(jdFromDate(day, month, year)) {
				text += "\n" + disputedCaveat
			}
			return chathelper.Reply(ctx, b, update.Message, text)
		},
	}
}

func duonglichCommand() modules.Command {
	return modules.Command{
		Name:        "duonglich",
		Visibility:  modules.VisibilityPublic,
		Description: "Đổi ngày âm lịch sang dương lịch",
		Parameters:  "<date> [nhuan]",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			fields := strings.Fields(chathelper.ArgAfterCommand(update.Message.Text))
			if len(fields) == 0 || len(fields) > 2 {
				return chathelper.Reply(ctx, b, update.Message, duonglichUsage)
			}
			// Missing month/year fall back to today's date expressed in the
			// same calendar as the input — the current lunar month and year.
			now := time.Now().In(saigonLocation)
			_, nowLunarMonth, nowLunarYear, _ := solarToLunar(now.Day(), int(now.Month()), now.Year())
			day, month, year, ok := parseDate(fields[0], nowLunarMonth, nowLunarYear)
			// Lunar months never exceed 30 days; deeper validity (day 30 of a
			// 29-day month, leap-month existence) is checked by lunarToSolar.
			if !ok || month < 1 || month > 12 || day < 1 || day > 30 {
				return chathelper.Reply(ctx, b, update.Message, duonglichUsage)
			}
			leap := false
			if len(fields) == 2 {
				switch strings.ToLower(fields[1]) {
				case "nhuan", "nhuận":
					leap = true
				default:
					return chathelper.Reply(ctx, b, update.Message, duonglichUsage)
				}
			}
			if year < minLunarYear || year > maxLunarYear {
				return chathelper.Reply(ctx, b, update.Message, yearRangeMessage)
			}
			solarDay, solarMonth, solarYear, err := lunarToSolar(day, month, year, leap)
			if err != nil {
				return chathelper.Reply(ctx, b, update.Message, "Không đổi được: "+err.Error()+".")
			}
			text := fmt.Sprintf("Âm lịch ngày %d tháng %d%s năm %s %d là %02d/%02d/%d dương lịch.",
				day, month, leapLabel(leap), canChiYear(year), year, solarDay, solarMonth, solarYear)
			// A bare month is ambiguous when the year also has that month as a
			// leap month and the exact leap date exists; probing lunarToSolar
			// reuses its validation (a 29-day leap month can't hide a day 30).
			if !leap {
				if _, _, _, hintErr := lunarToSolar(day, month, year, true); hintErr == nil {
					text += "\n" + fmt.Sprintf(leapHintFormat, year, month)
				}
			}
			if nearDisputedBoundary(jdFromDate(solarDay, solarMonth, solarYear)) {
				text += "\n" + disputedCaveat
			}
			return chathelper.Reply(ctx, b, update.Message, text)
		},
	}
}

// parseDate parses "d", "d/m", or "d/m/yyyy" (leading zeros optional, digits
// only); a missing month or year is filled from defMonth/defYear — today in
// the calendar the caller works in. It checks numeric shape only; calendar
// validity is the caller's job because solar and lunar dates have different
// rules.
func parseDate(s string, defMonth, defYear int) (day, month, year int, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 1 || len(parts) > 3 {
		return 0, 0, 0, false
	}
	nums := make([]int, 0, 3)
	for _, part := range parts {
		if part == "" || len(part) > 4 {
			return 0, 0, 0, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, 0, 0, false
			}
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, 0, 0, false
		}
		nums = append(nums, n)
	}
	day, month, year = nums[0], defMonth, defYear
	if len(nums) >= 2 {
		month = nums[1]
	}
	if len(nums) == 3 {
		year = nums[2]
	}
	return day, month, year, true
}

// isValidSolarDate reports whether the Gregorian date exists, using
// time.Date's normalization (31/02 rolls into March → mismatch → invalid).
func isValidSolarDate(day, month, year int) bool {
	if month < 1 || month > 12 || day < 1 {
		return false
	}
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t.Day() == day && int(t.Month()) == month && t.Year() == year
}
