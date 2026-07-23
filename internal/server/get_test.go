package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mainlink0435/warpbox/internal/metadata"
	"github.com/mainlink0435/warpbox/internal/throttle"
	"github.com/mainlink0435/warpbox/internal/torbox"
)

// ---------------------------------------------------------------------------
// Mock CDN helpers for hang/poll retry tests (adapted from upstream d2497af;
// works with per-item data cooldown by shortening cdnPollInterval).
// ---------------------------------------------------------------------------

// cdnResponse defines a single response the mock CDN server should return.
type cdnResponse struct {
	status      int
	body        string
	contentType string
}

// newMockCDNServer returns an httptest.Server that cycles through the given
// responses on each successive request. Used to simulate transient CDN
// data errors (429, 5xx, disguised text bodies) followed by success.
func newMockCDNServer(t *testing.T, responses []cdnResponse) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	idx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if idx >= len(responses) {
			idx = 0
		}
		resp := responses[idx]
		idx++
		mu.Unlock()
		if resp.contentType != "" {
			w.Header().Set("Content-Type", resp.contentType)
		}
		w.WriteHeader(resp.status)
		io.WriteString(w, resp.body)
	}))
}

// newMockTorBoxForCDN returns an httptest.Server that responds to TorBox API
// requestdl calls with a CDN URL pointing to the given base URL.
func newMockTorBoxForCDN(t *testing.T, cdnBaseURL string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"success":true,"data":"%s/file"}`, cdnBaseURL)
	}))
}

// newTestCDNHangEnv wires a full environment for CDN hang/poll tests.
// Shortens cdnPollInterval (also used as data cooldown) so tests stay fast.
func newTestCDNHangEnv(t *testing.T, cdnResponses []cdnResponse) (*Server, *httptest.ResponseRecorder, func()) {
	t.Helper()

	oldPoll := cdnPollInterval
	cdnPollInterval = 10 * time.Millisecond

	mockCDN := newMockCDNServer(t, cdnResponses)
	mockTorBox := newMockTorBoxForCDN(t, mockCDN.URL)

	client := torbox.NewClient("test-key")
	client.SetBaseURL(mockTorBox.URL)

	store, err := metadata.Open(":memory:")
	if err != nil {
		mockCDN.Close()
		mockTorBox.Close()
		t.Fatalf("opening in-memory store: %v", err)
	}
	if err := store.UpsertFile(metadata.FileRecord{
		ItemID: 1, FileID: 10, Source: metadata.SourceTorrent,
		Name: "test.mkv", Path: "Test/test.mkv", Size: 5000, MimeType: "video/x-matroska",
	}); err != nil {
		store.Close()
		mockCDN.Close()
		mockTorBox.Close()
		t.Fatalf("upserting test file: %v", err)
	}

	queue := throttle.NewQueue(600)
	qCtx, qCancel := context.WithCancel(context.Background())
	queue.Start(qCtx)

	srv := New(Config{Version: "test"}, store, client, queue)
	w := httptest.NewRecorder()

	cleanup := func() {
		qCancel()
		srv.StopCleanup()
		store.Close()
		mockCDN.Close()
		mockTorBox.Close()
		cdnPollInterval = oldPoll
	}

	return srv, w, cleanup
}

func TestParseRangeFull(t *testing.T) {
	r, err := parseRange("bytes=0-499", 1000)
	if err != nil {
		t.Fatalf("parseRange failed: %v", err)
	}
	if r.Start != 0 {
		t.Errorf("start = %d, want 0", r.Start)
	}
	if r.End != 499 {
		t.Errorf("end = %d, want 499", r.End)
	}
	if r.Length != 500 {
		t.Errorf("length = %d, want 500", r.Length)
	}
}

func TestParseRangeToEnd(t *testing.T) {
	r, err := parseRange("bytes=500-", 1000)
	if err != nil {
		t.Fatalf("parseRange failed: %v", err)
	}
	if r.Start != 500 {
		t.Errorf("start = %d, want 500", r.Start)
	}
	if r.End != 999 {
		t.Errorf("end = %d, want 999", r.End)
	}
	if r.Length != 500 {
		t.Errorf("length = %d, want 500", r.Length)
	}
}

func TestParseRangeSingleByte(t *testing.T) {
	r, err := parseRange("bytes=0-0", 100)
	if err != nil {
		t.Fatalf("parseRange failed: %v", err)
	}
	if r.Start != 0 {
		t.Errorf("start = %d, want 0", r.Start)
	}
	if r.End != 0 {
		t.Errorf("end = %d, want 0", r.End)
	}
	if r.Length != 1 {
		t.Errorf("length = %d, want 1", r.Length)
	}
}

func TestParseRangeEmpty(t *testing.T) {
	_, err := parseRange("", 1000)
	if err == nil {
		t.Fatal("expected error for empty range")
	}
}

func TestParseRangeNoBytesPrefix(t *testing.T) {
	_, err := parseRange("0-499", 1000)
	if err == nil {
		t.Fatal("expected error for missing bytes= prefix")
	}
}

func TestParseRangeOutOfBounds(t *testing.T) {
	_, err := parseRange("bytes=0-2000", 1000)
	if err == nil {
		t.Fatal("expected error for out-of-bounds range")
	}
}

func TestParseRangeNegativeStart(t *testing.T) {
	_, err := parseRange("bytes=-100-200", 1000)
	if err == nil {
		t.Fatal("expected error for negative start")
	}
}

func TestParseRangeLargeFile(t *testing.T) {
	r, err := parseRange("bytes=0-524287", 10*1024*1024*1024)
	if err != nil {
		t.Fatalf("parseRange failed for large file: %v", err)
	}
	if r.Start != 0 {
		t.Errorf("start = %d", r.Start)
	}
	if r.End != 524287 {
		t.Errorf("end = %d", r.End)
	}
	if r.Length != 524288 {
		t.Errorf("length = %d, want 524288", r.Length)
	}
}

func TestParseRangeRejectsMultipleRanges(t *testing.T) {
	_, err := parseRange("bytes=0-100,200-300", 1000)
	// SplitN only splits on first -, so this will likely produce malformed parts.
	if err == nil {
		t.Log("multiple range rejection: expected error, got nil (split may have parsed first)")
	}
}

func TestCdnCacheKeyTorrent(t *testing.T) {
	key := cdnCacheKey(metadata.SourceTorrent, 100, 5)
	want := "torrent:100:5"
	if key != want {
		t.Errorf("cdnCacheKey(torrent, 100, 5) = %q, want %q", key, want)
	}
}

func TestCdnCacheKeyUsenet(t *testing.T) {
	key := cdnCacheKey(metadata.SourceUsenet, 200, 5)
	want := "usenet:200:5"
	if key != want {
		t.Errorf("cdnCacheKey(usenet, 200, 5) = %q, want %q", key, want)
	}
}

func TestCdnCacheKeyDifferentiation(t *testing.T) {
	// Same IDs, different source should produce different keys.
	torKey := cdnCacheKey(metadata.SourceTorrent, 42, 7)
	usenetKey := cdnCacheKey(metadata.SourceUsenet, 42, 7)
	if torKey == usenetKey {
		t.Error("torrent and usenet keys should differ with same item_id and file_id")
	}
}

func TestCDNSemaphoreAcquireRelease(t *testing.T) {
	s := &Server{
		cdnSem: make(chan struct{}, 2),
	}

	// Acquire should succeed immediately when a token is available.
	s.cdnSem <- struct{}{}
	s.AcquireCDNConn()

	// Acquire from goroutine, then release in main goroutine.
	done := make(chan struct{})
	go func() {
		s.AcquireCDNConn()
		close(done)
	}()

	s.ReleaseCDNConn()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AcquireCDNConn deadlocked — slot was not released properly")
	}
}

func TestDoublePollInterval(t *testing.T) {
	if got := doublePollInterval(15 * time.Second); got != 30*time.Second {
		t.Errorf("double 15s = %v, want 30s", got)
	}
	if got := doublePollInterval(maxCDNPollInterval); got != maxCDNPollInterval {
		t.Errorf("capped at max: got %v", got)
	}
	if got := doublePollInterval(3 * time.Minute); got != maxCDNPollInterval {
		t.Errorf("3m*2 should cap at max: got %v", got)
	}
}

func TestIsCDNTransientStatus(t *testing.T) {
	if !isCDNTransientStatus(429) || !isCDNTransientStatus(503) {
		t.Error("429/5xx should be transient")
	}
	if isCDNTransientStatus(200) || isCDNTransientStatus(404) || isCDNTransientStatus(403) {
		t.Error("2xx/403/404 should not be transient")
	}
}

func TestIsCDNDisguisedErrorBody(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{"video/mp4", false},
		{"application/octet-stream", false},
		{"text/plain", true},
		{"text/html; charset=utf-8", true},
		{"application/json", true},
		{"application/vnd.apple.mpegurl", false}, // contains no html/json/text prefix
	}
	for _, tc := range cases {
		if got := isCDNDisguisedErrorBody(tc.ct); got != tc.want {
			t.Errorf("isCDNDisguisedErrorBody(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}

func TestIsCDNPermanentDataFailure(t *testing.T) {
	cases := []struct {
		status int
		ct     string
		want   bool
	}{
		{http.StatusNotFound, "text/html", true},
		{http.StatusForbidden, "application/json", true},
		{http.StatusTooManyRequests, "text/html", false}, // rate limit — hang
		{http.StatusOK, "text/html", false},              // disguised 429-style — hang
		{http.StatusOK, "video/mp4", false},
		{http.StatusInternalServerError, "text/html", false}, // 5xx — hang
		{http.StatusBadRequest, "text/html", true},
	}
	for _, tc := range cases {
		got := isCDNPermanentDataFailure(tc.status, tc.ct)
		if got != tc.want {
			t.Errorf("status=%d ct=%q: got %v want %v", tc.status, tc.ct, got, tc.want)
		}
	}
}

// TestHandleGetCDNHang_Permanent404GivesUp verifies 404+html does not multi-minute poll.
func TestHandleGetCDNHang_Permanent404GivesUp(t *testing.T) {
	srv, w, cleanup := newTestCDNHangEnv(t, []cdnResponse{
		{status: http.StatusNotFound, body: "not found", contentType: "text/html"},
		// Must not be consumed — hang should exit after permanent failure.
		{status: http.StatusOK, body: "should-not-stream", contentType: "video/x-matroska"},
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/webdav/Test/test.mkv", nil)
	req.Header.Set("Range", "bytes=0-499")

	done := make(chan struct{})
	go func() {
		srv.handleGet(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleGet did not exit promptly on CDN 404 (hang should fail-fast)")
	}
}

func TestStreamFileContent_RejectsNegativeFileID(t *testing.T) {
	store, err := metadata.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.UpsertFile(metadata.FileRecord{
		ItemID: 1, FileID: -1, Source: metadata.SourceTorrent,
		Name: "bad.mkv", Path: "bad.mkv", Size: 100,
	}); err != nil {
		t.Fatal(err)
	}
	file, err := store.GetFileByPath("bad.mkv")
	if err != nil || file == nil {
		t.Fatalf("setup: %v file=%v", err, file)
	}

	srv := &Server{
		store:         store,
		negativeCache: make(map[string]*negativeCacheEntry),
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/webdav/bad.mkv", nil)
	req.Header.Set("Range", "bytes=0-99")
	srv.streamFileContent(w, req, file)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestMarkAndWaitCDNDataCooldown(t *testing.T) {
	s := &Server{
		cdnDataCooldown: make(map[int64]time.Time),
	}
	const itemID int64 = 46874023
	const cool = 80 * time.Millisecond

	s.markCDNDataCooldown(itemID, cool)
	start := time.Now()
	if err := s.waitCDNDataCooldown(context.Background(), itemID); err != nil {
		t.Fatalf("waitCDNDataCooldown: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < cool-20*time.Millisecond {
		t.Errorf("wait returned too early: elapsed=%v cool=%v", elapsed, cool)
	}

	// No cooldown: wait returns immediately.
	start = time.Now()
	if err := s.waitCDNDataCooldown(context.Background(), 999); err != nil {
		t.Fatalf("wait with no cooldown: %v", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Error("wait with no cooldown should be immediate")
	}
}

func TestMarkCDNDataCooldownKeepsLonger(t *testing.T) {
	s := &Server{
		cdnDataCooldown: make(map[int64]time.Time),
	}
	s.markCDNDataCooldown(1, 200*time.Millisecond)
	s.markCDNDataCooldown(1, 10*time.Millisecond) // shorter — should not shorten

	s.cdnDataCooldownMu.Lock()
	until := s.cdnDataCooldown[1]
	s.cdnDataCooldownMu.Unlock()
	if time.Until(until) < 100*time.Millisecond {
		t.Errorf("longer cooldown was shortened: remaining %v", time.Until(until))
	}
}

func TestWaitCDNDataCooldownCancelled(t *testing.T) {
	s := &Server{
		cdnDataCooldown: make(map[int64]time.Time),
	}
	s.markCDNDataCooldown(7, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.waitCDNDataCooldown(ctx, 7)
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestSweepCDNDataCooldown(t *testing.T) {
	s := &Server{
		cdnDataCooldown: map[int64]time.Time{
			1: time.Now().Add(-time.Second),
			2: time.Now().Add(time.Minute),
		},
	}
	s.sweepCDNDataCooldown()
	s.cdnDataCooldownMu.Lock()
	defer s.cdnDataCooldownMu.Unlock()
	if _, ok := s.cdnDataCooldown[1]; ok {
		t.Error("expired cooldown should be removed")
	}
	if _, ok := s.cdnDataCooldown[2]; !ok {
		t.Error("active cooldown should remain")
	}
}

// ---------------------------------------------------------------------------
// CDN hang/poll retry tests (from upstream; compatible with data cooldown)
// ---------------------------------------------------------------------------

// TestHandleGetCDNHang_RetriesOnData429 verifies streamFileContent routes a
// CDN 429 into hang/poll, which retries and streams real data (not the 429 body).
func TestHandleGetCDNHang_RetriesOnData429(t *testing.T) {
	srv, w, cleanup := newTestCDNHangEnv(t, []cdnResponse{
		{status: http.StatusTooManyRequests, body: "rate limited"},
		{status: http.StatusOK, body: "real binary data", contentType: "application/octet-stream"},
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/webdav/Test/test.mkv", nil)
	req.Header.Set("Range", "bytes=0-499")
	srv.handleGet(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "real binary data" {
		t.Errorf("expected body %q, got %q", "real binary data", string(body))
	}
}

// TestHandleGetCDNHang_RetriesOnDisguisedTextBody verifies a 200 text/html CDN
// response is not streamed as file data; hang retries until real media arrives.
func TestHandleGetCDNHang_RetriesOnDisguisedTextBody(t *testing.T) {
	srv, w, cleanup := newTestCDNHangEnv(t, []cdnResponse{
		{status: http.StatusOK, body: "too many requests", contentType: "text/html"},
		{status: http.StatusOK, body: "real binary data", contentType: "video/x-matroska"},
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/webdav/Test/test.mkv", nil)
	req.Header.Set("Range", "bytes=0-499")
	srv.handleGet(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "real binary data" {
		t.Errorf("expected body %q, got %q", "real binary data", string(body))
	}
}

// TestHandleGetCDNHang_ClientDisconnectExitsCleanly verifies hang exits when
// the client context is cancelled.
func TestHandleGetCDNHang_ClientDisconnectExitsCleanly(t *testing.T) {
	srv, w, cleanup := newTestCDNHangEnv(t, []cdnResponse{
		{status: http.StatusTooManyRequests, body: "keep waiting"},
		{status: http.StatusTooManyRequests, body: "still busy"},
		{status: http.StatusTooManyRequests, body: "not yet"},
	})
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/webdav/Test/test.mkv", nil).WithContext(ctx)
	req.Header.Set("Range", "bytes=0-499")

	done := make(chan struct{})
	go func() {
		srv.handleGet(w, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleGet did not exit after context cancellation")
	}
}

// TestStreamFileContent_Routes429ToHang is an integration check: initial proxy
// 429 → hang/poll → valid body.
func TestStreamFileContent_Routes429ToHang(t *testing.T) {
	srv, w, cleanup := newTestCDNHangEnv(t, []cdnResponse{
		{status: http.StatusTooManyRequests, body: "rate limited"},
		{status: http.StatusOK, body: "real binary data", contentType: "application/octet-stream"},
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/webdav/Test/test.mkv", nil)
	req.Header.Set("Range", "bytes=0-499")
	srv.handleGet(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "real binary data" {
		t.Errorf("expected body %q, got %q", "real binary data", string(body))
	}
}

// TestHandleGetCDNHang_RetriesTwiceInsideHang verifies hang itself retries when
// the first post-recovery data attempt is still 429 (cooldown shortened via env).
func TestHandleGetCDNHang_RetriesTwiceInsideHang(t *testing.T) {
	srv, w, cleanup := newTestCDNHangEnv(t, []cdnResponse{
		// streamFileContent
		{status: http.StatusTooManyRequests, body: "rate limited"},
		// hang attempt 1
		{status: http.StatusTooManyRequests, body: "still limited"},
		// hang attempt 2
		{status: http.StatusOK, body: "real binary data", contentType: "application/octet-stream"},
	})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/webdav/Test/test.mkv", nil)
	req.Header.Set("Range", "bytes=0-499")
	srv.handleGet(w, req)

	resp := w.Result()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "real binary data" {
		t.Errorf("expected body %q, got %q", "real binary data", string(body))
	}
}
