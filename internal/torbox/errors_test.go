package torbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDailyQuotaErrorsPreserveEnvelopeAndHTTPAccounting(t *testing.T) {
	for _, status := range []int{200, 429} {
		for _, usenet := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/usenet=%t", status, usenet), func(t *testing.T) {
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "600")
					w.WriteHeader(status)
					io.WriteString(w, `{"success":false,"error":"DAILY_BANDWIDTH_LIMIT_EXCEEDED","data":{"daily_bandwidth_limit":3298534883328,"daily_bandwidth_usage":3601475426665}}`)
				}))
				defer api.Close()
				c := newTestClient(api.URL, "secret")
				var before, after, counted429 int
				var httpErr error
				c.SetRequestHooks(func(context.Context) error { before++; return nil }, func(err error) { after++; httpErr = err })
				c.HTTP429Callback = func() { counted429++ }
				var err error
				if usenet {
					_, err = c.GetUsenetDownloadURL(context.Background(), 1, 2, false)
				} else {
					_, err = c.GetDownloadURL(context.Background(), 1, 2, false)
				}
				wrapped := fmt.Errorf("wrapped: %w", err)
				var got *APIError
				if !errors.As(wrapped, &got) || !IsDailyBandwidthLimit(wrapped) || IsRetryable(wrapped) {
					t.Fatal("quota was not classified as a typed non-retryable refusal", wrapped)
				}
				if got.StatusCode != status || got.LimitBytes == nil || *got.LimitBytes != 3298534883328 || got.UsageBytes == nil || *got.UsageBytes != 3601475426665 || time.Until(got.RetryAt) < 599*time.Second {
					t.Fatalf("lost provider metadata: %+v", got)
				}
				if before != 1 || after != 1 || (httpErr != nil) != (status == 429) || counted429 != map[int]int{200: 0, 429: 1}[status] {
					t.Fatalf("HTTP accounting changed: before=%d after=%d 429=%d err=%v", before, after, counted429, httpErr)
				}
			})
		}
	}
}

func TestQuotaErrorOptionalDataAndExactCode(t *testing.T) {
	joined := errors.Join(newAPIError([]byte(`{"error":"ITEM_NOT_FOUND"}`), 404, time.Time{}, ""), newAPIError([]byte(`{"error":"DAILY_BANDWIDTH_LIMIT_EXCEEDED"}`), 429, time.Time{}, ""))
	if !IsDailyBandwidthLimit(joined) || IsRetryable(joined) {
		t.Fatal("joined errors hid the quota refusal", joined)
	}
	for _, data := range []string{`null`, `{}`, `"unknown"`, `{"daily_bandwidth_limit":"bad"}`, `{"daily_bandwidth_limit":-1,"daily_bandwidth_usage":-2}`} {
		e := newAPIError([]byte(`{"error":"DAILY_BANDWIDTH_LIMIT_EXCEEDED","data":`+data+`}`), 429, time.Time{}, "")
		if !IsDailyBandwidthLimit(e) || e.LimitBytes != nil || e.UsageBytes != nil {
			t.Fatalf("bad optional data handling: %+v", e)
		}
	}
	for _, err := range []error{
		newAPIError([]byte(`{"error":"RATE_LIMITED"}`), 429, time.Time{}, ""),
		fmt.Errorf("torbox: unexpected status 429 (DAILY_BANDWIDTH_LIMIT_EXCEEDED)"),
	} {
		if IsDailyBandwidthLimit(err) || !IsRetryable(err) {
			t.Fatal("ordinary 429 behavior changed", err)
		}
	}
	if got := newAPIError([]byte(`{"error":"DATABASE_ERROR"}`), 503, time.Time{}, "").Error(); got != "torbox: unexpected status 503 (DATABASE_ERROR)" {
		t.Fatal(got)
	}
	if got := newAPIError([]byte(`not JSON`), 404, time.Time{}, "").Error(); got != "torbox: unexpected status 404" {
		t.Fatal(got)
	}
	if err := downloadEnvelopeError([]byte(`{"error":"BAD_TOKEN","data":{}}`), "torbox API error: ", time.Time{}); err == nil || !strings.Contains(err.Error(), "torbox API error: BAD_TOKEN") {
		t.Fatal(err)
	}
}

func TestParseQuotaRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		header string
		want   time.Time
	}{
		{"600", now.Add(10 * time.Minute)},
		{now.Add(time.Hour).Format(http.TimeFormat), now.Add(time.Hour)},
		{"", time.Time{}}, {"-1", time.Time{}}, {"0", time.Time{}}, {"nonsense", time.Time{}},
		{"99999999999999999999", time.Time{}}, {now.Add(-time.Hour).Format(http.TimeFormat), time.Time{}},
	} {
		if got := parseRetryAfter(tt.header, now); !got.Equal(tt.want) {
			t.Fatalf("%q: got %v want %v", tt.header, got, tt.want)
		}
	}
}
