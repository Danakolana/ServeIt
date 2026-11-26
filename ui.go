package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	themeClassic    = "classic"
	themeModern     = "modern"
	themeHarbor     = "harbor"
	themeCookieName = "serveit_theme"
)

type ThemeOption struct {
	Name   string
	Label  string
	URL    string
	Active bool
}

var themeOrder = []struct {
	name  string
	label string
}{
	{themeClassic, "Classic"},
	{themeModern, "Modern"},
	{themeHarbor, "Harbor"},
}

type uiTemplateSet struct {
	browse *template.Template
	admin  *template.Template
	share  *template.Template
}

var uiTemplates map[string]uiTemplateSet

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"humanSize":  humanSize,
		"formatTime": func(value time.Time) string { return value.Format("2006-01-02 15:04") },
		"pathEscape": pathEscape,
	}
}

func pathEscape(value string) string {
	segments := strings.Split(value, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func initTemplates() error {
	sets := map[string][3]string{
		themeClassic: {"tmpl.html", "admin.html", "share.html"},
		themeModern:  {"themes/modern/tmpl.html", "themes/modern/admin.html", "themes/modern/share.html"},
		themeHarbor:  {"themes/harbor/tmpl.html", "themes/harbor/admin.html", "themes/harbor/share.html"},
	}
	uiTemplates = make(map[string]uiTemplateSet, len(sets))
	for theme, files := range sets {
		browse, err := parseEmbeddedTemplate(files[0])
		if err != nil {
			return fmt.Errorf("parse %s browse template: %w", theme, err)
		}
		admin, err := parseEmbeddedTemplate(files[1])
		if err != nil {
			return fmt.Errorf("parse %s admin template: %w", theme, err)
		}
		share, err := parseEmbeddedTemplate(files[2])
		if err != nil {
			return fmt.Errorf("parse %s share template: %w", theme, err)
		}
		uiTemplates[theme] = uiTemplateSet{browse: browse, admin: admin, share: share}
	}
	return nil
}

func parseEmbeddedTemplate(filename string) (*template.Template, error) {
	return template.New(path.Base(filename)).Funcs(templateFuncs()).ParseFS(tmplFS, filename)
}

func requestTheme(r *http.Request) string {
	cookie, err := r.Cookie(themeCookieName)
	if err == nil && isSupportedTheme(cookie.Value) {
		return cookie.Value
	}
	return themeClassic
}

func isSupportedTheme(theme string) bool {
	for _, option := range themeOrder {
		if option.name == theme {
			return true
		}
	}
	return false
}

func templatesForRequest(r *http.Request) uiTemplateSet {
	return uiTemplates[requestTheme(r)]
}

func otherTheme(r *http.Request) string {
	current := requestTheme(r)
	for i, option := range themeOrder {
		if option.name == current {
			return themeOrder[(i+1)%len(themeOrder)].name
		}
	}
	return themeClassic
}

func themeSwitchURL(r *http.Request) string {
	return "/theme?name=" + url.QueryEscape(otherTheme(r)) + "&next=" + url.QueryEscape(r.URL.RequestURI())
}

func themeOptions(r *http.Request) []ThemeOption {
	current := requestTheme(r)
	next := r.URL.RequestURI()
	options := make([]ThemeOption, 0, len(themeOrder))
	for _, option := range themeOrder {
		options = append(options, ThemeOption{
			Name:   option.name,
			Label:  option.label,
			Active: option.name == current,
			URL:    "/theme?name=" + url.QueryEscape(option.name) + "&next=" + url.QueryEscape(next),
		})
	}
	return options
}

func themeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	theme := r.URL.Query().Get("name")
	if !isSupportedTheme(theme) {
		http.Error(w, "Unknown theme", http.StatusBadRequest)
		return
	}
	next := safeThemeReturn(r.URL.Query().Get("next"))
	http.SetCookie(w, &http.Cookie{
		Name:     themeCookieName,
		Value:    theme,
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func safeThemeReturn(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "/"
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	return value
}

func registerUIRoutes(mux *http.ServeMux) error {
	staticFS, err := fs.Sub(tmplFS, "static")
	if err != nil {
		return err
	}
	files := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))
	mux.Handle("/static/", cacheableGetHead(files))
	mux.HandleFunc("/static", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Redirect(w, r, "/static/", http.StatusMovedPermanently)
	})

	favicon, err := tmplFS.ReadFile("static/favicon.ico")
	if err != nil {
		return err
	}
	mux.Handle("/favicon.ico", cacheableGetHead(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "favicon.ico", time.Time{}, bytes.NewReader(favicon))
	})))
	mux.HandleFunc("/theme", themeHandler)
	return nil
}

func cacheableGetHead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}
