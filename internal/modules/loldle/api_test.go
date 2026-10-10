package loldle

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tiennm99/tiennm99bot/internal/modules/loldle/web"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/guessgame"
)

func urlUnescape(t *testing.T, s string) string {
	t.Helper()
	u, err := url.QueryUnescape(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRules_MarksArrowsAndRefusals(t *testing.T) {
	r := rules{champions: loadChampions()}
	for guess, want := range map[string]string{"Akali": "cwwwccu", "wukong": "wcwccwc", "kaisa": "cwccwwd", "Ahri": "ccccccc"} {
		g, err := r.Judge(guess, "Ahri", nil)
		if err != nil || g.Marks != want {
			t.Errorf("%s vs Ahri = %+v, %v; want %s", guess, g, err, want)
		}
	}
	if g, _ := r.Judge("jinx", "Kai'Sa", nil); g.Word != "Jinx" || g.Marks != "cpccwcu" {
		t.Errorf("Jinx vs Kai'Sa = %+v", g)
	}
	for input, want := range map[string]error{"zzz": errUnknownChampion, "ka": errAmbiguous, "": errUnknownChampion} {
		if _, err := r.Judge(input, "Ahri", nil); err != want {
			t.Errorf("Judge(%q) = %v, want %v", input, err, want)
		}
	}
	if _, err := r.Judge("akali", "Ahri", []guessgame.Guess{{Word: "Akali"}}); err != errDuplicate {
		t.Errorf("duplicate = %v", err)
	}
	if _, err := r.Judge("akali", "Gone", nil); err != errTargetGone {
		t.Errorf("gone = %v", err)
	}
	if got := guessgame.EmojiRow(r, "cpwudcw"); got != "🟩🟨🟥🟥🟥🟩🟥" {
		t.Errorf("emoji = %q", got)
	}
}

func TestChampions_EveryRowHasAnIconID(t *testing.T) {
	seen := map[string]bool{}
	idRe := regexp.MustCompile(`^[A-Za-z]+$`)
	for _, c := range loadChampions() {
		if !idRe.MatchString(c.ID) || seen[c.ID] || c.Title == "" {
			t.Errorf("%s: id %q title %q", c.ChampionName, c.ID, c.Title)
		}
		seen[c.ID] = true
	}
}

// Data Dragon tile file names are case-sensitive and do not always match the
// champion key: Fiddlesticks' tile is FiddleSticks_0.jpg.
func TestChampions_IconIDsNameTheTileFile(t *testing.T) {
	ids := map[string]string{}
	for _, c := range loadChampions() {
		ids[c.ChampionName] = c.ID
	}
	for name, want := range map[string]string{"Fiddlesticks": "FiddleSticks", "Wukong": "MonkeyKing", "Ahri": "Ahri"} {
		if ids[name] != want {
			t.Errorf("%s icon id = %q, want %q", name, ids[name], want)
		}
	}
}

func TestAPI_ChampionListCarriesNamesAndIDsOnly(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePrefix+"champions.json", nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("champions.json: %d %v", rec.Code, rec.Header())
	}
	var list []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != len(loadChampions()) {
		t.Fatalf("list = %d, %v", len(list), err)
	}
	for _, c := range list {
		if len(c) != 2 || c["name"] == "" || c["id"] == "" {
			t.Fatalf("entry %v", c)
		}
	}
	if list[0]["name"] != "Aatrox" || !strings.Contains(rec.Body.String(), `{"name":"Wukong","id":"MonkeyKing"}`) {
		t.Fatalf("list starts %v", list[0])
	}
}

func TestAPI_HeadersAllowDataDragonImagesOnly(t *testing.T) {
	h := newHarness(t)
	for path, ctype := range map[string]string{"": "text/html", "app.js": "text/javascript", "app.css": "text/css"} {
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePrefix+path, nil))
		csp := rec.Header().Get("Content-Security-Policy")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), ctype) ||
			!strings.Contains(csp, "img-src 'self' data: https://ddragon.leagueoflegends.com;") || !strings.Contains(csp, "connect-src 'self'") {
			t.Errorf("GET %q: %d %v", path, rec.Code, rec.Header())
		}
	}
	if r := h.state("garbage"); r.code != http.StatusUnauthorized || r.err != "bad_token" {
		t.Fatalf("bad token = %d %s", r.code, r.body)
	}
}

var (
	inlineScriptRe  = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	urlRe           = regexp.MustCompile(`https?://[^'"\s)]+`)
	inlineHandlerRe = regexp.MustCompile(`\son[a-z]+=`)
)

// The page runs under the strict CSP: no inline script, style or handler,
// and it reaches no host but its own and Data Dragon's icons.
func TestWebAssets_FitTheCSP(t *testing.T) {
	index, _ := web.FS.ReadFile("index.html")
	for _, m := range inlineScriptRe.FindAllStringSubmatch(string(index), -1) {
		if strings.TrimSpace(m[2]) != "" || !strings.Contains(m[1], "src=") {
			t.Errorf("inline script breaks the CSP: %q", m[0])
		}
	}
	if strings.Contains(string(index), " style=") || strings.Contains(string(index), "<style") || inlineHandlerRe.Match(index) {
		t.Error("inline style or handler breaks the CSP")
	}
	js, _ := web.FS.ReadFile("app.js")
	for _, u := range urlRe.FindAllString(string(js), -1) {
		if !strings.HasPrefix(u, ddragonOrigin+"/") {
			t.Errorf("app.js reaches %s", u)
		}
	}
	// No champion attributes ship to the page.
	for _, leak := range []string{"Vastayan", "Manaless", "Shurima", "release_date"} {
		if strings.Contains(string(js), leak) {
			t.Errorf("app.js carries %q", leak)
		}
	}
}

// The board's column labels are small text, so they need WCAG AA's 4.5:1
// against the header background.
func TestWebAssets_ColumnLabelsMeetAAContrast(t *testing.T) {
	css, err := web.FS.ReadFile("app.css")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, m := range regexp.MustCompile(`(--[a-z-]+):\s*(#[0-9a-fA-F]{6});`).FindAllStringSubmatch(string(css), -1) {
		tokens[m[1]] = m[2]
	}
	rule := regexp.MustCompile(`(?s)\.head \.cell \{(.*?)\}`).FindStringSubmatch(string(css))
	if rule == nil {
		t.Fatal("no .head .cell rule")
	}
	prop := func(name string) string {
		m := regexp.MustCompile(`(?m)^\s*` + name + `:\s*var\((--[a-z-]+)\);`).FindStringSubmatch(rule[1])
		if m == nil || tokens[m[1]] == "" {
			t.Fatalf(".head .cell %s is not a colour token", name)
		}
		return tokens[m[1]]
	}
	fg, bg := prop("color"), prop("background")
	if r := contrast(fg, bg); r < 4.5 {
		t.Errorf("column labels %s on %s = %.2f:1, want at least 4.5:1", fg, bg, r)
	}
}

// contrast is the WCAG 2 contrast ratio of two #rrggbb colours.
func contrast(a, b string) float64 {
	lum := func(hex string) float64 {
		var ch [3]float64
		for i := range ch {
			var v int
			_, _ = fmt.Sscanf(hex[1+2*i:3+2*i], "%02x", &v)
			c := float64(v) / 255
			if c <= 0.03928 {
				ch[i] = c / 12.92
			} else {
				ch[i] = math.Pow((c+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*ch[0] + 0.7152*ch[1] + 0.0722*ch[2]
	}
	la, lb := lum(a), lum(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

func TestAPI_UnlimitedRoundNeverLeaksTheAnswer(t *testing.T) {
	h := newHarness(t)
	tok := h.token(1, groupChat, 5, "Alice", true)
	r := h.state(tok)
	if r.code != http.StatusOK || r.v.Mode != "unlimited" || r.v.Seq != 1 || r.v.Max != 8 || len(r.v.Columns) != 7 || r.v.Answer != nil || r.v.Stats != nil {
		t.Fatalf("fresh = %s", r.body)
	}
	for _, name := range []string{"Akali", "Wukong", "Kai'Sa"} {
		r = h.roundGuess(tok, 1, name)
		if r.code != http.StatusOK || strings.Contains(r.body, "Ahri") || strings.Contains(r.body, `"answer"`) {
			t.Fatalf("guess %s leaked or failed: %d %s", name, r.code, r.body)
		}
	}
	row := r.v.Guesses[2]
	if row.Name != "Kai'Sa" || row.ID != "Kaisa" || row.Marks != "cwccwwd" || len(row.Cells) != 7 {
		t.Fatalf("row = %+v", row)
	}
	// Cells show the guess's own values only.
	if c := row.Cells[4]; c.Key != "regions" || c.Value != "Shurima, Void" || c.Result != "wrong" {
		t.Fatalf("regions cell = %+v", c)
	}
	if c := row.Cells[6]; c.Value != "2018" || c.Dir != "down" || c.Result != "wrong" {
		t.Fatalf("year cell = %+v", c)
	}
	for name, code := range map[string]string{"zzz": "unknown", "ka": "ambiguous", "akali": "duplicate"} {
		if r := h.roundGuess(tok, 1, name); r.code != http.StatusUnprocessableEntity || r.err != code {
			t.Errorf("%s = %d %s", name, r.code, r.body)
		}
	}
	r = h.roundGuess(tok, 1, "ahri")
	if r.v.Status != "won" || r.v.Answer == nil || r.v.Answer.Name != "Ahri" || r.v.Answer.ID != "Ahri" || r.v.Answer.Title == "" ||
		r.v.Stats == nil || r.v.Stats.Played != 1 || len(r.v.Stats.Dist) != MaxGuessesCap || r.v.Stats.Dist[3] != 1 || r.v.Share != "" {
		t.Fatalf("win = %s", r.body)
	}
	if r = h.post("new", map[string]any{"token": tok, "seq": 1}); r.v.Seq != 2 || len(r.v.Guesses) != 0 || r.v.Abandoned != nil {
		t.Fatalf("new = %s", r.body)
	}
	// Giving up a round with guesses shows its champion before the next one.
	h.roundGuess(tok, 2, "Akali") // round 2 is Jinx
	if r = h.post("new", map[string]any{"token": tok, "seq": 2}); r.v.Seq != 3 || r.v.Answer != nil ||
		r.v.Abandoned == nil || r.v.Abandoned.Name != "Jinx" || r.v.Abandoned.ID != "Jinx" || r.v.Abandoned.Title == "" {
		t.Fatalf("abandon = %s", r.body)
	}
	if r = h.roundGuess(tok, 1, "akali"); r.code != http.StatusConflict || r.err != "new_round" {
		t.Fatalf("stale = %d %s", r.code, r.body)
	}
	if n := len(h.rep.snapshot()); n != 0 {
		t.Fatalf("unlimited win reported %d times", n)
	}
	if r = h.post("new", map[string]any{"token": h.token(1, groupChat, 5, "Alice", false), "seq": 1}); r.code != http.StatusForbidden || r.err != "bad_mode" {
		t.Fatalf("new on a daily card = %d %s", r.code, r.body)
	}
}

func TestDaily_SameChampionForEveryoneAndHiddenUntilFinished(t *testing.T) {
	h := newHarness(t)
	h.pinDaily(1, "Ahri")
	alice := h.token(1, groupChat, 7, "Alice", false)
	r := h.state(alice)
	if r.v.Mode != "daily" || r.v.Num != 1 || r.v.Date != "2026-10-09" || r.v.Max != 8 || r.v.NextAt != guessgame.Epoch.Add(24*time.Hour).Unix() {
		t.Fatalf("daily = %s", r.body)
	}
	r = h.dailyGuess(alice, 1, "Akali")
	if strings.Contains(r.body, "Ahri") || r.v.Guesses[0].Marks != "cwwwccu" {
		t.Fatalf("guess leaked or wrong: %s", r.body)
	}
	r = h.dailyGuess(alice, 1, "Ahri")
	if r.v.Status != "won" || r.v.Answer.Name != "Ahri" || r.v.Stats.Played != 1 || len(r.v.Stats.Dist) != 8 || r.v.Stats.Dist[1] != 1 ||
		r.v.Share != "LoLdle Daily #1 2/8\n\n🟩🟥🟥🟥🟩🟩🟥\n🟩🟩🟩🟩🟩🟩🟩" {
		t.Fatalf("win = %s", r.body)
	}
	if calls := h.rep.snapshot(); len(calls) != 1 || calls[0].score != 7 || calls[0].addr.ChatID != groupChat {
		t.Fatalf("score = %+v, want 9-2", calls)
	}
	if r := h.dailyGuess(alice, 1, "Jinx"); r.code != http.StatusUnprocessableEntity || r.err != "finished" {
		t.Fatalf("after the win = %d %s", r.code, r.body)
	}
	// Bob gets the same champion.
	bob := h.token(2, 2, 3, "Bob", false)
	if r := h.dailyGuess(bob, 1, "Ahri"); r.v.Status != "won" {
		t.Fatalf("bob = %s", r.body)
	}
	// At 07:00 ICT the next champion starts; a stale board is refused.
	h.clock.advance(12 * time.Hour)
	bob = h.token(2, 2, 3, "Bob", false)
	if r := h.dailyGuess(bob, 1, "Ahri"); r.code != http.StatusConflict || r.err != "new_puzzle" {
		t.Fatalf("stale = %d %s", r.code, r.body)
	}
	if r := h.state(bob); r.v.Num != 2 || len(r.v.Guesses) != 0 || r.v.Date != "2026-10-10" {
		t.Fatalf("day 2 = %s", r.body)
	}
	// Daily stats live under their own keys, apart from the legacy stats.
	if keys, _ := h.svc.Stats.List(context.Background(), "dstats:"); len(keys) != 2 {
		t.Fatalf("daily stats keys = %v", keys)
	}
}

// A champion pinned as a day's answer and later removed from the data keeps
// the day playable: guesses score wrong in every column, naming the answer
// wins, and the board finishes instead of failing every guess.
func TestDaily_PinnedAnswerMissingFromDataStaysPlayable(t *testing.T) {
	h := newHarness(t)
	h.pinDaily(1, "Retired Champ")
	alice := h.token(1, groupChat, 7, "Alice", false)
	r := h.dailyGuess(alice, 1, "Akali")
	if r.code != http.StatusOK || len(r.v.Guesses) != 1 || r.v.Guesses[0].Marks != "wwwwwww" || r.v.Answer != nil {
		t.Fatalf("guess = %d %s", r.code, r.body)
	}
	for name, code := range map[string]string{"zzz": "unknown", "ka": "ambiguous", "akali": "duplicate"} {
		if r := h.dailyGuess(alice, 1, name); r.code != http.StatusUnprocessableEntity || r.err != code {
			t.Errorf("%s = %d %s", name, r.code, r.body)
		}
	}
	r = h.dailyGuess(alice, 1, "retired champ")
	if r.code != http.StatusOK || r.v.Status != "won" || r.v.Answer == nil || r.v.Answer.Name != "Retired Champ" {
		t.Fatalf("win = %d %s", r.code, r.body)
	}
	bob := h.token(2, 2, 3, "Bob", false)
	for i, name := range []string{"Ahri", "Akali", "Jinx", "Wukong", "Kai'Sa", "Lux", "Garen", "Teemo"} {
		r = h.dailyGuess(bob, 1, name)
		if r.code != http.StatusOK {
			t.Fatalf("bob guess %d = %d %s", i, r.code, r.body)
		}
	}
	if r.v.Status != "lost" || r.v.Answer == nil || r.v.Answer.Name != "Retired Champ" {
		t.Fatalf("loss = %s", r.body)
	}
}

func TestDaily_AnswersAreKeyedChampionsWithoutRepeats(t *testing.T) {
	h := newHarness(t)
	n := len(loadChampions())
	seen := map[string]bool{}
	for num := 1; num <= n; num++ {
		a := h.svc.AnswerFor(num)
		if seen[a] || findChampionByExactName(h.svc.cfg.champions, a) == nil {
			t.Fatalf("puzzle %d answer %q repeats or is unknown", num, a)
		}
		seen[a] = true
	}
	other := testConfig(h.clock, h.coll, h.rep, nil, h.sched)
	other.rootKey = []byte("another-root-key-another-root-key")
	same := 0
	for num := 1; num <= 10; num++ {
		if newService(other).AnswerFor(num) == h.svc.AnswerFor(num) {
			same++
		}
	}
	if same == 10 {
		t.Fatal("the answer order ignores the key")
	}
	// The first resolution pins the day: a later key or data change keeps it.
	a, err := h.svc.ResolvePuzzle(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := newService(other).ResolvePuzzle(context.Background(), 1); got != a {
		t.Fatalf("pinned %q, then %q", a, got)
	}
}

// tokenFor-style helper with a thread; daily tests need topics.
func (h *harness) tokenTopic(user, chat int64, msg, thread int, name string) string {
	h.t.Helper()
	tok, err := h.svc.signToken(guessgame.Claims{UserID: user, Name: name, ChatID: chat, MessageID: msg, ThreadID: thread,
		Expiry: h.clock.now().Add(guessgame.TokenTTL).Unix()})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

func TestDaily_LiveResultsAreSpoilerFree(t *testing.T) {
	h := newHarness(t)
	h.pinDaily(1, "Ahri")
	alice := h.tokenTopic(1, groupChat, 7, 0, "Alice")
	h.dailyGuess(alice, 1, "Akali")
	h.dailyGuess(h.tokenTopic(2, groupChat, 7, 0, "Bob"), 1, "Kai'Sa")
	h.sched.run()
	sends := sentMethod(h.rb, "sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sends = %+v", h.rb.Sent())
	}
	text := sends[0].Text()
	if text != "LoLdle Daily #1 · live results\n\nAlice playing 1/8\n🟩🟥🟥🟥🟩🟩🟥\nBob playing 1/8\n🟩🟥🟩🟩🟥🟥🟥" {
		t.Fatalf("live = %q", text)
	}
	for _, c := range loadChampions() {
		if strings.Contains(text, c.ChampionName) {
			t.Fatalf("live results name %s", c.ChampionName)
		}
	}
	h.dailyGuess(alice, 1, "Ahri")
	h.sched.run()
	edits := sentMethod(h.rb, "editMessageText")
	if len(edits) == 0 || !strings.Contains(edits[len(edits)-1].Form["text"], "Alice 2/8\n🟩🟥🟥🟥🟩🟩🟥\n🟩🟩🟩🟩🟩🟩🟩") {
		t.Fatalf("edits = %+v", edits)
	}
}

func TestDaily_RecapAnswerFirstStreakCountsWinsOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	rb := install(t, h.svc.module(), 0)
	if got := send(t, rb, groupChat, 1, "/loldledaily_subscribe"); !strings.HasPrefix(got, "✅ Subscribed this chat to LoLdle Daily") {
		t.Fatalf("subscribe = %q", got)
	}
	// Day 1: Alice wins in 2, Dan in 2, Bob loses; day 2: Alice wins.
	h.pinDaily(1, "Ahri")
	h.pinDaily(2, "Jinx")
	h.pinDaily(3, "Wukong")
	h.dailyGuess(h.tokenTopic(1, groupChat, 7, 0, "Alice"), 1, "Akali")
	h.dailyGuess(h.tokenTopic(1, groupChat, 7, 0, "Alice"), 1, "Ahri")
	h.dailyGuess(h.tokenTopic(3, groupChat, 7, 0, "Dan"), 1, "Jinx")
	h.dailyGuess(h.tokenTopic(3, groupChat, 7, 0, "Dan"), 1, "Ahri")
	bob := h.tokenTopic(2, groupChat, 7, 0, "Bob")
	for _, c := range []string{"Akali", "Wukong", "Jinx", "Kai'Sa", "Aatrox", "Annie", "Ashe", "Braum"} {
		h.dailyGuess(bob, 1, c)
	}
	// Day 1's recap groups the finishers by guesses, the best crowned.
	got, err := h.svc.RenderRecap(ctx, 1, groupChat, 0)
	if err != nil || got != "LoLdle Daily #1 — yesterday's results\nChampion: Ahri\n👑 2/8: Alice, Dan\nX/8: Bob" {
		t.Fatalf("day 1 recap = %q, %v", got, err)
	}
	h.clock.advance(24 * time.Hour)
	h.dailyGuess(h.tokenTopic(1, groupChat, 8, 0, "Alice"), 2, "Jinx")
	h.clock.advance(24 * time.Hour)
	h.sched.run()
	h.rb.Reset()
	if err := h.svc.RunDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	sent := h.rb.Sent()
	if len(sent) != 2 || sent[1].Method != "sendGame" || sent[1].Form["game_short_name"] != "loldle" {
		t.Fatalf("push = %+v", sent)
	}
	if got := sent[0].Text(); got != "LoLdle Daily #2 — yesterday's results\nChampion: Jinx\n🔥 Your group is on a 2 day streak!\n👑 1/8: Alice" {
		t.Fatalf("recap = %q", got)
	}
	// Day 3 only a loss: the streak resets.
	h.dailyGuess(h.tokenTopic(1, groupChat, 9, 0, "Alice"), 3, "Akali")
	for _, c := range []string{"Ahri", "Jinx", "Kai'Sa", "Aatrox", "Annie", "Ashe", "Braum"} {
		h.dailyGuess(h.tokenTopic(1, groupChat, 9, 0, "Alice"), 3, c)
	}
	h.clock.advance(24 * time.Hour)
	h.rb.Reset()
	if err := h.svc.RunDailyPush(ctx, h.rb.Bot); err != nil {
		t.Fatal(err)
	}
	if got := h.rb.Sent()[0].Text(); got != "LoLdle Daily #3 — yesterday's results\nChampion: Wukong\nNobody solved it. Group streak reset.\nX/8: Alice" {
		t.Fatalf("loss recap = %q", got)
	}
}
