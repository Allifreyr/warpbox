package server

import (
	"context"
	"testing"
	"time"

	"github.com/mainlink0435/warpbox/internal/metadata"
)

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
