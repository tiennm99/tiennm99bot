package misc

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// namThangTemplate is the văn mẫu /namthang replies with. Three %s slots: the
// salary as the sender typed it, the tax range, and the insurance amount.
const namThangTemplate = "Thôi nằm thẳng luôn cho khỏe. Lương %s mà thuế TNCN cắn %s, bảo hiểm bắt buộc thêm gần %s, còn lại vừa đủ trả tiền trọ, tiền xăng, tiền nuôi con, chưa kể lạm phát chạy nhanh hơn cả KPI. Làm quần quật cả năm, tiền tăng ca không bù nổi tiền thuế với tiền mất giá, cuối tháng nhìn tài khoản chỉ muốn khóc. Mệt xác để cuối cùng vẫn nghèo, vẫn áp lực, vẫn bị vắt kiệt thì thà nằm thẳng còn hơn. Từ nay tao dậy trễ, cà phê sữa đá, chiều xem phim, tối nhậu nhẹt, sống chậm lại, giữ sức khỏe, giữ tinh thần. Làm ít ăn ít nhưng vui, còn hơn cày như trâu rồi vẫn trắng tay. Nằm thẳng không phải lười, mà là từ chối làm nô lệ của đồng tiền nữa. NẰM THẲNG LÀ TỰ DO"

const (
	// defaultNamThangSalary is the salary used when /namthang has no argument.
	defaultNamThangSalary = "25 triệu"
	// defaultNamThangAmount is defaultNamThangSalary in đồng. Free-text salaries
	// such as "ba cọc ba đồng" keep the figures for this amount.
	defaultNamThangAmount = 25_000_000.0

	// The rates reproduce the rant's figures at 25 triệu (tax 3-4 triệu,
	// insurance 3 triệu) and scale them linearly. They are deliberately not real
	// PIT or insurance rules: the rant overstates the deductions, and the real
	// brackets would break its original numbers.
	namThangTaxLowRate    = 0.12
	namThangTaxHighRate   = 0.16
	namThangInsuranceRate = 0.12

	// maxNamThangAmount bounds the đồng amount scaled exactly; anything larger is
	// treated as free text.
	maxNamThangAmount = 1e15
)

// namThangSalaryPattern matches a number, either dot/comma-grouped thousands
// (25.000.000) or a plain or decimal value (25, 25,5), then an optional unit.
var namThangSalaryPattern = regexp.MustCompile(`^(?:(\d{1,3}(?:[.,]\d{3})+)|(\d+(?:[.,]\d+)?))\s*(\p{L}*)$`)

// namThangUnits maps a lowercase salary unit to its value in đồng.
var namThangUnits = map[string]float64{
	"đ": 1, "d": 1, "đồng": 1, "dong": 1, "vnd": 1, "vnđ": 1,
	"k": 1e3, "nghìn": 1e3, "nghin": 1e3, "ngàn": 1e3, "ngan": 1e3,
	"tr": 1e6, "triệu": 1e6, "trieu": 1e6, "củ": 1e6, "cu": 1e6, "m": 1e6,
	"tỷ": 1e9, "tỉ": 1e9, "ty": 1e9, "ti": 1e9,
}

// parseNamThangSalary converts salary text to đồng. A bare number below 1000
// means triệu, as in "/namthang 25". It reports false for free text and for
// amounts that are zero or out of range.
func parseNamThangSalary(salary string) (float64, bool) {
	match := namThangSalaryPattern.FindStringSubmatch(strings.ToLower(salary))
	if match == nil {
		return 0, false
	}
	digits := strings.NewReplacer(".", "", ",", "").Replace(match[1])
	if match[1] == "" {
		digits = strings.ReplaceAll(match[2], ",", ".")
	}
	value, err := strconv.ParseFloat(digits, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	var multiplier float64
	switch unit := match[3]; {
	case unit != "":
		var known bool
		if multiplier, known = namThangUnits[unit]; !known {
			return 0, false
		}
	case value < 1000:
		multiplier = 1e6
	default:
		multiplier = 1
	}
	amount := value * multiplier
	if amount >= maxNamThangAmount {
		return 0, false
	}
	return amount, true
}

// roundNamThangAmount keeps two significant digits, to whole đồng at the least.
func roundNamThangAmount(amount float64) float64 {
	if amount <= 0 {
		return 0
	}
	step := math.Pow10(int(math.Floor(math.Log10(amount))) - 1)
	if step < 1 {
		step = 1
	}
	return math.Round(amount/step) * step
}

// splitNamThangAmount renders an amount as its number and largest fitting unit,
// using the Vietnamese decimal comma: 1200000 becomes "1,2" and "triệu".
func splitNamThangAmount(amount float64) (number, unit string) {
	divisor := 1.0
	unit = "đồng"
	switch {
	case amount >= 1e9:
		divisor, unit = 1e9, "tỷ"
	case amount >= 1e6:
		divisor, unit = 1e6, "triệu"
	case amount >= 1e3:
		divisor, unit = 1e3, "nghìn"
	}
	number = strconv.FormatFloat(amount/divisor, 'f', -1, 64)
	return strings.ReplaceAll(number, ".", ","), unit
}

func formatNamThangAmount(amount float64) string {
	number, unit := splitNamThangAmount(roundNamThangAmount(amount))
	return number + " " + unit
}

// formatNamThangRange names the unit once when both ends share it: "3-4 triệu",
// but "840 nghìn-1,1 triệu".
func formatNamThangRange(low, high float64) string {
	lowNumber, lowUnit := splitNamThangAmount(roundNamThangAmount(low))
	highNumber, highUnit := splitNamThangAmount(roundNamThangAmount(high))
	if lowUnit == highUnit {
		return lowNumber + "-" + highNumber + " " + highUnit
	}
	return lowNumber + " " + lowUnit + "-" + highNumber + " " + highUnit
}

// namThangText renders the rant for salary text, which appears as typed.
func namThangText(salary string) string {
	amount, ok := parseNamThangSalary(salary)
	if !ok {
		amount = defaultNamThangAmount
	}
	return fmt.Sprintf(namThangTemplate, salary,
		formatNamThangRange(amount*namThangTaxLowRate, amount*namThangTaxHighRate),
		formatNamThangAmount(amount*namThangInsuranceRate))
}

func namThangCommand() modules.Command {
	return modules.Command{
		Name:        "namthang",
		Visibility:  modules.VisibilityPublic,
		Description: "Văn mẫu nằm thẳng",
		Parameters:  "[salary...]",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			salary := strings.TrimSpace(chathelper.ArgAfterCommand(update.Message.Text))
			if salary == "" {
				salary = defaultNamThangSalary
			}
			return chathelper.Reply(ctx, b, update.Message, namThangText(salary))
		},
	}
}
