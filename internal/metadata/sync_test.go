package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mainlink0435/warpbox/internal/throttle"
	"github.com/mainlink0435/warpbox/internal/torbox"
)

func TestBuildFileRecordTorrent(t *testing.T) {
	f := torbox.TorrentFile{
		ID:        10,
		Name:      "movie.mkv",
		Size:      500,
		MimeType:  "video/x-matroska",
		S3Path:    "abc123/dir/movie.mkv",
		ShortName: "movie.mkv",
	}
	rec := buildFileRecord(42, f, 7, SourceTorrent, "2025-01-01T00:00:00Z", nil, nil, "")

	if rec.ItemID != 42 {
		t.Errorf("ItemID = %d, want 42", rec.ItemID)
	}
	if rec.FileID != 10 {
		t.Errorf("FileID = %d, want 10", rec.FileID)
	}
	if rec.Source != SourceTorrent {
		t.Errorf("Source = %d, want %d (SourceTorrent)", rec.Source, SourceTorrent)
	}
	if rec.SyncTag != 7 {
		t.Errorf("SyncTag = %d, want 7", rec.SyncTag)
	}
	if rec.CreatedAt != "2025-01-01T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want %q", rec.CreatedAt, "2025-01-01T00:00:00Z")
	}
	if rec.Path != "dir/movie.mkv" {
		t.Errorf("Path = %q, want %q", rec.Path, "dir/movie.mkv")
	}
}

func TestBuildFileRecordUsenet(t *testing.T) {
	f := torbox.TorrentFile{
		ID:        20,
		Name:      "usenet_file.mkv",
		Size:      1000,
		MimeType:  "video/x-matroska",
		S3Path:    "def456/usenet_file.mkv",
		ShortName: "usenet_file.mkv",
	}
	rec := buildFileRecord(1644029, f, 3, SourceUsenet, "2025-06-01T12:00:00Z", nil, nil, "")

	if rec.ItemID != 1644029 {
		t.Errorf("ItemID = %d, want 1644029", rec.ItemID)
	}
	if rec.Source != SourceUsenet {
		t.Errorf("Source = %d, want %d (SourceUsenet)", rec.Source, SourceUsenet)
	}
	if rec.SyncTag != 3 {
		t.Errorf("SyncTag = %d, want 3", rec.SyncTag)
	}
	if rec.Path != "usenet_file.mkv" {
		t.Errorf("Path = %q, want %q", rec.Path, "usenet_file.mkv")
	}
}

func TestBuildFileRecordSingleFileAtRoot(t *testing.T) {
	// Single-file items have s3_path like "hash/filename.ext" with no directory.
	f := torbox.TorrentFile{
		ID:        1,
		S3Path:    "abc123/movie.mkv",
		ShortName: "movie.mkv",
	}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", nil, nil, "")

	if rec.Path != "movie.mkv" {
		t.Errorf("single-file Path = %q, want %q", rec.Path, "movie.mkv")
	}
}

func TestBuildFileRecordMultiFileWithDir(t *testing.T) {
	// Multi-file items have s3_path like "hash/dir/file.ext".
	f := torbox.TorrentFile{
		ID:        2,
		S3Path:    "abc123/Season 1/episode.mkv",
		ShortName: "episode.mkv",
	}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", nil, nil, "")

	if rec.Path != "Season 1/episode.mkv" {
		t.Errorf("multi-file Path = %q, want %q", rec.Path, "Season 1/episode.mkv")
	}
}

func TestBuildFileRecordSanitizesPath(t *testing.T) {
	// Characters like & should be replaced.
	f := torbox.TorrentFile{
		ID:        3,
		S3Path:    "abc123/A & B/show.mkv",
		ShortName: "show.mkv",
	}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", nil, nil, "")

	if rec.Path != "A _ B/show.mkv" {
		t.Errorf("sanitized Path = %q, want %q", rec.Path, "A _ B/show.mkv")
	}
	if rec.Name != "show.mkv" {
		t.Errorf("sanitized Name = %q, want %q", rec.Name, "show.mkv")
	}
}

func TestSyncWorker_Stop_BeforeStart(t *testing.T) {
	w := NewSyncWorker(nil, nil, nil, time.Minute, 5000, false, 3, time.Second, nil)
	w.Stop()
}

func TestSyncWorker_Restart_BeforeStart(t *testing.T) {
	w := NewSyncWorker(nil, nil, nil, time.Minute, 5000, false, 3, time.Second, nil)
	w.Restart()
}

func newTestSyncEnv(t *testing.T) (*SyncWorker, *httptest.Server, *Store, func()) {
	t.Helper()

	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[],"success":true}`))
	}))

	client := torbox.NewClient("test-api-key")
	client.SetBaseURL(ts.URL)
	client.SetHTTPClient(&http.Client{})

	queue := throttle.NewQueue(99999)
	qCtx, qCancel := context.WithCancel(context.Background())
	queue.Start(qCtx)

	sw := NewSyncWorker(store, client, queue, time.Hour, 5000, false, 3, time.Second, nil)

	cleanup := func() {
		qCancel()
		ts.Close()
		store.Close()
	}

	return sw, ts, store, cleanup
}

func TestSyncWorker_StartStop_Lifecycle(t *testing.T) {
	w, _, _, cleanup := newTestSyncEnv(t)
	defer cleanup()

	swCtx, swCancel := context.WithCancel(context.Background())
	defer swCancel()

	done := make(chan struct{})
	go func() {
		w.Start(swCtx)
		close(done)
	}()

	// Wait for the first sync cycle to complete.
	deadline := time.Now().Add(10 * time.Second)
	for w.Status().LastSuccess.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for initial sync")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Stop the worker.
	stopDone := make(chan struct{})
	go func() {
		w.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not complete within 5s")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker goroutine did not exit after Stop")
	}
}

func newTestSyncEnvWithHandler(t *testing.T, handler http.HandlerFunc, retryAttempts int, retryBackoff time.Duration) (*SyncWorker, func()) {
	t.Helper()

	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(handler)

	client := torbox.NewClient("test-api-key")
	client.SetBaseURL(ts.URL)
	client.SetHTTPClient(&http.Client{})

	queue := throttle.NewQueue(99999)
	qCtx, qCancel := context.WithCancel(context.Background())
	queue.Start(qCtx)

	sw := NewSyncWorker(store, client, queue, time.Hour, 5000, false, retryAttempts, retryBackoff, nil)

	cleanup := func() {
		qCancel()
		ts.Close()
		store.Close()
	}

	return sw, cleanup
}

func TestSyncWorker_RetryOnTransientErrors(t *testing.T) {
	t.Run("retries succeed after transient failures", func(t *testing.T) {
		callCount := 0
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Fail on the first call (torrents attempt 0), succeed thereafter.
			if callCount < 1 {
				callCount++
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte("error code: 502"))
				return
			}
			callCount++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[],"success":true}`))
		})

		sw, cleanup := newTestSyncEnvWithHandler(t, handler, 1, 100*time.Millisecond)
		defer cleanup()

		sw.SyncNow()

		if sw.Status().LastError != "" {
			t.Fatalf("expected sync to succeed after retry, got error: %s", sw.Status().LastError)
		}
		if sw.Status().LastSuccess.IsZero() {
			t.Fatal("expected LastSuccess to be set after successful sync")
		}
	})

	t.Run("no retry means failure on first error", func(t *testing.T) {
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("error code: 502"))
		})

		sw, cleanup := newTestSyncEnvWithHandler(t, handler, 0, time.Second)
		defer cleanup()

		sw.SyncNow()

		if sw.Status().LastError == "" {
			t.Fatal("expected sync to fail when retry_attempts is 0")
		}
	})
}

func TestSyncWorker_Restart_Lifecycle(t *testing.T) {
	w, _, _, cleanup := newTestSyncEnv(t)
	defer cleanup()

	swCtx, swCancel := context.WithCancel(context.Background())
	defer swCancel()

	done := make(chan struct{})
	go func() {
		w.Start(swCtx)
		close(done)
	}()

	// Wait for the first sync cycle to complete.
	deadline := time.Now().Add(10 * time.Second)
	for w.Status().LastSuccess.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for initial sync")
		}
		time.Sleep(5 * time.Millisecond)
	}
	firstSync := w.Status().LastSuccess

	// Restart the worker.
	restartDone := make(chan struct{})
	go func() {
		w.Restart()
		close(restartDone)
	}()

	select {
	case <-restartDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Restart did not complete within 10s")
	}

	// Wait for the restarted loop to complete at least one sync cycle.
	syncDeadline := time.Now().Add(10 * time.Second)
	for {
		if w.Status().LastSuccess.After(firstSync) {
			break
		}
		if time.Now().After(syncDeadline) {
			t.Fatal("timed out waiting for restarted sync")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Stop the restarted worker.
	stopDone := make(chan struct{})
	go func() {
		w.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop after Restart did not complete within 5s")
	}
}

func TestBuildFileRecordWithTags(t *testing.T) {
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/Cow and Chicken/episode.avi",
		ShortName: "episode.avi",
	}
	overrideTags := map[string]bool{"forcedtv": true}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"forcedtv"}, overrideTags, "")

	if rec.FilterTags != "forcedtv" {
		t.Errorf("FilterTags = %q, want %q", rec.FilterTags, "forcedtv")
	}
	// Virtual path must remain unchanged (derived from S3 path only).
	if rec.Path != "Cow and Chicken/episode.avi" {
		t.Errorf("Path = %q, want %q (path must not include tags)", rec.Path, "Cow and Chicken/episode.avi")
	}
}

func TestBuildFileRecordEmptyTags(t *testing.T) {
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/Movie Name/movie.mkv",
		ShortName: "movie.mkv",
	}
	overrideTags := map[string]bool{"forcedtv": true}

	// nil tags
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", nil, overrideTags, "")
	if rec.FilterTags != "" {
		t.Errorf("nil tags: FilterTags = %q, want empty", rec.FilterTags)
	}

	// empty tags slice
	rec = buildFileRecord(1, f, 1, SourceTorrent, "", []string{}, overrideTags, "")
	if rec.FilterTags != "" {
		t.Errorf("empty tags: FilterTags = %q, want empty", rec.FilterTags)
	}
}

func TestBuildFileRecordTagNotInOverrideList(t *testing.T) {
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/Some Show/ep.mkv",
		ShortName: "ep.mkv",
	}
	overrideTags := map[string]bool{"forcedtv": true}

	// Tag "comedy" is not in the override list — should be ignored.
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"comedy", "drama"}, overrideTags, "")
	if rec.FilterTags != "" {
		t.Errorf("non-override tags: FilterTags = %q, want empty", rec.FilterTags)
	}

	// Mix of override and non-override tags — only override tag is stored.
	rec = buildFileRecord(1, f, 1, SourceTorrent, "", []string{"comedy", "forcedtv", "drama"}, overrideTags, "")
	if rec.FilterTags != "forcedtv" {
		t.Errorf("mixed tags: FilterTags = %q, want %q", rec.FilterTags, "forcedtv")
	}
}

func TestBuildFileRecordTagCaseInsensitive(t *testing.T) {
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/Some Show/ep.mkv",
		ShortName: "ep.mkv",
	}
	overrideTags := map[string]bool{"forcedtv": true}

	// Tags with different casing should still match.
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"ForcedTV"}, overrideTags, "")
	if rec.FilterTags == "" {
		t.Error("case-insensitive tag should match override list")
	}
}

func TestBuildFileRecordWithRenameTag(t *testing.T) {
	// Multi-file torrent: rename replaces top-level directory only
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/Cow and Chicken/episode.avi",
		ShortName: "episode.avi",
	}
	overrideTags := map[string]bool{"rename": true}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"rename"}, overrideTags, "Cow and Chicken S01-04")

	if rec.Path != "Cow and Chicken S01-04/episode.avi" {
		t.Errorf("Path = %q, want %q", rec.Path, "Cow and Chicken S01-04/episode.avi")
	}
}

func TestBuildFileRecordRenamePreservesSubdirs(t *testing.T) {
	// Nested structure: only top-level dir is replaced
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/OldName/Season 1/ep01.mkv",
		ShortName: "ep01.mkv",
	}
	overrideTags := map[string]bool{"rename": true}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"rename"}, overrideTags, "Better Name")

	if rec.Path != "Better Name/Season 1/ep01.mkv" {
		t.Errorf("Path = %q, want %q", rec.Path, "Better Name/Season 1/ep01.mkv")
	}
}

func TestBuildFileRecordNoRenameTagNoChange(t *testing.T) {
	// Without rename tag: itemName is ignored, path comes from S3
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/Original Name/movie.mkv",
		ShortName: "movie.mkv",
	}
	overrideTags := map[string]bool{"forcedtv": true}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"forcedtv"}, overrideTags, "Different Dashboard Name")

	if rec.Path != "Original Name/movie.mkv" {
		t.Errorf("Path = %q, want %q (rename tag absent, should use S3 path)", rec.Path, "Original Name/movie.mkv")
	}
}

func TestBuildFileRecordRenameSingleFile(t *testing.T) {
	// Single-file torrent: wraps in named directory
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/movie.mkv",
		ShortName: "movie.mkv",
	}
	overrideTags := map[string]bool{"rename": true}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"rename"}, overrideTags, "My Movie 2024")

	if rec.Path != "My Movie 2024/movie.mkv" {
		t.Errorf("Path = %q, want %q", rec.Path, "My Movie 2024/movie.mkv")
	}
}

func TestBuildFileRecordWithForcedMovieTag(t *testing.T) {
	f := torbox.TorrentFile{
		ID:        10,
		S3Path:    "abc123/Some.Movie.S01.2024/movie.mkv",
		ShortName: "movie.mkv",
	}
	overrideTags := map[string]bool{"forcedmovie": true}
	rec := buildFileRecord(1, f, 1, SourceTorrent, "", []string{"forcedmovie"}, overrideTags, "")

	if rec.FilterTags != "forcedmovie" {
		t.Errorf("FilterTags = %q, want %q", rec.FilterTags, "forcedmovie")
	}
	// Virtual path must remain unchanged (derived from S3 path only).
	if rec.Path != "Some.Movie.S01.2024/movie.mkv" {
		t.Errorf("Path = %q, want %q (path must not include tags)", rec.Path, "Some.Movie.S01.2024/movie.mkv")
	}
}