package torbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Allifreyr/warpbox/internal/throttle"
)

type hookTransport func(*http.Request) (*http.Response, error)

func (f hookTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("broken response body") }
func (brokenBody) Close() error             { return nil }

func TestRequestHooksObserveHTTPAttempts(t *testing.T) {
	for _, tt := range []struct {
		name                           string
		status                         int
		transportErr, bodyErr, wantErr bool
	}{
		{"success", 200, false, false, false}, {"status failure", 503, false, false, true},
		{"transport failure", 0, true, false, true}, {"body failure", 200, false, true, true},
		{"rate limit", 429, false, false, true}, {"rate limit broken body", 429, false, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient("private-token")
			var before, after, sent, rateLimits int
			var result error
			c.SetRequestHooks(func(context.Context) error { before++; return nil }, func(err error) { after++; result = err })
			c.HTTP429Callback = func() { rateLimits++ }
			c.SetHTTPClient(&http.Client{Transport: hookTransport(func(*http.Request) (*http.Response, error) {
				sent++
				if tt.transportErr {
					return nil, errors.New("network unavailable")
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(`{"success":true,"data":{}}`))
				if tt.bodyErr {
					body = brokenBody{}
				}
				return &http.Response{StatusCode: tt.status, Body: body, Header: make(http.Header)}, nil
			})})
			_, err := c.GetUserInfo(context.Background())
			if (err != nil) != tt.wantErr || (result != nil) != tt.wantErr || before != 1 || after != 1 || sent != 1 {
				t.Fatalf("err=%v observed=%v before=%d after=%d sent=%d", err, result, before, after, sent)
			}
			want429 := 0
			if tt.status == 429 {
				want429 = 1
			}
			if rateLimits != want429 {
				t.Fatal("429 count", rateLimits)
			}
		})
	}
}

func TestRequestHookCancellationNeverSends(t *testing.T) {
	for _, preCancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "already cancelled"}[preCancelled], func(t *testing.T) {
			c := NewClient("key")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if preCancelled {
				cancel()
			}
			entered := make(chan struct{}, 1)
			c.SetRequestHooks(func(ctx context.Context) error { entered <- struct{}{}; <-ctx.Done(); return ctx.Err() }, func(error) { t.Error("rejected admission recorded as an attempt") })
			c.SetHTTPClient(&http.Client{Transport: hookTransport(func(*http.Request) (*http.Response, error) {
				t.Error("cancelled request reached transport")
				return nil, errors.New("unexpected")
			})})
			done := make(chan error, 1)
			go func() { _, err := c.GetUserInfo(ctx); done <- err }()
			if !preCancelled {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("request did not reach admission hook")
				}
				cancel()
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled request did not return")
			}
		})
	}
}

func TestRequestHooksPaceEveryRedirectHop(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/api/user/me":
					http.Redirect(w, r, "/first", http.StatusTemporaryRedirect)
				case "/first":
					http.Redirect(w, r, "/final", http.StatusPermanentRedirect)
				case "/final":
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"success":true,"data":{}}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()
			l := throttle.NewLimiter(600) // Each of the two redirect hops waits 100ms.
			c := NewClient("key")
			c.SetBaseURL(api.URL)
			c.SetRequestHooks(l.BeginRequest, l.EndRequest)
			c.HTTP429Callback = l.Record429
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			started := time.Now()
			_, err := c.GetUserInfo(ctx)
			if (err != nil) != (status != http.StatusOK) {
				t.Fatal("unexpected final result", err)
			}
			if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
				t.Fatalf("redirects bypassed pacing: %v", elapsed)
			}
			wantSuccess, wantFailed, want429 := int64(3), int64(0), int64(0)
			if status != http.StatusOK {
				wantSuccess, wantFailed = 2, 1
			}
			if status == http.StatusTooManyRequests {
				want429 = 1
			}
			stats := l.RequestStats()
			if stats.TotalCalls != 3 || stats.CallsLastMinute != 3 || stats.SuccessfulCalls != wantSuccess || stats.FailedCalls != wantFailed || stats.HTTP429Calls != want429 {
				t.Fatal("redirect attempts were not counted individually", stats)
			}
		})
	}
}

func TestRequestHooksCancelRedirectAdmission(t *testing.T) {
	l := throttle.NewLimiter(250)
	c := NewClient("key")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var before, sent int
	c.SetRequestHooks(func(ctx context.Context) error {
		before++
		if before == 2 {
			cancel()
		}
		return l.BeginRequest(ctx)
	}, l.EndRequest)
	c.SetHTTPClient(&http.Client{Transport: hookTransport(func(*http.Request) (*http.Response, error) {
		sent++
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": {"/next"}},
			Body:       http.NoBody,
		}, nil
	})})
	if _, err := c.GetUserInfo(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("redirect admission did not return cancellation", err)
	}
	stats := l.RequestStats()
	if before != 2 || sent != 1 || stats.TotalCalls != 1 || stats.SuccessfulCalls != 1 || stats.FailedCalls != 0 {
		t.Fatalf("cancelled hop consumed budget: before=%d sent=%d stats=%+v", before, sent, stats)
	}
}

func TestRequestHooksRedirectWaitRespectsHTTPTimeout(t *testing.T) {
	l := throttle.NewLimiter(10) // The next hop would wait six seconds.
	c := NewClient("key")
	c.SetRequestHooks(l.BeginRequest, l.EndRequest)
	var sent int
	c.SetHTTPClient(&http.Client{
		Timeout: 50 * time.Millisecond,
		Transport: hookTransport(func(*http.Request) (*http.Response, error) {
			sent++
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"/next"}}, Body: http.NoBody}, nil
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	if _, err := c.GetUserInfo(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("redirect wait did not return the HTTP timeout", err)
	}
	stats := l.RequestStats()
	if sent != 1 || stats.TotalCalls != 1 || stats.SuccessfulCalls != 1 || stats.FailedCalls != 0 {
		t.Fatalf("timed-out hop consumed budget: sent=%d stats=%+v", sent, stats)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("redirect admission ignored the HTTP client's shorter timeout")
	}
}

func TestRequestHooksIncludeFinalRedirectFailures(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "body", true: "transport"}[transportFailure], func(t *testing.T) {
			c := NewClient("key")
			var before, sent int
			var results []error
			c.SetRequestHooks(func(context.Context) error { before++; return nil }, func(err error) { results = append(results, err) })
			c.SetHTTPClient(&http.Client{Transport: hookTransport(func(*http.Request) (*http.Response, error) {
				sent++
				if sent == 1 {
					return &http.Response{StatusCode: http.StatusSeeOther, Header: http.Header{"Location": {"/next"}}, Body: http.NoBody}, nil
				}
				if transportFailure {
					return nil, errors.New("network unavailable")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: brokenBody{}}, nil
			})})
			if _, err := c.GetUserInfo(context.Background()); err == nil {
				t.Fatal("expected final hop failure")
			}
			if before != 2 || sent != 2 || len(results) != 2 || results[0] != nil || results[1] == nil {
				t.Fatalf("redirect failure accounting: before=%d sent=%d results=%v", before, sent, results)
			}
		})
	}
}

func TestRequestHooksPreserveRedirectPolicyAndSharedClient(t *testing.T) {
	var before, after, policyCalls, sent int
	c := NewClient("key")
	c.SetRequestHooks(func(context.Context) error { before++; return nil }, func(err error) {
		after++
		if err == nil {
			t.Error("unfollowed redirect should retain its status error")
		}
	})
	hc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { policyCalls++; return http.ErrUseLastResponse },
		Transport: hookTransport(func(*http.Request) (*http.Response, error) {
			sent++
			return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {"/next"}}, Body: http.NoBody}, nil
		}),
	}
	c.SetHTTPClient(hc)
	if _, err := c.GetUserInfo(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected status 307") {
		t.Fatal("redirect policy was not preserved", err)
	}
	// Reusing the supplied HTTP client outside TorBox must not invoke its hooks.
	resp, err := hc.Get("https://cdn.example/video")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if before != 1 || after != 1 || policyCalls != 2 || sent != 2 {
		t.Fatalf("HTTP client settings changed: before=%d after=%d policy=%d sent=%d", before, after, policyCalls, sent)
	}
}

func TestRequestHooksRetainDefaultRedirectLimit(t *testing.T) {
	c := NewClient("key")
	var before, after, sent, failed int
	c.SetRequestHooks(func(context.Context) error { before++; return nil }, func(err error) {
		after++
		if err != nil {
			failed++
		}
	})
	c.SetHTTPClient(&http.Client{Transport: hookTransport(func(*http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"/loop"}}, Body: http.NoBody}, nil
	})})
	if _, err := c.GetUserInfo(context.Background()); err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatal("default redirect limit was not preserved", err)
	}
	if before != 10 || after != 10 || sent != 10 || failed != 1 {
		t.Fatalf("redirect loop accounting: before=%d after=%d sent=%d failed=%d", before, after, sent, failed)
	}
}
