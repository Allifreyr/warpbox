package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mainlink0435/warpbox/internal/metadata"
	"github.com/mainlink0435/warpbox/internal/throttle"
)

// TestPlaybackNotBlockedBySync: a long-running metadata-sync Execute must not
// delay a cold-play requestdl on the playback queue.
func TestPlaybackNotBlockedBySync(t *testing.T) {
	srv, _, cleanup := newTestCDNHangEnv(t, []cdnResponse{
		{status: http.StatusOK, body: "data", contentType: "video/x-matroska"},
	})
	defer cleanup()

	syncQueue := throttle.NewQueue(600)
	syncCtx, syncCancel := context.WithCancel(context.Background())
	defer syncCancel()
	syncQueue.Start(syncCtx)
	srv.SetSyncQueue(syncQueue)

	syncStarted := make(chan struct{})
	release := make(chan struct{})
	syncQueue.Enqueue(throttle.Request{
		Label: "metadata sync: ListUsenet",
		Execute: func(context.Context) error {
			close(syncStarted)
			<-release
			return nil
		},
	})

	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("sync request never started")
	}

	type result struct {
		url string
		err error
	}
	done := make(chan result, 1)
	go func() {
		url, err := srv.fetchCDNURL(metadata.SourceTorrent, 1, 10)
		done <- result{url, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("fetchCDNURL failed while sync held open: %v", res.err)
		}
		if res.url == "" {
			t.Fatal("fetchCDNURL returned an empty URL")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cold-play requestdl blocked behind metadata sync")
	}

	close(release)
}

func TestThrottleStatsAggregation(t *testing.T) {
	limiter := throttle.NewLimiter(600)
	requestQueue := throttle.NewQueueWithLimiter(600, limiter)
	syncQueue := throttle.NewQueueWithLimiter(600, limiter)

	store, err := metadata.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	syncCtx, syncCancel := context.WithCancel(context.Background())
	defer syncCancel()
	requestQueue.Start(reqCtx)
	syncQueue.Start(syncCtx)

	srv := New(Config{Version: "test"}, store, nil, requestQueue)
	defer srv.StopCleanup()
	srv.SetSyncQueue(syncQueue)

	requestQueue.Enqueue(throttle.Request{Label: "playback", Execute: func(context.Context) error { return nil }})
	syncQueue.Enqueue(throttle.Request{Label: "sync", Execute: func(context.Context) error { return nil }})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ts := srv.throttleStats()
		if ts.TotalCalls >= 2 && ts.SuccessfulCalls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	ts := srv.throttleStats()
	if ts.TotalCalls < 2 || ts.SuccessfulCalls < 2 {
		t.Fatalf("expected aggregated stats across both queues, got total=%d success=%d", ts.TotalCalls, ts.SuccessfulCalls)
	}

	requestQueue.Record429()
	if n := srv.throttleStats().HTTP429Calls; n != 1 {
		t.Fatalf("expected 1 aggregated 429 after one Record429, got %d", n)
	}

	// Same limiter: recording on the other queue increments the same counter,
	// but throttleStats must not *sum* the two Stats() snapshots (which would
	// read 2+2=4).
	syncQueue.Record429()
	if n := srv.throttleStats().HTTP429Calls; n != 2 {
		t.Fatalf("expected 2 aggregated 429 (not double-counted as 4), got %d", n)
	}
}
