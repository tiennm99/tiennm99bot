package blacklist_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/tiennm99/tiennm99bot/internal/modules"
	"github.com/tiennm99/tiennm99bot/internal/modules/blacklist"
	"github.com/tiennm99/tiennm99bot/internal/storage"
	"github.com/tiennm99/tiennm99bot/internal/testutil"
)

// installBlacklist builds a registry holding only this module. Every command is
// public, so no auth is needed for them to dispatch.
func installBlacklist(t *testing.T) *testutil.RecordingBot {
	t.Helper()
	rb := testutil.NewRecordingBot(t)
	reg, err := modules.Build([]string{"blacklist"},
		map[string]modules.Factory{"blacklist": blacklist.New},
		storage.NewMemoryProvider(), modules.BuildOptions{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	modules.Install(rb.Bot, reg, modules.Auth{})
	return rb
}

// inTopic builds a supergroup message inside a forum topic.
func inTopic(chatID int64, threadID int, text string) *models.Update {
	upd := testutil.NewSupergroupMessage(chatID, 7, text)
	upd.Message.MessageThreadID = threadID
	upd.Message.IsTopicMessage = true
	return upd
}

// send dispatches one command and returns the text of the reply it produced.
func send(t *testing.T, rb *testutil.RecordingBot, upd *models.Update) string {
	t.Helper()
	rb.Reset()
	rb.Bot.ProcessUpdate(context.Background(), upd)
	sent := rb.Sent()
	if len(sent) == 0 {
		t.Fatalf("no reply to %q", upd.Message.Text)
	}
	return sent[len(sent)-1].Text()
}

func TestAdd_StoresAsTypedAndKeysNormalized(t *testing.T) {
	rb := installBlacklist(t)

	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add  Cat  Dog ")); !strings.Contains(got, "Added") {
		t.Fatalf("add reply = %q", got)
	}

	// Matching is normalized, so a differently cased and spaced text hits it.
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check CAT   DOG")); !strings.Contains(got, "🚫") {
		t.Fatalf("check reply = %q; want blocked", got)
	}

	// Listing shows what was typed, not the normalized form.
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules")); !strings.Contains(got, "<code>Cat  Dog</code>") {
		t.Fatalf("rules reply = %q; want the text as typed", got)
	}
}

func TestThreadsAndChatsAreIsolated(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, inTopic(-100, 11, "/blacklist_add cat"))

	// A sibling topic of the same forum.
	if got := send(t, rb, inTopic(-100, 12, "/blacklist_check cat")); strings.Contains(got, "🚫") {
		t.Fatalf("topic 12 saw topic 11's entry: %q", got)
	}
	// A DM, which is its own scope entirely.
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check cat")); strings.Contains(got, "🚫") {
		t.Fatalf("DM saw a group entry: %q", got)
	}
	// The original topic still has it.
	if got := send(t, rb, inTopic(-100, 11, "/blacklist_check cat")); !strings.Contains(got, "🚫") {
		t.Fatalf("topic 11 lost its own entry: %q", got)
	}
}

func TestAdd_FromReply(t *testing.T) {
	rb := installBlacklist(t)
	upd := testutil.NewPrivateMessage(7, "/blacklist_add")
	upd.Message.ReplyToMessage = &models.Message{Text: "xin chào"}

	if got := send(t, rb, upd); !strings.Contains(got, "xin chào") {
		t.Fatalf("add reply = %q; want the replied text", got)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check Xin Chào")); !strings.Contains(got, "🚫") {
		t.Fatalf("check reply = %q; want blocked", got)
	}
}

func TestAdd_FromReplyCaption(t *testing.T) {
	rb := installBlacklist(t)
	upd := testutil.NewPrivateMessage(7, "/blacklist_add")
	upd.Message.ReplyToMessage = &models.Message{Caption: "captioned"}

	if got := send(t, rb, upd); !strings.Contains(got, "captioned") {
		t.Fatalf("add reply = %q; want the caption stored", got)
	}
}

func TestAdd_WithoutTextOrReplyIsUsage(t *testing.T) {
	rb := installBlacklist(t)
	got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add"))
	if !strings.Contains(got, "Usage") || !strings.Contains(got, "reply") {
		t.Fatalf("reply = %q; want usage mentioning the reply form", got)
	}
}

func TestAdd_RefusesTooLongReply(t *testing.T) {
	rb := installBlacklist(t)
	upd := testutil.NewPrivateMessage(7, "/blacklist_add")
	upd.Message.ReplyToMessage = &models.Message{Text: strings.Repeat("a", 4096)}

	got := send(t, rb, upd)
	if !strings.Contains(got, "too long") {
		t.Fatalf("reply = %q; want a length refusal", got)
	}
	if rules := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules")); strings.Contains(rules, "aaa") {
		t.Fatal("an over-long entry was stored anyway")
	}
}

func TestAdd_DuplicateIsReportedNotDoubled(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add cat"))

	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add CAT")); !strings.Contains(got, "already") {
		t.Fatalf("reply = %q; want an already-present notice", got)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules")); !strings.Contains(got, "<b>Blacklist</b> (1)") {
		t.Fatalf("rules reply = %q; want exactly one entry", got)
	}
}

func TestDel_RemovesAndReportsAbsence(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add cat"))

	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_del CAT")); !strings.Contains(got, "Removed") {
		t.Fatalf("reply = %q; want a removal", got)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_del cat")); !strings.Contains(got, "is not in") {
		t.Fatalf("reply = %q; want an absence notice, not a removal", got)
	}
}

// _del takes its text as an argument only, so a bare invocation is usage even
// when it replies to something.
func TestDel_DoesNotTakeTextFromAReply(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add cat"))

	upd := testutil.NewPrivateMessage(7, "/blacklist_del")
	upd.Message.ReplyToMessage = &models.Message{Text: "cat"}
	if got := send(t, rb, upd); !strings.Contains(got, "Usage") {
		t.Fatalf("reply = %q; want usage", got)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check cat")); !strings.Contains(got, "🚫") {
		t.Fatal("the entry was removed via a reply")
	}
}

func TestLists_AreSeparate(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_add exception"))

	got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules"))
	if !strings.Contains(got, "<b>Blacklist</b> (0)") {
		t.Fatalf("rules reply = %q; want an empty blacklist", got)
	}
	if !strings.Contains(got, "<b>Whitelist</b> (1)") {
		t.Fatalf("rules reply = %q; want the whitelist entry", got)
	}
}

func TestRules_EmptyShowsBothHeadings(t *testing.T) {
	rb := installBlacklist(t)
	got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules"))
	for _, want := range []string{"<b>Blacklist</b> (0)", "<b>Whitelist</b> (0)", "nothing yet"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rules reply = %q; missing %q", got, want)
		}
	}
}

func TestRules_EscapesUserText(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add <b>bold</b>"))

	got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules"))
	if strings.Contains(got, "<b>bold</b>") {
		t.Fatalf("rules reply = %q; user markup was not escaped", got)
	}
	if !strings.Contains(got, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("rules reply = %q; want the escaped form", got)
	}
}

func TestRules_TrimsToOneMessage(t *testing.T) {
	rb := installBlacklist(t)
	const entries = 400
	for i := range entries {
		send(t, rb, testutil.NewPrivateMessage(7,
			fmt.Sprintf("/blacklist_add %s%03d", strings.Repeat("x", 30), i)))
	}

	got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules"))
	if len([]rune(got)) > 4096 {
		t.Fatalf("rules reply is %d characters, over Telegram's limit", len([]rune(got)))
	}
	if !strings.Contains(got, "more.") {
		t.Fatalf("rules reply = %q; want a trim notice", got)
	}
	// Both headings survive a blacklist long enough to fill the message.
	if !strings.Contains(got, "<b>Whitelist</b>") {
		t.Fatal("the whitelist heading was trimmed away entirely")
	}
}

// The case the containment rule exists for, end to end through the handlers.
func TestCheck_WhitelistRescuesOnlyWhatItSpans(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add ass"))
	send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_add assassin"))

	tests := []struct {
		text    string
		blocked bool
	}{
		{text: "assassin", blocked: false},
		{text: "dumbass", blocked: true},
		{text: "I met an assassin, dumbass", blocked: true},
		{text: "nothing here", blocked: false},
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check "+tc.text))
			if blocked := strings.Contains(got, "🚫"); blocked != tc.blocked {
				t.Fatalf("check %q = %q; want blocked=%v", tc.text, got, tc.blocked)
			}
		})
	}
}

func TestCheck_NamesTheRescuingEntry(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add ass"))
	send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_add assassin"))

	got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check assassin"))
	if !strings.Contains(got, "<code>ass</code>") || !strings.Contains(got, "<code>assassin</code>") {
		t.Fatalf("check reply = %q; want both entries named", got)
	}
}

func TestCheck_EmptyListsAllowEverything(t *testing.T) {
	rb := installBlacklist(t)
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check anything")); !strings.Contains(got, "✅") {
		t.Fatalf("check reply = %q; want allowed", got)
	}
}

func TestCheck_WithoutTextIsUsage(t *testing.T) {
	rb := installBlacklist(t)
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check")); !strings.Contains(got, "Usage") {
		t.Fatalf("check reply = %q; want usage", got)
	}
}

// Diacritics are significant by design: "ma" and "má" are separate entries.
func TestCheck_DiacriticsAreSignificant(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add ma"))

	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check má")); strings.Contains(got, "🚫") {
		t.Fatalf("check reply = %q; \"má\" must not match the entry \"ma\"", got)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check MA")); !strings.Contains(got, "🚫") {
		t.Fatalf("check reply = %q; case must still fold", got)
	}
}

// An entry containing '/' (which cannot appear literally in a storage key) and
// '%' (the escape marker) must survive the round trip through the store.
func TestEntry_WithKeyHazardsRoundTrips(t *testing.T) {
	rb := installBlacklist(t)
	const hazard = "50%2F/off"
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add "+hazard))

	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules")); !strings.Contains(got, hazard) {
		t.Fatalf("rules reply = %q; want %q intact", got, hazard)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check "+hazard)); !strings.Contains(got, "🚫") {
		t.Fatalf("check reply = %q; want blocked", got)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_del "+hazard)); !strings.Contains(got, "Removed") {
		t.Fatalf("del reply = %q; want a removal", got)
	}
}

// Replies use parse_mode HTML, so every site that echoes user text must escape
// it. These pin the four add and remove confirmations: added, already present,
// removed, and not there.
func TestAddAndDel_EscapeUserText(t *testing.T) {
	rb := installBlacklist(t)

	add := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add <b>bold</b>"))
	if strings.Contains(add, "<b>bold</b>") || !strings.Contains(add, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("add reply = %q; want the markup escaped", add)
	}

	dup := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add <b>bold</b>"))
	if strings.Contains(dup, "<b>bold</b>") {
		t.Fatalf("duplicate reply = %q; want the markup escaped", dup)
	}

	del := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_del <b>bold</b>"))
	if strings.Contains(del, "<b>bold</b>") || !strings.Contains(del, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("del reply = %q; want the markup escaped", del)
	}

	absent := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_del <b>bold</b>"))
	if strings.Contains(absent, "<b>bold</b>") {
		t.Fatalf("absence reply = %q; want the markup escaped", absent)
	}
}

func TestCheck_EscapesEntryNames(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add <b>"))

	blocked := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check x<b>y"))
	if strings.Contains(blocked, "<code><b></code>") || !strings.Contains(blocked, "&lt;b&gt;") {
		t.Fatalf("blocked verdict = %q; want the entry escaped", blocked)
	}

	// The rescued branch names two entries; both must be escaped.
	send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_add x<b>y"))
	rescued := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check x<b>y"))
	if !strings.Contains(rescued, "✅") {
		t.Fatalf("verdict = %q; want the rescued branch", rescued)
	}
	if strings.Contains(rescued, "<code><b></code>") || strings.Contains(rescued, "<code>x<b>y</code>") {
		t.Fatalf("rescued verdict = %q; want both entries escaped", rescued)
	}
}

// NFKC can expand as easily as it can contract, so the byte cap has to be
// applied to the normalized text as well as to what the user typed. These ten
// runes are 30 bytes as sent and 330 once normalized.
func TestAdd_RefusesTextThatExpandsPastTheCap(t *testing.T) {
	rb := installBlacklist(t)
	raw := strings.Repeat("ﷺ", 10)

	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add "+raw)); !strings.Contains(got, "too long") {
		t.Fatalf("reply = %q; want a length refusal", got)
	}
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules")); !strings.Contains(got, "<b>Blacklist</b> (0)") {
		t.Fatalf("rules reply = %q; the over-long entry was stored anyway", got)
	}
}

// Telegram sets a thread id for reply chains in ordinary supergroups too, not
// only for forum topics. Those must fall back to the chat-wide list, or text
// added by replying would land somewhere a plain /blacklist_rules cannot read.
func TestReplyChainThreadIsNotATopic(t *testing.T) {
	rb := installBlacklist(t)

	// A reply in a non-forum supergroup: thread id set, IsTopicMessage false.
	add := testutil.NewSupergroupMessage(-100, 7, "/blacklist_add")
	add.Message.MessageThreadID = 4242
	add.Message.ReplyToMessage = &models.Message{Text: "cat"}
	send(t, rb, add)

	// A later command with no thread id at all must still see it.
	if got := send(t, rb, testutil.NewSupergroupMessage(-100, 7, "/blacklist_check cat")); !strings.Contains(got, "🚫") {
		t.Fatalf("check reply = %q; a reply-chain add was stored out of reach", got)
	}
	if got := send(t, rb, testutil.NewSupergroupMessage(-100, 7, "/blacklist_rules")); !strings.Contains(got, "<code>cat</code>") {
		t.Fatalf("rules reply = %q; want the entry listed", got)
	}
}

// A mutation answers with the current contents of the list it changed, so the
// sender never has to follow it with /blacklist_rules.
func TestAddAndDel_ShowTheChangedList(t *testing.T) {
	rb := installBlacklist(t)

	first := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add cat"))
	if !strings.Contains(first, "<b>Blacklist</b> (1)") || !strings.Contains(first, "<code>cat</code>") {
		t.Fatalf("add reply = %q; want the list appended", first)
	}

	second := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add dog"))
	if !strings.Contains(second, "<b>Blacklist</b> (2)") {
		t.Fatalf("add reply = %q; want both entries counted", second)
	}
	if !strings.Contains(second, "<code>cat</code>") || !strings.Contains(second, "<code>dog</code>") {
		t.Fatalf("add reply = %q; want the whole list, not just the new entry", second)
	}

	removed := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_del cat"))
	if !strings.Contains(removed, "<b>Blacklist</b> (1)") {
		t.Fatalf("del reply = %q; want the list after removal", removed)
	}
	// Only the listing may be checked for absence: the confirmation line above
	// it echoes the removed text by design.
	_, listing, _ := strings.Cut(removed, "<b>Blacklist</b>")
	if strings.Contains(listing, "<code>cat</code>") {
		t.Fatalf("del reply = %q; the removed entry is still listed", removed)
	}
	if !strings.Contains(listing, "<code>dog</code>") {
		t.Fatalf("del reply = %q; want the surviving entry listed", removed)
	}

	empty := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_del dog"))
	if !strings.Contains(empty, "<b>Blacklist</b> (0)") || !strings.Contains(empty, "nothing yet") {
		t.Fatalf("del reply = %q; want an empty list shown", empty)
	}
}

// Each list answers with itself: a whitelist change shows the whitelist, not
// the blacklist.
func TestWhitelistMutation_ShowsOnlyTheWhitelist(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add cat"))

	got := send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_add exception"))
	if !strings.Contains(got, "<b>Whitelist</b> (1)") {
		t.Fatalf("whitelist add reply = %q; want the whitelist", got)
	}
	if strings.Contains(got, "<b>Blacklist</b>") {
		t.Fatalf("whitelist add reply = %q; want only the changed list", got)
	}
}

// The confirmation shares the list's byte budget, so a long list cannot push
// the combined reply past Telegram's limit.
func TestAdd_ReplyStaysWithinOneMessage(t *testing.T) {
	rb := installBlacklist(t)
	var last string
	for i := range 400 {
		last = send(t, rb, testutil.NewPrivateMessage(7,
			fmt.Sprintf("/blacklist_add %s%03d", strings.Repeat("x", 30), i)))
	}
	if n := len([]rune(last)); n > 4096 {
		t.Fatalf("add reply is %d characters, over Telegram's limit", n)
	}
	if !strings.Contains(last, "more.") {
		t.Fatalf("add reply = %q; want a trim notice", last)
	}
}

// /blacklist is the whole module behind one name: bare it lists, with an
// argument it checks.
func TestShortCommand_ListsWhenBareAndChecksWithText(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add cat"))

	bare := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist"))
	rules := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_rules"))
	if bare != rules {
		t.Fatalf("/blacklist = %q; want the same as /blacklist_rules = %q", bare, rules)
	}

	hit := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist cat"))
	check := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_check cat"))
	if hit != check {
		t.Fatalf("/blacklist cat = %q; want the same as /blacklist_check cat = %q", hit, check)
	}
	if !strings.Contains(hit, "🚫") {
		t.Fatalf("/blacklist cat = %q; want blocked", hit)
	}
}

// Whitespace alone is not an argument, so it still lists rather than checking.
func TestShortCommand_BlankArgumentStillLists(t *testing.T) {
	rb := installBlacklist(t)
	if got := send(t, rb, testutil.NewPrivateMessage(7, "/blacklist    ")); !strings.Contains(got, "<b>Blacklist</b>") {
		t.Fatalf("/blacklist with blank argument = %q; want the listing", got)
	}
}

func TestWhitelistRandom_RefusesAnEmptyList(t *testing.T) {
	rb := installBlacklist(t)
	// A populated blacklist must not make the whitelist look non-empty.
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add cat"))

	got := send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_rnd"))
	if !strings.Contains(got, "empty") {
		t.Fatalf("/whitelist_rnd = %q; want an empty-list message", got)
	}
}

func TestWhitelistRandom_PicksFromTheWhitelistOnly(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/blacklist_add nope"))
	entries := []string{"alpha", "beta", "gamma"}
	for _, e := range entries {
		send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_add "+e))
	}

	seen := map[string]bool{}
	for range 30 {
		got := send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_rnd"))
		if strings.Contains(got, "nope") {
			t.Fatalf("/whitelist_rnd = %q; picked from the blacklist", got)
		}
		match := ""
		for _, e := range entries {
			if strings.Contains(got, "<code>"+e+"</code>") {
				match = e
			}
		}
		if match == "" {
			t.Fatalf("/whitelist_rnd = %q; want one of %v", got, entries)
		}
		seen[match] = true
	}
	// Three entries over thirty draws: landing on one every time would mean the
	// pick is not random at all.
	if len(seen) < 2 {
		t.Fatalf("30 draws returned only %v", seen)
	}
}

func TestWhitelistRandom_IsPerTopic(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, inTopic(-100, 11, "/whitelist_add alpha"))

	if got := send(t, rb, inTopic(-100, 12, "/whitelist_rnd")); !strings.Contains(got, "empty") {
		t.Fatalf("/whitelist_rnd in a sibling topic = %q; want an empty-list message", got)
	}
}

func TestWhitelistRandom_EscapesUserText(t *testing.T) {
	rb := installBlacklist(t)
	send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_add <b>bold</b>"))

	got := send(t, rb, testutil.NewPrivateMessage(7, "/whitelist_rnd"))
	if strings.Contains(got, "<b>bold</b>") || !strings.Contains(got, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Fatalf("/whitelist_rnd = %q; want the markup escaped", got)
	}
}
