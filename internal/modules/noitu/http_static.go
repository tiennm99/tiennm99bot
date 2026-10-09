package noitu

import (
	"net/http"

	"github.com/tiennm99/tiennm99bot/internal/modules/noitu/web"
	"github.com/tiennm99/tiennm99bot/internal/modules/util/htmlgame"
)

// serveAsset serves one embedded page file with a short public cache. See
// htmlgame.ServeAsset.
func serveAsset(name string) http.HandlerFunc { return htmlgame.ServeAsset(web.FS, name) }
