package alias

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

const (
	// maxInlineResults is Telegram's cap on results per answerInlineQuery.
	maxInlineResults = 50

	// inlineCacheSeconds is how long Telegram may reuse an answer for the same
	// query. Kept short because the namespace is shared and writable: a name
	// saved now should show up in the picker within seconds, not minutes.
	inlineCacheSeconds = 5

	// inlineTimeout bounds one answer, far tighter than the 10s handlerTimeout
	// the command handlers use.
	//
	// An inline query has a shelf life: Telegram invalidates the id and rejects
	// the answer as "query is too old". Every keystroke opens a new query, and
	// updates are dispatched one at a time, so a single slow answer also holds
	// up the queries queued behind it — which are themselves ageing while they
	// wait. Giving up early loses one result set; running long loses the whole
	// burst.
	inlineTimeout = 3 * time.Second
)

// handleInline answers "@botname <prefix>" with the matching aliases.
//
// This is the reason the module stores a file_id rather than bytes: every
// result below is a "Cached" inline type, which takes an id Telegram already
// holds. Nothing is uploaded, and the picker renders real previews.
func (s *state) handleInline(ctx context.Context, b *bot.Bot, update *models.Update) error {
	query := update.InlineQuery
	if query == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, inlineTimeout)
	defer cancel()
	started := time.Now()

	// One round trip for names *and* values. Reading the names and then each
	// alias would cost a round trip per saved name on every keystroke, which is
	// exactly the latency an expiring query cannot absorb.
	docs, err := s.store.Scan(ctx, "")
	if err != nil {
		log.Error("alias_inline_scan", "err", err)
		// Answer with nothing rather than leaving the client spinning. An empty
		// answer is also what Telegram expects when a query has no matches.
		return s.answer(ctx, b, query.ID, nil, started)
	}

	// Scan returns key order, so the picker is sorted with no sort here.
	prefix := strings.ToLower(strings.TrimSpace(query.Query))
	results := make([]models.InlineQueryResult, 0, min(len(docs), maxInlineResults))
	for _, doc := range docs {
		if prefix != "" && !strings.HasPrefix(doc.ID, prefix) {
			continue
		}
		if r := inlineResult(doc.ID, doc.Val); r != nil {
			results = append(results, r)
		}
		if len(results) == maxInlineResults {
			break // Telegram's cap; the rest stay reachable by name
		}
	}
	return s.answer(ctx, b, query.ID, results, started)
}

func (s *state) answer(ctx context.Context, b *bot.Bot, queryID string, results []models.InlineQueryResult, started time.Time) error {
	_, err := b.AnswerInlineQuery(ctx, &bot.AnswerInlineQueryParams{
		InlineQueryID: queryID,
		Results:       results,
		CacheTime:     inlineCacheSeconds,
	})
	if err != nil {
		// The elapsed time is the whole diagnosis when Telegram rejects the
		// answer as stale: it separates "this handler was slow" from "the query
		// was already old when it reached us".
		return fmt.Errorf("answer %d results after %s: %w",
			len(results), time.Since(started).Round(time.Millisecond), err)
	}
	return nil
}

// inlineResult maps one alias to the inline result type that carries it, or nil
// when the kind has no cached inline form.
//
// Telegram defines no InlineQueryResultCachedVideoNote, so a video-note alias
// simply does not appear in the picker — it stays reachable through /insert and
// its own /name. Returning nil rather than substituting another type is
// deliberate: sending a round video note as a plain video would change what the
// user saved.
func inlineResult(name string, a Alias) models.InlineQueryResult {
	switch a.Kind {
	case kindSticker:
		// No Title field on this type — Telegram shows the sticker itself.
		return &models.InlineQueryResultCachedSticker{ID: name, StickerFileID: a.FileID}
	case kindPhoto:
		return &models.InlineQueryResultCachedPhoto{
			ID: name, PhotoFileID: a.FileID, Title: name, Caption: a.Text, CaptionEntities: a.Entities,
		}
	case kindAnimation:
		return &models.InlineQueryResultCachedGif{
			ID: name, GifFileID: a.FileID, Title: name, Caption: a.Text, CaptionEntities: a.Entities,
		}
	case kindVideo:
		return &models.InlineQueryResultCachedVideo{
			ID: name, VideoFileID: a.FileID, Title: name, Caption: a.Text, CaptionEntities: a.Entities,
		}
	case kindAudio:
		return &models.InlineQueryResultCachedAudio{
			ID: name, AudioFileID: a.FileID, Caption: a.Text, CaptionEntities: a.Entities,
		}
	case kindVoice:
		return &models.InlineQueryResultCachedVoice{
			ID: name, VoiceFileID: a.FileID, Title: name, Caption: a.Text, CaptionEntities: a.Entities,
		}
	case kindDocument:
		return &models.InlineQueryResultCachedDocument{
			ID: name, DocumentFileID: a.FileID, Title: name, Caption: a.Text, CaptionEntities: a.Entities,
		}
	case kindText:
		return &models.InlineQueryResultArticle{
			ID:          name,
			Title:       name,
			Description: a.Text,
			InputMessageContent: &models.InputTextMessageContent{
				MessageText: a.Text,
				Entities:    a.Entities,
			},
		}
	}
	return nil
}
