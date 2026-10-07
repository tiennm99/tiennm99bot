package amlich

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installAmlich wires the amlich module to a recording bot. The module is
// stateless, so no store is needed.
func installAmlich(t *testing.T) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	mod := New(modules.Deps{})

	reg := &modules.Registry{
		Modules:     []modules.Module{{Name: "amlich", Commands: mod.Commands}},
		AllCommands: map[string]modules.Command{},
	}
	for _, c := range mod.Commands {
		reg.AllCommands[c.Name] = c
	}
	modules.Install(rb.Bot, reg, modules.Auth{BotOwnerID: 999})
	return rb
}

func TestNew_RegistersExpectedCommands(t *testing.T) {
	mod := New(modules.Deps{})

	want := map[string]modules.Visibility{
		"amlich":    modules.VisibilityPublic,
		"duonglich": modules.VisibilityPublic,
	}
	if len(mod.Commands) != len(want) {
		t.Fatalf("commands count = %d, want %d", len(mod.Commands), len(want))
	}
	for _, c := range mod.Commands {
		v, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected command %q", c.Name)
			continue
		}
		if c.Visibility != v {
			t.Errorf("command %q visibility = %d, want %d", c.Name, c.Visibility, v)
		}
		if c.Handler == nil {
			t.Errorf("command %q has nil handler", c.Name)
		}
	}
}

func TestAmlich_ConvertsKnownDate(t *testing.T) {
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/amlich 29/01/2025"))

	want := "Dương lịch 29/01/2025 là ngày 1 tháng 1 năm Ất Tỵ 2025 âm lịch."
	if got := rb.LastSent().Text(); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestAmlich_LeapMonth(t *testing.T) {
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/amlich 22/03/2023"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "tháng 2 nhuận") || !strings.Contains(got, "Quý Mão") {
		t.Errorf("reply = %q, want leap month 2 of Quý Mão", got)
	}
}

func TestAmlich_DefaultsToToday(t *testing.T) {
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/amlich"))

	got := rb.LastSent().Text()
	if !strings.HasPrefix(got, "Dương lịch ") || !strings.Contains(got, "âm lịch") {
		t.Errorf("no-arg reply = %q, want a conversion of today", got)
	}
}

func TestParseDate_PartialDatesFillDefaults(t *testing.T) {
	cases := []struct {
		in      string
		d, m, y int
		ok      bool
	}{
		{"25", 25, 6, 2026, true},
		{"25/8", 25, 8, 2026, true},
		{"25/08/2027", 25, 8, 2027, true},
		{"05", 5, 6, 2026, true},
		{"", 0, 0, 0, false},
		{"25/8/2027/9", 0, 0, 0, false},
		{"2a", 0, 0, 0, false},
		{"25//2027", 0, 0, 0, false},
		{"-5", 0, 0, 0, false},
		{"25/8/20270", 0, 0, 0, false},
	}
	for _, tc := range cases {
		d, m, y, ok := parseDate(tc.in, 6, 2026)
		if d != tc.d || m != tc.m || y != tc.y || ok != tc.ok {
			t.Errorf("parseDate(%q, 6, 2026) = %d/%d/%d ok=%v, want %d/%d/%d ok=%v",
				tc.in, d, m, y, ok, tc.d, tc.m, tc.y, tc.ok)
		}
	}
}

// replyFor drives one command through a fresh bot and returns the reply text.
func replyFor(t *testing.T, text string) string {
	t.Helper()
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, text))
	return rb.LastSent().Text()
}

// Partial dates must answer exactly like their fully spelled-out equivalent.
// Expectations are computed from the same clock the handler uses, so the tests
// hold on any date (a midnight flip between the two reads is the only, and
// vanishingly unlikely, race).
func TestAmlich_PartialDateFillsCurrentMonthYear(t *testing.T) {
	now := time.Now().In(saigonLocation)
	full := replyFor(t, fmt.Sprintf("/amlich 15/%d/%d", int(now.Month()), now.Year()))
	if got := replyFor(t, "/amlich 15"); got != full {
		t.Errorf("/amlich 15 = %q, want %q", got, full)
	}
	if got := replyFor(t, fmt.Sprintf("/amlich 15/%d", int(now.Month()))); got != full {
		t.Errorf("/amlich 15/m = %q, want %q", got, full)
	}
}

func TestDuonglich_PartialDateFillsCurrentLunarMonthYear(t *testing.T) {
	now := time.Now().In(saigonLocation)
	_, lunarMonth, lunarYear, _ := solarToLunar(now.Day(), int(now.Month()), now.Year())
	full := replyFor(t, fmt.Sprintf("/duonglich 10/%d/%d", lunarMonth, lunarYear))
	if got := replyFor(t, "/duonglich 10"); got != full {
		t.Errorf("/duonglich 10 = %q, want %q", got, full)
	}
	if got := replyFor(t, fmt.Sprintf("/duonglich 10/%d", lunarMonth)); got != full {
		t.Errorf("/duonglich 10/m = %q, want %q", got, full)
	}
}

func TestAmlich_InvalidInputRepliesUsage(t *testing.T) {
	for _, text := range []string{
		"/amlich foo",
		"/amlich 31/02/2025", // nonexistent Gregorian date
		"/amlich 1-1-2025",
		"/amlich 01/13/2025",
	} {
		t.Run(text, func(t *testing.T) {
			rb := installAmlich(t)
			rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, text))

			if got := rb.LastSent().Text(); got != amlichUsage {
				t.Errorf("reply = %q, want usage", got)
			}
		})
	}
}

func TestAmlich_YearOutOfRange(t *testing.T) {
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/amlich 01/01/1500"))

	if got := rb.LastSent().Text(); got != yearRangeMessage {
		t.Errorf("reply = %q, want year-range message", got)
	}
}

func TestDuonglich_ConvertsKnownDate(t *testing.T) {
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/duonglich 01/01/2025"))

	want := "Âm lịch ngày 1 tháng 1 năm Ất Tỵ 2025 là 29/01/2025 dương lịch."
	if got := rb.LastSent().Text(); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestDuonglich_LeapMonth(t *testing.T) {
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/duonglich 01/02/2023 nhuan"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "tháng 2 nhuận") || !strings.Contains(got, "22/03/2023") {
		t.Errorf("reply = %q, want leap month 2/2023 → 22/03/2023", got)
	}
}

func TestDuonglich_RejectsWrongLeapMonth(t *testing.T) {
	rb := installAmlich(t)
	rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, "/duonglich 01/03/2023 nhuan"))

	got := rb.LastSent().Text()
	if !strings.Contains(got, "không có tháng 3 nhuận") {
		t.Errorf("reply = %q, want leap-month rejection", got)
	}
}

// A bare month that is also the year's leap month is ambiguous input; the
// reply must carry the disambiguation hint — and only then.
func TestDuonglich_LeapMonthHint(t *testing.T) {
	solarDay, solarMonth, solarYear, err := lunarToSolar(5, 5, 2028, false)
	if err != nil {
		t.Fatalf("lunarToSolar(5/5/2028): %v", err)
	}
	want := fmt.Sprintf("Âm lịch ngày 5 tháng 5 năm Mậu Thân 2028 là %02d/%02d/%d dương lịch.\n", solarDay, solarMonth, solarYear) +
		`Lưu ý: năm âm lịch 2028 có tháng 5 nhuận — thêm "nhuan" nếu ý bạn là tháng nhuận.`
	if got := replyFor(t, "/duonglich 5/5/2028"); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
}

func TestDuonglich_NoHintWhenNhuanExplicit(t *testing.T) {
	if got := replyFor(t, "/duonglich 5/5/2028 nhuan"); strings.Contains(got, "Lưu ý") {
		t.Errorf("explicit nhuan reply = %q, want no hint", got)
	}
}

func TestDuonglich_NoHintWithoutLeapMonth(t *testing.T) {
	if got := replyFor(t, "/duonglich 5/5/2027"); strings.Contains(got, "Lưu ý") {
		t.Errorf("non-leap-year reply = %q, want no hint", got)
	}
}

// Dates inside a lunar month touching a disputed boundary (see
// disputedMonthStarts) must carry the caveat; nearby ordinary dates must not.
// Absence on modern dates is also pinned by the exact-string assertions in
// TestAmlich_ConvertsKnownDate and TestDuonglich_ConvertsKnownDate.
func TestAmlich_DisputedBoundaryCaveat(t *testing.T) {
	if got := replyFor(t, "/amlich 20/12/2072"); !strings.HasSuffix(got, "\n"+disputedCaveat) {
		t.Errorf("reply = %q, want disputed-boundary caveat", got)
	}
	if got := replyFor(t, "/amlich 01/06/2072"); strings.Contains(got, "Lưu ý") {
		t.Errorf("reply = %q, want no caveat", got)
	}
}

func TestDuonglich_DisputedBoundaryCaveat(t *testing.T) {
	lunarDay, lunarMonth, lunarYear, leap := solarToLunar(20, 12, 2072)
	cmd := fmt.Sprintf("/duonglich %d/%d/%d", lunarDay, lunarMonth, lunarYear)
	if leap {
		cmd += " nhuan"
	}
	if got := replyFor(t, cmd); !strings.HasSuffix(got, "\n"+disputedCaveat) {
		t.Errorf("reply for %q = %q, want disputed-boundary caveat", cmd, got)
	}
}

func TestDuonglich_InvalidInputRepliesUsage(t *testing.T) {
	for _, text := range []string{
		"/duonglich",
		"/duonglich foo",
		"/duonglich 31/01/2025", // lunar day > 30
		"/duonglich 01/13/2025",
		"/duonglich 01/01/2025 xyz",
		"/duonglich 01/01/2025 nhuan extra",
	} {
		t.Run(text, func(t *testing.T) {
			rb := installAmlich(t)
			rb.Bot.ProcessUpdate(context.Background(), testutil.NewPrivateMessage(7, text))

			if got := rb.LastSent().Text(); got != duonglichUsage {
				t.Errorf("reply = %q, want usage", got)
			}
		})
	}
}
