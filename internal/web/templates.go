package web

import (
	"embed"
	"html/template"
)

//go:embed templates/*.html
var templateFS embed.FS

// parseTemplates loads every page template. Each page is standalone rather than
// composed from a shared layout: two pages do not justify the indirection.
func parseTemplates() (*template.Template, error) {
	return template.ParseFS(templateFS, "templates/*.html")
}
