package misc

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

// ffTemplate is the fixed rant /ff replies with. Static text, no substitution —
// the joke is that it is a copy-paste "văn mẫu" pasted into the group chat every
// time T1 starts losing, arguing that watching on only makes it hurt more.
//
// Every punchline is a running joke of the Vietnamese LMHT community rather than
// a role-by-role callout: "Tếu 1"/"rạp xiếc trung ương"/"di sản văn hóa hài kịch
// của LCK", the "gói Premium hết hạn tháng 11" bit about peaking only at Worlds,
// the "vía" the fan rituals are meant to summon, and "Tê Con" — the affectionate
// nickname Vietnamese fans gave T1 that the org itself adopted.
const ffTemplate = `🏳️ TẮT STREAM ĐI, TẾU 1 LẠI LÊN SÓNG RỒI 🏳️

Nhìn tổng thể là đủ hiểu: rạp xiếc trung ương mở màn, di sản văn hóa hài kịch của LCK lại tấu một show mới. Không cần soi ai riêng đâu — cả đội đang tấu hài rất đều, rất đoàn kết.

Xem tiếp KHÔNG cứu được, xem tiếp chỉ ĐAU HƠN, càng nhiều highlight miễn phí cho đối thủ.

Gói Premium hết hạn từ tháng 11 rồi, năm nay không gia hạn nữa đâu — vía cũng hết hạn theo luôn.

Tắt sớm = ngủ đủ giấc, mai còn đi làm.
Cố xem tới hết = vẫn thua, mà mất ngủ, còn làm tâm trạng bực bội thêm.

/FF SAVE TIME ĐI TÊ CON ƠI. 🙏🙏`

func ffCommand() modules.Command {
	return modules.Command{
		Name:        "ff",
		Visibility:  modules.VisibilityProtected,
		Description: "Văn mẫu đầu hàng khi T1 sắp thua — tắt stream cho đỡ đau",
		Handler: func(ctx context.Context, b *bot.Bot, update *models.Update) error {
			if update.Message == nil {
				return nil
			}
			return chathelper.Reply(ctx, b, update.Message, ffTemplate)
		},
	}
}
