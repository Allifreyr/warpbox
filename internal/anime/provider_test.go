package anime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderCachesAndSurvivesOutage(t *testing.T) {
	v, _ := setup(t)
	var calls atomic.Int32
	var offline atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("missing credential")
		}
		if offline.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"id":1429,"name":"Dr. Stone"}`)
	}))
	defer s.Close()
	c := &Client{Store: v.Store, Token: "test-secret", TTL: time.Hour, BaseURL: s.URL, HTTP: s.Client()}
	ctx := context.Background()
	if _, err := c.Show(ctx, 1429); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Show(ctx, 1429); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("fresh cache missed")
	}
	c.TTL = -time.Second
	offline.Store(true)
	c.next = time.Time{}
	if _, err := c.Show(ctx, 1429); err != nil {
		t.Fatalf("stale cache unavailable: %v", err)
	}
	if _, err := c.Show(ctx, 1429); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("outage repeatedly queried provider")
	}
	if _, err := c.Show(ctx, 999); err == nil {
		t.Fatal("uncached error accepted")
	}
	c.next = time.Now().Add(time.Hour)
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Search(cancelCtx, "unknown"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
