package stock

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

func answerDividendCallback(ctx context.Context, b *bot.Bot, queryID, text string, alert bool) error {
	_, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: queryID,
		Text:            text,
		ShowAlert:       alert,
	})
	return err
}

func removeDividendButton(ctx context.Context, b *bot.Bot, chatID int64, messageID int) error {
	_, err := b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      chatID,
		MessageID:   messageID,
		ReplyMarkup: &models.InlineKeyboardMarkup{},
	})
	return err
}

func (s *state) invalidateDividendAction(ctx context.Context, b *bot.Bot, actionKey string, action PendingDividendAction) (error, error) {
	var deleteErr error
	if s.pending != nil {
		deleteErr = s.pending.Delete(ctx, actionKey)
	}
	return deleteErr, removeDividendButton(ctx, b, action.ChatID, action.MessageID)
}

func (s *state) rejectDividendAction(ctx context.Context, b *bot.Bot, queryID, actionKey string, action PendingDividendAction, text string) error {
	_, _ = s.invalidateDividendAction(ctx, b, actionKey, action)
	return answerDividendCallback(ctx, b, queryID, text, true)
}

func (s *state) handleDividendCallback(ctx context.Context, b *bot.Bot, update *models.Update) error {
	if update == nil || update.CallbackQuery == nil {
		return nil
	}
	query := update.CallbackQuery
	action, msg, actionKey, handled, err := s.resolveDividendCallback(ctx, b, query)
	if err != nil || handled {
		return err
	}
	defer s.locks.Acquire(strconv.FormatInt(action.OwnerUserID, 10))()
	return s.consumeDividendAction(ctx, b, query, msg, actionKey, action)
}

func (s *state) resolveDividendCallback(ctx context.Context, b *bot.Bot, query *models.CallbackQuery) (PendingDividendAction, *models.Message, string, bool, error) {
	ownerID, eventID, ok := parseDividendCallback(query.Data)
	if !ok || s.pending == nil {
		err := answerDividendCallback(ctx, b, query.ID, "This dividend suggestion is invalid.", true)
		return PendingDividendAction{}, nil, "", true, err
	}
	actionKey := pendingDividendKey(ownerID, eventID)
	action, _, err := s.pending.Get(ctx, actionKey)
	if errors.Is(err, storage.ErrNotFound) {
		err = answerDividendCallback(ctx, b, query.ID, "This dividend suggestion expired or was already used.", true)
		return PendingDividendAction{}, nil, "", true, err
	}
	if err != nil {
		log.Error("stock_load_dividend_action", "err", err)
		err = answerDividendCallback(ctx, b, query.ID, "Could not load this dividend suggestion. Try again.", true)
		return PendingDividendAction{}, nil, "", true, err
	}
	if query.From.ID == 0 || query.From.ID != action.OwnerUserID {
		err = answerDividendCallback(ctx, b, query.ID, "Only the user who requested this portfolio can apply this dividend.", true)
		return PendingDividendAction{}, nil, "", true, err
	}
	msg := query.Message.Message
	if msg == nil || msg.Chat.ID != action.ChatID || msg.ID != action.MessageID || action.MessageID == 0 {
		err = answerDividendCallback(ctx, b, query.ID, "This dividend suggestion is no longer valid here.", true)
		return PendingDividendAction{}, nil, "", true, err
	}
	now := s.now().UnixMilli()
	if action.ExpiresAt <= now {
		err = s.rejectDividendAction(ctx, b, query.ID, actionKey, action, "This suggestion expired. Run /stock_portfolio again.")
		return PendingDividendAction{}, nil, "", true, err
	}
	return action, msg, actionKey, false, nil
}

func (s *state) consumeDividendAction(ctx context.Context, b *bot.Bot, query *models.CallbackQuery, msg *models.Message, actionKey string, action PendingDividendAction) error {
	now := s.now().UnixMilli()
	// Re-read under the user lock so two callback workers cannot consume the
	// same action using stale state if handler execution becomes asynchronous.
	action, _, err := s.pending.Get(ctx, actionKey)
	if errors.Is(err, storage.ErrNotFound) {
		return answerDividendCallback(ctx, b, query.ID, "This dividend was already handled.", true)
	}
	if err != nil {
		return fmt.Errorf("reload pending dividend action: %w", err)
	}
	p, err := LoadPortfolio(ctx, s.store, action.OwnerUserID, now)
	if err != nil {
		log.Error("stock_load_portfolio", "user", action.OwnerUserID, "err", err)
		return answerDividendCallback(ctx, b, query.ID, "Could not load your portfolio. Try again.", true)
	}
	record, exists := p.dividendRecord(action.Symbol, action.ProviderEventID)
	if !exists {
		return s.rejectDividendAction(ctx, b, query.ID, actionKey, action, "This dividend is no longer available.")
	}
	if record.Processed {
		return s.rejectDividendAction(ctx, b, query.ID, actionKey, action, "This dividend was already applied.")
	}
	if !dividendRecordDue(record, s.now()) {
		return s.rejectDividendAction(ctx, b, query.ID, actionKey, action, "This dividend is not available before Record date.")
	}
	position, held := p.Assets[action.Symbol]
	if !held || position.Quantity <= 0 {
		return s.rejectDividendAction(ctx, b, query.ID, actionKey, action, "You no longer hold "+action.Symbol+". Dividend not applied.")
	}
	if position.OpenedAt != action.PositionOpenedAt {
		return s.rejectDividendAction(ctx, b, query.ID, actionKey, action, "This "+action.Symbol+" position was closed after the suggestion. Run /stock_portfolio again.")
	}
	if !positionOpenedByRecordDate(position, record) {
		return s.rejectDividendAction(ctx, b, query.ID, actionKey, action, "This "+action.Symbol+" position was opened after Record date. Dividend not applied.")
	}

	result, err := applySuggestedDividend(&p, action.Symbol, record, position.Quantity, now)
	if err != nil {
		log.Error("stock_apply_suggested_dividend", "user", action.OwnerUserID, "ticker", action.Symbol, "event", action.ProviderEventID, "err", err)
		return answerDividendCallback(ctx, b, query.ID, "Could not safely apply this dividend.", true)
	}
	record.Processed = true
	p.setDividendRecord(action.Symbol, action.ProviderEventID, record)
	if err := SavePortfolio(ctx, s.store, action.OwnerUserID, p); err != nil {
		log.Error("stock_save_portfolio", "user", action.OwnerUserID, "err", err)
		return answerDividendCallback(ctx, b, query.ID, "Could not save your portfolio. Try again.", true)
	}

	// The portfolio history is the source of truth for idempotency. Cleanup and
	// Telegram UI updates are best-effort after the atomic portfolio save.
	deleteErr, removeErr := s.invalidateDividendAction(ctx, b, actionKey, action)
	if deleteErr != nil {
		log.Error("stock_delete_dividend_action", "err", deleteErr)
	}
	if removeErr != nil {
		log.Error("stock_remove_dividend_button", "user", action.OwnerUserID, "ticker", action.Symbol, "err", removeErr)
	}
	if err := answerDividendCallback(ctx, b, query.ID, "Dividend applied.", false); err != nil {
		log.Error("stock_answer_dividend_callback", "user", action.OwnerUserID, "ticker", action.Symbol, "err", err)
	}
	return chathelper.Reply(ctx, b, msg, result)
}

func applySuggestedDividend(p *Portfolio, symbol string, record DividendRecord, held, now int64) (string, error) {
	switch record.Kind {
	case DividendKindCash:
		total, err := cashDividendTotal(held, record.VNDPerShare)
		if err != nil {
			return "", err
		}
		balance, err := checkedVNDBalance(p.VND, total)
		if err != nil {
			return "", err
		}
		baseBefore := p.Assets[symbol].Base
		if err := p.ApplyCashDividend(symbol, total, balance, now); err != nil {
			return "", err
		}
		return "Applied cash dividend for " + symbol + ": " + FormatVND(float64(record.VNDPerShare)) +
			" × " + formatShareQuantity(held) + " = " + FormatVND(float64(total)) +
			"\nBalance: " + FormatVND(balance) +
			"\nCost basis: " + formatVNDNumber(baseBefore) + " → " + FormatVND(p.Assets[symbol].Base), nil

	case DividendKindShares:
		ratio := shareRatio{owned: record.OwnedShares, new: record.NewShares,
			raw: strconv.FormatInt(record.OwnedShares, 10) + ":" + strconv.FormatInt(record.NewShares, 10)}
		newShares, err := shareDividendEntitlement(held, ratio)
		if err != nil {
			return "", err
		}
		if newShares == 0 {
			return "", errors.New("share dividend rounds down to zero")
		}
		finalHolding, err := checkedHoldingAfterDividend(held, newShares)
		if err != nil {
			return "", err
		}
		if err := p.ApplyShareDividend(symbol, finalHolding, now); err != nil {
			return "", err
		}
		return "Applied share dividend for " + symbol + " (" + ratio.raw + "): +" +
			formatShareQuantity(newShares) + "\nHolding: " + formatShareQuantity(held) + " → " +
			formatShareQuantity(finalHolding), nil
	default:
		return "", errors.New("unsupported dividend kind")
	}
}
