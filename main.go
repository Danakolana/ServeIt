package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─── embedded HTML template ──────────────────────────────────────────────────

//go:embed tmpl.html admin.html share.html themes/modern/*.html themes/harbor/*.html static
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
	CurrentPath    string
	Entries        []Entry
	Parents        []Crumb
	Query          string
	ThemeSwitchURL string
	ThemeOptions   []ThemeOption
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

const (
	maxSearchResults      = 200
	maxConcurrentSearches = 4
)

// ─── globals ─────────────────────────────────────────────────────────────────

var (
	root          string
	password      string
	showHidden    bool
	allowedHosts  map[string]struct{}
	serverAddress string
	searchSlots   = make(chan struct{}, maxConcurrentSearches)
)

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	port := flag.String("port", "8080", "port to listen on")
	pass := flag.String("password", "", "basic-auth password (username is always 'admin'); leave empty to disable auth")
	adminPass := flag.String("admin-password", "", "super-admin password (username is always 'admin'); generated when omitted")
	publicURLFlag := flag.String("public-url", "", "canonical http(s) origin used in guest links and QR codes")
	listenAddressFlag := flag.String("listen", "0.0.0.0", "IP address to listen on")
	stateFileFlag := flag.String("state-file", "", "path for persistent share state (defaults to a per-user ServeIt state file)")
	showHiddenFlag := flag.Bool("show-hidden", false, "include files and folders whose path segment starts with '.'")
	var allowHostFlags stringListFlag
	flag.Var(&allowHostFlags, "allow-host", "additional Host name or IP accepted by the server (repeatable)")
	dir := flag.String("dir", ".", "directory to serve")
	flag.Parse()

	var err error
	root, err = filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("cannot resolve directory: %v", err)
	}
	password = *pass
	showHidden = *showHiddenFlag
	serverPort = *port
	stateFileOverride = strings.TrimSpace(*stateFileFlag)
	if stateFileOverride == "" {
		stateFileOverride = strings.TrimSpace(os.Getenv("SERVEIT_STATE_FILE"))
	}
	serverAddress = strings.TrimSpace(*listenAddressFlag)
	if strings.HasPrefix(serverAddress, "[") && strings.HasSuffix(serverAddress, "]") {
		serverAddress = strings.TrimSuffix(strings.TrimPrefix(serverAddress, "["), "]")
	}
	if serverAddress == "" {
		log.Fatal("--listen must not be empty")
	}
	allowedHosts = buildAllowedHosts(serverAddress, allowHostFlags)
	publicBaseURL, err = normalizePublicURL(*publicURLFlag)
	if err != nil {
		log.Fatalf("invalid --public-url: %v", err)
	}
	if publicBaseURL != "" {
		if parsed, parseErr := url.Parse(publicBaseURL); parseErr == nil {
			allowedHosts[canonicalHost(parsed.Host)] = struct{}{}
		}
	}
	if err := initSharing(*adminPass); err != nil {
		log.Fatalf("cannot initialize sharing: %v", err)
	}

	if err := initTemplates(); err != nil {
		log.Fatalf("cannot load UI templates: %v", err)
	}

	mux := http.NewServeMux()
	if err := registerUIRoutes(mux); err != nil {
		log.Fatalf("cannot load UI assets: %v", err)
	}
	mux.HandleFunc("/s/", shareHandler)
	mux.HandleFunc("/admin", withAdminAuth(adminHandler))
	mux.HandleFunc("/admin/", withAdminAuth(adminHandler))
	mux.HandleFunc("/api/admin/shares", withAdminAuth(sharesCollectionHandler))
	mux.HandleFunc("/api/admin/shares/", withAdminAuth(shareItemHandler))
	mux.HandleFunc("/api/download", withAuth(downloadHandler))
	mux.HandleFunc("/search", withAuth(searchHandler))
	mux.HandleFunc("/", withAuth(browseHandler))

	listener, err := net.Listen("tcp", net.JoinHostPort(serverAddress, *port))
	if err != nil {
		log.Fatalf("cannot listen on port %s: %v", *port, err)
	}
	printStartup(*port)
	server := &http.Server{
		Handler:           hostValidation(securityHeaders(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := server.Serve(listener); err != nil {
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

func hostValidation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "Invalid Host header", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func hostAllowed(value string) bool {
	host := canonicalHost(value)
	if host == "" {
		return false
	}
	_, ok := allowedHosts[host]
	return ok
}

func canonicalHost(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, "."))
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.Trim(value, "[]")
	}
	if ip := net.ParseIP(value); ip != nil {
		return strings.ToLower(ip.String())
	}
	return strings.ToLower(strings.TrimSuffix(value, "."))
}

func buildAllowedHosts(listenAddress string, explicit []string) map[string]struct{} {
	allowed := map[string]struct{}{
		"localhost": {},
		"127.0.0.1": {},
		"::1":       {},
	}
	if hostname, err := os.Hostname(); err == nil {
		if host := canonicalHost(hostname); host != "" {
			allowed[host] = struct{}{}
		}
	}
	if host := canonicalHost(listenAddress); host != "" && host != "0.0.0.0" && host != "::" {
		allowed[host] = struct{}{}
	}
	if interfaces, err := net.Interfaces(); err == nil {
		for _, networkInterface := range interfaces {
			addresses, _ := networkInterface.Addrs()
			for _, address := range addresses {
				var ip net.IP
				switch value := address.(type) {
				case *net.IPNet:
					ip = value.IP
				case *net.IPAddr:
					ip = value.IP
				}
				if ip != nil {
					allowed[canonicalHost(ip.String())] = struct{}{}
				}
			}
		}
	}
	for _, value := range explicit {
		if host := canonicalHost(value); host != "" {
			allowed[host] = struct{}{}
		}
	}
	return allowed
}

func isHiddenPath(rel string) bool {
	if showHidden {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part != "" && strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// ─── browse handler ───────────────────────────────────────────────────────────

func browseHandler(w http.ResponseWriter, r *http.Request) {
	rel, err := cleanShareRel(strings.TrimPrefix(r.URL.Path, "/"), true)
	if err != nil || isShareStatePath(rel) || isHiddenPath(rel) {
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
		if err := serveRootFile(w, r, rel); err != nil {
			http.NotFound(w, r)
		}
		return
	}

	// directory listing
	entries, err := readDir(abs, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := PageData{
		CurrentPath:    "/" + filepath.ToSlash(rel),
		Entries:        entries,
		Parents:        breadcrumbs(rel),
		ThemeSwitchURL: themeSwitchURL(r),
		ThemeOptions:   themeOptions(r),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templatesForRequest(r).browse.Execute(w, data); err != nil {
		log.Printf("template error: %v", err)
	}
}

func serveRootFile(w http.ResponseWriter, r *http.Request, rel string) error {
	rootHandle, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return err
	}
	defer rootHandle.Close()
	file, err := rootHandle.Open(filepath.FromSlash(rel))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("path is a directory")
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	return nil
}

func readDir(abs, rel string) ([]Entry, error) {
	dirEntries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}

	var entries []Entry
	for _, de := range dirEntries {
		relPath := filepath.ToSlash(filepath.Join(rel, de.Name()))
		if isHiddenPath(relPath) {
			continue
		}
		if _, err := secureResolve(relPath); err != nil {
			continue
		}
		entryAbs := filepath.Join(abs, de.Name())
		info, err := os.Stat(entryAbs)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Name:    de.Name(),
			RelPath: relPath,
			IsDir:   info.IsDir(),
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
	select {
	case searchSlots <- struct{}{}:
		defer func() { <-searchSlots }()
	case <-r.Context().Done():
		return
	}

	var results []SearchResult
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if r.Context().Err() != nil {
			return filepath.SkipAll
		}
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
		if isHiddenPath(filepath.ToSlash(relPath)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.Contains(strings.ToLower(d.Name()), q) {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, err := secureResolve(rel); err != nil {
				return nil
			}
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
		if len(results) >= maxSearchResults {
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
