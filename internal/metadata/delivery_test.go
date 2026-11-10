package metadata

import (
	"context"
	"github.com/Allifreyr/warpbox/internal/throttle"
	"github.com/Allifreyr/warpbox/internal/torbox"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSyncItemCanceledWhileLocked(t *testing.T) {
	w := NewSyncWorker(nil, nil, nil, time.Minute, 5000, false, 3, time.Second, 0, nil)
	w.syncMu.Lock()
	defer w.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := w.SyncItem(ctx, SourceTorrent, 1); done <- err }()
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled sync still waiting for lock")
	}
}

func TestUpsertDoesNotClaimReadyAfterWriteFailure(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.db.Exec(`CREATE TRIGGER fail_file BEFORE INSERT ON files WHEN NEW.file_id=2 BEGIN SELECT RAISE(FAIL,'injected'); END`)
	if err != nil {
		t.Fatal(err)
	}
	w := NewSyncWorker(store, nil, nil, time.Minute, 5000, false, 3, time.Second, 0, nil)
	item := torbox.Torrent{ID: 42, DownloadState: "cached", Files: []torbox.TorrentFile{
		{ID: 1, ShortName: "one.mkv", S3Path: "hash/Show/one.mkv", Size: 123},
		{ID: 2, ShortName: "two.mkv", S3Path: "hash/Show/two.mkv", Size: 123},
	}}
	count, ready, _ := w.upsertItemFiles(item, SourceTorrent, 0)
	if ready || count != 1 {
		t.Fatalf("count=%d ready=%v", count, ready)
	}
	item.Files = item.Files[1:]
	count, ready, _ = w.upsertItemFiles(item, SourceTorrent, 0)
	if ready || count != 0 {
		t.Fatalf("count=%d ready=%v", count, ready)
	}
}

func TestUpsertNoUsableFilesNotReady(t *testing.T) {
	w := NewSyncWorker(nil, nil, nil, time.Minute, 5000, false, 3, time.Second, 0, nil)
	count, ready, _ := w.upsertItemFiles(torbox.Torrent{ID: 1, DownloadState: "cached", Files: []torbox.TorrentFile{{ID: -1}}}, SourceTorrent, 0)
	if count != 0 || ready {
		t.Fatalf("count=%d ready=%v", count, ready)
	}
}

func TestSyncItemCanceledWhileQueuedReleasesLock(t *testing.T) {
	q := throttle.NewQueue(60)
	w := NewSyncWorker(nil, nil, q, time.Hour, 5000, false, 0, time.Second, 0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := w.SyncItem(ctx, SourceTorrent, 42); done <- err }()
	select {
	case err := <-done:
		if err != context.DeadlineExceeded {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled item still waits for queue")
	}
	if !w.syncMu.TryLock() {
		t.Fatal("sync lock leaked")
	}
	w.syncMu.Unlock()
}

func TestSyncItemStopsInFlightCallWhenQueueStops(t *testing.T) {
	started := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- true; <-r.Context().Done() }))
	defer server.Close()
	client := torbox.NewClient("key")
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(&http.Client{})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := throttle.NewQueue(99999)
	q.Start(parent)
	sw := NewSyncWorker(nil, client, q, time.Hour, 5000, false, 0, time.Second, 0, nil)
	done := make(chan error, 1)
	go func() { _, err := sw.SyncItem(context.Background(), SourceTorrent, 42); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("item request not started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("queue shutdown did not cancel item request")
	}
}
