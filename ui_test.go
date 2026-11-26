package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestBothUITemplateSetsParseAndRender(t *testing.T) {
	if err := initTemplates(); err != nil {
		t.Fatal(err)
	}
	data := PageData{CurrentPath: "/", Parents: []Crumb{{Name: "root", Path: "/"}}, ThemeSwitchURL: "/theme"}
	for theme, marker := range map[string]string{themeClassic: "Modern UI", themeModern: "/static/app.css"} {
		var output bytes.Buffer
		if err := uiTemplates[theme].browse.Execute(&output, data); err != nil {
			t.Fatalf("render %s UI: %v", theme, err)
		}
		if !strings.Contains(output.String(), marker) {
			t.Fatalf("rendered %s UI does not contain %q", theme, marker)
		}
	}
}

func TestUIRoutesArePublicAndReadOnly(t *testing.T) {
	mux := http.NewServeMux()
	if err := registerUIRoutes(mux); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/static/app.css", "/favicon.ico"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s returned %d", target, recorder.Code)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=3600" {
			t.Fatalf("GET %s Cache-Control = %q", target, got)
		}
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/static/app.css", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /static/app.css returned %d", recorder.Code)
	}
}

func TestThemeSwitchUsesCookieAndSafeLocalRedirect(t *testing.T) {
	recorder := httptest.NewRecorder()
	themeHandler(recorder, httptest.NewRequest(http.MethodGet, "/theme?name=modern&next=%2Fadmin%3Fpath%3Ddocs", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/admin?path=docs" {
		t.Fatalf("theme switch returned status %d and Location %q", recorder.Code, recorder.Header().Get("Location"))
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != themeCookieName || cookies[0].Value != themeModern || !cookies[0].HttpOnly {
		t.Fatalf("unexpected theme cookie: %#v", cookies)
	}

	recorder = httptest.NewRecorder()
	themeHandler(recorder, httptest.NewRequest(http.MethodGet, "/theme?name=modern&next=https%3A%2F%2Fexample.com", nil))
	if got := recorder.Header().Get("Location"); got != "/" {
		t.Fatalf("external redirect was not rejected: %q", got)
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
