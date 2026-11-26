package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	maxDownloadSelections = 5000
	maxZipEntries         = 100000
	maxDownloadBytes      = int64(10 << 30)
	zipCopyBufferSize     = 256 << 10
)

var zipCopyBuffers = sync.Pool{New: func() any {
	buffer := make([]byte, zipCopyBufferSize)
	return &buffer
}}

type zipEntry struct {
	abs  string
	rel  string
	name string
	dir  bool
	size int64
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	paths, err := readDownloadPaths(r)
	if err != nil {
		writeShareError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if r.URL.Query().Get("check") == "1" {
		if err := validateDownloadPaths(paths, nil); err != nil {
			writeShareError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeShareJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	entries, total, err := collectZipEntries(paths, nil)
	if err != nil {
		writeShareError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.URL.Query().Get("plan") == "1" {
		writeShareJSON(w, http.StatusOK, zipPlan(entries, total))
		return
	}
	writeZipDownload(w, entries)
}

func shareDownloadHandler(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	shared, err := shareStore.GetActive(token)
	if err != nil {
		serveShareError(w, err)
		return
	}
	paths, err := readDownloadPaths(r)
	if err != nil {
		writeShareError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	allow := func(rel string) bool {
		return shareAllows(&shared, rel)
	}
	if r.URL.Query().Get("check") == "1" {
		if err := validateDownloadPaths(paths, allow); err != nil {
			writeShareError(w, downloadErrorStatus(err), err.Error())
			return
		}
		writeShareJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	entries, total, err := collectZipEntries(paths, allow)
	if err != nil {
		writeShareError(w, downloadErrorStatus(err), err.Error())
		return
	}
	if r.URL.Query().Get("plan") == "1" {
		writeShareJSON(w, http.StatusOK, zipPlan(entries, total))
		return
	}
	if _, err := shareStore.Use(token, "", true); err != nil {
		serveShareError(w, err)
		return
	}
	writeZipDownload(w, entries)
}

func downloadErrorStatus(err error) int {
	if err.Error() == "this path is not included in the share" {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

func validateDownloadPaths(paths []string, allow func(string) bool) error {
	if len(paths) == 0 {
		return errors.New("select at least one file or folder")
	}
	if len(paths) > maxDownloadSelections {
		return errors.New("too many selected items")
	}
	for _, raw := range paths {
		rel, err := cleanShareRel(raw, false)
		if err != nil || isShareStatePath(rel) || isHiddenPath(rel) {
			return fmt.Errorf("invalid path: %s", raw)
		}
		if allow != nil && !allow(rel) {
			return errors.New("this path is not included in the share")
		}
		abs, err := secureResolve(rel)
		if err != nil {
			return errors.New("a selected path is no longer available")
		}
		if _, err := os.Stat(abs); err != nil {
			return errors.New("a selected path is no longer available")
		}
	}
	return nil
}

func zipPlan(entries []zipEntry, total int64) map[string]int64 {
	var files int64
	for _, entry := range entries {
		if !entry.dir {
			files++
		}
	}
	return map[string]int64{"files": files, "bytes": total}
}

func readDownloadPaths(r *http.Request) ([]string, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		var body struct {
			Paths []string `json:"paths"`
		}
		r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			return nil, err
		}
		return body.Paths, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return r.Form["path"], nil
}

func writeZipDownload(w http.ResponseWriter, entries []zipEntry) {
	started, err := streamStoredZip(w, entries)
	if err != nil && started {
		panic(http.ErrAbortHandler)
	}
	if err != nil {
		writeShareError(w, http.StatusConflict, "A selected file disappeared before it could be packed.")
	}
}

func streamStoredZip(w http.ResponseWriter, entries []zipEntry) (bool, error) {
	rootHandle, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return false, err
	}
	defer rootHandle.Close()
	var writer *zip.Writer
	started := false
	begin := func() {
		if started {
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="ServeIt.zip"`)
		w.Header().Set("Cache-Control", "no-store")
		writer = zip.NewWriter(w)
		started = true
	}
	for _, entry := range entries {
		if entry.dir {
			begin()
			header := &zip.FileHeader{Name: entry.name}
			header.Method = zip.Store
			header.SetMode(os.ModeDir | 0o755)
			if _, err := writer.CreateHeader(header); err != nil {
				return started, err
			}
			continue
		}
		file, err := rootHandle.Open(filepath.FromSlash(entry.rel))
		if entry.rel == "" {
			file, err = os.Open(entry.abs)
		}
		if err != nil {
			return started, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return started, err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			file.Close()
			return started, err
		}
		header.Name = entry.name
		header.Method = zip.Store
		begin()
		part, err := writer.CreateHeader(header)
		if err != nil {
			file.Close()
			return started, err
		}
		buffer := zipCopyBuffers.Get().(*[]byte)
		_, copyErr := io.CopyBuffer(part, file, *buffer)
		zipCopyBuffers.Put(buffer)
		if copyErr != nil {
			file.Close()
			return started, copyErr
		}
		file.Close()
	}
	if writer == nil {
		return false, errors.New("nothing available to download")
	}
	if err := writer.Close(); err != nil {
		return started, err
	}
	return started, nil
}

func collectZipEntries(paths []string, allow func(string) bool) ([]zipEntry, int64, error) {
	if len(paths) == 0 {
		return nil, 0, errors.New("select at least one file or folder")
	}
	if len(paths) > maxDownloadSelections {
		return nil, 0, errors.New("too many selected items")
	}
	seenPath := make(map[string]bool)
	seenName := make(map[string]bool)
	visitedDirs := make(map[string]bool)
	var entries []zipEntry
	var total int64
	add := func(entry zipEntry) error {
		if seenName[entry.name] {
			return nil
		}
		if len(entries) >= maxZipEntries {
			return errors.New("too many files in this download")
		}
		if !entry.dir && (entry.size < 0 || total > maxDownloadBytes-entry.size) {
			return errors.New("download exceeds the 10 GiB limit")
		}
		seenName[entry.name] = true
		entries = append(entries, entry)
		if !entry.dir {
			total += entry.size
		}
		return nil
	}
	var walk func(abs, prefix string) error
	walk = func(abs, prefix string) error {
		if visitedDirs[abs] {
			return nil
		}
		visitedDirs[abs] = true
		return filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("a selected path is no longer available")
			}
			if path == abs {
				return nil
			}
			real, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("a selected path is no longer available")
			}
			child, err := relInsideRoot(real)
			if err != nil {
				return err
			}
			if isHiddenPath(filepath.ToSlash(child)) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if isShareStatePath(child) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			suffix, err := filepath.Rel(abs, path)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(filepath.Join(prefix, suffix))
			if allow != nil && !allow(name) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := os.Stat(real)
			if err != nil {
				return fmt.Errorf("a selected path is no longer available")
			}
			if info.IsDir() {
				if d.Type()&os.ModeSymlink != 0 {
					if err := add(zipEntry{abs: real, rel: filepath.ToSlash(child), name: strings.TrimSuffix(name, "/") + "/", dir: true}); err != nil {
						return err
					}
					return walk(real, name)
				}
				return add(zipEntry{abs: real, rel: filepath.ToSlash(child), name: name + "/", dir: true})
			}
			return add(zipEntry{abs: real, rel: filepath.ToSlash(child), name: name, size: info.Size()})
		})
	}
	for _, raw := range paths {
		rel, err := cleanShareRel(raw, false)
		if err != nil || isShareStatePath(rel) || isHiddenPath(rel) {
			return nil, 0, fmt.Errorf("invalid path: %s", raw)
		}
		if seenPath[rel] {
			continue
		}
		seenPath[rel] = true
		if allow != nil && !allow(rel) {
			return nil, 0, errors.New("this path is not included in the share")
		}
		abs, err := secureResolve(rel)
		if err != nil {
			return nil, 0, errors.New("a selected path is no longer available")
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, 0, errors.New("a selected path is no longer available")
		}
		if !info.IsDir() {
			if err := add(zipEntry{abs: abs, rel: rel, name: rel, size: info.Size()}); err != nil {
				return nil, 0, err
			}
			continue
		}
		if err := add(zipEntry{abs: abs, rel: rel, name: rel + "/", dir: true}); err != nil {
			return nil, 0, err
		}
		if err := walk(abs, rel); err != nil {
			return nil, 0, err
		}
	}
	if len(entries) == 0 {
		return nil, 0, errors.New("nothing available to download")
	}
	return entries, total, nil
}

func relInsideRoot(real string) (string, error) {
	within, err := filepath.Rel(resolvedRoot, real)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) || filepath.IsAbs(within) {
		return "", errors.New("a selected path escapes the served directory")
	}
	if within == "." {
		return "", nil
	}
	return filepath.ToSlash(within), nil
}
