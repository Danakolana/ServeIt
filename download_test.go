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
	_, _, err = collectZipEntries([]string{"nope"}, func(string) bool { return false })
	if err == nil || err.Error() != "this path is not included in the share" {
		t.Fatalf("allow error = %v", err)
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
