package main

import (
	"archive/zip"
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChooseLANURLsPrefers192168(t *testing.T) {
	urls, skipped := chooseLANURLs("8080", []net.IP{
		net.ParseIP("192.168.1.20"),
		net.ParseIP("172.17.0.1"),
		net.ParseIP("10.1.1.1"),
		net.ParseIP("169.254.4.4"),
		net.ParseIP("127.0.0.1"),
		net.ParseIP("192.168.1.20"),
	})
	if skipped != 3 {
		t.Fatalf("skipped = %d, want 3", skipped)
	}
	if len(urls) != 1 || urls[0] != "http://192.168.1.20:8080" {
		t.Fatalf("urls = %#v", urls)
	}
}

func TestChooseLANURLsFallsBackToLoopback(t *testing.T) {
	urls, skipped := chooseLANURLs("9000", []net.IP{net.ParseIP("172.17.0.1")})
	if skipped != 1 || len(urls) != 1 || urls[0] != "http://127.0.0.1:9000" {
		t.Fatalf("urls=%#v skipped=%d", urls, skipped)
	}
}

func TestChooseLANURLsSupportsPrivateIPv6(t *testing.T) {
	urls, skipped := chooseLANURLs("9000", []net.IP{net.ParseIP("fd12:3456::20"), net.ParseIP("fe80::1")})
	if skipped != 1 || len(urls) != 1 || urls[0] != "http://[fd12:3456::20]:9000" {
		t.Fatalf("urls=%#v skipped=%d", urls, skipped)
	}
	urls, skipped = chooseLANURLs("9000", []net.IP{net.ParseIP("::1")})
	if skipped != 0 || len(urls) != 1 || urls[0] != "http://[::1]:9000" {
		t.Fatalf("IPv6 fallback urls=%#v skipped=%d", urls, skipped)
	}
}

func TestDownloadZipIsStoredAndStreamsSelection(t *testing.T) {
	dir := t.TempDir()
	restoreRoot(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("world!!"), 0o644); err != nil {
		t.Fatal(err)
	}

	planReq := httptest.NewRequest(http.MethodPost, "/api/download?plan=1", strings.NewReader(`{"paths":["a.txt","sub"]}`))
	planReq.Header.Set("Content-Type", "application/json")
	planRec := httptest.NewRecorder()
	downloadHandler(planRec, planReq)
	if planRec.Code != http.StatusOK || !strings.Contains(planRec.Body.String(), `"bytes":12`) || !strings.Contains(planRec.Body.String(), `"files":2`) {
		t.Fatalf("plan %d %s", planRec.Code, planRec.Body.String())
	}
	checkReq := httptest.NewRequest(http.MethodPost, "/api/download?check=1", strings.NewReader(`{"paths":["a.txt","sub"]}`))
	checkReq.Header.Set("Content-Type", "application/json")
	checkRec := httptest.NewRecorder()
	downloadHandler(checkRec, checkReq)
	if checkRec.Code != http.StatusOK {
		t.Fatalf("check %d %s", checkRec.Code, checkRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(`{"paths":["a.txt","sub"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	downloadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	reader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, file := range reader.File {
		if file.Method != zip.Store {
			t.Fatalf("%s method = %d", file.Name, file.Method)
		}
		if file.FileInfo().IsDir() {
			continue
		}
		body, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(body); err != nil {
			t.Fatal(err)
		}
		body.Close()
		got[file.Name] = buf.String()
	}
	if got["a.txt"] != "hello" || got["sub/b.txt"] != "world!!" {
		t.Fatalf("zip contents %#v", got)
	}
}

func TestDownloadRejectsMissingAndEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	restoreRoot(t, dir)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	_, _, err := collectZipEntries([]string{"link.txt"}, nil)
	if err == nil {
		t.Fatal("expected symlink escape to fail")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/download", strings.NewReader(`{"paths":["missing.txt"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	downloadHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	checkReq := httptest.NewRequest(http.MethodPost, "/api/download?check=1", strings.NewReader(`{"paths":["missing.txt"]}`))
	checkReq.Header.Set("Content-Type", "application/json")
	checkRec := httptest.NewRecorder()
	downloadHandler(checkRec, checkReq)
	if checkRec.Code != http.StatusBadRequest {
		t.Fatalf("missing check status = %d", checkRec.Code)
	}
	_, _, err = collectZipEntries([]string{"nope"}, func(string) bool { return false })
	if err == nil || err.Error() != "this path is not included in the share" {
		t.Fatalf("allow error = %v", err)
	}
}

func TestDownloadRejectsOversizedSelection(t *testing.T) {
	dir := t.TempDir()
	restoreRoot(t, dir)
	path := filepath.Join(dir, "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxDownloadBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectZipEntries([]string{"large.bin"}, nil); err == nil || !strings.Contains(err.Error(), "10 GiB") {
		t.Fatalf("oversized download error = %v", err)
	}
}

func TestShareDoesNotFollowSymlinkOutsideSelection(t *testing.T) {
	dir := t.TempDir()
	restoreRoot(t, dir)
	if err := os.Mkdir(filepath.Join(dir, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private", "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "private", "secret.txt"), filepath.Join(dir, "shared", "link.txt")); err != nil {
		t.Fatal(err)
	}
	share := Share{Items: []ShareItem{{Path: "shared", IsDir: true}}}
	if shareAllows(&share, "shared/link.txt") {
		t.Fatal("symlink into private directory was allowed")
	}
	previousStore := shareStore
	shareStore = &ShareStore{shares: []*Share{{Token: "test", Items: share.Items, ExpiresAt: time.Now().Add(time.Hour), HardExpiresAt: time.Now().Add(time.Hour)}}}
	t.Cleanup(func() { shareStore = previousStore })
	rec := httptest.NewRecorder()
	shareHandler(rec, httptest.NewRequest(http.MethodGet, "/s/test/?path=shared/link.txt", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest download status = %d", rec.Code)
	}
	checkReq := httptest.NewRequest(http.MethodPost, "/s/test/download?check=1", strings.NewReader(`{"paths":["shared/link.txt"]}`))
	checkReq.Header.Set("Content-Type", "application/json")
	checkRec := httptest.NewRecorder()
	shareDownloadHandler(checkRec, checkReq, "test")
	if checkRec.Code != http.StatusForbidden {
		t.Fatalf("guest check status = %d", checkRec.Code)
	}
	entries, _, err := collectZipEntries([]string{"shared"}, func(rel string) bool { return shareAllows(&share, rel) })
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.name == "shared/link.txt" {
			t.Fatal("private file included in archive")
		}
	}
}

func TestShareStateSymlinkCannotBeDownloaded(t *testing.T) {
	dir := t.TempDir()
	restoreRoot(t, dir)
	if err := os.WriteFile(filepath.Join(dir, shareStateFilename), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, shareStateFilename), filepath.Join(dir, "alias.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := secureResolve("alias.json"); err == nil {
		t.Fatal("share state alias resolved")
	}
	rec := httptest.NewRecorder()
	browseHandler(rec, httptest.NewRequest(http.MethodGet, "/alias.json", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("browser alias status = %d", rec.Code)
	}
	if _, _, err := collectZipEntries([]string{"alias.json"}, nil); err == nil {
		t.Fatal("share state alias added to archive")
	}
}

func TestBrowseServesLiteralFilenamesAndSymlinkDirectories(t *testing.T) {
	dir := t.TempDir()
	restoreRoot(t, dir)
	if err := initTemplates(); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{
		"nested/index.html": "index bytes",
		"a#b.txt":           "hash bytes",
		"what?.txt":         "question bytes",
		"100%41.txt":        "percent bytes",
		"فایل.txt":          "unicode bytes",
	}
	for name, body := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target", "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	browseHandler(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("listing status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"/a%23b.txt", "/what%3F.txt", "/100%2541.txt", "/%D9%81%D8%A7%DB%8C%D9%84.txt", "/linked/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("listing does not contain escaped URL %q:\n%s", want, body)
		}
	}

	for _, test := range []struct {
		path string
		want string
	}{
		{"/nested/index.html", "index bytes"},
		{"/a%23b.txt", "hash bytes"},
		{"/what%3F.txt", "question bytes"},
		{"/100%2541.txt", "percent bytes"},
		{"/%D9%81%D8%A7%DB%8C%D9%84.txt", "unicode bytes"},
	} {
		recorder := httptest.NewRecorder()
		browseHandler(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != test.want {
			t.Fatalf("GET %s returned status %d body %q", test.path, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
			t.Fatalf("GET %s Content-Disposition = %q", test.path, got)
		}
	}

	recorder = httptest.NewRecorder()
	browseHandler(recorder, httptest.NewRequest(http.MethodGet, "/linked/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "inside.txt") {
		t.Fatalf("symlink directory listing status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func restoreRoot(t *testing.T, dir string) {
	t.Helper()
	previousRoot, previousResolved := root, resolvedRoot
	root = dir
	resolvedRoot = dir
	t.Cleanup(func() {
		root = previousRoot
		resolvedRoot = previousResolved
	})
}
