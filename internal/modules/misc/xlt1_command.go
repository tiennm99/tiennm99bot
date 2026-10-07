package misc

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// xlt1Template is the "đơn xin lỗi T1" văn mẫu /xlt1 replies with. Two %s slots,
// both the sender mention: the "Tôi tên là" field and the signature block.
//
// The joke is the form, not the words: Vietnamese apology memes parody an
// administrative petition — quốc hiệu, "Kính gửi", "Nội dung sự việc",
// "Tôi xin cam kết", a signature block — and the laugh comes from that shape
// wrapping something trivial. So the bureaucratic scaffolding stays intact even
// where it reads stiff; trimming it is what would break the bit.
//
// It is deliberately the sequel to ffTemplate: the same fan who panicked and
// typed /ff mid-series now has to file paperwork because the team came back.
// That is why the punchlines are quoted back as things the sender said
// ("Tếu 1", "rạp xiếc trung ương", "gói Premium hết hạn từ tháng 11") rather
// than asserted — /xlt1 retracts what /ff claimed.
//
// No concrete title counts or tournament results appear anywhere: a static
// template that names a scoreline goes stale the next split, while "sự thật đã
// chứng minh tôi sai" stays true every season.
//
// Rendered as HTML for the tg://user mention, so the prose must stay free of
// raw <, > and & or Telegram rejects the send.
const xlt1Template = `CỘNG HÒA XÃ HỘI CHỦ NGHĨA VIỆT NAM
Độc lập – Tự do – Hạnh phúc
━━━━━━━ oOo ━━━━━━━

🙏 ĐƠN XIN LỖI 🙏

Kính gửi:
– Ban lãnh đạo T1 Esports
– Toàn thể Tê Con gần xa

Tôi tên là: %s
Chức vụ: Fan ruột (bán thời gian), chuyên gia trù đội nhà
Địa chỉ: Nhóm chat này

NỘI DUNG SỰ VIỆC:
Trong lúc trận đấu còn chưa kết thúc, tôi đã hoảng loạn gõ /ff, gọi đội nhà là "Tếu 1", là "rạp xiếc trung ương", là "di sản văn hóa hài kịch của LCK". Tôi còn tuyên bố chắc nịch rằng "gói Premium hết hạn từ tháng 11", rằng năm nay hết vía, rồi tắt stream đi ngủ cho đỡ đau.

Sau đó sự thật đã chứng minh tôi sai. Toàn bộ trách nhiệm thuộc về tôi và cái miệng của tôi.

TÔI XIN CAM KẾT:
1. Không trù đội nhà trước khi Nexus nổ.
2. Không tắt stream sớm, dù bảng tỉ số có xấu tới đâu.
3. Không đếm cúp hộ người khác khi tủ cúp nhà mình còn đang trống.
4. Chỉ nhắc lại chuyện "hết thời" khi... thật sự hết thời.

Kính mong Ban lãnh đạo và toàn thể cộng đồng xem xét, đại xá cho tôi lần này.
Tôi xin trân trọng cảm ơn! 🙏

                    Người làm đơn
                    %s`

func xlt1Command() modules.Command {
	return modules.Command{
		Name:        "xlt1",
		Visibility:  modules.VisibilityPublic,
		Description: "Văn mẫu đơn xin lỗi T1 — dành cho lúc trù xong phải quay xe",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			mention := senderMention(update.Message.From)
			return chathelper.ReplyHTML(ctx, b, update.Message,
				fmt.Sprintf(xlt1Template, mention, mention))
		},
	}
}
