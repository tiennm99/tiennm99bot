package htmlgame

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/tiennm99/tiennm99bot/internal/log"
)

// BaseURLEnv names the public https base every game page is served under.
const BaseURLEnv = "GAME_BASE_URL"

// MaxBodyBytes caps one API request body.
const MaxBodyBytes = 4 << 10

// ErrBadRequest is a body that is too big, not one JSON object, or carries
// unknown fields.
var ErrBadRequest = errors.New("htmlgame: bad request")

// ParseBaseURL accepts https://host[/path] and returns it without a trailing
// slash. Anything else is logged, naming game, and disables that game.
func ParseBaseURL(raw, game string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		log.Warn("GAME_BASE_URL must be https://host[/path]; " + game + " game disabled")
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

// SecurityHeaders applies to every response under a game's route prefix. The
// token travels in the page URL, so no referrer may leak it. Telegram Web
// embeds games in an iframe, so framing is deliberately not restricted. API
// answers under prefix+"api/" are never cached.
func SecurityHeaders(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://telegram.org; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'")
		if strings.HasPrefix(r.URL.Path, prefix+"api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// WriteJSON writes v as the JSON response body with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON reads one JSON object of at most MaxBodyBytes with no unknown
// fields and nothing after it. It returns ErrBadRequest otherwise; the caller
// writes its own error body.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return ErrBadRequest
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ErrBadRequest
	}
	return nil
}

var assetTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

// ServeAsset serves one embedded page file from fsys. The files are small
// and fixed for a binary's lifetime, so a short public cache is safe; the
// page's token lives in the query string, which does not change the content.
func ServeAsset(fsys fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", assetTypes[path.Ext(name)])
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
	}
}
