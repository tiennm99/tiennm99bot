package blacklist

import (
	"context"
	"errors"
	"fmt"
	"html"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/log"
	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/chathelper"
	"github.com/tiennm99/tiennm99bot/internal/storage"
)

const (
	// handlerTimeout bounds every handler. The bot dispatches updates inline on
	// a single worker with no deadline of its own, so without this the
	// library's 60s per-call HTTP ceiling is the only bound. These handlers
	// only touch storage, so the budget is generous.
	handlerTimeout = 10 * time.Second

	// maxListBytes keeps every list reply — /blacklist_rules, a bare
	// /blacklist, and the listing appended to each add and remove — inside
	// Telegram's 4096-character sendMessage limit, with room for the second
	// heading and a trim notice after the budget is spent.
	//
	// The budget counts the <code> markup, not only the entries: Telegram
	// measures the message it is sent, and at 13 bytes a pair the tags outweigh
	// a short entry.
	maxListBytes = 3800
)

const genericFailure = "Something went wrong. Try again in a moment."

// The three ways resolveText can fail, kept apart because saying which one
// happened is most of the value of the reply.
var (
	errNoText    = errors.New("blacklist: nothing to read text from")
	errEmptyText = errors.New("blacklist: text normalizes to nothing")
	errLongText  = errors.New("blacklist: text too long to store as a rule")
)

// threadOf returns the scope of a message: one forum topic, or the whole chat.
//
// MessageThreadID on its own is not enough to identify a topic. Telegram
// associates a thread id with any reply chain in a supergroup, not only with a
// forum topic, so trusting the field alone would give a reply-form
// /blacklist_add its own scope — one that a later standalone /blacklist_rules
// in the same chat could never read back. IsTopicMessage is the flag that marks
// a real forum topic, so everything else — a plain group, a DM, a forum's
// General topic — is thread 0.
func threadOf(msg *models.Message) (int64, int) {
	if !msg.IsTopicMessage {
		return msg.Chat.ID, 0
	}
	return msg.Chat.ID, msg.MessageThreadID
}

// listName names a list tag for a sentence, listTitle for a heading.
func listName(list string) string {
	if list == listWhite {
		return "whitelist"
	}
	return "blacklist"
}

func listTitle(list string) string {
	if list == listWhite {
		return "Whitelist"
	}
	return "Blacklist"
}

// textOf reads the text of a message, falling back to a caption so replying to
// a captioned photo works the same way as replying to a plain message.
func textOf(msg *models.Message) string {
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}

// resolveText picks the entry text out of an update: the command argument when
// there is one, otherwise — when allowReply — the replied-to message's text.
//
// It returns the raw text for echoing back and the normalized form for keying.
// The length cap is checked against both: a user reads the raw text they typed,
// while the key is built from the normalized one, and NFKC can expand as easily
// as it can contract.
func resolveText(msg *models.Message, allowReply bool) (raw, normText string, err error) {
	raw = chathelper.ArgAfterCommand(msg.Text)
	if raw == "" && allowReply && msg.ReplyToMessage != nil {
		raw = textOf(msg.ReplyToMessage)
	}
	raw = strings.TrimSpace(raw)

	switch {
	case raw == "":
		return "", "", errNoText
	case len(raw) > maxEntryBytes:
		return "", "", errLongText
	}

	normText, ok := Normalize(raw)
	switch {
	case !ok:
		// Defensive: TrimSpace and Normalize agree on what whitespace is, so
		// non-empty raw text should always normalize to something.
		return "", "", errEmptyText
	case len(normText) > maxEntryBytes:
		return "", "", errLongText
	}
	return raw, normText, nil
}

// usageFor words a resolveText failure for the user.
func usageFor(command string, err error, allowReply bool) string {
	switch {
	case errors.Is(err, errLongText):
		return fmt.Sprintf(
			"That text is too long to keep as a rule. Keep it to at most %d bytes — roughly %d plain letters, or a third of that in Vietnamese.",
			maxEntryBytes, maxEntryBytes)
	case errors.Is(err, errEmptyText):
		return "There is nothing in that text to store as a rule."
	case allowReply:
		return fmt.Sprintf("Usage: /%s [text...] — or reply to a message with /%s.", command, command)
	default:
		return fmt.Sprintf("Usage: /%s <text...>", command)
	}
}

// get reads an entry. A missing key is not an error — it is the normal state
// for text nobody has listed.
func (s *state) get(ctx context.Context, key string) (Entry, bool, error) {
	entry, _, err := s.store.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	return entry, true, nil
}

// replyWithList answers an add or a remove with its confirmation followed by
// the current contents of the list, so the sender sees the result of what they
// just did without running /blacklist_rules.
//
// Every outcome of those four commands ends here, including the two that change
// nothing: "already present" and "not there" are exactly when someone wants to
// see what the list actually holds, and "every add and remove shows the list"
// is a simpler rule to rely on than one conditional on whether a write landed.
//
// The confirmation goes into the builder first, so renderSection's byte budget
// already accounts for it and the combined message still fits one reply.
//
// A failed read here is logged but not reported: the change itself succeeded,
// and answering a completed write with a generic failure would be a lie.
func (s *state) replyWithList(ctx context.Context, b *bot.Bot, msg *models.Message, list, prefix, confirmation string) error {
	var sb strings.Builder
	sb.WriteString(confirmation)

	docs, err := s.store.Scan(ctx, prefix)
	if err != nil {
		log.Error("blacklist_mutation_scan", "list", list, "err", err)
		return chathelper.ReplyHTML(ctx, b, msg, sb.String())
	}
	renderSection(&sb, list, prefix, docs)
	return chathelper.ReplyHTML(ctx, b, msg, sb.String())
}

// handleAdd stores text in one of the thread's two lists.
func (s *state) handleAdd(list string) modules.CommandHandler {
	command := listName(list) + "_add"
	return func(ctx context.Context, b *bot.Bot, update *models.Update) error {
		ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
		defer cancel()

		msg := update.Message
		if msg == nil {
			return nil
		}

		raw, normText, err := resolveText(msg, true)
		if err != nil {
			return chathelper.Reply(ctx, b, msg, usageFor(command, err, true))
		}

		chatID, threadID := threadOf(msg)
		prefix := scopePrefix(chatID, threadID, list)
		key := entryKey(chatID, threadID, list, normText)

		// Read before writing purely to word the reply. The write is
		// unconditional either way, so a concurrent add costs a wrong verb in
		// one sentence, not wrong stored state.
		existing, found, err := s.get(ctx, key)
		if err != nil {
			log.Error("blacklist_add_lookup", "list", list, "err", err)
			return chathelper.Reply(ctx, b, msg, genericFailure)
		}
		if found {
			return s.replyWithList(ctx, b, msg, list, prefix, fmt.Sprintf(
				"<code>%s</code> is already in this topic's %s.",
				html.EscapeString(existing.Text), listName(list)))
		}

		entry := Entry{Text: raw, CreatedAt: chathelper.NowMillis()}
		if msg.From != nil {
			entry.OwnerID = msg.From.ID
		}
		if err := s.store.Put(ctx, key, entry); err != nil {
			log.Error("blacklist_add", "list", list, "err", err)
			return chathelper.Reply(ctx, b, msg, genericFailure)
		}
		return s.replyWithList(ctx, b, msg, list, prefix, fmt.Sprintf(
			"Added <code>%s</code> to this topic's %s.",
			html.EscapeString(raw), listName(list)))
	}
}

// handleDel removes text from one of the thread's two lists.
//
// Anyone may remove anyone's entry. The list belongs to the thread, so the
// permission model does too; a per-owner rule would strand entries whose adder
// has left the group.
func (s *state) handleDel(list string) modules.CommandHandler {
	command := listName(list) + "_del"
	return func(ctx context.Context, b *bot.Bot, update *models.Update) error {
		ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
		defer cancel()

		msg := update.Message
		if msg == nil {
			return nil
		}

		raw, normText, err := resolveText(msg, false)
		if err != nil {
			return chathelper.Reply(ctx, b, msg, usageFor(command, err, false))
		}

		chatID, threadID := threadOf(msg)
		prefix := scopePrefix(chatID, threadID, list)
		key := entryKey(chatID, threadID, list, normText)

		// Read first so absent text is reported as such. Delete on a missing
		// key is indistinguishable from a successful one in the store
		// contract, and "removed" for something that was never there reads as
		// a bug.
		if _, found, err := s.get(ctx, key); err != nil {
			log.Error("blacklist_del_lookup", "list", list, "err", err)
			return chathelper.Reply(ctx, b, msg, genericFailure)
		} else if !found {
			return s.replyWithList(ctx, b, msg, list, prefix, fmt.Sprintf(
				"<code>%s</code> is not in this topic's %s.",
				html.EscapeString(raw), listName(list)))
		}

		if err := s.store.Delete(ctx, key); err != nil {
			log.Error("blacklist_del", "list", list, "err", err)
			return chathelper.Reply(ctx, b, msg, genericFailure)
		}
		return s.replyWithList(ctx, b, msg, list, prefix, fmt.Sprintf(
			"Removed <code>%s</code> from this topic's %s.",
			html.EscapeString(raw), listName(list)))
	}
}

// entriesFor returns one list's normalized entries, sorted.
//
// The key holds the normalized text, so this needs no per-entry document read —
// the difference between one round trip and one per rule on the /blacklist_check
// path.
func (s *state) entriesFor(ctx context.Context, chatID int64, threadID int, list string) ([]string, error) {
	prefix := scopePrefix(chatID, threadID, list)
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, decodeKeyText(strings.TrimPrefix(k, prefix)))
	}
	sort.Strings(out)
	return out, nil
}

// handleCheck judges a text against this thread's rules.
func (s *state) handleCheck(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}

	normText, ok := Normalize(chathelper.ArgAfterCommand(msg.Text))
	if !ok {
		return chathelper.Reply(ctx, b, msg, "Usage: /blacklist_check <text...>")
	}
	return s.checkText(ctx, b, msg, normText)
}

// handleShort is /blacklist: the whole module behind one short name. Bare it
// lists both lists, with an argument it judges that text.
//
// The two behaviours are the two questions someone actually has — "what is set
// up here?" and "is this blocked?" — and neither needs its own long name once
// the argument distinguishes them.
func (s *state) handleShort(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}

	normText, ok := Normalize(chathelper.ArgAfterCommand(msg.Text))
	if !ok {
		return s.showRules(ctx, b, msg)
	}
	return s.checkText(ctx, b, msg, normText)
}

// checkText answers the verdict for already-normalized text.
//
// Unlike the mutation commands this applies no length cap: judging a long
// message is the point, and nothing here becomes a storage key.
func (s *state) checkText(ctx context.Context, b *bot.Bot, msg *models.Message, normText string) error {
	chatID, threadID := threadOf(msg)
	black, err := s.entriesFor(ctx, chatID, threadID, listBlack)
	if err != nil {
		log.Error("blacklist_check_list", "list", listBlack, "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}
	white, err := s.entriesFor(ctx, chatID, threadID, listWhite)
	if err != nil {
		log.Error("blacklist_check_list", "list", listWhite, "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}

	return chathelper.ReplyHTML(ctx, b, msg, renderVerdict(Check(normText, black, white)))
}

// renderVerdict words the three shapes a Verdict comes in.
//
// The rescued case names both entries rather than just saying "allowed": it is
// the only way someone who added an exception can confirm it is doing anything.
func renderVerdict(v Verdict) string {
	switch {
	case v.Blocked:
		return fmt.Sprintf("🚫 Blacklisted in this topic — matches <code>%s</code>.",
			html.EscapeString(v.Entry))
	case v.Entry != "":
		return fmt.Sprintf(
			"✅ Allowed in this topic. It matches <code>%s</code>, but the whitelist entry <code>%s</code> covers it.",
			html.EscapeString(v.Entry), html.EscapeString(v.RescuedBy))
	default:
		return "✅ Allowed in this topic — nothing in the blacklist matches."
	}
}

// handleRules lists both of the thread's lists in one message.
func (s *state) handleRules(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}
	return s.showRules(ctx, b, msg)
}

// showRules renders both lists for the message's thread.
func (s *state) showRules(ctx context.Context, b *bot.Bot, msg *models.Message) error {
	chatID, threadID := threadOf(msg)
	blackPrefix := scopePrefix(chatID, threadID, listBlack)
	whitePrefix := scopePrefix(chatID, threadID, listWhite)

	// Scan rather than List: this needs the stored text of every entry, and
	// Scan reads a whole list in one round trip where List would cost a Get per
	// rule. Handlers run inline on the bot's single update worker, so a read
	// that scales with the entry count is how an ordinary store latency becomes
	// a request that expires before it is answered.
	black, err := s.store.Scan(ctx, blackPrefix)
	if err != nil {
		log.Error("blacklist_rules_scan", "list", listBlack, "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}
	white, err := s.store.Scan(ctx, whitePrefix)
	if err != nil {
		log.Error("blacklist_rules_scan", "list", listWhite, "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}

	var sb strings.Builder
	renderSection(&sb, listBlack, blackPrefix, black)
	sb.WriteString("\n")
	renderSection(&sb, listWhite, whitePrefix, white)
	return chathelper.ReplyHTML(ctx, b, msg, sb.String())
}

// renderSection appends one headed list, trimmed to what is left of the shared
// byte budget.
//
// Both sections draw on the one budget, and the heading is written before the
// budget is consulted, so a blacklist long enough to fill the message still
// leaves the whitelist visibly present rather than silently absent.
//
// Scan returns entries ordered by key, which is their normalized form, so the
// listing is stable across calls without a sort here.
func renderSection(sb *strings.Builder, list, prefix string, docs []storage.Doc[Entry]) {
	fmt.Fprintf(sb, "\n<b>%s</b> (%d)", listTitle(list), len(docs))
	if len(docs) == 0 {
		sb.WriteString("\n— nothing yet")
		return
	}

	for i, doc := range docs {
		// The record carries what the adder typed; the key carries only the
		// normalized form, which is the fallback if a record ever lacks text.
		text := doc.Val.Text
		if text == "" {
			text = decodeKeyText(strings.TrimPrefix(doc.ID, prefix))
		}

		// Wrapped in <code> so tapping an entry copies it ready to paste into a
		// _del command. Reserve room for the trim notice before committing to a
		// line, so the trim can never be what pushes the message over.
		line := "\n<code>" + html.EscapeString(text) + "</code>"
		if sb.Len()+len(line) > maxListBytes {
			fmt.Fprintf(sb, "\n…and %d more.", len(docs)-i)
			return
		}
		sb.WriteString(line)
	}
}

// handleWhitelistRandom picks one whitelist entry at random.
func (s *state) handleWhitelistRandom(ctx context.Context, b *bot.Bot, update *models.Update) error {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()

	msg := update.Message
	if msg == nil {
		return nil
	}

	chatID, threadID := threadOf(msg)
	prefix := scopePrefix(chatID, threadID, listWhite)
	docs, err := s.store.Scan(ctx, prefix)
	if err != nil {
		log.Error("blacklist_rnd_scan", "list", listWhite, "err", err)
		return chathelper.Reply(ctx, b, msg, genericFailure)
	}
	if len(docs) == 0 {
		return chathelper.Reply(ctx, b, msg,
			"This topic's whitelist is empty. Add something with /whitelist_add first.")
	}

	pick := docs[rand.IntN(len(docs))]
	// The record carries what the adder typed; the key carries only the
	// normalized form, which is the fallback if a record ever lacks text.
	text := pick.Val.Text
	if text == "" {
		text = decodeKeyText(strings.TrimPrefix(pick.ID, prefix))
	}
	return chathelper.ReplyHTML(ctx, b, msg, "<code>"+html.EscapeString(text)+"</code>")
}
