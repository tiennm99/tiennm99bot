package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
)

// SentCall captures one outbound Telegram API call (sendMessage, sendSticker,
// etc.) made by the bot during dispatch. Method is the API method ("sendMessage")
// and Form is the multipart form fields collapsed to plain string→string —
// the bot library only emits primitive scalars for our handler call sites.
type SentCall struct {
	Method string
	Form   map[string]string
}

// Text is a convenience accessor for the most common assertion: SendMessage's
// "text" field. Returns "" if the call wasn't a sendMessage.
func (c SentCall) Text() string { return c.Form["text"] }

// ChatID returns the "chat_id" form field as-is (string form). Empty string
// if absent.
func (c SentCall) ChatID() string { return c.Form["chat_id"] }

// RecordingBot wraps a *bot.Bot wired to an httptest server that captures
// outbound API calls instead of contacting Telegram. Always Close() in a
// defer to release the test server.
type RecordingBot struct {
	Bot    *bot.Bot
	Server *httptest.Server

	mu            sync.Mutex
	calls         []SentCall
	failures      map[string]failureResponse
	stubs         map[string]string
	nextMessageID int
}

type failureResponse struct {
	status int
	body   string
}

// NewRecordingBot constructs a recording bot. The bot uses a synthetic token
// and disables async handlers so dispatch is deterministic in tests.
func NewRecordingBot(t *testing.T) *RecordingBot {
	t.Helper()
	rb := &RecordingBot{}
	rb.Server = httptest.NewServer(http.HandlerFunc(rb.handle))
	t.Cleanup(rb.Server.Close)

	b, err := bot.New("test-token",
		bot.WithSkipGetMe(),
		bot.WithNotAsyncHandlers(),
		bot.WithServerURL(rb.Server.URL),
	)
	if err != nil {
		t.Fatalf("recording bot init: %v", err)
	}
	rb.Bot = b
	return rb
}

// Sent returns a copy of all calls captured so far, in chronological order.
func (rb *RecordingBot) Sent() []SentCall {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	out := make([]SentCall, len(rb.calls))
	copy(out, rb.calls)
	return out
}

// LastSent returns the most-recent recorded call, or zero-value SentCall if
// none have been made yet.
func (rb *RecordingBot) LastSent() SentCall {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if len(rb.calls) == 0 {
		return SentCall{}
	}
	return rb.calls[len(rb.calls)-1]
}

// Reset drops all captured calls.
//
// It deliberately does NOT clear registered failures or stubs — those are setup,
// not observations. A sub-test that needs different responses should register
// them explicitly or build its own bot.
func (rb *RecordingBot) Reset() {
	rb.mu.Lock()
	rb.calls = nil
	rb.mu.Unlock()
}

// StubMethod makes method return resultJSON as the "result" field of an ok
// response, so methods that decode into a struct can be exercised at all.
//
// Without a stub, okResponseFor answers every non-message-producing method with
// `{"ok":true,"result":true}` — which getStickerSet, getFile, uploadStickerFile,
// and getMe cannot decode, so under the bare harness they can only ever return
// "json: cannot unmarshal bool" errors.
//
// resultJSON is the raw JSON value for "result" — an object, array, or scalar:
//
//	rb.StubMethod("getStickerSet", `{"name":"p_by_bot","title":"P","sticker_type":"regular","stickers":[]}`)
//
// A failure registered for the same method wins, so a test can override a
// stubbed happy path without unregistering the stub.
func (rb *RecordingBot) StubMethod(method string, resultJSON string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.stubs == nil {
		rb.stubs = map[string]string{}
	}
	rb.stubs[method] = resultJSON
}

// FailMethodCode makes method fail with a Telegram-shaped error carrying an
// error_code, so the library maps it to the same sentinel production emits
// (bot.ErrorBadRequest for 400, bot.ErrorForbidden for 403, and so on).
//
// This is the difference from FailMethod: the library switches on the
// error_code *in the response body* (raw_request.go), not the HTTP
// status, so a codeless failure never takes a sentinel shape. Handlers that
// classify errors with errors.Is must be tested through this method.
//
//	rb.FailMethodCode("addStickerToSet", 400, "Bad Request: STICKERS_TOO_MUCH")
func (rb *RecordingBot) FailMethodCode(method string, errorCode int, description string) {
	body, _ := json.Marshal(map[string]any{
		"ok":          false,
		"error_code":  errorCode,
		"description": description,
	})
	rb.FailMethod(method, errorCode, string(body))
}

// FailMethod makes the recording server return a Telegram API error for a
// specific method while still recording the attempted call.
//
// The body is emitted verbatim, so unless it carries an "error_code" field the
// resulting error is **codeless**: the library returns a generic decode/status
// error rather than bot.ErrorBadRequest or any other sentinel. Use
// FailMethodCode when the test asserts on the error's classification.
func (rb *RecordingBot) FailMethod(method string, status int, body string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	if rb.failures == nil {
		rb.failures = map[string]failureResponse{}
	}
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if body == "" {
		body = `{"ok":false,"description":"forced test failure"}`
	}
	rb.failures[method] = failureResponse{status: status, body: body}
}

// ClearFailure removes a failure registered with FailMethod or
// FailMethodCode, so the method succeeds again (a test of a retry after a
// transient error).
func (rb *RecordingBot) ClearFailure(method string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	delete(rb.failures, method)
}

// handle is the httptest server's request handler. Path shape is
// "/bot<token>/<method>" per the go-telegram/bot URL builder. We extract the
// method, parse the multipart form, record, and respond with a minimal-ok
// JSON body shaped to satisfy whichever method was called.
func (rb *RecordingBot) handle(w http.ResponseWriter, r *http.Request) {
	method := apiMethodFromPath(r.URL.Path)

	// Read the body before parsing, because an empty body and a corrupt one are
	// otherwise indistinguishable: multipart reports both as "no parts".
	//
	// The distinction matters. Parameterless methods (getMe) genuinely send no
	// body, and rejecting them would make those methods untestable. A body that
	// is present but unparseable is a real fault, and answering it 200 with an
	// empty form would quietly satisfy every test that asserts a field is
	// *absent* — several outside this package do exactly that.
	//
	// 8 MiB cap: well above any realistic test payload, bounded for gosec.
	const maxBody = 8 << 20
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	form := map[string]string{}
	if len(body) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(body))
		// #nosec G120 — bounded by maxBody above
		if err := r.ParseMultipartForm(maxBody); err != nil {
			http.Error(w, "bad multipart form: "+err.Error(), http.StatusBadRequest)
			return
		}
		for k, vs := range r.MultipartForm.Value {
			if len(vs) > 0 {
				form[k] = vs[0]
			}
		}
	}

	rb.mu.Lock()
	rb.calls = append(rb.calls, SentCall{Method: method, Form: form})
	failure, shouldFail := rb.failures[method]
	stub, hasStub := rb.stubs[method]
	messageID := rb.nextMessageID
	if !shouldFail && isMessageProducingMethod(method) {
		rb.nextMessageID++
		messageID = rb.nextMessageID
	}
	rb.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	// Failures win over stubs so a test can override a stubbed happy path.
	if shouldFail {
		w.WriteHeader(failure.status)
		_, _ = w.Write([]byte(failure.body))
		return
	}
	if hasStub {
		_, _ = w.Write([]byte(`{"ok":true,"result":` + stub + `}`))
		return
	}
	_, _ = w.Write([]byte(okResponseFor(method, messageID)))
}

// isMessageProducingMethod reports whether the API method returns a Message,
// so each successful call gets a distinct incrementing message_id — mirroring
// real Telegram chats, where handlers key follow-up edits on the returned ID.
func isMessageProducingMethod(method string) bool {
	switch method {
	case "sendMessage", "sendSticker", "sendPhoto", "sendDocument", "sendVideo", "sendAnimation",
		// setGameScore returns the edited game Message for a chat message (an
		// inline message gets true instead, which the library cannot decode, so
		// that path goes through a raw call outside the recording bot).
		"sendGame", "setGameScore":
		return true
	}
	return false
}

// apiMethodFromPath extracts the API method from "/bot<token>/<method>".
// Returns "" on shapes that don't match (which the test still records as a
// call to "" — surfaces accidentally weird URLs in test output).
func apiMethodFromPath(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx < 0 {
		return ""
	}
	return p[idx+1:]
}

// okResponseFor returns a minimal `{ok:true, result:...}` payload that the
// bot library will accept for the named API method. Message-producing methods
// (see isMessageProducingMethod) expect a Message; most others accept a bool.
func okResponseFor(method string, messageID int) string {
	if isMessageProducingMethod(method) {
		// Minimal shape: id, date, chat. Bot library decodes via json so
		// extra fields are ignored.
		msg := map[string]any{
			"message_id": messageID,
			"date":       0,
			"chat": map[string]any{
				"id":   1,
				"type": "private",
			},
		}
		body := map[string]any{"ok": true, "result": msg}
		out, _ := json.Marshal(body)
		return string(out)
	}
	return `{"ok":true,"result":true}`
}

// AssertSentText fails the test if no recorded sendMessage contains the
// substring needle. Matches the most common assertion pattern: "did the
// handler include this phrase?"
func (rb *RecordingBot) AssertSentText(t *testing.T, needle string) {
	t.Helper()
	for _, c := range rb.Sent() {
		if c.Method == "sendMessage" && strings.Contains(c.Text(), needle) {
			return
		}
	}
	t.Errorf("no sendMessage contained %q. Sent calls: %s", needle, rb.dumpCalls())
}

// dumpCalls renders the captured calls for error messages.
func (rb *RecordingBot) dumpCalls() string {
	calls := rb.Sent()
	var parts []string
	for i, c := range calls {
		parts = append(parts, fmt.Sprintf("[%d] %s text=%q chat=%s", i, c.Method, c.Text(), c.ChatID()))
	}
	if len(parts) == 0 {
		return "(no calls)"
	}
	return strings.Join(parts, "; ")
}
