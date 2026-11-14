package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Allifreyr/warpbox/internal/metadata"
	"github.com/Allifreyr/warpbox/internal/throttle"
	"github.com/Allifreyr/warpbox/internal/torbox"
)

const quotaResponse = `{"success":false,"error":"DAILY_BANDWIDTH_LIMIT_EXCEEDED","data":{"daily_bandwidth_limit":3298534883328,"daily_bandwidth_usage":3601475426665}}`

type quotaClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []struct {
		at time.Time
		ch chan time.Time
	}
}

func newQuotaClock(q *quotaController) *quotaClock {
	c := &quotaClock{now: time.Now()}
	q.now = c.Now
	q.after = c.After
	return c
}
func (c *quotaClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *quotaClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.timers = append(c.timers, struct {
			at time.Time
			ch chan time.Time
		}{c.now.Add(d), ch})
	}
	return ch
}
func (c *quotaClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var pending []struct {
		at time.Time
		ch chan time.Time
	}
	for _, timer := range c.timers {
		if !timer.at.After(c.now) {
			timer.ch <- c.now
		} else {
			pending = append(pending, timer)
		}
	}
	c.timers = pending
}
func (c *quotaClock) Pending() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.timers) }

func eventuallyQuota(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for quota test condition")
		}
		time.Sleep(time.Millisecond)
	}
}
func quotaRefusal() *torbox.APIError {
	return &torbox.APIError{StatusCode: 429, Code: torbox.DailyBandwidthLimitExceeded}
}

func TestQuotaControllerBackoffOneProbeAndIdle(t *testing.T) {
	q := newQuotaController()
	defer q.close()
	clock := newQuotaClock(q)
	q.observe(quotaRefusal())
	clock.Advance(5 * time.Minute)
	if q.snapshot().Probing {
		t.Fatal("idle controller made a probe")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var checks atomic.Int64
	var recovered atomic.Bool
	results := make(chan error, 24)
	probe := func(context.Context) (string, error) {
		checks.Add(1)
		if recovered.Load() {
			return "https://cdn.example/recovered", nil
		}
		return "", quotaRefusal()
	}
	for i := 0; i < 24; i++ {
		go func() { _, err := q.wait(ctx, probe); results <- err }()
	}
	for i, delay := range []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		wantChecks := int64(i + 1)
		eventuallyQuota(t, func() bool {
			snap := q.snapshot()
			return checks.Load() == wantChecks && !snap.Probing && snap.Next.Equal(clock.Now().Add(delay))
		})
		if i < 3 {
			clock.Advance(delay)
		}
	}
	recovered.Store(true)
	clock.Advance(30 * time.Minute)
	for i := 0; i < 24; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("readers did not resume")
		}
	}
	if checks.Load() != 5 || q.snapshot().Paused {
		t.Fatalf("checks=%d state=%+v", checks.Load(), q.snapshot())
	}
	if newQuotaController().snapshot().Paused {
		t.Fatal("quota state persisted into a new controller")
	}
}

func TestQuotaControllerProviderTimingAndFailedCandidate(t *testing.T) {
	q := newQuotaController()
	defer q.close()
	clock := newQuotaClock(q)
	e := quotaRefusal()
	e.RetryAt = clock.Now().Add(time.Hour)
	q.observe(e)
	if !q.snapshot().Next.Equal(e.RetryAt) {
		t.Fatal(q.snapshot())
	}
	clock.Advance(time.Hour)
	wantErr := errors.New("missing file")
	_, err := q.wait(context.Background(), func(context.Context) (string, error) { return "", wantErr })
	if !errors.Is(err, wantErr) || !q.snapshot().Paused || q.snapshot().Probing {
		t.Fatal(err, q.snapshot())
	}
	clock.Advance(5 * time.Minute)
	u, err := q.wait(context.Background(), func(context.Context) (string, error) { return "replacement", nil })
	if err != nil || u != "replacement" || q.snapshot().Paused {
		t.Fatal(u, err, q.snapshot())
	}
}

func TestQuotaProbeCancellationAndShutdownReleaseOwnership(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			q := newQuotaController()
			defer q.close()
			clock := newQuotaClock(q)
			q.observe(quotaRefusal())
			clock.Advance(5 * time.Minute)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, err := q.wait(ctx, func(ctx context.Context) (string, error) { close(entered); <-ctx.Done(); return "", ctx.Err() })
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("probe did not start")
			}
			if shutdown {
				q.close()
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("probe was stranded")
			}
			// Reader cancellation does not own the account's bounded check.
			// Server shutdown must still cancel it and release ownership.
			q.close()
			eventuallyQuota(t, func() bool { return !q.snapshot().Probing })
		})
	}
}

func newQuotaPlaybackServer(t *testing.T, handler http.HandlerFunc) (*Server, *throttle.Limiter) {
	t.Helper()
	// SQLite :memory: creates a separate database for each pooled connection.
	// Concurrent readers need a real WAL database, as in production.
	store, err := metadata.Open(t.TempDir() + "/playback.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := New(Config{CDNTtlMinutes: 120, CDNURLRetryCount: 2, CDNURLAutoRepair: true, CDNURLRepairRetries: 2,
		NegativeCacheTTLSeconds: 30, CircuitBreakerFailures: 5, CircuitBreakerWindowSec: 600, CircuitBreakerStaleMin: 5}, store, nil, nil)
	api := httptest.NewServer(handler)
	l := throttle.NewLimiter(1000000)
	c := torbox.NewClient("private-test-key")
	c.SetBaseURL(api.URL)
	c.SetRequestHooks(l.BeginRequest, l.EndRequest)
	c.HTTP429Callback = l.Record429
	s.torBox = c
	s.queue = throttle.NewExecutionQueue(1000000, l)
	s.cfg.APIStats = l.RequestStats
	ctx, cancel := context.WithCancel(context.Background())
	s.queue.Start(ctx)
	t.Cleanup(func() { cancel(); s.StopCleanup(); api.Close() })
	return s, l
}

func quotaTestFile(t *testing.T, s *Server, id int64) *metadata.FileRecord {
	t.Helper()
	p := fmt.Sprintf("Show/file%d.mkv", id)
	if err := s.store.UpsertFile(metadata.FileRecord{Source: metadata.SourceTorrent, ItemID: id, FileID: 0, Path: p, Name: fmt.Sprintf("file%d.mkv", id), Size: 4, MimeType: "video/x-matroska"}); err != nil {
		t.Fatal(err)
	}
	f, err := s.store.GetFileByPath(p)
	if err != nil || f == nil {
		t.Fatal(f, err)
	}
	return f
}

func TestQuotaBlocksConcurrentLinkRequestsWithoutPoisoningFiles(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "requestdl") {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			w.WriteHeader(429)
			io.WriteString(w, quotaResponse)
			return
		}
		io.WriteString(w, `{"success":true,"data":{"id":1}}`)
	})
	file := quotaTestFile(t, s, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 32)
	go func() { _, err := s.fetchWithFallback(ctx, file); results <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var started sync.WaitGroup
	started.Add(31)
	for i := 0; i < 31; i++ {
		go func(id int64) {
			started.Done()
			_, err := s.fetchCDNURL(ctx, metadata.SourceTorrent, id, 0)
			results <- err
		}(int64(i + 2))
	}
	started.Wait()
	close(release)
	for i := 0; i < 32; i++ {
		select {
		case err := <-results:
			if !isQuotaPause(err) {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if calls.Load() != 1 || len(s.negativeCache) != 0 || len(s.torrentFailures) != 0 {
		t.Fatalf("calls=%d negative=%d failures=%d", calls.Load(), len(s.negativeCache), len(s.torrentFailures))
	}
	if got := l.RequestStats(); got.TotalCalls != 1 || got.HTTP429Calls != 1 {
		t.Fatal(got)
	}
	if _, err := s.torBox.GetTorrent(ctx, 1); err != nil {
		t.Fatal("quota blocked metadata", err)
	}
	for _, tt := range []struct {
		method, path string
		status       int
	}{{"HEAD", "/webdav/__all__/Show/file1.mkv", 200}, {"PROPFIND", "/webdav/__all__/Show/", 207}, {"GET", "/http/__all__/Show/", 200}, {"GET", "/", 200}} {
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
		if w.Code != tt.status {
			t.Fatalf("%s %s: %d", tt.method, tt.path, w.Code)
		}
		if tt.path == "/" && (!strings.Contains(w.Body.String(), "Paused by TorBox") || !strings.Contains(w.Body.String(), "Last reported usage")) {
			t.Fatal(w.Body.String())
		}
	}
	if l.RequestStats().TotalCalls != 2 {
		t.Fatal("cached operations contacted TorBox", l.RequestStats())
	}
}

func TestQuotaWaitingReadFlushesHeadersAndResumesFromOneProbe(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-3" {
			t.Error("range changed", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(206)
		io.WriteString(w, "data")
	}))
	defer cdn.Close()
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL) })
	file := quotaTestFile(t, s, 1)
	s.cacheCDNURL(file, cdn.URL)
	clock := newQuotaClock(s.quota)
	s.quota.observe(quotaRefusal())
	web := httptest.NewServer(s.mux)
	defer web.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", web.URL+"/webdav/__all__/Show/file1.mkv", nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("waiting headers were not flushed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 || resp.ContentLength != 4 || l.RequestStats().TotalCalls != 0 || len(s.cdnSem) != cap(s.cdnSem) {
		t.Fatal(resp.StatusCode, resp.ContentLength, l.RequestStats(), len(s.cdnSem))
	}
	clock.Advance(5 * time.Minute)
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "data" {
		t.Fatalf("bad recovered media: %q %v", body, err)
	}
	if got := l.RequestStats(); got.TotalCalls != 1 || got.SuccessfulCalls != 1 {
		t.Fatal("probe URL not reused", got)
	}
}

func TestQuotaPausesCachedRedirectAndShutdownCancelsReaders(t *testing.T) {
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"success":true,"data":"https://cdn.example/new"}`)
	})
	file := quotaTestFile(t, s, 1)
	s.cacheCDNURL(file, "https://cdn.example/old")
	clock := newQuotaClock(s.quota)
	s.quota.observe(quotaRefusal())
	web := httptest.NewServer(s.mux)
	defer web.Close()
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	result := make(chan *http.Response, 1)
	errs := make(chan error, 1)
	go func() {
		resp, err := client.Get(web.URL + "/webdav/__all__/Show/file1.mkv")
		if err != nil {
			errs <- err
		} else {
			result <- resp
		}
	}()
	eventuallyQuota(t, func() bool { return clock.Pending() > 0 })
	select {
	case resp := <-result:
		resp.Body.Close()
		t.Fatal("redirect escaped quota")
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	clock.Advance(5 * time.Minute)
	select {
	case resp := <-result:
		resp.Body.Close()
		if resp.StatusCode != 302 || resp.Header.Get("Location") != "https://cdn.example/new" {
			t.Fatal(resp)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("redirect did not resume")
	}
	if l.RequestStats().TotalCalls != 1 {
		t.Fatal(l.RequestStats())
	}
	s.quota.observe(quotaRefusal())
	req, _ := http.NewRequest("GET", web.URL+"/webdav/__all__/Show/file1.mkv", nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(resp.Body); len(body) != 0 {
		t.Fatal("shutdown streamed fake media", string(body))
	}
	if l.RequestStats().TotalCalls != 1 {
		t.Fatal("shutdown made another probe", l.RequestStats())
	}
}

func TestQuotaConcurrentReadersReuseProbeURL(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(206)
		io.WriteString(w, "data")
	}))
	defer cdn.Close()
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL) })
	file := quotaTestFile(t, s, 1)
	clock := newQuotaClock(s.quota)
	s.quota.observe(quotaRefusal())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		go func() {
			r := httptest.NewRequest("GET", "/webdav/__all__/Show/file1.mkv", nil).WithContext(ctx)
			r.Header.Set("Range", "bytes=0-3")
			w := httptest.NewRecorder()
			s.streamFileContent(w, r, file)
			if w.Body.String() != "data" {
				results <- fmt.Errorf("bad media %q", w.Body.String())
			} else {
				results <- nil
			}
		}()
	}
	eventuallyQuota(t, func() bool { return clock.Pending() >= 12 })
	clock.Advance(5 * time.Minute)
	for i := 0; i < 12; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if got := l.RequestStats(); got.TotalCalls != 1 {
		t.Fatal("readers refetched the probe URL", got)
	}
}

func TestQuotaUncachedFullReadWaitsAsStream(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(206)
		io.WriteString(w, "data")
	}))
	defer cdn.Close()
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL) })
	quotaTestFile(t, s, 1)
	clock := newQuotaClock(s.quota)
	s.quota.observe(quotaRefusal())
	web := httptest.NewServer(s.mux)
	defer web.Close()
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(web.URL + "/webdav/__all__/Show/file1.mkv")
	if err != nil {
		t.Fatal("uncached full read did not flush waiting headers", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || l.RequestStats().TotalCalls != 0 {
		t.Fatal(resp.StatusCode, l.RequestStats())
	}
	clock.Advance(5 * time.Minute)
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "data" {
		t.Fatal(string(body), err)
	}
	if l.RequestStats().TotalCalls != 1 {
		t.Fatal(l.RequestStats())
	}
}

func TestQuotaProbeSurvivesInitiatingReaderDisconnect(t *testing.T) {
	q := newQuotaController()
	defer q.close()
	clock := newQuotaClock(q)
	q.observe(quotaRefusal())
	clock.Advance(5 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	first := make(chan error, 1)
	go func() {
		_, err := q.wait(ctx, func(ctx context.Context) (string, error) {
			close(entered)
			select {
			case <-release:
				return "url", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader cancellation stalled")
	}
	// Let a different reader use the account's pending result rather than
	// waiting another five minutes or dispatching its own check.
	live, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	second := make(chan error, 1)
	go func() {
		_, err := q.wait(live, func(context.Context) (string, error) {
			t.Error("duplicate quota probe")
			return "", errors.New("unexpected")
		})
		second <- err
	}()
	// Release without closing twice in the deferred cleanup.
	release <- struct{}{}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-live.Done():
		t.Fatal(live.Err())
	}
	if q.snapshot().Paused {
		t.Fatal("successful shared probe did not resume account")
	}
}

func TestQuotaProbeDeadlineRemainsBoundedWithoutReaders(t *testing.T) {
	q := newQuotaController()
	defer q.close()
	q.probeTimeout = 20 * time.Millisecond
	clock := newQuotaClock(q)
	q.observe(quotaRefusal())
	clock.Advance(5 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := q.wait(ctx, func(ctx context.Context) (string, error) { close(entered); <-ctx.Done(); return "", ctx.Err() })
		done <- err
	}()
	<-entered
	cancel()
	<-done
	eventuallyQuota(t, func() bool { return !q.snapshot().Probing })
	if !q.snapshot().Paused || !q.snapshot().Next.After(clock.Now()) {
		t.Fatal("timed-out probe cleared quota", q.snapshot())
	}
}
