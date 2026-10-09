package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SetInlineGameScore records a game score on a message sent via the bot
// (inline mode, or a t.me/<bot>?game= share), addressed by inline_message_id.
//
// go-telegram/bot v1.20.0 cannot make this call: SetGameScoreParams types
// InlineMessageID as an int while Telegram's id is a string, and SetGameScore
// always decodes a Message while Telegram answers an inline target with true.
// This is a plain JSON POST instead. The request URL carries the bot token, so
// errors never include it.
//
// A score that does not beat the user's current one is rejected by Telegram
// with BOT_SCORE_NOT_MODIFIED; the error description is returned as is so the
// caller can classify it.
func SetInlineGameScore(ctx context.Context, token, inlineMessageID string, userID int64, score int) error {
	return setInlineGameScoreAt(ctx, telegramAPIBase, token, inlineMessageID, userID, score)
}

// setInlineGameScoreAt is the testable core; base points tests at an httptest
// server instead of the live API.
func setInlineGameScoreAt(ctx context.Context, base, token, inlineMessageID string, userID int64, score int) error {
	payload, err := json.Marshal(map[string]any{
		"user_id":           userID,
		"score":             score,
		"inline_message_id": inlineMessageID,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/setGameScore", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("setGameScore: build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// *url.Error embeds the URL, which carries the token.
		return fmt.Errorf("setGameScore: request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("setGameScore: read response: %w", err)
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("decode setGameScore response (status %d): %w", resp.StatusCode, err)
	}
	if !r.OK {
		return fmt.Errorf("setGameScore rejected (status %d): %s", resp.StatusCode, r.Description)
	}
	return nil
}
