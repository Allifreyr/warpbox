package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ben/warpbox/internal/metadata"
)

func TestServeDirListingRoot(t *testing.T) {
	// Open an in-memory store with some test data.
	store, err := metadata.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory store: %v", err)
	}
	defer store.Close()

	// Seed some files.
	files := []metadata.FileRecord{
		{TorrentID: 1, FileID: 10, Name: "file1.mkv", Path: "Movie.A/file1.mkv", Size: 1000, MimeType: "video/x-matroska"},
		{TorrentID: 1, FileID: 11, Name: "file2.mkv", Path: "Movie.A/file2.mkv", Size: 2000, MimeType: "video/x-matroska"},
		{TorrentID: 2, FileID: 20, Name: "ep1.mkv",  Path: "Show.B/ep1.mkv", Size: 500, MimeType: "video/x-matroska"},
	}
	for _, f := range files {
		if err := store.UpsertFile(f); err != nil {
			t.Fatalf("failed to upsert file: %v", err)
		}
	}

	// Create a server pointing to the in-memory store.
	srv := New(Config{WebDAVRoot: "/webdav", Version: "test"}, store, nil, nil, nil)

	// Simulate GET /webdav/
	req := httptest.NewRequest(http.MethodGet, "/webdav/", nil)
	w := httptest.NewRecorder()
	srv.handleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Verify status is 207 Multi-Status.
	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("expected 207 Multi-Status, got %d %s", resp.StatusCode, resp.Status)
	}

	// Verify Content-Type is XML.
	ct := resp.Header.Get("Content-Type")
	if ct != "application/xml; charset=utf-8" {
		t.Errorf("expected XML content type, got %q", ct)
	}

	// Verify the DAV header is present.
	if resp.Header.Get("DAV") != "1" {
		t.Errorf("expected DAV: 1 header")
	}

	// Verify the XML is well-formed and contains expected elements.
	body := readAllStr(resp.Body)
	if !strings.Contains(body, "<D:multistatus") {
		t.Error("expected <D:multistatus> element")
	}
	if !strings.Contains(body, "<D:href>/webdav/</D:href>") {
		t.Error("expected root href /webdav/")
	}
	if !strings.Contains(body, "<D:collection>") {
		t.Error("expected collection element for directory")
	}
	if !strings.Contains(body, "Movie.A") || !strings.Contains(body, "Show.B") {
		t.Error("expected Movie.A and Show.B in response")
	}
}

func TestServeDirListingSubdir(t *testing.T) {
	store, err := metadata.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory store: %v", err)
	}
	defer store.Close()

	files := []metadata.FileRecord{
		{TorrentID: 1, FileID: 10, Name: "file1.mkv", Path: "Movie.A/file1.mkv", Size: 1000, MimeType: "video/x-matroska"},
	}
	for _, f := range files {
		if err := store.UpsertFile(f); err != nil {
			t.Fatalf("failed to upsert file: %v", err)
		}
	}

	srv := New(Config{WebDAVRoot: "/webdav", Version: "test"}, store, nil, nil, nil)

	// Simulate GET /webdav/Movie.A/ — a subdirectory.
	req := httptest.NewRequest(http.MethodGet, "/webdav/Movie.A/", nil)
	w := httptest.NewRecorder()
	srv.handleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("expected 207 Multi-Status, got %d %s", resp.StatusCode, resp.Status)
	}

	body := readAllStr(resp.Body)
	if !strings.Contains(body, "<D:multistatus") {
		t.Error("expected <D:multistatus> element")
	}
	if !strings.Contains(body, "<D:href>/webdav/Movie.A/</D:href>") {
		t.Error("expected dir href /webdav/Movie.A/")
	}
	if !strings.Contains(body, "<D:collection>") {
		t.Error("expected collection element for directory")
	}
	if !strings.Contains(body, "file1.mkv") {
		t.Error("expected file1.mkv in response")
	}
}

func TestServeDirListingMissingPath(t *testing.T) {
	store, err := metadata.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory store: %v", err)
	}
	defer store.Close()

	srv := New(Config{WebDAVRoot: "/webdav", Version: "test"}, store, nil, nil, nil)

	// GET on a path that doesn't exist and has no children.
	req := httptest.NewRequest(http.MethodGet, "/webdav/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent path, got %d", resp.StatusCode)
	}
}

func readAllStr(r io.ReadCloser) string {
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b)
}

func TestServeDirListingGETRootNoSlash(t *testing.T) {
	store, err := metadata.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory store: %v", err)
	}
	defer store.Close()

	files := []metadata.FileRecord{
		{TorrentID: 1, FileID: 10, Name: "file.mkv", Path: "Torrent/file.mkv", Size: 1000, MimeType: "video/x-matroska"},
	}
	for _, f := range files {
		if err := store.UpsertFile(f); err != nil {
			t.Fatalf("failed to upsert file: %v", err)
		}
	}

	srv := New(Config{WebDAVRoot: "/webdav", Version: "test"}, store, nil, nil, nil)

	// GET /webdav (without trailing slash) — this is the case the user reported.
	req := httptest.NewRequest(http.MethodGet, "/webdav", nil)
	w := httptest.NewRecorder()
	srv.handleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("expected 207 Multi-Status, got %d", resp.StatusCode)
	}

	body := readAllStr(resp.Body)
	if !strings.Contains(body, "<D:multistatus") {
		t.Error("expected valid multi-status XML")
	}
}
