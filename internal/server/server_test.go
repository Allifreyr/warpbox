package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecordTorrentFailure(t *testing.T) {
	srv := testServer(t, Config{
		CircuitBreakerFailures:  5,
		CircuitBreakerWindowSec: 60,
		CircuitBreakerStaleMin:  5,
	})

	itemID := int64(42)

	srv.recordTorrentFailure(itemID)
	tracker, exists := srv.torrentFailures[itemID]
	if !exists {
		t.Fatal("first failure: expected tracker to be created")
	}
	if len(tracker.failures) != 1 {
		t.Errorf("first failure: got %d failures, want 1", len(tracker.failures))
	}
	if !tracker.staleUntil.IsZero() {
		t.Errorf("first failure: staleUntil should be zero, got %v", tracker.staleUntil)
	}
}

func TestRecordTorrentFailure_belowThreshold(t *testing.T) {
	srv := testServer(t, Config{
		CircuitBreakerFailures:  5,
		CircuitBreakerWindowSec: 60,
		CircuitBreakerStaleMin:  5,
	})

	itemID := int64(42)
	for i := 0; i < 4; i++ {
		srv.recordTorrentFailure(itemID)
	}

	tracker := srv.torrentFailures[itemID]
	if len(tracker.failures) != 4 {
		t.Errorf("got %d failures, want 4", len(tracker.failures))
	}
	if !tracker.staleUntil.IsZero() {
		t.Errorf("below threshold: staleUntil should be zero, got %v", tracker.staleUntil)
	}
}

func TestRecordTorrentFailure_hitsThreshold(t *testing.T) {
	srv := testServer(t, Config{
		CircuitBreakerFailures:  5,
		CircuitBreakerWindowSec: 60,
		CircuitBreakerStaleMin:  5,
	})

	itemID := int64(42)
	for i := 0; i < 5; i++ {
		srv.recordTorrentFailure(itemID)
	}

	tracker := srv.torrentFailures[itemID]
	if len(tracker.failures) != 5 {
		t.Errorf("got %d failures, want 5", len(tracker.failures))
	}
	if tracker.staleUntil.IsZero() {
		t.Fatal("at threshold: staleUntil should be set")
	}
	expectedStale := time.Now().Add(5 * time.Minute)
	if tracker.staleUntil.Before(expectedStale.Add(-time.Second)) {
		t.Errorf("staleUntil too early: got %v, expected near %v", tracker.staleUntil, expectedStale)
	}
}

func TestRecordTorrentFailure_prunesOldFailures(t *testing.T) {
	srv := testServer(t, Config{
		CircuitBreakerFailures:  3,
		CircuitBreakerWindowSec: 60,
		CircuitBreakerStaleMin:  5,
	})

	itemID := int64(42)

	srv.torrentFailuresMu.Lock()
	oldTime := time.Now().Add(-120 * time.Second)
	tracker := &torrentFailureTracker{
		failures: []time.Time{oldTime, oldTime, oldTime},
	}
	srv.torrentFailures[itemID] = tracker
	srv.torrentFailuresMu.Unlock()

	srv.recordTorrentFailure(itemID)

	tracker = srv.torrentFailures[itemID]
	if len(tracker.failures) != 1 {
		t.Errorf("old failures should be pruned: got %d failures, want 1", len(tracker.failures))
	}
	if !tracker.staleUntil.IsZero() {
		t.Errorf("pruned case: staleUntil should be zero (only 1 active failure), got %v", tracker.staleUntil)
	}
}

func TestIsTorrentStale_noTracker(t *testing.T) {
	srv := testServer(t)
	if srv.isTorrentStale(42) {
		t.Error("no tracker: expected false")
	}
}

func TestIsTorrentStale_staleActive(t *testing.T) {
	srv := testServer(t)

	srv.torrentFailuresMu.Lock()
	srv.torrentFailures[42] = &torrentFailureTracker{
		staleUntil: time.Now().Add(5 * time.Minute),
	}
	srv.torrentFailuresMu.Unlock()

	if !srv.isTorrentStale(42) {
		t.Error("active stale period: expected true")
	}

	srv.torrentFailuresMu.Lock()
	if _, exists := srv.torrentFailures[42]; !exists {
		t.Error("active stale: tracker should still exist in map")
	}
	srv.torrentFailuresMu.Unlock()
}

func TestIsTorrentStale_periodExpired(t *testing.T) {
	srv := testServer(t)

	srv.torrentFailuresMu.Lock()
	srv.torrentFailures[42] = &torrentFailureTracker{
		staleUntil: time.Now().Add(-1 * time.Minute),
	}
	srv.torrentFailuresMu.Unlock()

	if srv.isTorrentStale(42) {
		t.Error("expired stale period: expected false")
	}

	srv.torrentFailuresMu.Lock()
	if _, exists := srv.torrentFailures[42]; exists {
		t.Error("expired stale: tracker should have been removed")
	}
	srv.torrentFailuresMu.Unlock()
}

func TestIsTorrentStale_notYetStale(t *testing.T) {
	srv := testServer(t)

	srv.torrentFailuresMu.Lock()
	srv.torrentFailures[42] = &torrentFailureTracker{
		failures: []time.Time{time.Now()},
	}
	srv.torrentFailuresMu.Unlock()

	if srv.isTorrentStale(42) {
		t.Error("failures but not stale: expected false")
	}

	srv.torrentFailuresMu.Lock()
	if _, exists := srv.torrentFailures[42]; !exists {
		t.Error("not stale: tracker should still exist")
	}
	srv.torrentFailuresMu.Unlock()
}

func TestSweepNegativeCache_removesExpired(t *testing.T) {
	srv := testServer(t, Config{NegativeCacheMaxEntries: 10})

	srv.negativeCacheMu.Lock()
	srv.negativeCache["expired1"] = &negativeCacheEntry{expiresAt: time.Now().Add(-1 * time.Minute)}
	srv.negativeCache["expired2"] = &negativeCacheEntry{expiresAt: time.Now().Add(-2 * time.Minute)}
	srv.negativeCache["fresh"] = &negativeCacheEntry{expiresAt: time.Now().Add(1 * time.Minute)}
	srv.negativeCacheMu.Unlock()

	srv.sweepNegativeCache()

	srv.negativeCacheMu.Lock()
	if _, exists := srv.negativeCache["expired1"]; exists {
		t.Error("expired entry 1 should have been removed")
	}
	if _, exists := srv.negativeCache["expired2"]; exists {
		t.Error("expired entry 2 should have been removed")
	}
	if _, exists := srv.negativeCache["fresh"]; !exists {
		t.Error("fresh entry should still exist")
	}
	if len(srv.negativeCache) != 1 {
		t.Errorf("expected 1 entry, got %d", len(srv.negativeCache))
	}
	srv.negativeCacheMu.Unlock()
}

func TestSweepNegativeCache_allExpired(t *testing.T) {
	srv := testServer(t, Config{NegativeCacheMaxEntries: 10})

	srv.negativeCacheMu.Lock()
	srv.negativeCache["a"] = &negativeCacheEntry{expiresAt: time.Now().Add(-1 * time.Minute)}
	srv.negativeCache["b"] = &negativeCacheEntry{expiresAt: time.Now().Add(-2 * time.Minute)}
	srv.negativeCacheMu.Unlock()

	srv.sweepNegativeCache()

	srv.negativeCacheMu.Lock()
	if len(srv.negativeCache) != 0 {
		t.Errorf("all expired: expected 0 entries, got %d", len(srv.negativeCache))
	}
	srv.negativeCacheMu.Unlock()
}

func TestSweepNegativeCache_noneExpired(t *testing.T) {
	srv := testServer(t, Config{NegativeCacheMaxEntries: 10})

	srv.negativeCacheMu.Lock()
	srv.negativeCache["a"] = &negativeCacheEntry{expiresAt: time.Now().Add(1 * time.Minute)}
	srv.negativeCache["b"] = &negativeCacheEntry{expiresAt: time.Now().Add(2 * time.Minute)}
	srv.negativeCacheMu.Unlock()

	srv.sweepNegativeCache()

	srv.negativeCacheMu.Lock()
	if len(srv.negativeCache) != 2 {
		t.Errorf("none expired: expected 2 entries, got %d", len(srv.negativeCache))
	}
	srv.negativeCacheMu.Unlock()
}

func TestSweepNegativeCache_empty(t *testing.T) {
	srv := testServer(t, Config{NegativeCacheMaxEntries: 10})

	srv.sweepNegativeCache()

	srv.negativeCacheMu.Lock()
	if len(srv.negativeCache) != 0 {
		t.Errorf("empty: expected 0 entries, got %d", len(srv.negativeCache))
	}
	srv.negativeCacheMu.Unlock()
}

func TestSweepNegativeCache_evictsOldestWhenOverMax(t *testing.T) {
	srv := testServer(t, Config{NegativeCacheMaxEntries: 3})

	now := time.Now()
	srv.negativeCacheMu.Lock()
	srv.negativeCache["a"] = &negativeCacheEntry{expiresAt: now.Add(1 * time.Minute)}
	srv.negativeCache["b"] = &negativeCacheEntry{expiresAt: now.Add(2 * time.Minute)}
	srv.negativeCache["c"] = &negativeCacheEntry{expiresAt: now.Add(3 * time.Minute)}
	srv.negativeCache["d"] = &negativeCacheEntry{expiresAt: now.Add(4 * time.Minute)}
	srv.negativeCacheMu.Unlock()

	srv.sweepNegativeCache()

	srv.negativeCacheMu.Lock()
	if len(srv.negativeCache) != 3 {
		t.Errorf("over max: expected 3 entries, got %d", len(srv.negativeCache))
	}
	// a (oldest expiring) should have been evicted
	if _, exists := srv.negativeCache["a"]; exists {
		t.Error("oldest entry 'a' should have been evicted")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, exists := srv.negativeCache[k]; !exists {
			t.Errorf("entry %q should have survived", k)
		}
	}
	srv.negativeCacheMu.Unlock()
}

func TestSweepNegativeCache_evictsAfterExpiredRemoved(t *testing.T) {
	srv := testServer(t, Config{NegativeCacheMaxEntries: 2})

	now := time.Now()
	srv.negativeCacheMu.Lock()
	srv.negativeCache["exp"] = &negativeCacheEntry{expiresAt: now.Add(-1 * time.Minute)}
	srv.negativeCache["a"] = &negativeCacheEntry{expiresAt: now.Add(1 * time.Minute)}
	srv.negativeCache["b"] = &negativeCacheEntry{expiresAt: now.Add(2 * time.Minute)}
	srv.negativeCache["c"] = &negativeCacheEntry{expiresAt: now.Add(3 * time.Minute)}
	srv.negativeCacheMu.Unlock()

	srv.sweepNegativeCache()

	srv.negativeCacheMu.Lock()
	if len(srv.negativeCache) != 2 {
		t.Errorf("expected 2 entries after sweep+evict, got %d", len(srv.negativeCache))
	}
	if _, exists := srv.negativeCache["exp"]; exists {
		t.Error("expired entry should have been removed")
	}
	if _, exists := srv.negativeCache["a"]; exists {
		t.Error("oldest surviving entry 'a' should have been evicted")
	}
	srv.negativeCacheMu.Unlock()
}

func TestSweepCircuitBreaker_removesExpired(t *testing.T) {
	srv := testServer(t, Config{CircuitBreakerMaxEntries: 10})

	srv.torrentFailuresMu.Lock()
	srv.torrentFailures[1] = &torrentFailureTracker{staleUntil: time.Now().Add(-1 * time.Minute)}
	srv.torrentFailures[2] = &torrentFailureTracker{staleUntil: time.Now().Add(1 * time.Minute)}
	srv.torrentFailures[3] = &torrentFailureTracker{staleUntil: time.Now().Add(-2 * time.Minute)}
	srv.torrentFailuresMu.Unlock()

	srv.sweepCircuitBreaker()

	srv.torrentFailuresMu.Lock()
	if _, exists := srv.torrentFailures[1]; exists {
		t.Error("expired tracker 1 should have been removed")
	}
	if _, exists := srv.torrentFailures[3]; exists {
		t.Error("expired tracker 3 should have been removed")
	}
	if _, exists := srv.torrentFailures[2]; !exists {
		t.Error("active tracker 2 should survive")
	}
	if len(srv.torrentFailures) != 1 {
		t.Errorf("expected 1 tracker, got %d", len(srv.torrentFailures))
	}
	srv.torrentFailuresMu.Unlock()
}

func TestSweepCircuitBreaker_neverStaleSurvives(t *testing.T) {
	srv := testServer(t, Config{CircuitBreakerMaxEntries: 10})

	srv.torrentFailuresMu.Lock()
	srv.torrentFailures[1] = &torrentFailureTracker{
		failures:   []time.Time{time.Now()},
		staleUntil: time.Time{}, // zero — never went stale
	}
	srv.torrentFailures[2] = &torrentFailureTracker{staleUntil: time.Now().Add(-1 * time.Minute)}
	srv.torrentFailuresMu.Unlock()

	srv.sweepCircuitBreaker()

	srv.torrentFailuresMu.Lock()
	if _, exists := srv.torrentFailures[1]; !exists {
		t.Error("never-stale tracker 1 should survive")
	}
	if _, exists := srv.torrentFailures[2]; exists {
		t.Error("expired tracker 2 should have been removed")
	}
	srv.torrentFailuresMu.Unlock()
}

func TestSweepCircuitBreaker_empty(t *testing.T) {
	srv := testServer(t, Config{CircuitBreakerMaxEntries: 10})

	srv.sweepCircuitBreaker()

	srv.torrentFailuresMu.Lock()
	if len(srv.torrentFailures) != 0 {
		t.Errorf("empty: expected 0, got %d", len(srv.torrentFailures))
	}
	srv.torrentFailuresMu.Unlock()
}

func TestSweepCircuitBreaker_evictsOldestWhenOverMax(t *testing.T) {
	srv := testServer(t, Config{CircuitBreakerMaxEntries: 2})

	now := time.Now()
	srv.torrentFailuresMu.Lock()
	srv.torrentFailures[1] = &torrentFailureTracker{staleUntil: now.Add(1 * time.Minute)}
	srv.torrentFailures[2] = &torrentFailureTracker{staleUntil: now.Add(2 * time.Minute)}
	srv.torrentFailures[3] = &torrentFailureTracker{staleUntil: now.Add(3 * time.Minute)}
	srv.torrentFailuresMu.Unlock()

	srv.sweepCircuitBreaker()

	srv.torrentFailuresMu.Lock()
	if len(srv.torrentFailures) != 2 {
		t.Errorf("over max: expected 2 entries, got %d", len(srv.torrentFailures))
	}
	// 1 has the oldest (soonest) staleUntil — should be evicted
	if _, exists := srv.torrentFailures[1]; exists {
		t.Error("oldest entry 1 should have been evicted")
	}
	for _, id := range []int64{2, 3} {
		if _, exists := srv.torrentFailures[id]; !exists {
			t.Errorf("entry %d should have survived", id)
		}
	}
	srv.torrentFailuresMu.Unlock()
}

func TestHandleStatsJSON_returnsMetrics(t *testing.T) {
	srv := testServer(t, Config{
		Version:           "test",
		StatsChartMinutes: 60,
	})

	if err := srv.store.RecordStats(map[string]float64{
		"api_calls_success": 42,
		"api_calls_failed":  3,
	}); err != nil {
		t.Fatalf("RecordStats failed: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/stats.json", nil)
	r.Header.Set("X-CSRF-Token", srv.csrfToken)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, r)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var data map[string][]map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}

	if len(data) != 2 {
		t.Errorf("expected 2 metric keys, got %d", len(data))
	}

	success, ok := data["api_calls_success"]
	if !ok {
		t.Fatal("expected key 'api_calls_success'")
	}
	if len(success) != 1 {
		t.Fatalf("expected 1 data point for success, got %d", len(success))
	}
	if v, ok := success[0]["v"].(float64); !ok || v != 42 {
		t.Errorf("expected value 42, got %v", success[0]["v"])
	}
	if tStr, ok := success[0]["t"].(string); !ok || tStr == "" {
		t.Errorf("expected non-empty timestamp, got %q", tStr)
	}

	failed, ok := data["api_calls_failed"]
	if !ok {
		t.Fatal("expected key 'api_calls_failed'")
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 data point for failed, got %d", len(failed))
	}
	if v, ok := failed[0]["v"].(float64); !ok || v != 3 {
		t.Errorf("expected value 3, got %v", failed[0]["v"])
	}
}

func TestHandleStatsJSON_emptyStore(t *testing.T) {
	srv := testServer(t, Config{
		Version:           "test",
		StatsChartMinutes: 60,
	})

	r := httptest.NewRequest(http.MethodGet, "/stats.json", nil)
	r.Header.Set("X-CSRF-Token", srv.csrfToken)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, r)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("expected empty JSON object, got %d keys", len(data))
	}
}

func TestHandleStatsJSON_unauthenticated(t *testing.T) {
	srv := testServer(t, Config{
		Version:      "test",
		AuthEnabled:  true,
		AuthUsername: "admin",
		AuthPassword: "secret",
	})

	r := httptest.NewRequest(http.MethodGet, "/stats.json", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, r)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestRecordStats_storesAllMetrics(t *testing.T) {
	srv := testServer(t, Config{
		Version: "test",
	})

	srv.recordStats()

	since := time.Now().Add(-24 * time.Hour)
	metrics, err := srv.store.QueryAllStatsSince(since)
	if err != nil {
		t.Fatalf("QueryAllStatsSince failed: %v", err)
	}

	expectedMetrics := []string{
		"api_calls_success",
		"api_calls_failed",
		"api_calls_429",
		"db_lock_errors",
		"gc_cycles",
		"sys_mb",
		"alloc_mb",
		"heap_objects",
		"negative_cache_entries",
		"circuit_breaker_entries",
	}

	for _, name := range expectedMetrics {
		if _, ok := metrics[name]; !ok {
			t.Errorf("expected metric %q not found in stored data", name)
		}
	}

	if len(metrics) != len(expectedMetrics) {
		t.Errorf("expected %d metrics, got %d", len(expectedMetrics), len(metrics))
	}
}

func TestRecordStats_zeroDeltasOnSecondCall(t *testing.T) {
	srv := testServer(t, Config{
		Version: "test",
	})

	srv.recordStats()
	srv.recordStats()

	since := time.Now().Add(-24 * time.Hour)
	metrics, err := srv.store.QueryAllStatsSince(since)
	if err != nil {
		t.Fatalf("QueryAllStatsSince failed: %v", err)
	}

	successRecords := metrics["api_calls_success"]
	if len(successRecords) != 2 {
		t.Fatalf("expected 2 records for api_calls_success, got %d", len(successRecords))
	}

	if successRecords[1].Value != 0 {
		t.Errorf("second call: expected delta 0 for api_calls_success, got %f", successRecords[1].Value)
	}

	sysRecords := metrics["sys_mb"]
	if len(sysRecords) != 2 {
		t.Fatalf("expected 2 records for sys_mb, got %d", len(sysRecords))
	}
	if sysRecords[1].Value <= 0 {
		t.Errorf("sys_mb should be positive, got %f", sysRecords[1].Value)
	}
}

func TestHandleStatsJSON_minutesFallback(t *testing.T) {
	srv := testServer(t, Config{
		Version:           "test",
		StatsChartMinutes: 0,
	})

	if err := srv.store.RecordStats(map[string]float64{"test": 1.0}); err != nil {
		t.Fatalf("RecordStats failed: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/stats.json", nil)
	r.Header.Set("X-CSRF-Token", srv.csrfToken)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, r)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}
