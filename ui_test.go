package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPathEscapePreservesSegments(t *testing.T) {
	got := pathEscape("/folder/a#b?100%41.txt")
	if want := "/folder/a%23b%3F100%2541.txt"; got != want {
		t.Fatalf("pathEscape() = %q, want %q", got, want)
	}
}

func TestAllUITemplateSetsParseAndRender(t *testing.T) {
	if err := initTemplates(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin?path=docs", nil)
	options := themeOptions(request)
	entries := []Entry{{Name: "notes.txt", RelPath: "notes.txt", Size: 12, ModTime: time.Unix(1_700_000_000, 0)}}
	page := PageData{CurrentPath: "/", Entries: entries, Parents: []Crumb{{Name: "root", Path: "/"}}, ThemeSwitchURL: themeSwitchURL(request), ThemeOptions: options}
	admin := AdminPageData{CurrentPath: "docs", CurrentRel: "docs", Entries: entries, Parents: []Crumb{{Name: "Share", Path: "/"}}, ThemeSwitchURL: themeSwitchURL(request), ThemeOptions: options}
	shared := SharedPageData{Token: "token", CurrentPath: "/", Entries: entries, Parents: []Crumb{{Name: "Shared with you", Path: "/"}}, RemainingSeconds: 60, HardRemainingSeconds: 120, IdleTimeoutMinutes: 15, ThemeSwitchURL: themeSwitchURL(request), ThemeOptions: options}
	markers := map[string]string{themeClassic: "theme-picker", themeModern: "/static/app.css", themeHarbor: "/static/harbor.css"}
	for theme, marker := range markers {
		var output bytes.Buffer
		if err := uiTemplates[theme].browse.Execute(&output, page); err != nil {
			t.Fatalf("render %s UI: %v", theme, err)
		}
		if !strings.Contains(output.String(), marker) {
			t.Fatalf("rendered %s UI does not contain %q", theme, marker)
		}
		if !strings.Contains(output.String(), `class="theme-current"`) || strings.Count(output.String(), `class="theme-choice"`) != 2 {
			t.Fatalf("rendered %s UI does not expose a collapsed picker with two alternate themes", theme)
		}
		for name, templateData := range map[string]struct {
			view *template.Template
			data any
		}{"admin": {uiTemplates[theme].admin, admin}, "share": {uiTemplates[theme].share, shared}} {
			output.Reset()
			if err := templateData.view.Execute(&output, templateData.data); err != nil {
				t.Fatalf("render %s %s UI: %v", theme, name, err)
			}
			if !strings.Contains(output.String(), "theme-picker") {
				t.Fatalf("rendered %s %s UI has no theme picker", theme, name)
			}
			if !strings.Contains(output.String(), `class="theme-current"`) || strings.Count(output.String(), `class="theme-choice"`) != 2 {
				t.Fatalf("rendered %s %s UI does not expose a collapsed picker with two alternate themes", theme, name)
			}
		}
	}
}

func TestUIRoutesArePublicAndReadOnly(t *testing.T) {
	mux := http.NewServeMux()
	if err := registerUIRoutes(mux); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/static/app.css", "/static/harbor.css", "/favicon.ico"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", target, recorder.Code)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=3600" {
			t.Fatalf("GET %s Cache-Control = %q", target, got)
		}
		if target == "/static/harbor.css" && !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/css") {
			t.Fatalf("Harbor stylesheet Content-Type = %q", recorder.Header().Get("Content-Type"))
		}
		if target == "/static/harbor.css" && !strings.Contains(recorder.Body.String(), "ServeIt — Harbor theme") {
			t.Fatal("Harbor stylesheet was not served")
		}
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/static/app.css", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /static/app.css returned %d", recorder.Code)
	}
}

func TestThemeSwitchUsesCookieAndSafeLocalRedirect(t *testing.T) {
	for _, theme := range []string{themeClassic, themeModern, themeHarbor} {
		recorder := httptest.NewRecorder()
		themeHandler(recorder, httptest.NewRequest(http.MethodGet, "/theme?name="+theme+"&next=%2Fadmin%3Fpath%3Ddocs", nil))
		if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/admin?path=docs" {
			t.Fatalf("%s theme switch returned status %d and Location %q", theme, recorder.Code, recorder.Header().Get("Location"))
		}
		cookies := recorder.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != themeCookieName || cookies[0].Value != theme || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatalf("unexpected %s theme cookie: %#v", theme, cookies)
		}
	}

	for _, next := range []string{
		"https%3A%2F%2Fexample.com",
		"%2F%2Fevil.example.com%2Fpath",
		"%2F%ZZ",
		"https%3Aexample.com",
	} {
		recorder := httptest.NewRecorder()
		themeHandler(recorder, httptest.NewRequest(http.MethodGet, "/theme?name=modern&next="+next, nil))
		if got := recorder.Header().Get("Location"); got != "/" {
			t.Fatalf("unsafe redirect %q was not rejected: %q", next, got)
		}
	}
	for _, cookieValue := range []string{"", "invalid", themeClassic, themeModern, themeHarbor} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if cookieValue != "" {
			req.AddCookie(&http.Cookie{Name: themeCookieName, Value: cookieValue})
		}
		got := requestTheme(req)
		want := themeClassic
		if isSupportedTheme(cookieValue) {
			want = cookieValue
		}
		if got != want {
			t.Fatalf("requestTheme(%q) = %q, want %q", cookieValue, got, want)
		}
	}
	recorder := httptest.NewRecorder()
	themeHandler(recorder, httptest.NewRequest(http.MethodGet, "/theme?name=invalid", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid theme was accepted with status %d", recorder.Code)
	}
}

func TestPublicURLAndAdminMutationChecks(t *testing.T) {
	previous := publicBaseURL
	publicBaseURL = ""
	t.Cleanup(func() { publicBaseURL = previous })

	if got, err := normalizePublicURL("https://files.example.test/"); err != nil || got != "https://files.example.test" {
		t.Fatalf("normalizePublicURL() = %q, %v", got, err)
	}
	if _, err := normalizePublicURL("https://files.example.test/share"); err == nil {
		t.Fatal("path-bearing public URL was accepted")
	}

	req := httptest.NewRequest(http.MethodPost, "http://files.example.test/api/admin/shares", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if err := validateAdminMutation(req, true); err == nil {
		t.Fatal("admin mutation without request header was accepted")
	}
	req.Header.Set("X-ServeIt-Request", "admin")
	req.Header.Set("Origin", "http://evil.example.test")
	if err := validateAdminMutation(req, true); err == nil {
		t.Fatal("cross-origin admin mutation was accepted")
	}
	req.Header.Set("Origin", "http://files.example.test")
	if err := validateAdminMutation(req, true); err != nil {
		t.Fatalf("same-origin admin mutation was rejected: %v", err)
	}

	publicBaseURL = "https://public.example.test"
	if got := requestBaseURL(httptest.NewRequest(http.MethodGet, "http://attacker.example.test/admin", nil)); got != publicBaseURL {
		t.Fatalf("requestBaseURL() = %q, want %q", got, publicBaseURL)
	}
}

func TestHostValidationAndHiddenPathDefaults(t *testing.T) {
	previousHosts, previousHidden := allowedHosts, showHidden
	t.Cleanup(func() { allowedHosts, showHidden = previousHosts, previousHidden })
	allowedHosts = buildAllowedHosts("127.0.0.1", []string{"files.example.test"})
	showHidden = false
	if !hostAllowed("files.example.test:8080") || hostAllowed("attacker.example.test:8080") {
		t.Fatal("unexpected Host validation result")
	}
	if !isHiddenPath(".env") || !isHiddenPath("project/.git/config") || isHiddenPath("project/readme.md") {
		t.Fatal("unexpected hidden path classification")
	}
	showHidden = true
	if isHiddenPath(".env") {
		t.Fatal("hidden path was not enabled by showHidden")
	}
}

func TestHiddenPathsAreExcludedAcrossAccessPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousHidden := showHidden
	showHidden = false
	t.Cleanup(func() { showHidden = previousHidden })
	restoreRoot(t, dir)

	entries, err := readDir(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".") {
			t.Fatalf("hidden entry listed: %q", entry.Name)
		}
	}
	if _, err := normalizeShareItems([]string{".env"}); err == nil {
		t.Fatal("hidden file could be selected for sharing")
	}
	if _, _, err := collectZipEntries([]string{".env"}, nil); err == nil {
		t.Fatal("hidden file could be added to a ZIP")
	}
	recorder := httptest.NewRecorder()
	searchHandler(recorder, httptest.NewRequest(http.MethodGet, "/search?q=env", nil))
	if strings.Contains(recorder.Body.String(), ".env") {
		t.Fatal("hidden file appeared in search results")
	}
}

func TestShareUseRollsBackWhenPersistenceFails(t *testing.T) {
	dir := t.TempDir()
	share := &Share{ID: "id", Token: "token", LastActivityAt: time.Unix(100, 0), ExpiresAt: time.Now().Add(time.Hour), HardExpiresAt: time.Now().Add(2 * time.Hour), IdleTimeoutMinutes: 10}
	before := *share
	store := &ShareStore{path: filepath.Join(dir, "missing", "state.json"), shares: []*Share{share}}
	if _, err := store.Use("token", "", false); !errors.Is(err, errShareStorage) {
		t.Fatalf("Use() error = %v, want storage error", err)
	}
	if !reflect.DeepEqual(*share, before) {
		t.Fatal("share was mutated after persistence failure")
	}
	if err := store.Revoke("id"); !errors.Is(err, errShareStorage) {
		t.Fatalf("Revoke() error = %v, want storage error", err)
	}
	if share.Revoked {
		t.Fatal("share was revoked after persistence failure")
	}
}

func TestShareRemainingSecondsUsesEarliestDeadline(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	remaining, hard := shareRemainingSeconds(Share{ExpiresAt: now.Add(90 * time.Second), HardExpiresAt: now.Add(5 * time.Minute)}, now)
	if remaining != 90 || hard != 300 {
		t.Fatalf("remaining = %d, hard = %d", remaining, hard)
	}
}

func TestLegacyShareStateIsLoadedWithServeItWritePath(t *testing.T) {
	dir := t.TempDir()
	payload, err := json.Marshal([]Share{{ID: "legacy-share"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, legacyShareStateFilename), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	previousRoot, previousResolved := root, resolvedRoot
	previousPassword, previousAdminPassword := password, adminPassword
	previousGenerated, previousStore := adminPasswordGenerated, shareStore
	root, resolvedRoot, password = dir, dir, "main-password"
	t.Cleanup(func() {
		root, resolvedRoot = previousRoot, previousResolved
		password, adminPassword = previousPassword, previousAdminPassword
		adminPasswordGenerated, shareStore = previousGenerated, previousStore
	})

	if err := initSharing("admin-password"); err != nil {
		t.Fatal(err)
	}
	if len(shareStore.shares) != 1 || shareStore.shares[0].ID != "legacy-share" {
		t.Fatalf("legacy shares were not loaded: %#v", shareStore.shares)
	}
	if shareStore.path != filepath.Join(dir, shareStateFilename) {
		t.Fatalf("new writes use %q, want %q", shareStore.path, filepath.Join(dir, shareStateFilename))
	}
}
