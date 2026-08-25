package httpapi

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed templates/*.html
var templateFS embed.FS

var pageTemplates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

type pageData struct {
	ManagerUsername string
	ServiceURL      string
	Problems        []string
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	// The catch-all rewrite sends every unmatched path here; only "/" is the
	// setup page.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.render(w, "setup.html", pageData{
		ManagerUsername: s.cfg.ManagerBotUsername,
		ServiceURL:      s.cfg.ResolveBaseURL(r),
		Problems:        s.problems,
	})
}

func (s *Server) privacyPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "privacy.html", pageData{
		ManagerUsername: s.cfg.ManagerBotUsername,
		ServiceURL:      s.cfg.ResolveBaseURL(r),
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The pages carry no secret, but they do report deployment state, so they
	// should not be cached by an intermediary.
	w.Header().Set("Cache-Control", "no-store")
	// Everything is inline and same-origin; no external resource is loaded.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src data:; base-uri 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := pageTemplates.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("could not render page", "template", name, "error", err)
	}
}
