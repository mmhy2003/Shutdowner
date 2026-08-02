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
