package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// staticHandler serves the embedded CSS and JS. They are deliberately not
// behind the session gate: the login page references them too.
func staticHandler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub)), nil
}

// faviconContentType is set by hand rather than left to net/http, which would
// take it from the OS mime table — the Windows registry here, /etc/mime.types
// elsewhere — and answer differently depending on where the binary runs. With
// X-Content-Type-Options: nosniff the browser has to believe whatever comes
// back, so it is worth pinning.
const faviconContentType = "image/x-icon"

// handleFavicon serves the icon from the path browsers ask for unprompted. The
// file is under static/ and so is already reachable at /static/favicon.ico, but
// having the pages link to the same URL the browser probes for keeps it to one
// request and one cache entry. Like the rest of static/ it is outside the
// session gate, since the login page shows an icon too.
func handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", faviconContentType)
	http.ServeFileFS(w, r, staticFS, "static/favicon.ico")
}
