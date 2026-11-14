package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Allifreyr/warpbox/internal/metadata"
	"github.com/Allifreyr/warpbox/internal/throttle"
	"github.com/Allifreyr/warpbox/internal/torbox"
)

func TestHTTPBudgetCoversPagesRetriesPlaybackAndStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var mu sync.Mutex
	var offsets []string
	retried := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "mylist") && r.URL.Query().Get("id") == "":
			offset := r.URL.Query().Get("offset")
			mu.Lock()
			offsets = append(offsets, offset)
			fail := offset == "1" && !retried
			if fail {
				retried = true
			}
			mu.Unlock()
			if fail {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"DATABASE_ERROR"}`)
				return
			}
			if offset == "2" {
				fmt.Fprint(w, `{"data":[],"success":true}`)
			} else {
				fmt.Fprint(w, `{"data":[{"id":1}],"success":true}`)
			}
		case strings.HasSuffix(r.URL.Path, "mylist"):
			fmt.Fprint(w, `{"data":{"id":1,"name":"item"},"success":true}`)
		case strings.HasSuffix(r.URL.Path, "requestdl"):
			fmt.Fprint(w, `{"data":"https://cdn.example/video","success":true}`)
		default:
			fmt.Fprint(w, `{"data":{},"success":true}`)
		}
	}))
	defer api.Close()
	limiter := throttle.NewLimiter(1000) // 60ms slots allow scheduler tolerance.
	client := torbox.NewClient("key")
	client.SetBaseURL(api.URL)
	var admissions []time.Time
	client.SetRequestHooks(func(ctx context.Context) error {
		if err := limiter.BeginRequest(ctx); err != nil {
			return err
		}
		mu.Lock()
		admissions = append(admissions, time.Now())
		mu.Unlock()
		return nil
	}, limiter.EndRequest)
	client.HTTP429Callback = limiter.Record429
	playback := throttle.NewExecutionQueue(1000, limiter)
	syncQueue := throttle.NewExecutionQueue(1000, limiter)
	playback.Start(ctx)
	syncQueue.Start(ctx)
	errors := make(chan error, 5)
	syncQueue.Enqueue(throttle.Request{Execute: func(ctx context.Context) error {
		_, err := client.ListTorrents(ctx, torbox.ListFilesParams{PageSize: 1, RetryAttempts: 1, RetryBackoff: time.Millisecond})
		errors <- err
		return err
	}})
	playback.Enqueue(throttle.Request{Execute: func(ctx context.Context) error {
		_, err := client.GetDownloadURL(ctx, 1, 0, false)
		errors <- err
		return err
	}})
	playback.Enqueue(throttle.Request{Execute: func(ctx context.Context) error {
		_, err := client.GetUsenetDownloadURL(ctx, 1, 0, false)
		errors <- err
		return err
	}})
	syncQueue.Enqueue(throttle.Request{Execute: func(ctx context.Context) error {
		_, err := client.GetTorrent(ctx, 1)
		if err == nil {
			_, err = client.GetUsenet(ctx, 1)
		}
		errors <- err
		return err
	}})
	// Startup uses the client directly and must share the exact same budget.
	go func() { _, err := client.GetUserInfo(ctx); errors <- err }()
	for i := 0; i < 5; i++ {
		select {
		case err := <-errors:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(offsets, []string{"0", "1", "1", "2"}) {
		t.Fatal("pagination/retry changed", offsets)
	}
	if len(admissions) != 9 {
		t.Fatal("missing individual admissions", len(admissions))
	}
	sort.Slice(admissions, func(i, j int) bool { return admissions[i].Before(admissions[j]) })
	for i := 1; i < len(admissions); i++ {
		if gap := admissions[i].Sub(admissions[i-1]); gap < 40*time.Millisecond {
			t.Fatalf("shared request spacing violated: %v", gap)
		}
	}
	stats := limiter.RequestStats()
	if stats.TotalCalls != 9 || stats.SuccessfulCalls != 8 || stats.FailedCalls != 1 || stats.CallsLastMinute != 9 {
		t.Fatal(stats)
	}
}

func TestCachedBrowsingAndPlaybackDoNotUseTorBoxBudget(t *testing.T) {
	l := throttle.NewLimiter(250)
	s := testServer(t, Config{APIStats: l.RequestStats})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("cached request reached TorBox")
		w.WriteHeader(500)
	}))
	defer api.Close()
	c := torbox.NewClient("key")
	c.SetBaseURL(api.URL)
	c.SetRequestHooks(l.BeginRequest, l.EndRequest)
	s.torBox = c
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Content-Range", "bytes 0-3/4")
		w.WriteHeader(206)
		fmt.Fprint(w, "data")
	}))
	defer cdn.Close()
	if err := s.store.UpsertFile(metadata.FileRecord{ItemID: 1, FileID: 0, Name: "episode.mkv", Path: "Show/episode.mkv", Size: 4, MimeType: "video/x-matroska"}); err != nil {
		t.Fatal(err)
	}
	f, err := s.store.GetFileByPath("Show/episode.mkv")
	if err != nil || f == nil {
		t.Fatal(f, err)
	}
	if err := s.store.SetCDNURL(f.ID, cdn.URL, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{"PROPFIND", "/webdav/__all__/Show/", 207}, {"HEAD", "/webdav/__all__/Show/episode.mkv", 200},
		{"GET", "/http/__all__/Show/", 200}, {"GET", "/webdav/__all__/Show/episode.mkv", 206},
	} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.Header.Set("Range", "bytes=0-3")
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		if w.Code != tt.status {
			t.Fatalf("%s %s: %d %s", tt.method, tt.path, w.Code, w.Body.String())
		}
	}
	if stats := l.RequestStats(); stats.TotalCalls != 0 {
		t.Fatal("cached paths consumed API budget", stats)
	}
}

func TestRequestPacingDoesNotHoldPlaybackBehindSyncResponse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "mylist") {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"data":[],"success":true}`)
		} else {
			fmt.Fprint(w, `{"data":"https://cdn.example/video","success":true}`)
		}
	}))
	defer api.Close()
	defer close(release)
	l := throttle.NewLimiter(3000)
	c := torbox.NewClient("key")
	c.SetBaseURL(api.URL)
	c.SetRequestHooks(l.BeginRequest, l.EndRequest)
	q1, q2 := throttle.NewExecutionQueue(3000, l), throttle.NewExecutionQueue(3000, l)
	q1.Start(ctx)
	q2.Start(ctx)
	doneSync, donePlay := make(chan error, 1), make(chan error, 1)
	q1.Enqueue(throttle.Request{Execute: func(ctx context.Context) error {
		_, err := c.ListUsenet(ctx, torbox.ListFilesParams{})
		doneSync <- err
		return err
	}})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	q2.Enqueue(throttle.Request{Execute: func(ctx context.Context) error {
		_, err := c.GetDownloadURL(ctx, 1, 0, false)
		donePlay <- err
		return err
	}})
	select {
	case err := <-donePlay:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("playback blocked by active sync response")
	}
	select {
	case <-doneSync:
		t.Fatal("sync was expected to remain blocked")
	default:
	}
}

func TestDashboardUsesHTTPAttemptsWithoutDoubleCounting(t *testing.T) {
	l := throttle.NewLimiter(1000000)
	s := testServer(t, Config{APIStats: l.RequestStats})
	s.SetSyncQueue(throttle.NewExecutionQueue(1000000, l))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"RATE_LIMITED"}`)
	}))
	defer api.Close()
	c := torbox.NewClient("key")
	c.SetBaseURL(api.URL)
	c.SetRequestHooks(l.BeginRequest, l.EndRequest)
	c.HTTP429Callback = l.Record429
	if _, err := c.GetUserInfo(context.Background()); err == nil {
		t.Fatal("expected HTTP failure")
	}
	if got := s.throttleStats(); got.TotalCalls != 1 || got.FailedCalls != 1 || got.HTTP429Calls != 1 {
		t.Fatal(got)
	}
	s.recordStats()
	s.recordStats()
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest("GET", "/stats.json", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var series map[string][]struct {
		V float64 `json:"v"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &series); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api_calls_failed", "api_calls_429"} {
		points := series[name]
		if len(points) != 2 || points[0].V != 1 || points[1].V != 0 {
			t.Fatalf("%s: %+v", name, points)
		}
	}
}
