package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─── embedded HTML template ──────────────────────────────────────────────────

//go:embed tmpl.html admin.html share.html
var tmplFS embed.FS

// ─── data types ──────────────────────────────────────────────────────────────

type Entry struct {
	Name    string
	RelPath string // relative to served root, URL-safe
	IsDir   bool
	Size    int64
	ModTime time.Time
}

type PageData struct {
	CurrentPath string
	Entries     []Entry
	Parents     []Crumb
	Query       string
}

type Crumb struct {
	Name string
	Path string
}

type SearchResult struct {
	Name    string `json:"name"`
	RelPath string `json:"relPath"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
}

// ─── globals ─────────────────────────────────────────────────────────────────

var (
	root     string
	password string
	tmpl     *template.Template
)

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	port := flag.String("port", "8080", "port to listen on")
	pass := flag.String("password", "", "basic-auth password (username is always 'admin'); leave empty to disable auth")
	adminPass := flag.String("admin-password", "", "super-admin password (username is always 'admin'); generated when omitted")
	dir := flag.String("dir", ".", "directory to serve")
	flag.Parse()

	var err error
	root, err = filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("cannot resolve directory: %v", err)
	}
	password = *pass
	if err := initSharing(*adminPass); err != nil {
		log.Fatalf("cannot initialize sharing: %v", err)
	}

	// load embedded template
	raw, err := tmplFS.ReadFile("tmpl.html")
	if err != nil {
		log.Fatalf("cannot read embedded template: %v", err)
	}
	tmpl = template.Must(template.New("page").Funcs(template.FuncMap{
		"humanSize": humanSize,
		"formatTime": func(t time.Time) string {
			return t.Format("2006-01-02 15:04")
		},
	}).Parse(string(raw)))
	if err := initShareTemplates(); err != nil {
		log.Fatalf("cannot load sharing templates: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/s/", shareHandler)
	mux.HandleFunc("/admin", withAdminAuth(adminHandler))
	mux.HandleFunc("/admin/", withAdminAuth(adminHandler))
	mux.HandleFunc("/api/admin/shares", withAdminAuth(sharesCollectionHandler))
	mux.HandleFunc("/api/admin/shares/", withAdminAuth(shareItemHandler))
	mux.HandleFunc("/api/download", withAuth(downloadHandler))
	mux.HandleFunc("/search", withAuth(searchHandler))
	mux.HandleFunc("/", withAuth(browseHandler))

	listener, err := net.Listen("tcp", "0.0.0.0:"+*port)
	if err != nil {
		log.Fatalf("cannot listen on port %s: %v", *port, err)
	}
	printStartup(*port)
	if err := http.Serve(listener, securityHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}

// ─── middleware ───────────────────────────────────────────────────────────────

func withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if password != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != "admin" || pass != password {
				w.Header().Set("WWW-Authenticate", `Basic realm="ServeIt"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

// ─── browse handler ───────────────────────────────────────────────────────────

func browseHandler(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanShareRel(strings.TrimPrefix(r.URL.Path, "/"), true)
	if err != nil || isShareStatePath(rel) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	abs, err := secureResolve(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// if it's a file, serve it for download
	if !info.IsDir() {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, info.Name()))
		http.ServeFile(w, r, abs)
		return
	}

	// directory listing
	entries, err := readDir(abs, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := PageData{
		CurrentPath: "/" + filepath.ToSlash(rel),
		Entries:     entries,
		Parents:     breadcrumbs(rel),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("template error: %v", err)
	}
}

func readDir(abs, rel string) ([]Entry, error) {
	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}

	var entries []Entry
	for _, de := range dirEntries {
		if isShareStatePath(filepath.ToSlash(filepath.Join(rel, de.Name()))) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		relPath := filepath.ToSlash(filepath.Join(rel, de.Name()))
		entries = append(entries, Entry{
			Name:    de.Name(),
			RelPath: relPath,
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	// dirs first, then files, both alphabetical
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

func breadcrumbs(rel string) []Crumb {
	crumbs := []Crumb{{Name: "root", Path: "/"}}
	if rel == "" || rel == "." {
		return crumbs
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		crumbs = append(crumbs, Crumb{
			Name: p,
			Path: "/" + strings.Join(parts[:i+1], "/"),
		})
	}
	return crumbs
}

// ─── search handler ───────────────────────────────────────────────────────────

func searchHandler(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if q == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]SearchResult{})
		return
	}

	var results []SearchResult
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable
		}
		relPath, _ := filepath.Rel(root, path)
		if isShareStatePath(filepath.ToSlash(relPath)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), q) {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			var size int64
			if !d.IsDir() {
				if info, e := d.Info(); e == nil {
					size = info.Size()
				}
			}
			results = append(results, SearchResult{
				Name:    d.Name(),
				RelPath: rel,
				IsDir:   d.IsDir(),
				Size:    size,
			})
		}
		if len(results) >= 200 { // cap results
			return filepath.SkipAll
		}
		return nil
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
