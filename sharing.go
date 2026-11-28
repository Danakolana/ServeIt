package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	shareStateFilename       = ".serveit-shares.json"
	legacyShareStateFilename = ".lanserve-shares.json"
	maxShareItems            = 500
	maxShareTimeoutMinutes   = 1440
	maxShareTitleRunes       = 80
	maxShareNoteRunes        = 240
	shareExpiredHeading      = "This link has expired."
	shareExpiredDetail       = "The time limit has ended. Ask for a new link if you still need these files."
	shareRevokedHeading      = "This link was revoked."
	shareRevokedDetail       = "The person who shared these files withdrew access."
	shareRetentionPeriod     = 30 * 24 * time.Hour
	activityCoalesceDelay    = 250 * time.Millisecond
)

var (
	adminPassword          string
	adminPasswordGenerated bool
	publicBaseURL          string
	serverPort             string
	shareStore             *ShareStore
	resolvedRoot           string
	stateFileOverride      string
	activeStatePaths       []string
)

type cspNonceKey struct{}

type AdminPageData struct {
	CurrentPath    string
	CurrentRel     string
	Entries        []Entry
	Parents        []Crumb
	ThemeSwitchURL string
	ThemeOptions   []ThemeOption
	CSPNonce       string
}

type ShareGoneData struct {
	Heading  string
	Detail   string
	CSPNonce string
}

type SharedPageData struct {
	Token                string
	CurrentPath          string
	Entries              []Entry
	Parents              []Crumb
	ExpiresAt            time.Time
	HardExpiresAt        time.Time
	IdleTimeoutMinutes   int
	RemainingSeconds     int64
	HardRemainingSeconds int64
	Title                string
	Note                 string
	ThemeSwitchURL       string
	ThemeOptions         []ThemeOption
	CSPNonce             string
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
	Title              string
	Note               string
	PageViews          int64
	Downloads          int64
	Revoked            bool
}

type ShareAdminView struct {
	ID                 string
	URL                string
	QRDataURL          string `json:"QRDataURL,omitempty"`
	Items              []ShareItem
	CreatedAt          time.Time
	LastActivityAt     time.Time
	ExpiresAt          time.Time
	HardExpiresAt      time.Time
	IdleTimeoutMinutes int
	MaxLifetimeMinutes int
	Title              string
	Note               string
	PageViews          int64
	Downloads          int64
	Status             string
	RemainingSeconds   int64
}

type ShareStore struct {
	mu            sync.Mutex
	persistMu     sync.Mutex
	version       uint64
	path          string
	shares        []*Share
	activityDirty bool
	activityTimer *time.Timer
}

var (
	errShareNotFound = errors.New("share not found")
	errShareExpired  = errors.New("share expired")
	errShareRevoked  = errors.New("share revoked")
	errShareDenied   = errors.New("path is not included in this share")
	errShareStorage  = errors.New("share storage unavailable")
)

func initSharing(configuredPassword string) error {
	var err error
	resolvedRoot, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	adminPassword = configuredPassword
	if adminPassword == "" {
		adminPassword, err = randomSecret(18)
		if err != nil {
			return err
		}
		adminPasswordGenerated = true
	}
	statePath, err := resolveStatePath()
	if err != nil {
		return err
	}
	activeStatePaths = []string{statePath}
	if _, statErr := os.Stat(statePath); errors.Is(statErr, os.ErrNotExist) && stateFileOverride == "" && strings.TrimSpace(os.Getenv("SERVEIT_STATE_FILE")) == "" {
		for _, legacyPath := range []string{filepath.Join(root, shareStateFilename), filepath.Join(root, legacyShareStateFilename)} {
			if _, legacyErr := os.Stat(legacyPath); legacyErr == nil {
				shareStore, err = loadShareStore(legacyPath)
				if err == nil {
					shareStore.path = statePath
					activeStatePaths = append(activeStatePaths, legacyPath)
				}
				return err
			} else if !errors.Is(legacyErr, os.ErrNotExist) {
				return legacyErr
			}
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	shareStore, err = loadShareStore(statePath)
	return err
}

func resolveStatePath() (string, error) {
	value := strings.TrimSpace(stateFileOverride)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("SERVEIT_STATE_FILE"))
	}
	if value != "" {
		if !filepath.IsAbs(value) {
			absolute, err := filepath.Abs(value)
			if err != nil {
				return "", err
			}
			value = absolute
		}
		return filepath.Clean(value), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(configDir) == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("cannot determine a per-user state directory: %w", err)
		}
		configDir = filepath.Join(home, ".config")
	}
	digest := sha256.Sum256([]byte(resolvedRoot))
	return filepath.Join(configDir, "ServeIt", "shares", fmt.Sprintf("%x.json", digest[:12])), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce, err := randomSecret(18)
		if err != nil {
			nonce = "serveit-csp-fallback"
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), cspNonceKey{}, nonce)))
	})
}

func withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authenticateRequest(w, r, adminPassword, `Basic realm="ServeIt admin"`) {
			return
		}
		next(w, r)
	}
}

func validateAdminMutation(r *http.Request, requireJSON bool) error {
	if r.Header.Get("X-ServeIt-Request") != "admin" {
		return errors.New("missing admin request header")
	}
	if requireJSON {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			return errors.New("admin mutations require application/json")
		}
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid request origin")
	}
	expectedScheme := "http"
	expectedHost := r.Host
	if r.TLS != nil {
		expectedScheme = "https"
	}
	if publicBaseURL != "" {
		canonical, parseErr := url.Parse(publicBaseURL)
		if parseErr != nil {
			return errors.New("invalid canonical URL")
		}
		expectedScheme, expectedHost = canonical.Scheme, canonical.Host
	}
	if !strings.EqualFold(parsed.Scheme, expectedScheme) || !strings.EqualFold(parsed.Host, expectedHost) {
		return errors.New("request origin does not match this server")
	}
	return nil
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}
	rel, err := cleanShareRel(r.URL.Query().Get("path"), true)
	if err != nil || isShareStatePath(rel) || isHiddenPath(rel) {
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
	data := AdminPageData{CurrentPath: displaySharePath(rel), CurrentRel: rel, Entries: entries, Parents: adminBreadcrumbs(rel), ThemeSwitchURL: themeSwitchURL(r), ThemeOptions: themeOptions(r), CSPNonce: requestCSPNonce(r)}
	if err := templatesForRequest(r).admin.Execute(w, data); err != nil {
		log.Printf("admin template error: %v", err)
	}
}

func sharesCollectionHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		etag := fmt.Sprintf(`"shares-%d"`, shareStore.Version())
		w.Header().Set("ETag", etag)
		if strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		list := shareStore.Snapshot()
		views := make([]ShareAdminView, 0, len(list))
		for _, item := range list {
			views = append(views, makeAdminView(r, item))
		}
		writeShareJSON(w, http.StatusOK, views)
	case http.MethodPost:
		if err := validateAdminMutation(r, true); err != nil {
			writeShareError(w, http.StatusForbidden, "Forbidden admin request")
			return
		}
		var request struct {
			Items              []string
			IdleTimeoutMinutes int
			MaxLifetimeMinutes int
			Title              string
			Note               string
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeShareError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		if request.IdleTimeoutMinutes < 1 || request.IdleTimeoutMinutes > maxShareTimeoutMinutes {
			writeShareError(w, http.StatusBadRequest, fmt.Sprintf("Idle timeout must be between 1 and %d minutes", maxShareTimeoutMinutes))
			return
		}
		if request.MaxLifetimeMinutes < request.IdleTimeoutMinutes || request.MaxLifetimeMinutes > maxShareTimeoutMinutes {
			writeShareError(w, http.StatusBadRequest, fmt.Sprintf("Maximum lifetime must be at least the idle timeout and no more than %d minutes", maxShareTimeoutMinutes))
			return
		}
		items, err := normalizeShareItems(request.Items)
		if err != nil {
			writeShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		title, err := normalizeShareLabel(request.Title, maxShareTitleRunes, "Title")
		if err != nil {
			writeShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		note, err := normalizeShareLabel(request.Note, maxShareNoteRunes, "Note")
		if err != nil {
			writeShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		created, err := shareStore.Create(items, request.IdleTimeoutMinutes, request.MaxLifetimeMinutes, title, note)
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
	if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/qr") {
		shareQRHandler(w, r)
		return
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := validateAdminMutation(r, false); err != nil {
		writeShareError(w, http.StatusForbidden, "Forbidden admin request")
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/shares/"), "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if err := shareStore.Revoke(id); err != nil {
		if errors.Is(err, errShareStorage) {
			writeShareError(w, http.StatusServiceUnavailable, "The share state could not be saved. Try again.")
			return
		}
		writeShareError(w, http.StatusNotFound, "Share not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func shareQRHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/admin/shares/"), "/qr"), "/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	for _, item := range shareStore.Snapshot() {
		if item.ID != id {
			continue
		}
		shareURL := requestBaseURL(r) + "/s/" + item.Token + "/"
		png, err := qrcode.Encode(shareURL, qrcode.Medium, 256)
		if err != nil {
			writeShareError(w, http.StatusInternalServerError, "Could not generate QR code")
			return
		}
		writeShareJSON(w, http.StatusOK, map[string]string{"QRDataURL": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
		return
	}
	http.NotFound(w, r)
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
		if serveShareGonePage(w, r, err) {
			return
		}
		serveShareError(w, err)
		return
	}
	rel, err := cleanShareRel(r.URL.Query().Get("path"), true)
	if err != nil || isShareStatePath(rel) || isHiddenPath(rel) || (rel != "" && !shareAllows(&shared, rel)) {
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
			trackDownload := r.Method == http.MethodGet && r.Header.Get("Range") == ""
			writer := w
			statusWriter := &responseStatusWriter{ResponseWriter: w}
			if trackDownload {
				writer = statusWriter
			}
			if err := serveRootFile(writer, r, rel); err != nil {
				http.NotFound(w, r)
				return
			}
			if trackDownload && statusWriter.status >= http.StatusOK && statusWriter.status < http.StatusMultipleChoices {
				if _, err := shareStore.Touch(token, rel, true); err != nil {
					log.Printf("share download activity update failed: %v", err)
				}
			}
			return
		}
		entries, err = readDir(abs, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		visible := entries[:0]
		for _, entry := range entries {
			if shareAllows(&shared, entry.RelPath) {
				visible = append(visible, entry)
			}
		}
		entries = visible
	}
	shared, err = shareStore.Touch(token, rel, false)
	if err != nil {
		if serveShareGonePage(w, r, err) {
			return
		}
		serveShareError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	remaining, hardRemaining := shareRemainingSeconds(shared, time.Now())
	data := SharedPageData{Token: token, CurrentPath: sharedDisplayPath(rel), Entries: entries, Parents: sharedBreadcrumbs(&shared, rel), ExpiresAt: shared.ExpiresAt, HardExpiresAt: shared.HardExpiresAt, IdleTimeoutMinutes: shared.IdleTimeoutMinutes, RemainingSeconds: remaining, HardRemainingSeconds: hardRemaining, Title: shared.Title, Note: shared.Note, ThemeSwitchURL: themeSwitchURL(r), ThemeOptions: themeOptions(r), CSPNonce: requestCSPNonce(r)}
	if err := templatesForRequest(r).share.Execute(w, data); err != nil {
		log.Printf("share template error: %v", err)
	}
}

func shareRemainingSeconds(shared Share, now time.Time) (int64, int64) {
	remaining := shared.ExpiresAt.Sub(now)
	hardRemaining := shared.HardExpiresAt.Sub(now)
	if hardRemaining < remaining {
		remaining = hardRemaining
	}
	if remaining < 0 {
		remaining = 0
	}
	if hardRemaining < 0 {
		hardRemaining = 0
	}
	return int64(remaining.Seconds()), int64(hardRemaining.Seconds())
}

func normalizeShareItems(paths []string) ([]ShareItem, error) {
	if len(paths) == 0 {
		return nil, errors.New("select at least one file or folder")
	}
	if len(paths) > maxShareItems {
		return nil, fmt.Errorf("a share can contain at most %d selected items", maxShareItems)
	}
	seen := make(map[string]bool)
	items := make([]ShareItem, 0, len(paths))
	for _, raw := range paths {
		rel, err := cleanShareRel(raw, false)
		if err != nil || isShareStatePath(rel) || isHiddenPath(rel) {
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
		if isHiddenPath(item.Path) {
			continue
		}
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
	if isHiddenPath(rel) {
		return false
	}
	requested, err := secureResolve(rel)
	if err != nil {
		return false
	}
	for _, item := range shared.Items {
		if rel != item.Path && !(item.IsDir && strings.HasPrefix(rel, item.Path+"/")) {
			continue
		}
		selected, err := secureResolve(item.Path)
		if err != nil {
			continue
		}
		if requested == selected || (item.IsDir && strings.HasPrefix(requested, selected+string(filepath.Separator))) {
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
	if performanceMetrics.Load() {
		secureResolveCount.Add(1)
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	within, err := filepath.Rel(resolvedRoot, real)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) || filepath.IsAbs(within) {
		return "", errors.New("path escapes served root")
	}
	if isShareStatePath(filepath.ToSlash(within)) {
		return "", errors.New("path is reserved for share state")
	}
	return real, nil
}

func isShareStatePath(rel string) bool {
	for _, filename := range []string{shareStateFilename, legacyShareStateFilename} {
		if rel == filename || strings.HasPrefix(rel, filename+".") || strings.HasPrefix(rel, filename+"/") {
			return true
		}
	}
	if root != "" && len(activeStatePaths) > 0 {
		candidate := filepath.Join(root, filepath.FromSlash(rel))
		candidateInfo, err := os.Stat(candidate)
		if err == nil {
			for _, statePath := range activeStatePaths {
				stateInfo, stateErr := os.Stat(statePath)
				if stateErr == nil && os.SameFile(candidateInfo, stateInfo) {
					return true
				}
			}
		}
	}
	return false
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
			backup := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
			if renameErr := os.Rename(path, backup); renameErr != nil {
				return nil, fmt.Errorf("state file is corrupt and could not be quarantined: %w", renameErr)
			}
			log.Printf("warning: corrupt share state moved to %s", backup)
			return store, nil
		}
	}
	if store.prune(time.Now()) {
		if err := store.persistSnapshot(store.shares); err != nil {
			log.Printf("warning: could not persist pruned share state: %v", err)
		}
	}
	return store, nil
}

func (store *ShareStore) prune(now time.Time) bool {
	kept := store.shares[:0]
	changed := false
	for _, share := range store.shares {
		deadline := share.HardExpiresAt
		if share.Revoked {
			deadline = share.LastActivityAt
		}
		if !deadline.IsZero() && now.After(deadline.Add(shareRetentionPeriod)) {
			changed = true
			continue
		}
		kept = append(kept, share)
	}
	store.shares = kept
	return changed
}

func (store *ShareStore) Create(items []ShareItem, idleMinutes, maxMinutes int, title, note string) (Share, error) {
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
	created := &Share{ID: id, Token: token, Items: items, CreatedAt: now, LastActivityAt: now, ExpiresAt: expiry, HardExpiresAt: hardExpiry, IdleTimeoutMinutes: idleMinutes, MaxLifetimeMinutes: maxMinutes, Title: title, Note: note}
	store.persistMu.Lock()
	defer store.persistMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.shares = append([]*Share{created}, store.shares...)
	if err := store.writeSnapshot(store.shares); err != nil {
		store.shares = store.shares[1:]
		return Share{}, err
	}
	store.version++
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

func (store *ShareStore) Version() uint64 {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.version
}

func (store *ShareStore) GetActive(token string) (Share, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := time.Now()
	for _, item := range store.shares {
		if item.Token != token {
			continue
		}
		if err := shareAccessError(item, now); err != nil {
			return Share{}, err
		}
		copy := *item
		copy.Items = append([]ShareItem(nil), item.Items...)
		return copy, nil
	}
	return Share{}, errShareNotFound
}

func (store *ShareStore) Use(token, rel string, download bool) (Share, error) {
	store.persistMu.Lock()
	defer store.persistMu.Unlock()
	for attempt := 0; attempt < 3; attempt++ {
		store.mu.Lock()
		index := -1
		var snapshot Share
		version := store.version
		for i, item := range store.shares {
			if item.Token == token {
				index = i
				snapshot = cloneShare(*item)
				break
			}
		}
		all := cloneShares(store.shares)
		store.mu.Unlock()
		if index < 0 {
			return Share{}, errShareNotFound
		}
		now := time.Now()
		if err := shareAccessError(&snapshot, now); err != nil {
			return Share{}, err
		}
		if rel != "" && !shareAllows(&snapshot, rel) {
			return Share{}, errShareDenied
		}
		snapshot.LastActivityAt = now
		snapshot.ExpiresAt = now.Add(time.Duration(snapshot.IdleTimeoutMinutes) * time.Minute)
		if snapshot.ExpiresAt.After(snapshot.HardExpiresAt) {
			snapshot.ExpiresAt = snapshot.HardExpiresAt
		}
		if download {
			snapshot.Downloads++
		} else {
			snapshot.PageViews++
		}
		all[index] = &snapshot
		if err := store.writeSnapshot(all); err != nil {
			return Share{}, fmt.Errorf("%w: %v", errShareStorage, err)
		}
		store.mu.Lock()
		if store.version != version {
			store.mu.Unlock()
			continue
		}
		store.shares[index] = &snapshot
		store.version++
		result := cloneShare(snapshot)
		store.mu.Unlock()
		return result, nil
	}
	return Share{}, fmt.Errorf("%w: share state changed during update", errShareStorage)
}

// Touch records guest activity in memory and persists it after a short bounded
// delay. Share creation and revocation still use synchronous durable writes.
func (store *ShareStore) Touch(token, rel string, download bool) (Share, error) {
	shared, err := store.GetActive(token)
	if err != nil {
		return Share{}, err
	}
	if rel != "" && !shareAllows(&shared, rel) {
		return Share{}, errShareDenied
	}
	now := time.Now()
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, item := range store.shares {
		if item.Token != token {
			continue
		}
		if err := shareAccessError(item, now); err != nil {
			return Share{}, err
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
		store.version++
		store.activityDirty = true
		if store.path != "" {
			store.scheduleActivityFlushLocked()
		}
		return cloneShare(*item), nil
	}
	return Share{}, errShareNotFound
}

func (store *ShareStore) flushActivity() error {
	store.persistMu.Lock()
	defer store.persistMu.Unlock()
	store.mu.Lock()
	if !store.activityDirty {
		store.activityTimer = nil
		store.mu.Unlock()
		return nil
	}
	shares := cloneShares(store.shares)
	version := store.version
	store.mu.Unlock()
	if err := store.writeSnapshot(shares); err != nil {
		store.mu.Lock()
		store.activityTimer = nil
		store.mu.Unlock()
		return err
	}
	store.mu.Lock()
	store.activityTimer = nil
	if store.version == version {
		store.activityDirty = false
	} else {
		store.scheduleActivityFlushLocked()
	}
	store.mu.Unlock()
	return nil
}

func (store *ShareStore) scheduleActivityFlushLocked() {
	if store.activityTimer != nil {
		return
	}
	store.activityTimer = time.AfterFunc(activityCoalesceDelay, func() {
		if err := store.flushActivity(); err != nil {
			log.Printf("share activity flush failed: %v", err)
		}
	})
}

func (store *ShareStore) Revoke(id string) error {
	store.persistMu.Lock()
	defer store.persistMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, item := range store.shares {
		if item.ID == id {
			before := *item
			item.Revoked = true
			if err := store.writeSnapshot(store.shares); err != nil {
				*item = before
				return fmt.Errorf("%w: %v", errShareStorage, err)
			}
			store.version++
			return nil
		}
	}
	return errShareNotFound
}

func (store *ShareStore) persistSnapshot(shares []*Share) error {
	store.persistMu.Lock()
	defer store.persistMu.Unlock()
	return store.writeSnapshot(shares)
}

func (store *ShareStore) writeSnapshot(shares []*Share) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(shares, "", "  ")
	if err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, store.path); err != nil {
		return err
	}
	removeTemporary = false
	if directory, err := os.Open(filepath.Dir(store.path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func (store *ShareStore) Flush() error {
	store.persistMu.Lock()
	defer store.persistMu.Unlock()
	for {
		store.mu.Lock()
		if store.activityTimer != nil {
			store.activityTimer.Stop()
			store.activityTimer = nil
		}
		shares := cloneShares(store.shares)
		version := store.version
		store.mu.Unlock()
		if err := store.writeSnapshot(shares); err != nil {
			return err
		}
		store.mu.Lock()
		if store.version == version {
			store.activityDirty = false
			store.mu.Unlock()
			return nil
		}
		store.mu.Unlock()
	}
}

func cloneShare(share Share) Share {
	share.Items = append([]ShareItem(nil), share.Items...)
	return share
}

func cloneShares(shares []*Share) []*Share {
	result := make([]*Share, len(shares))
	for i, share := range shares {
		copy := cloneShare(*share)
		result[i] = &copy
	}
	return result
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
	return ShareAdminView{ID: item.ID, URL: shareURL, Items: item.Items, CreatedAt: item.CreatedAt, LastActivityAt: item.LastActivityAt, ExpiresAt: item.ExpiresAt, HardExpiresAt: item.HardExpiresAt, IdleTimeoutMinutes: item.IdleTimeoutMinutes, MaxLifetimeMinutes: item.MaxLifetimeMinutes, Title: item.Title, Note: item.Note, PageViews: item.PageViews, Downloads: item.Downloads, Status: status, RemainingSeconds: int64(remaining.Seconds())}
}

func requestBaseURL(r *http.Request) string {
	if publicBaseURL != "" {
		return publicBaseURL
	}
	port := serverPort
	if port == "" {
		_, port, _ = net.SplitHostPort(r.Host)
	}
	if port != "" {
		if discovered, _ := discoverLANURLs(port); len(discovered) > 0 {
			return discovered[0]
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func normalizePublicURL(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("public URL must be an http(s) origin without a path, query, or fragment")
	}
	return value, nil
}

func writeShareJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeShareError(w http.ResponseWriter, status int, message string) {
	writeShareJSON(w, status, map[string]string{"error": message})
}

func shareAccessError(item *Share, now time.Time) error {
	if item.Revoked {
		return errShareRevoked
	}
	if !now.Before(item.ExpiresAt) || !now.Before(item.HardExpiresAt) {
		return errShareExpired
	}
	return nil
}

func normalizeShareLabel(value string, limit int, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%s must be a single line", name)
	}
	if utf8.RuneCountInString(value) > limit {
		return "", fmt.Errorf("%s must be at most %d characters", name, limit)
	}
	return value, nil
}

func shareGoneCopy(err error) (string, string, bool) {
	if errors.Is(err, errShareExpired) {
		return shareExpiredHeading, shareExpiredDetail, true
	}
	if errors.Is(err, errShareRevoked) {
		return shareRevokedHeading, shareRevokedDetail, true
	}
	return "", "", false
}

func serveShareGonePage(w http.ResponseWriter, r *http.Request, err error) bool {
	heading, detail, ok := shareGoneCopy(err)
	if !ok {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusGone)
	data := ShareGoneData{Heading: heading, Detail: detail, CSPNonce: requestCSPNonce(r)}
	if execErr := templatesForRequest(r).gone.Execute(w, data); execErr != nil {
		log.Printf("share gone template error: %v", execErr)
	}
	return true
}

func serveShareError(w http.ResponseWriter, err error) {
	if errors.Is(err, errShareStorage) {
		http.Error(w, "The share state could not be saved. Try again.", http.StatusServiceUnavailable)
		return
	}
	if heading, _, ok := shareGoneCopy(err); ok {
		writeShareError(w, http.StatusGone, heading)
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
		{"Listen", serverAddress},
		{"Port", port},
		{"File browser", browser},
		{"Hidden files", map[bool]string{true: "included (--show-hidden)", false: "excluded by default"}[showHidden]},
		{"Admin user", "admin"},
		{"Admin password", secret},
		{"Stop", "Ctrl+C"},
	}
	if password == "" {
		facts = append(facts, [2]string{"Warning", "main browser has no password; use --password for shared networks"})
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
	files := qrColumn("Files", base, authorizedURL(base, password))
	admin := qrColumn("Admin", base+"/admin", authorizedURL(base+"/admin", adminPassword))
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

// authorizedURL puts the fixed basic-auth user into a QR payload so a scan
// opens the page without a login prompt. An empty secret leaves the URL unchanged.
func authorizedURL(raw, secret string) string {
	if secret == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return raw
	}
	parsed.User = url.UserPassword("admin", secret)
	return parsed.String()
}

func qrColumn(title, caption, payload string) []string {
	lines := []string{title, caption, ""}
	code, err := qrcode.New(payload, qrcode.Medium)
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
	if configured := net.ParseIP(serverAddress); configured != nil {
		ips = append(ips, configured)
	}
	return chooseLANURLs(port, ips)
}

func chooseLANURLs(port string, ips []net.IP) ([]string, int) {
	seen := make(map[string]bool)
	var urls []string
	var loopback net.IP
	skipped := 0
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if ip.IsLoopback() {
			if loopback == nil {
				loopback = ip
			}
			continue
		}
		ip4 := ip.To4()
		if ip4 != nil && (ip4[0] != 192 || ip4[1] != 168) {
			if !seen["skip:"+ip4.String()] {
				seen["skip:"+ip4.String()] = true
				skipped++
			}
			continue
		}
		if ip4 == nil && !ip.IsPrivate() {
			if !seen["skip:"+ip.String()] {
				seen["skip:"+ip.String()] = true
				skipped++
			}
			continue
		}
		base := lanURL(ip, port)
		if !seen[base] {
			seen[base] = true
			urls = append(urls, base)
		}
	}
	sort.Strings(urls)
	if len(urls) == 0 {
		if loopback != nil && loopback.To4() == nil {
			return []string{lanURL(loopback, port)}, skipped
		}
		return []string{"http://127.0.0.1:" + port}, skipped
	}
	return urls, skipped
}

func lanURL(ip net.IP, port string) string {
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("http://%s:%s", ip4.String(), port)
	}
	return fmt.Sprintf("http://[%s]:%s", ip.String(), port)
}
