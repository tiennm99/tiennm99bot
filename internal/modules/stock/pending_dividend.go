package stock

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	dividendCallbackPrefix = "stock_div:"
	pendingDividendPrefix  = "pending-dividend:"
	pendingDividendTTL     = 24 * time.Hour
	// Telegram rejects inline-button callback data longer than 64 bytes.
	maxDividendCallbackBytes = 64
)

// PendingDividendStore is the typed view of the module collection that holds
// pending dividend actions under pendingDividendPrefix keys.
type PendingDividendStore = storage.DocStore[PendingDividendAction]

// PendingDividendAction is the server-side half of an inline button. Financial
// values live in the user's dividend history; the button's callback data
// carries only the owner user ID and provider event ID, and every press is
// validated against the stored owner, chat, and message binding.
type PendingDividendAction struct {
	OwnerUserID      int64  `json:"ownerUserId" bson:"ownerUserId"`
	ChatID           int64  `json:"chatId" bson:"chatId"`
	MessageID        int    `json:"messageId" bson:"messageId"`
	ProviderEventID  string `json:"providerEventId" bson:"providerEventId"`
	Symbol           string `json:"symbol" bson:"symbol"`
	PositionOpenedAt int64  `json:"positionOpenedAt,omitempty" bson:"positionOpenedAt,omitempty"`
	CreatedAt        int64  `json:"createdAt" bson:"createdAt"`
	ExpiresAt        int64  `json:"expiresAt" bson:"expiresAt"`
}

// pendingDividendKey is deterministic per (user, event): re-suggesting the
// same dividend overwrites the previous action instead of accumulating keys,
// and applying it leaves exactly one key to delete.
func pendingDividendKey(userID int64, eventID string) string {
	return pendingDividendPrefix + strconv.FormatInt(userID, 10) + ":" + eventID
}

// dividendCallbackData builds the inline-button payload. It fails only when
// the provider event ID is malformed or would push the payload past
// Telegram's byte limit.
func dividendCallbackData(userID int64, eventID string) (string, bool) {
	if userID <= 0 || !ssiProviderIDPattern.MatchString(eventID) {
		return "", false
	}
	data := dividendCallbackPrefix + strconv.FormatInt(userID, 10) + ":" + eventID
	if len(data) > maxDividendCallbackBytes {
		return "", false
	}
	return data, true
}

// parseDividendCallback recovers the owner user ID and provider event ID from
// button callback data. Callback data is client-controlled, so both parts are
// validated here and the resolved action is re-checked against the presser.
func parseDividendCallback(data string) (int64, string, bool) {
	if len(data) > maxDividendCallbackBytes {
		return 0, "", false
	}
	rest, ok := strings.CutPrefix(data, dividendCallbackPrefix)
	if !ok {
		return 0, "", false
	}
	ownerPart, eventID, ok := strings.Cut(rest, ":")
	if !ok || !ssiProviderIDPattern.MatchString(eventID) {
		return 0, "", false
	}
	ownerID, err := strconv.ParseInt(ownerPart, 10, 64)
	if err != nil || ownerID <= 0 {
		return 0, "", false
	}
	return ownerID, eventID, true
}

// cleanupExpiredDividends deletes pending actions past ExpiresAt. It is
// best-effort: list and delete failures are ignored because every button press
// re-checks expiry anyway.
func (s *state) cleanupExpiredDividends(ctx context.Context, now int64) {
	if s.pending == nil {
		return
	}
	keys, err := s.pending.List(ctx, pendingDividendPrefix)
	if err != nil {
		return
	}
	for _, key := range keys {
		action, _, getErr := s.pending.Get(ctx, key)
		if getErr == nil && action.ExpiresAt > 0 && action.ExpiresAt <= now {
			_ = s.pending.Delete(ctx, key)
		}
	}
}
