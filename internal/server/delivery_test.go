package server

import (
	"context"
	"encoding/json"
	"github.com/Allifreyr/warpbox/internal/config"
	"github.com/Allifreyr/warpbox/internal/metadata"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeliveryCSRFWithoutDatabase(t *testing.T) {
	s := testServer(t, Config{})
	s.store.Close()
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/csrf", nil))
	var result map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result["csrf_token"] != s.csrfToken || result["delivery_api"] != float64(1) {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token may be cached")
	}
}

func TestDeliverySyncVisibility(t *testing.T) {
	s := testServer(t, Config{VirtualPaths: []config.VirtualPathConfig{{Name: "watchlisted-tv", DirectoryInclude: "watchlisttv", FileRegex: `(?i)\.mkv$`}}})
	old := syncItemFn
	defer func() { syncItemFn = old }()
	syncItemFn = func(ctx context.Context, source metadata.FileSource, id int64) (metadata.SyncItemResult, error) {
		return metadata.SyncItemResult{ItemID: id, Source: source, Ready: true, FilesUpserted: 2}, nil
	}
	for _, f := range []metadata.FileRecord{
		{ItemID: 42, FileID: 1, Path: "Show/S01E01.mkv", Name: "S01E01.mkv", FilterTags: "watchlisttv", Size: 123},
		{ItemID: 42, FileID: 2, Path: "Show/S01E02.mp4", Name: "S01E02.mp4", FilterTags: "watchlisttv", Size: 123},
		{ItemID: 43, FileID: 3, Path: "Other/S01E01.mkv", Name: "S01E01.mkv", FilterTags: "watchlisttv", Size: 123},
	} {
		if err := s.store.UpsertFile(f); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("POST", "/actions/sync-item", strings.NewReader("source=torrent&id=42&mount=watchlisted-tv"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-CSRF-Token", s.csrfToken)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	var result struct {
		Ready    bool             `json:"ready"`
		Visible  []map[string]any `json:"visible_files"`
		Excluded []map[string]any `json:"excluded_files"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	if !result.Ready || len(result.Visible) != 1 || len(result.Excluded) != 1 {
		t.Fatalf("unexpected: %s", w.Body)
	}
}

func TestDeliveryCSRFRequiresConfiguredAuthentication(t *testing.T) {
	s := testServer(t, Config{AuthEnabled: true, AuthUsername: "operator", AuthPassword: "password"})
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/csrf", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/csrf", nil)
	r.SetBasicAuth("operator", "password")
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated status=%d", w.Code)
	}
}
