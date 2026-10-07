package stock

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
)

const (
	// dividendFetchTimeout bounds all provider fetches for one /stock_portfolio
	// run, independent of the SSI client's per-request timeout.
	dividendFetchTimeout = 12 * time.Second
	dividendFetchWorkers = 4
)

type dividendRef struct {
	symbol  string
	eventID string
}

// dividendFetchJob is one provider query. A recent job discovers every event
// in the discovery window for a held ticker; a historical job re-fetches one
// Asia/Saigon publication day to refresh only targetIDs: unprocessed events
// published before that window whose Record date is unknown or already reached.
type dividendFetchJob struct {
	symbol    string
	after     time.Time
	through   time.Time
	recent    bool
	targetIDs map[string]struct{}
}

type dividendCheckResult struct {
	job    dividendFetchJob
	events []DividendEvent
	err    error
}

// includes drops events outside the job's own range, including the provider's
// day-granularity overlap, and restricts historical jobs to their targets.
func (job dividendFetchJob) includes(event DividendEvent) bool {
	if event.Symbol != job.symbol || event.PublishedAt.Before(job.after) || event.PublishedAt.After(job.through) {
		return false
	}
	if job.recent {
		return true
	}
	_, wanted := job.targetIDs[event.ProviderID]
	return wanted
}

// notifyDividendEvents syncs dividend history, then posts one message per
// unprocessed event on a held ticker: a notice before Record date, or an
// "Apply dividend" suggestion once it is due. Failed tickers are summarized in
// a single reply instead of failing the portfolio command.
func (s *state) notifyDividendEvents(ctx context.Context, b *bot.Bot, msg *models.Message, userID int64, snapshot Portfolio, checkedThrough time.Time) error {
	if s.pending != nil {
		s.cleanupExpiredDividends(ctx, checkedThrough.UnixMilli())
	}

	failed, err := s.syncDividendHistory(ctx, userID, snapshot, checkedThrough)
	if err != nil {
		log.Error("stock_save_dividend_history", "user", userID, "err", err)
		if replyErr := chathelper.Reply(ctx, b, msg, "Dividend events were checked, but the history could not be saved. Try again later."); replyErr != nil {
			return replyErr
		}
		return nil
	}

	latest, err := LoadPortfolio(ctx, s.store, userID, checkedThrough.UnixMilli())
	if err != nil {
		return err
	}
	refs := sortedDividendRefs(latest)
	for _, ref := range refs {
		position, held := latest.Assets[ref.symbol]
		if !held || position.Quantity <= 0 {
			continue
		}
		record, exists := latest.dividendRecord(ref.symbol, ref.eventID)
		if !exists || record.Processed {
			continue
		}
		if !dividendRecordDue(record, checkedThrough) {
			if err := s.sendFutureDividendNotice(ctx, b, msg, userID, position.OpenedAt, ref); err != nil {
				log.Error("stock_send_dividend_notice", "user", userID, "ticker", ref.symbol, "event", ref.eventID, "err", err)
				failed = append(failed, ref.symbol)
			}
			continue
		}
		if err := s.sendDividendSuggestion(ctx, b, msg, userID, position.OpenedAt, ref); err != nil {
			log.Error("stock_send_dividend_suggestion", "user", userID, "ticker", ref.symbol, "event", ref.eventID, "err", err)
			failed = append(failed, ref.symbol)
		}
	}

	if len(failed) > 0 {
		sort.Strings(failed)
		failed = uniqueStrings(failed)
		return chathelper.Reply(ctx, b, msg, "Dividend events could not be checked for "+strings.Join(failed, ", ")+". Try again later.")
	}
	return nil
}

// syncDividendHistory fetches events without the user lock, then reloads the
// portfolio under it to prune expired history and merge the results, so a
// concurrent trade is never overwritten by the pre-fetch snapshot. It returns
// the tickers whose fetch failed.
func (s *state) syncDividendHistory(ctx context.Context, userID int64, snapshot Portfolio, now time.Time) ([]string, error) {
	jobs := dividendFetchJobs(snapshot, now)
	results := s.fetchDividendJobs(ctx, jobs)
	failed := make([]string, 0)

	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()
	latest, err := LoadPortfolio(ctx, s.store, userID, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	changed := latest.pruneDividendHistory(now)
	for _, result := range results {
		if result.err != nil {
			log.Error("stock_dividend_events_checked",
				"user", userID,
				"ticker", result.job.symbol,
				"from", result.job.after.UnixMilli(),
				"through", result.job.through.UnixMilli(),
				"events", 0,
				"status", "error",
				"err", result.err)
			failed = append(failed, result.job.symbol)
			continue
		}
		log.Info("stock_dividend_events_checked",
			"user", userID,
			"ticker", result.job.symbol,
			"from", result.job.after.UnixMilli(),
			"through", result.job.through.UnixMilli(),
			"events", len(result.events),
			"status", "success")
		for _, event := range result.events {
			if !result.job.includes(event) {
				continue
			}
			if dividendRecordExpired(dividendRecordFromEvent(event), now) {
				continue
			}
			if latest.upsertDividendEvent(event) {
				changed = true
			}
		}
	}
	if changed {
		if err := SavePortfolio(ctx, s.store, userID, latest); err != nil {
			return failed, err
		}
	}
	return failed, nil
}

func dividendFetchJobs(p Portfolio, now time.Time) []dividendFetchJob {
	recentAfter := now.Add(-dividendDiscoveryWindow)
	jobs := make([]dividendFetchJob, 0, len(p.Assets))
	for symbol, position := range p.Assets {
		if position.Quantity > 0 {
			jobs = append(jobs, dividendFetchJob{symbol: symbol, after: recentAfter, through: now, recent: true})
		}
	}

	type historicalKey struct {
		symbol string
		day    int64
	}
	historical := map[historicalKey]map[string]struct{}{}
	for symbol, events := range p.Dividends {
		if position, held := p.Assets[symbol]; !held || position.Quantity <= 0 {
			continue
		}
		for eventID, event := range events {
			if event.Processed || event.PublishedAt <= 0 || !millisTime(event.PublishedAt).Before(recentAfter) {
				continue
			}
			if event.RecordDate != 0 && !dividendRecordDue(event, now) {
				continue
			}
			day := startOfSaigonDay(millisTime(event.PublishedAt))
			key := historicalKey{symbol: symbol, day: day.UnixMilli()}
			if historical[key] == nil {
				historical[key] = map[string]struct{}{}
			}
			historical[key][eventID] = struct{}{}
		}
	}
	for key, targetIDs := range historical {
		day := millisTime(key.day)
		jobs = append(jobs, dividendFetchJob{
			symbol: key.symbol, after: day, through: day.Add(24*time.Hour - time.Millisecond), targetIDs: targetIDs,
		})
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].symbol != jobs[j].symbol {
			return jobs[i].symbol < jobs[j].symbol
		}
		return jobs[i].after.Before(jobs[j].after)
	})
	return jobs
}

func (s *state) fetchDividendJobs(ctx context.Context, jobs []dividendFetchJob) []dividendCheckResult {
	if len(jobs) == 0 || s.dividends == nil {
		return nil
	}
	results := make([]dividendCheckResult, len(jobs))
	fetchCtx, cancel := context.WithTimeout(ctx, dividendFetchTimeout)
	defer cancel()
	sem := make(chan struct{}, dividendFetchWorkers)
	var wg sync.WaitGroup
	for index, job := range jobs {
		index, job := index, job
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			events, err := s.dividends.FetchDividendEvents(fetchCtx, job.symbol, job.after, job.through)
			results[index] = dividendCheckResult{job: job, events: events, err: err}
		}()
	}
	wg.Wait()
	return results
}

func sortedDividendRefs(p Portfolio) []dividendRef {
	refs := make([]dividendRef, 0)
	for symbol, events := range p.Dividends {
		for eventID := range events {
			refs = append(refs, dividendRef{symbol: symbol, eventID: eventID})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		left := p.Dividends[refs[i].symbol][refs[i].eventID]
		right := p.Dividends[refs[j].symbol][refs[j].eventID]
		if left.PublishedAt != right.PublishedAt {
			return left.PublishedAt < right.PublishedAt
		}
		if refs[i].symbol != refs[j].symbol {
			return refs[i].symbol < refs[j].symbol
		}
		return refs[i].eventID < refs[j].eventID
	})
	return refs
}

// sendFutureDividendNotice posts a non-actionable notice for an event whose
// Record date has not arrived. Like sendDividendSuggestion, it re-checks the
// event and position under the user lock; expectedOpenedAt skips a position
// that was closed and reopened since the history sync.
func (s *state) sendFutureDividendNotice(ctx context.Context, b *bot.Bot, msg *models.Message, userID, expectedOpenedAt int64, ref dividendRef) error {
	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()

	now := s.now()
	p, err := LoadPortfolio(ctx, s.store, userID, now.UnixMilli())
	if err != nil {
		return err
	}
	record, exists := p.dividendRecord(ref.symbol, ref.eventID)
	if !exists || record.Processed || dividendRecordDue(record, now) {
		return nil
	}
	position, held := p.Assets[ref.symbol]
	if !held || position.Quantity <= 0 || position.OpenedAt != expectedOpenedAt {
		return nil
	}
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: msg.MessageThreadID,
		Text:            dividendEventText(record.event(ref.symbol, ref.eventID), position.Quantity, false),
		ParseMode:       models.ParseModeHTML,
	}); err != nil {
		return err
	}
	return nil
}

// sendDividendSuggestion posts the actionable message and then binds its
// pending action to the sent message ID, so only that message's button works.
func (s *state) sendDividendSuggestion(ctx context.Context, b *bot.Bot, msg *models.Message, userID, expectedOpenedAt int64, ref dividendRef) error {
	defer s.locks.Acquire(strconv.FormatInt(userID, 10))()

	now := s.now()
	p, err := LoadPortfolio(ctx, s.store, userID, now.UnixMilli())
	if err != nil {
		return err
	}
	record, exists := p.dividendRecord(ref.symbol, ref.eventID)
	if !exists || record.Processed || !dividendRecordDue(record, now) {
		return nil
	}
	position, held := p.Assets[ref.symbol]
	if !held || position.Quantity <= 0 || position.OpenedAt != expectedOpenedAt || !positionOpenedByRecordDate(position, record) {
		return nil
	}
	if s.pending == nil {
		return errors.New("stock: pending dividend store unavailable")
	}
	data, ok := dividendCallbackData(userID, ref.eventID)
	if !ok {
		return fmt.Errorf("stock: dividend event %q cannot be encoded as callback data", ref.eventID)
	}
	key := pendingDividendKey(userID, ref.eventID)
	previous, _, previousErr := s.pending.Get(ctx, key)
	action := PendingDividendAction{
		OwnerUserID: userID, ChatID: msg.Chat.ID, ProviderEventID: ref.eventID, Symbol: ref.symbol,
		PositionOpenedAt: position.OpenedAt, CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(pendingDividendTTL).UnixMilli(),
	}
	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          msg.Chat.ID,
		MessageThreadID: msg.MessageThreadID,
		Text:            dividendEventText(record.event(ref.symbol, ref.eventID), position.Quantity, true),
		ParseMode:       models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{
			Text: "Apply dividend", CallbackData: data,
		}}}},
	})
	if err != nil {
		return err
	}
	action.MessageID = sent.ID
	if err := s.pending.Put(ctx, key, action); err != nil {
		_ = removeDividendButton(ctx, b, msg.Chat.ID, sent.ID)
		return fmt.Errorf("bind dividend action message: %w", err)
	}
	// The Put above superseded any earlier suggestion for this event; retire
	// the old message's button so only the newest suggestion is pressable.
	if previousErr == nil && previous.MessageID != 0 && (previous.ChatID != msg.Chat.ID || previous.MessageID != sent.ID) {
		_ = removeDividendButton(ctx, b, previous.ChatID, previous.MessageID)
	}
	return nil
}

func dividendEventText(event DividendEvent, observedHolding int64, actionable bool) string {
	var value string
	switch event.Kind {
	case DividendKindCash:
		value = FormatVND(float64(event.VNDPerShare)) + "/share"
	case DividendKindShares:
		value = strconv.FormatInt(event.OwnedShares, 10) + ":" + strconv.FormatInt(event.NewShares, 10) + " shares"
	}
	headline := "Upcoming dividend event · "
	if actionable {
		headline = "Dividend ready to apply · "
	}
	lines := []string{"<b>" + headline + html.EscapeString(event.Symbol) + "</b>", html.EscapeString(value)}
	if !event.ExDate.IsZero() {
		lines = append(lines, "Ex-right: "+event.ExDate.Format("02/01/2006"))
	}
	if !event.RecordDate.IsZero() {
		lines = append(lines, "Record: "+event.RecordDate.Format("02/01/2006"))
	} else {
		lines = append(lines, "Record: awaiting SSI update")
	}
	if !event.PaymentDate.IsZero() {
		lines = append(lines, "Payment/trading: "+event.PaymentDate.Format("02/01/2006"))
	}
	if title := truncateRunes(strings.TrimSpace(event.Title), 240); title != "" {
		lines = append(lines, html.EscapeString(title))
	}
	lines = append(lines, "SSI event: <code>"+html.EscapeString(event.ProviderID)+"</code>")
	if actionable {
		lines = append(lines,
			"Current holding: "+html.EscapeString(formatShareQuantity(observedHolding))+" shares.",
			"The dividend uses your current holding when you accept.",
			"Button expires in 24 hours.",
		)
	} else {
		lines = append(lines, "Approval becomes available from Record date.")
	}
	return strings.Join(lines, "\n")
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}
