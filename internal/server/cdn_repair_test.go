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
)

const invalidTokenHTML = `<html><title>TorBox Satellite</title><h1>Invalid Presigned Token</h1><p>This link is invalid. Please get a new download link from TorBox.</p></html>`

func TestInvalidTokenClassifierIsNarrowAndBounded(t *testing.T) {
	for _, tt := range []struct {
		body string
		want bool
	}{
		{invalidTokenHTML, true}, {`<H1> INVALID <span>Presigned</span> &#84;oken </H1>`, true},
		{`{"error":"INVALID_PRESIGNED_TOKEN"}`, true}, {`{"message":"Invalid Presigned Token"}`, true},
		{"Invalid Presigned Token: get a new link", true}, {"invalid token", false},
		{`<p>For Invalid Presigned Token errors, see the help page.</p><h1>Bad Range</h1>`, false},
		{`{"error":"BAD_RANGE","detail":"Invalid Presigned Token"}`, false}, {`<h1>Expired token</h1>`, false},
	} {
		if got := invalidPresignedToken([]byte(tt.body)); got != tt.want {
			t.Errorf("%q: %t", tt.body, got)
		}
	}
	fileServer, _ := newQuotaPlaybackServer(t, func(http.ResponseWriter, *http.Request) { t.Error("inspection contacted API") })
	file := quotaTestFile(t, fileServer, 1)
	for _, body := range []io.ReadCloser{io.NopCloser(strings.NewReader(strings.Repeat("x", cdnErrorBodyLimit) + invalidTokenHTML)), brokenCDNErrorBody{}} {
		resp := &http.Response{StatusCode: 400, Header: make(http.Header), Body: body}
		req := httptest.NewRequest("GET", "https://cdn.example/private-token?token=secret", nil)
		if inspectCDN400(resp, req, file) {
			t.Error("truncated or unreadable error triggered repair")
		}
		body.Close()
	}
}

type brokenCDNErrorBody struct{}

func (brokenCDNErrorBody) Read([]byte) (int, error) { return 0, errors.New("unreadable") }
func (brokenCDNErrorBody) Close() error             { return nil }

func TestCDNInvalidTokenRecoversInStreamAndHang(t *testing.T) {
	for _, hang := range []bool{false, true} {
		for _, jsonBody := range []bool{false, true} {
			t.Run(fmt.Sprintf("hang=%t/json=%t", hang, jsonBody), func(t *testing.T) {
				var dataCalls atomic.Int64
				cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					dataCalls.Add(1)
					if r.Header.Get("Range") != "bytes=0-3" {
						t.Error("range changed", r.Header.Get("Range"))
					}
					if r.URL.Path == "/old" {
						w.Header().Set("Content-Type", "text/html")
						w.WriteHeader(400)
						if jsonBody {
							io.WriteString(w, `{"error":"INVALID_PRESIGNED_TOKEN"}`)
						} else {
							io.WriteString(w, invalidTokenHTML)
						}
						return
					}
					w.Header().Set("Content-Type", "video/x-matroska")
					w.WriteHeader(206)
					io.WriteString(w, "data")
				}))
				defer cdn.Close()
				var urls atomic.Int64
				s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
					n := urls.Add(1)
					path := "/new"
					if hang && n == 1 {
						path = "/old"
					}
					fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL+path)
				})
				file := quotaTestFile(t, s, 1)
				if !hang {
					s.cacheCDNURL(file, cdn.URL+"/old")
				}
				req := httptest.NewRequest("GET", "/webdav/__all__/Show/file1.mkv", nil)
				req.Header.Set("Range", "bytes=0-3")
				w := httptest.NewRecorder()
				if hang {
					s.handleGetCDNHang(w, req, file)
				} else {
					s.streamFileContent(w, req, file)
				}
				if w.Code != 206 || w.Body.String() != "data" {
					t.Fatal(w.Code, w.Body.String())
				}
				want := int64(1)
				if hang {
					want = 2
				}
				if l.RequestStats().TotalCalls != want || dataCalls.Load() != 2 || len(s.cdnSem) != cap(s.cdnSem) {
					t.Fatal(l.RequestStats(), dataCalls.Load(), len(s.cdnSem))
				}
				if got, _ := s.store.GetCDNURL(file.ID); got != cdn.URL+"/new" {
					t.Fatal("fresh URL not cached", got)
				}
				if len(s.torrentFailures) != 0 {
					t.Fatal("invalid token marked item stale")
				}
			})
		}
	}
}

func TestCDNTokenRepairHonorsControlsAndExhaustion(t *testing.T) {
	for _, tt := range []struct {
		name        string
		enabled     bool
		budget      int
		body        string
		wantCalls   int64
		invalidated bool
	}{
		{"bounded", true, 2, invalidTokenHTML, 2, true},
		{"disabled", false, 2, invalidTokenHTML, 0, false},
		{"zero", true, 0, invalidTokenHTML, 0, true},
		{"unknown", true, 2, `{"error":"BAD_RANGE"}`, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var dataCalls atomic.Int64
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				dataCalls.Add(1)
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(400)
				io.WriteString(w, tt.body)
			}))
			defer cdn.Close()
			s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL) })
			s.cfg.CDNURLAutoRepair = tt.enabled
			s.cfg.CDNURLRepairRetries = tt.budget
			file := quotaTestFile(t, s, 1)
			s.cacheCDNURL(file, cdn.URL)
			r := httptest.NewRequest("GET", "/webdav/__all__/Show/file1.mkv", nil)
			r.Header.Set("Range", "bytes=0-3")
			w := httptest.NewRecorder()
			s.streamFileContent(w, r, file)
			if w.Code != 502 || l.RequestStats().TotalCalls != tt.wantCalls || dataCalls.Load() != tt.wantCalls+1 {
				t.Fatal(w.Code, l.RequestStats(), dataCalls.Load())
			}
			cached, _ := s.store.GetCDNURL(file.ID)
			if (cached == "") != tt.invalidated {
				t.Fatal("wrong cache state", cached)
			}
			if (len(s.negativeCache) > 0) != tt.invalidated || len(s.torrentFailures) != 0 {
				t.Fatal("wrong failure classification", len(s.negativeCache), len(s.torrentFailures))
			}
		})
	}
}

func TestConcurrentCDNRepairsCoalesceAndPreserveReplacement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		io.WriteString(w, `{"success":true,"data":"https://cdn.example/new"}`)
	})
	file := quotaTestFile(t, s, 1)
	s.cacheCDNURL(file, "https://cdn.example/old")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan error, 32)
	var started sync.WaitGroup
	started.Add(32)
	for i := 0; i < 32; i++ {
		go func() {
			started.Done()
			u, err := s.refreshCDNURL(ctx, file, "https://cdn.example/old")
			if err == nil && u != "https://cdn.example/new" {
				err = fmt.Errorf("wrong URL %q", u)
			}
			results <- err
		}()
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	started.Wait()
	close(release)
	for i := 0; i < 32; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if calls.Load() != 1 || l.RequestStats().TotalCalls != 1 {
		t.Fatal(calls.Load(), l.RequestStats())
	}
	s.exhaustedTokenRepair(file, "https://cdn.example/old")
	if u, _ := s.store.GetCDNURL(file.ID); u != "https://cdn.example/new" || len(s.negativeCache) != 0 {
		t.Fatal("late failure poisoned replacement", u)
	}
	if len(s.repairs) != 0 {
		t.Fatal("repair flight leaked")
	}
}

func TestCDNRepairCancelledLeaderAllowsLiveFollower(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int64
	s, _ := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		io.WriteString(w, `{"success":true,"data":"https://cdn.example/new"}`)
	})
	file := quotaTestFile(t, s, 1)
	s.cacheCDNURL(file, "https://cdn.example/old")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := s.refreshCDNURL(ctx, file, "https://cdn.example/old"); first <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("leader did not start")
	}
	live, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	second := make(chan error, 1)
	go func() {
		u, err := s.refreshCDNURL(live, file, "https://cdn.example/old")
		if err == nil && u != "https://cdn.example/new" {
			err = errors.New("bad replacement")
		}
		second <- err
	}()
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-live.Done():
		t.Fatal(live.Err())
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-live.Done():
		t.Fatal(live.Err())
	}
	if calls.Load() != 2 || len(s.repairs) != 0 {
		t.Fatal(calls.Load(), len(s.repairs))
	}
}

func TestQuotaDuringTokenRepairWaitsWithoutStreamingErrorPage(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			w.WriteHeader(400)
			io.WriteString(w, invalidTokenHTML)
			return
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(206)
		io.WriteString(w, "data")
	}))
	defer cdn.Close()
	var calls atomic.Int64
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			io.WriteString(w, quotaResponse)
			return
		}
		fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL+"/new")
	})
	file := quotaTestFile(t, s, 1)
	s.cacheCDNURL(file, cdn.URL+"/old")
	clock := newQuotaClock(s.quota)
	web := httptest.NewServer(s.mux)
	defer web.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", web.URL+"/webdav/__all__/Show/file1.mkv", nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 206 || !s.quota.snapshot().Paused || len(s.cdnSem) != cap(s.cdnSem) {
		t.Fatal(resp.StatusCode, s.quota.snapshot(), len(s.cdnSem))
	}
	clock.Advance(5 * time.Minute)
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "data" {
		t.Fatalf("error page streamed or recovery failed: %q %v", body, err)
	}
	if l.RequestStats().TotalCalls != 2 || l.RequestStats().HTTP429Calls != 1 || len(s.torrentFailures) != 0 || len(s.negativeCache) != 0 {
		t.Fatal(l.RequestStats(), len(s.torrentFailures), len(s.negativeCache))
	}
}

func TestTokenRepairBudgetSurvivesHangHandoff(t *testing.T) {
	oldPoll := cdnPollInterval
	cdnPollInterval = 10 * time.Millisecond
	defer func() { cdnPollInterval = oldPoll }()
	var calls atomic.Int64
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(400)
		io.WriteString(w, invalidTokenHTML)
	}))
	defer cdn.Close()
	s, l := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL) })
	file := quotaTestFile(t, s, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", "/webdav/__all__/Show/file1.mkv", nil).WithContext(ctx)
	r.Header.Set("Range", "bytes=0-3")
	w := httptest.NewRecorder()
	s.streamFileContent(w, r, file)
	if calls.Load() != 4 || l.RequestStats().TotalCalls != 4 || w.Body.Len() != 0 {
		t.Fatal("repair budget reset at hang handoff", calls.Load(), l.RequestStats(), w.Body.String())
	}
	if len(s.cdnSem) != cap(s.cdnSem) || len(s.torrentFailures) != 0 {
		t.Fatal("resources or failure classification changed")
	}
}
