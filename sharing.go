package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const shareStateFilename = ".lanserve-shares.json"

var (
	adminPassword          string
	adminPasswordGenerated bool
	adminTmpl              *template.Template
	shareTmpl              *template.Template
	shareStore             *ShareStore
	resolvedRoot           string
)

type AdminPageData struct {
	CurrentPath string
	CurrentRel  string
	Entries     []Entry
	Parents     []Crumb
}

type SharedPageData struct {
	Token              string
	CurrentPath        string
	Entries            []Entry
	Parents            []Crumb
	ExpiresAt          time.Time
	HardExpiresAt      time.Time
	IdleTimeoutMinutes int
}

type ShareItem struct {
	Path  string
	Name  string
	IsDir bool
}

type Share struct {
	ID                 string
	Token              string
	Items              []ShareItem
	CreatedAt          time.Time
	LastActivityAt     time.Time
	ExpiresAt          time.Time
	HardExpiresAt      time.Time
	IdleTimeoutMinutes int
	MaxLifetimeMinutes int
	PageViews          int64
	Downloads          int64
	Revoked            bool
}

type ShareAdminView struct {
	ID                 string
	URL                string
	QRDataURL          string
	Items              []ShareItem
	CreatedAt          time.Time
	LastActivityAt     time.Time
	ExpiresAt          time.Time
	HardExpiresAt      time.Time
	IdleTimeoutMinutes int
	MaxLifetimeMinutes int
	PageViews          int64
	Downloads          int64
	Status             string
	RemainingSeconds   int64
}

type ShareStore struct {
	mu     sync.Mutex
	path   string
	shares []*Share
}

var (
	errShareNotFound = errors.New("share not found")
	errShareExpired  = errors.New("share expired")
	errShareDenied   = errors.New("path is not included in this share")
)

func initSharing(configuredPassword string) error {
	var err error
	resolvedRoot, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	adminPassword = configuredPassword
	if adminPassword == "" {
		adminPassword = password
	}
	if adminPassword == "" {
		adminPassword, err = randomSecret(18)
		if err != nil {
			return err
		}
		adminPasswordGenerated = true
	}
	shareStore, err = loadShareStore(filepath.Join(root, shareStateFilename))
	return err
}

func initShareTemplates() error {
	funcs := template.FuncMap{
		"humanSize":  humanSize,
		"formatTime": func(value time.Time) string { return value.Format("2006-01-02 15:04") },
	}
	var err error
	adminTmpl, err = template.New("admin.html").Funcs(funcs).ParseFS(tmplFS, "admin.html")
	if err != nil {
		return err
	}
	shareTmpl, err = template.New("share.html").Funcs(funcs).ParseFS(tmplFS, "share.html")
	return err
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, supplied, ok := r.BasicAuth()
		if !ok || user != "admin" || supplied != adminPassword {
			w.Header().Set("WWW-Authenticate", "Basic realm=\"ServeIt admin\"")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	rel, err := cleanShareRel(r.URL.Query().Get("path"), true)
	if err != nil || isShareStatePath(rel) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	abs, err := secureResolve(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		http.Error(w, "Admin browser path must be a directory", http.StatusBadRequest)
		return
	}
	entries, err := readDir(abs, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := AdminPageData{CurrentPath: displaySharePath(rel), CurrentRel: rel, Entries: entries, Parents: adminBreadcrumbs(rel)}
	if err := adminTmpl.Execute(w, data); err != nil {
		fmt.Printf("admin template error: %v\n", err)
	}
}

func sharesCollectionHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list := shareStore.Snapshot()
		views := make([]ShareAdminView, 0, len(list))
		for _, item := range list {
			views = append(views, makeAdminView(r, item))
		}
		writeShareJSON(w, http.StatusOK, views)
	case http.MethodPost:
		var request struct {
			Items              []string
			IdleTimeoutMinutes int
			MaxLifetimeMinutes int
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeShareError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		if request.IdleTimeoutMinutes < 1 || request.IdleTimeoutMinutes > 1440 {
			writeShareError(w, http.StatusBadRequest, "Idle timeout must be between 1 and 1440 minutes")
			return
		}
		if request.MaxLifetimeMinutes < request.IdleTimeoutMinutes || request.MaxLifetimeMinutes > 1440 {
			writeShareError(w, http.StatusBadRequest, "Maximum lifetime must be at least the idle timeout and no more than 1440 minutes")
			return
		}
		items, err := normalizeShareItems(request.Items)
		if err != nil {
			writeShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		created, err := shareStore.Create(items, request.IdleTimeoutMinutes, request.MaxLifetimeMinutes)
		if err != nil {
			writeShareError(w, http.StatusInternalServerError, "Could not create share")
			return
		}
		writeShareJSON(w, http.StatusCreated, makeAdminView(r, created))
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func shareItemHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/shares/"), "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if err := shareStore.Revoke(id); err != nil {
		writeShareError(w, http.StatusNotFound, "Share not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func shareHandler(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/s/"), "/")
	token, extra, found := strings.Cut(rest, "/")
	if found && extra == "download" {
		shareDownloadHandler(w, r, token)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if token == "" || found {
		http.NotFound(w, r)
		return
	}
	shared, err := shareStore.GetActive(token)
	if err != nil {
		serveShareError(w, err)
		return
	}
	rel, err := cleanShareRel(r.URL.Query().Get("path"), true)
	if err != nil || isShareStatePath(rel) || (rel != "" && !shareAllows(&shared, rel)) {
		http.Error(w, "This path is not included in the share.", http.StatusForbidden)
		return
	}
	var entries []Entry
	if rel == "" {
		entries = selectedEntries(&shared)
	} else {
		abs, resolveErr := secureResolve(rel)
		if resolveErr != nil {
			http.NotFound(w, r)
			return
		}
		info, statErr := os.Stat(abs)
		if statErr != nil {
			http.NotFound(w, r)
			return
		}
		if !info.IsDir() {
			if _, err := shareStore.Use(token, rel, true); err != nil {
				serveShareError(w, err)
				return
			}
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
			http.ServeFile(w, r, abs)
			return
		}
		entries, err = readDir(abs, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	shared, err = shareStore.Use(token, rel, false)
	if err != nil {
		serveShareError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := SharedPageData{Token: token, CurrentPath: sharedDisplayPath(rel), Entries: entries, Parents: sharedBreadcrumbs(&shared, rel), ExpiresAt: shared.ExpiresAt, HardExpiresAt: shared.HardExpiresAt, IdleTimeoutMinutes: shared.IdleTimeoutMinutes}
	if err := shareTmpl.Execute(w, data); err != nil {
		fmt.Printf("share template error: %v\n", err)
	}
}

func normalizeShareItems(paths []string) ([]ShareItem, error) {
	if len(paths) == 0 {
		return nil, errors.New("select at least one file or folder")
	}
	if len(paths) > 500 {
		return nil, errors.New("a share can contain at most 500 selected items")
	}
	seen := make(map[string]bool)
	items := make([]ShareItem, 0, len(paths))
	for _, raw := range paths {
		rel, err := cleanShareRel(raw, false)
		if err != nil || isShareStatePath(rel) {
			return nil, fmt.Errorf("invalid path: %s", raw)
		}
		if seen[rel] {
			continue
		}
		abs, err := secureResolve(rel)
		if err != nil {
			return nil, fmt.Errorf("path does not exist: %s", rel)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("cannot read path: %s", rel)
		}
		seen[rel] = true
		items = append(items, ShareItem{Path: rel, Name: info.Name(), IsDir: info.IsDir()})
	}
	return items, nil
}

func selectedEntries(shared *Share) []Entry {
	entries := make([]Entry, 0, len(shared.Items))
	for _, item := range shared.Items {
		abs, err := secureResolve(item.Path)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{Name: item.Path, RelPath: item.Path, IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries
}

func shareAllows(shared *Share, rel string) bool {
	for _, item := range shared.Items {
		if rel == item.Path || (item.IsDir && strings.HasPrefix(rel, item.Path+"/")) {
			return true
		}
	}
	return false
}

func cleanShareRel(raw string, allowEmpty bool) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" || raw == "." {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("empty path")
	}
	if strings.HasPrefix(raw, "/") {
		return "", errors.New("absolute path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") || filepath.IsAbs(cleaned) {
		return "", errors.New("path escapes root")
	}
	return cleaned, nil
}

func secureResolve(rel string) (string, error) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	within, err := filepath.Rel(resolvedRoot, real)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) || filepath.IsAbs(within) {
		return "", errors.New("path escapes served root")
	}
	return real, nil
}

func isShareStatePath(rel string) bool {
	return rel == shareStateFilename || strings.HasPrefix(rel, shareStateFilename+".") || strings.HasPrefix(rel, shareStateFilename+"/")
}

func displaySharePath(rel string) string {
	if rel == "" {
		return "/"
	}
	return "/" + rel
}

func adminBreadcrumbs(rel string) []Crumb {
	crumbs := []Crumb{{Name: "root", Path: ""}}
	if rel == "" {
		return crumbs
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		crumbs = append(crumbs, Crumb{Name: part, Path: strings.Join(parts[:i+1], "/")})
	}
	return crumbs
}

func sharedDisplayPath(rel string) string {
	if rel == "" {
		return "Shared items"
	}
	return filepath.Base(rel)
}

func sharedBreadcrumbs(shared *Share, rel string) []Crumb {
	crumbs := []Crumb{{Name: "shared items", Path: ""}}
	if rel == "" {
		return crumbs
	}
	base := ""
	for _, item := range shared.Items {
		if item.IsDir && (rel == item.Path || strings.HasPrefix(rel, item.Path+"/")) && len(item.Path) > len(base) {
			base = item.Path
		}
	}
	if base == "" {
		return crumbs
	}
	crumbs = append(crumbs, Crumb{Name: filepath.Base(base), Path: base})
	remainder := strings.TrimPrefix(strings.TrimPrefix(rel, base), "/")
	if remainder == "" {
		return crumbs
	}
	parts := strings.Split(remainder, "/")
	for i, part := range parts {
		crumbs = append(crumbs, Crumb{Name: part, Path: base + "/" + strings.Join(parts[:i+1], "/")})
	}
	return crumbs
}

func loadShareStore(path string) (*ShareStore, error) {
	store := &ShareStore{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &store.shares); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (store *ShareStore) Create(items []ShareItem, idleMinutes, maxMinutes int) (Share, error) {
	id, err := randomSecret(9)
	if err != nil {
		return Share{}, err
	}
	token, err := randomSecret(32)
	if err != nil {
		return Share{}, err
	}
	now := time.Now()
	hardExpiry := now.Add(time.Duration(maxMinutes) * time.Minute)
	expiry := now.Add(time.Duration(idleMinutes) * time.Minute)
	if expiry.After(hardExpiry) {
		expiry = hardExpiry
	}
	created := &Share{ID: id, Token: token, Items: items, CreatedAt: now, LastActivityAt: now, ExpiresAt: expiry, HardExpiresAt: hardExpiry, IdleTimeoutMinutes: idleMinutes, MaxLifetimeMinutes: maxMinutes}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.shares = append([]*Share{created}, store.shares...)
	if err := store.persistLocked(); err != nil {
		store.shares = store.shares[1:]
		return Share{}, err
	}
	return *created, nil
}

func (store *ShareStore) Snapshot() []Share {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Share, len(store.shares))
	for i, item := range store.shares {
		result[i] = *item
		result[i].Items = append([]ShareItem(nil), item.Items...)
	}
	return result
}

func (store *ShareStore) GetActive(token string) (Share, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := time.Now()
	for _, item := range store.shares {
		if item.Token != token {
			continue
		}
		if item.Revoked || !now.Before(item.ExpiresAt) || !now.Before(item.HardExpiresAt) {
			return Share{}, errShareExpired
		}
		copy := *item
		copy.Items = append([]ShareItem(nil), item.Items...)
		return copy, nil
	}
	return Share{}, errShareNotFound
}

func (store *ShareStore) Use(token, rel string, download bool) (Share, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := time.Now()
	for _, item := range store.shares {
		if item.Token != token {
			continue
		}
		if item.Revoked || !now.Before(item.ExpiresAt) || !now.Before(item.HardExpiresAt) {
			return Share{}, errShareExpired
		}
		if rel != "" && !shareAllows(item, rel) {
			return Share{}, errShareDenied
		}
		item.LastActivityAt = now
		item.ExpiresAt = now.Add(time.Duration(item.IdleTimeoutMinutes) * time.Minute)
		if item.ExpiresAt.After(item.HardExpiresAt) {
			item.ExpiresAt = item.HardExpiresAt
		}
		if download {
			item.Downloads++
		} else {
			item.PageViews++
		}
		if err := store.persistLocked(); err != nil {
			return Share{}, err
		}
		copy := *item
		copy.Items = append([]ShareItem(nil), item.Items...)
		return copy, nil
	}
	return Share{}, errShareNotFound
}

func (store *ShareStore) Revoke(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, item := range store.shares {
		if item.ID == id {
			item.Revoked = true
			return store.persistLocked()
		}
	}
	return errShareNotFound
}

func (store *ShareStore) persistLocked() error {
	data, err := json.MarshalIndent(store.shares, "", "  ")
	if err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, store.path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func makeAdminView(r *http.Request, item Share) ShareAdminView {
	now := time.Now()
	status := "active"
	remaining := item.ExpiresAt.Sub(now)
	if hardRemaining := item.HardExpiresAt.Sub(now); hardRemaining < remaining {
		remaining = hardRemaining
	}
	if item.Revoked {
		status = "revoked"
		remaining = 0
	} else if !now.Before(item.ExpiresAt) || !now.Before(item.HardExpiresAt) {
		status = "expired"
		remaining = 0
	}
	shareURL := requestBaseURL(r) + "/s/" + item.Token + "/"
	qrDataURL := ""
	if png, err := qrcode.Encode(shareURL, qrcode.Medium, 256); err == nil {
		qrDataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	return ShareAdminView{ID: item.ID, URL: shareURL, QRDataURL: qrDataURL, Items: item.Items, CreatedAt: item.CreatedAt, LastActivityAt: item.LastActivityAt, ExpiresAt: item.ExpiresAt, HardExpiresAt: item.HardExpiresAt, IdleTimeoutMinutes: item.IdleTimeoutMinutes, MaxLifetimeMinutes: item.MaxLifetimeMinutes, PageViews: item.PageViews, Downloads: item.Downloads, Status: status, RemainingSeconds: int64(remaining.Seconds())}
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := r.Host
	hostname, port, err := net.SplitHostPort(host)
	if err == nil && (hostname == "localhost" || (net.ParseIP(hostname) != nil && net.ParseIP(hostname).IsLoopback())) {
		if discovered, _ := discoverLANURLs(port); len(discovered) > 0 {
			return discovered[0]
		}
	}
	return scheme + "://" + host
}

func writeShareJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeShareError(w http.ResponseWriter, status int, message string) {
	writeShareJSON(w, status, map[string]string{"error": message})
}

func serveShareError(w http.ResponseWriter, err error) {
	if errors.Is(err, errShareExpired) {
		http.Error(w, "This share link has expired or was revoked.", http.StatusGone)
		return
	}
	if errors.Is(err, errShareDenied) {
		http.Error(w, "This path is not included in the share.", http.StatusForbidden)
		return
	}
	http.Error(w, "Share link not found.", http.StatusNotFound)
}

func randomSecret(length int) (string, error) {
	buffer := make([]byte, length)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func printStartup(port string) {
	width := terminalWidth()
	if width > 20 {
		width--
	}
	fmt.Print(renderStartup(port, width))
}

func renderStartup(port string, width int) string {
	if width < 40 {
		width = 40
	}
	browser := "open on the LAN"
	if password != "" {
		browser = "password required, user admin"
	}
	secret := adminPassword
	if adminPasswordGenerated {
		secret += "  (generated for this run)"
	}
	facts := [][2]string{
		{"Directory", root},
		{"Port", port},
		{"File browser", browser},
		{"Admin user", "admin"},
		{"Admin password", secret},
		{"Stop", "Ctrl+C"},
	}
	urls, skipped := discoverLANURLs(port)
	if skipped > 0 {
		note := "Skipped 1 other address"
		if skipped != 1 {
			note = fmt.Sprintf("Skipped %d other addresses", skipped)
		}
		facts = append(facts, [2]string{"Note", note})
	}
	var out strings.Builder
	out.WriteString("\n")
	out.WriteString(startupBox("ServeIt", facts, width))
	out.WriteString("\n")
	for _, base := range urls {
		out.WriteString(renderAddressBlock(base, width))
		out.WriteString("\n")
	}
	return out.String()
}

func startupBox(title string, rows [][2]string, width int) string {
	inner := width - 4
	if inner < 24 {
		inner = 24
	}
	labelWidth := 16
	var body []string
	body = append(body, padRight(title, inner))
	body = append(body, strings.Repeat("-", inner))
	for _, row := range rows {
		valueWidth := inner - labelWidth - 1
		if valueWidth < 8 {
			valueWidth = 8
		}
		lines := wrapText(row[1], valueWidth)
		if len(lines) == 0 {
			lines = []string{""}
		}
		for i, line := range lines {
			label := ""
			if i == 0 {
				label = row[0]
			}
			body = append(body, padRight(padRight(label, labelWidth)+" "+line, inner))
		}
	}
	var out strings.Builder
	rule := "+" + strings.Repeat("-", inner+2) + "+\n"
	out.WriteString(rule)
	for _, line := range body {
		out.WriteString("| " + padRight(line, inner) + " |\n")
	}
	out.WriteString(rule)
	return out.String()
}

func renderAddressBlock(base string, width int) string {
	files := qrColumn("Files", base)
	admin := qrColumn("Admin", base+"/admin")
	gap := 4
	pairedWidth := columnWidth(files) + gap + columnWidth(admin)
	var lines []string
	if pairedWidth <= width {
		lines = joinColumns(files, admin, gap)
	} else {
		lines = append(lines, files...)
		lines = append(lines, "")
		lines = append(lines, admin...)
	}
	host := strings.TrimPrefix(base, "http://")
	var out strings.Builder
	out.WriteString(host)
	out.WriteString("\n\n")
	for _, line := range lines {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func qrColumn(title, address string) []string {
	lines := []string{title, address, ""}
	code, err := qrcode.New(address, qrcode.Medium)
	if err != nil {
		lines = append(lines, "(QR code unavailable)")
		return lines
	}
	raw := strings.TrimRight(code.ToSmallString(false), "\n")
	if raw == "" {
		lines = append(lines, "(QR code unavailable)")
		return lines
	}
	return append(lines, strings.Split(raw, "\n")...)
}

func joinColumns(left, right []string, gap int) []string {
	width := columnWidth(left)
	rows := len(left)
	if len(right) > rows {
		rows = len(right)
	}
	spacer := strings.Repeat(" ", gap)
	out := make([]string, rows)
	for i := 0; i < rows; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = padRight(l, width) + spacer + r
	}
	return out
}

func columnWidth(lines []string) int {
	width := 0
	for _, line := range lines {
		if n := runeLen(line); n > width {
			width = n
		}
	}
	return width
}

func wrapText(value string, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	rest := value
	for rest != "" {
		if runeLen(rest) <= width {
			lines = append(lines, rest)
			break
		}
		cut := width
		if cut > len(rest) {
			cut = len(rest)
		}
		chunk := string([]rune(rest)[:cut])
		rest = string([]rune(rest)[cut:])
		lines = append(lines, chunk)
	}
	return lines
}

func padRight(value string, width int) string {
	if extra := width - runeLen(value); extra > 0 {
		return value + strings.Repeat(" ", extra)
	}
	return value
}

func runeLen(value string) int {
	n := 0
	for _, r := range value {
		_ = r
		n++
	}
	return n
}

func discoverLANURLs(port string) ([]string, int) {
	var ips []net.IP
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, networkInterface := range interfaces {
			if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addresses, _ := networkInterface.Addrs()
			for _, address := range addresses {
				switch value := address.(type) {
				case *net.IPNet:
					ips = append(ips, value.IP)
				case *net.IPAddr:
					ips = append(ips, value.IP)
				}
			}
		}
	}
	return chooseLANURLs(port, ips)
}

func chooseLANURLs(port string, ips []net.IP) ([]string, int) {
	seen := make(map[string]bool)
	var urls []string
	skipped := 0
	for _, ip := range ips {
		ip4 := ip.To4()
		if ip4 == nil || ip.IsLoopback() {
			continue
		}
		if ip4[0] != 192 || ip4[1] != 168 {
			if !seen["skip:"+ip4.String()] {
				seen["skip:"+ip4.String()] = true
				skipped++
			}
			continue
		}
		base := fmt.Sprintf("http://%s:%s", ip4.String(), port)
		if !seen[base] {
			seen[base] = true
			urls = append(urls, base)
		}
	}
	sort.Strings(urls)
	if len(urls) == 0 {
		return []string{"http://127.0.0.1:" + port}, skipped
	}
	return urls, skipped
}
