package wordledaily

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/tiennm99/tiennm99bot/internal/modules/wordledaily/web"
)

func TestAPI_HeadersAndRoutes(t *testing.T) {
	h := newHarness(t)
	rec := h.post("state", `{}`)
	hdr := rec.Header()
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "default-src 'self'") ||
		hdr.Get("Referrer-Policy") != "no-referrer" ||
		hdr.Get("Cache-Control") != "no-store" ||
		hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("API headers = %v", hdr)
	}
	for path, ctype := range map[string]string{"": "text/html", "app.js": "text/javascript", "app.css": "text/css"} {
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePrefix+path+"?t=abc", nil))
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), ctype) || rec.Body.Len() == 0 ||
			rec.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Errorf("GET %q: %d %v", path, rec.Code, rec.Header())
		}
	}
	for _, path := range []string{"web.go", "api/state", "api/guess", "../noitu/"} {
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePrefix+path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %q served", path)
		}
	}
}

var inlineScriptRe = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)

// The page must run under the strict CSP: no inline script or style, and no
// word list shipped to the client.
func TestWebAssets_FitTheCSP(t *testing.T) {
	index, err := web.FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range inlineScriptRe.FindAllStringSubmatch(string(index), -1) {
		if strings.TrimSpace(m[2]) != "" || !strings.Contains(m[1], "src=") {
			t.Errorf("inline script breaks the CSP: %q", m[0])
		}
	}
	if strings.Contains(string(index), " style=") || strings.Contains(string(index), "<style") {
		t.Error("inline style breaks the CSP")
	}
	js, _ := web.FS.ReadFile("app.js")
	for _, w := range []string{"crane", "abbey", "zebra"} {
		if strings.Contains(string(js), w) {
			t.Errorf("app.js carries the word %q", w)
		}
	}
}

var iconButtonRe = regexp.MustCompile(`<button[^>]*class="icon[^"]*"[^>]*>(.*?)</button>`)

// Icon-only buttons are named by aria-label: their glyph would otherwise be
// the accessible name, and title only a description.
func TestWebAssets_IconButtonsHaveNames(t *testing.T) {
	index, err := web.FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	buttons := iconButtonRe.FindAllStringSubmatch(string(index), -1)
	if len(buttons) != 3 {
		t.Fatalf("icon buttons = %d", len(buttons))
	}
	for _, b := range buttons {
		if !strings.Contains(b[0], `aria-label="`) || !strings.HasPrefix(b[1], `<span aria-hidden="true">`) {
			t.Errorf("unnamed icon button: %s", b[0])
		}
	}
}
