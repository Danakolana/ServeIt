package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// TestPerformanceProbe measures the archive request path without slowing routine test runs.
// Run with PERF_PROBE=1 go test -run TestPerformanceProbe -v -count=1.
func TestPerformanceProbe(t *testing.T) {
	if os.Getenv("PERF_PROBE") == "" {
		t.Skip("set PERF_PROBE=1")
	}
	dir := t.TempDir()
	restoreRoot(t, dir)
	performanceMetrics.Store(true)
	t.Cleanup(func() { performanceMetrics.Store(false) })
	small := filepath.Join(dir, "small")
	if err := os.Mkdir(small, 0755); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), 4096)
	for i := 0; i < 2000; i++ {
		if err := os.WriteFile(filepath.Join(small, fmt.Sprintf("f%04d", i)), payload, 0644); err != nil {
			t.Fatal(err)
		}
	}
	big := bytes.Repeat([]byte("z"), 32<<20)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0644); err != nil {
		t.Fatal(err)
	}
	big = nil
	server := httptest.NewServer(http.HandlerFunc(downloadHandler))
	defer server.Close()
	client := server.Client()
	for _, tc := range []struct{ name, body string }{{"small", `{"paths":["small"]}`}, {"big", `{"paths":["big.bin"]}`}} {
		for i := 0; i < 3; i++ {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			requests := 0
			resolvesBefore := secureResolveCount.Load()
			checkReq, err := http.NewRequest("POST", server.URL+"?check=1", bytes.NewBufferString(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			checkReq.Header.Set("Content-Type", "application/json")
			checkResp, err := client.Do(checkReq)
			if err != nil {
				t.Fatal(err)
			}
			requests++
			if checkResp.StatusCode != 200 {
				t.Fatalf("check: %s", checkResp.Status)
			}
			if _, err := io.Copy(io.Discard, checkResp.Body); err != nil {
				checkResp.Body.Close()
				t.Fatal(err)
			}
			checkResp.Body.Close()
			checkDone := time.Now()
			req, err := http.NewRequest("POST", server.URL, bytes.NewBufferString(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			requests++
			if resp.StatusCode != 200 {
				t.Fatalf("download: %s", resp.Status)
			}
			first := make([]byte, 1)
			n, err := resp.Body.Read(first)
			if err != nil {
				t.Fatal(err)
			}
			firstByte := time.Since(start)
			copied, err := io.Copy(io.Discard, resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			total := time.Since(start)
			resolves := secureResolveCount.Load() - resolvesBefore
			runtime.ReadMemStats(&after)
			cancelled := httptest.NewRequest(http.MethodGet, "/search?q=cancel", nil).WithContext(context.Background())
			cancelledContext, cancel := context.WithCancel(cancelled.Context())
			cancel()
			cancelled = cancelled.WithContext(cancelledContext)
			cancelStart := time.Now()
			cancelledRecorder := httptest.NewRecorder()
			searchHandler(cancelledRecorder, cancelled)
			cancelDelay := time.Since(cancelStart)
			shareLatency := benchmarkShareTouchLatency(t, dir, i)
			t.Logf("%s run=%d requests=%d check=%s first_byte=%s total=%s bytes=%d throughput=%.1f MiB/s heap_delta=%d total_alloc_delta=%d resolve_calls=%d cancellation_delay=%s share_touch=%s first_read=%d", tc.name, i+1, requests, checkDone.Sub(start), firstByte, total, copied+int64(n), float64(copied+int64(n))/1048576/total.Seconds(), int64(after.HeapAlloc)-int64(before.HeapAlloc), after.TotalAlloc-before.TotalAlloc, resolves, cancelDelay, shareLatency, n)
		}
	}
}

func benchmarkShareTouchLatency(t *testing.T, dir string, run int) time.Duration {
	store := &ShareStore{
		path: filepath.Join(dir, fmt.Sprintf("share-benchmark-%d.json", run)),
		shares: []*Share{{
			Token:              "benchmark-token",
			IdleTimeoutMinutes: 10,
			ExpiresAt:          time.Now().Add(time.Hour),
			HardExpiresAt:      time.Now().Add(2 * time.Hour),
		}},
	}
	const workers, touchesPerWorker = 8, 16
	start := time.Now()
	var group sync.WaitGroup
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer group.Done()
			for i := 0; i < touchesPerWorker; i++ {
				if _, err := store.Touch("benchmark-token", "", false); err != nil {
					t.Errorf("share benchmark touch: %v", err)
					return
				}
			}
		}()
	}
	group.Wait()
	if err := store.Flush(); err != nil {
		t.Fatalf("share benchmark flush: %v", err)
	}
	return time.Since(start) / (workers * touchesPerWorker)
}
