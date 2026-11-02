package misc

import (
	"context"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

func TestParseNamThangSalary(t *testing.T) {
	valid := map[string]float64{
		"25":          25e6,
		"25 triệu":    25e6,
		"25 TRIỆU":    25e6,
		"25tr":        25e6,
		"25 củ":       25e6,
		"25,5 triệu":  25.5e6,
		"25.5tr":      25.5e6,
		"500k":        500e3,
		"500 nghìn":   500e3,
		"1 tỷ":        1e9,
		"1,5 tỉ":      1.5e9,
		"25000000":    25e6,
		"25.000.000":  25e6,
		"25,000,000":  25e6,
		"25.000.000đ": 25e6,
		"800 đồng":    800,
	}
	for input, want := range valid {
		if got, ok := parseNamThangSalary(input); !ok || got != want {
			t.Errorf("parse(%q) = %v, %v; want %v, true", input, got, ok, want)
		}
	}

	for _, input := range []string{"", "ba cọc ba đồng", "25 con gà", "0", "0 triệu", "-5", "1.234,5", "9999999 tỷ"} {
		if got, ok := parseNamThangSalary(input); ok {
			t.Errorf("parse(%q) = %v, true; want false", input, got)
		}
	}
}

func TestNamThangText_ScalesFigures(t *testing.T) {
	cases := []struct{ salary, want string }{
		{"25 triệu", "Lương 25 triệu mà thuế TNCN cắn 3-4 triệu, bảo hiểm bắt buộc thêm gần 3 triệu,"},
		{"25", "Lương 25 mà thuế TNCN cắn 3-4 triệu, bảo hiểm bắt buộc thêm gần 3 triệu,"},
		{"100 triệu", "Lương 100 triệu mà thuế TNCN cắn 12-16 triệu, bảo hiểm bắt buộc thêm gần 12 triệu,"},
		{"10tr", "Lương 10tr mà thuế TNCN cắn 1,2-1,6 triệu, bảo hiểm bắt buộc thêm gần 1,2 triệu,"},
		{"7 triệu", "Lương 7 triệu mà thuế TNCN cắn 840 nghìn-1,1 triệu, bảo hiểm bắt buộc thêm gần 840 nghìn,"},
		{"5 triệu", "Lương 5 triệu mà thuế TNCN cắn 600-800 nghìn, bảo hiểm bắt buộc thêm gần 600 nghìn,"},
		{"10 tỷ", "Lương 10 tỷ mà thuế TNCN cắn 1,2-1,6 tỷ, bảo hiểm bắt buộc thêm gần 1,2 tỷ,"},
		{"ba cọc ba đồng", "Lương ba cọc ba đồng mà thuế TNCN cắn 3-4 triệu, bảo hiểm bắt buộc thêm gần 3 triệu,"},
	}
	for _, c := range cases {
		if got := namThangText(c.salary); !strings.Contains(got, c.want) {
			t.Errorf("namThangText(%q) = %q, want it to contain %q", c.salary, got, c.want)
		}
	}
}

func TestNamThang_DefaultSalary(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/namthang"))

	if got, want := rb.LastSent().Text(), namThangText(defaultNamThangSalary); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestNamThang_CustomSalary(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/namthang 100 triệu"))

	if got, want := rb.LastSent().Text(), namThangText("100 triệu"); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestNamThang_SalaryIsSentAsPlainText(t *testing.T) {
	rb, _ := installMisc(t, 999)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/namthang <b>&"))

	if got := rb.LastSent().Text(); !strings.Contains(got, "Lương <b>& mà") {
		t.Errorf("reply altered the salary: %q", got)
	}
}
