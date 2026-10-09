package noitu

import (
	"net/http"
	"path"

	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/web"
)

var assetTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
}

// serveAsset serves one embedded page file. The files are small and fixed
// for a binary's lifetime, so a short public cache is safe; the page's token
// lives in the query string, which does not change the content.
func serveAsset(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, err := web.FS.ReadFile(name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", assetTypes[path.Ext(name)])
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
	}
}
