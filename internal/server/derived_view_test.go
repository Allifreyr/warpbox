package server

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Allifreyr/warpbox/internal/anime"
	"github.com/Allifreyr/warpbox/internal/config"
	"github.com/Allifreyr/warpbox/internal/metadata"
	"github.com/Allifreyr/warpbox/internal/torbox"
)

func TestAnimeDerivedViewRoutesAndPlayback(t *testing.T) {
	s := testServer(t, Config{VirtualPaths: []config.VirtualPathConfig{{Name: "anime", DirectoryInclude: "SubsPlease"}, {Name: "movies", DirectoryInclude: "Movie"}, {Name: "tv", DirectoryInclude: "TV"}}})
	v, err := anime.New(s.store, config.LibraryConfig{Anime: config.AnimeConfig{Source: "anime", Name: "anime-plex"}, VirtualPaths: []config.VirtualPathConfig{{Name: "anime", DirectoryInclude: "SubsPlease"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.DerivedView = v
	ctx := context.Background()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-3" {
			t.Error("range not forwarded")
		}
		w.Header().Set("Content-Range", "bytes 0-3/8")
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(206)
		fmt.Fprint(w, "test")
	}))
	defer cdn.Close()
	raw := "[SubsPlease] Dr. Stone S2 batch/[SubsPlease] Dr. Stone S2 - 01 [ABC].mkv"
	for i, p := range []string{raw, "Movie/movie.mkv", "TV/TV S01E01.mkv"} {
		if err := s.store.UpsertFile(metadata.FileRecord{ItemID: int64(i + 1), FileID: 0, Path: p, Name: "episode.mkv", Size: 8, MimeType: "video/x-matroska"}); err != nil {
			t.Fatal(err)
		}
	}
	file, _ := s.store.GetFileByPath(raw)
	s.store.SetCDNURL(file.ID, cdn.URL, time.Now().Add(time.Hour))
	alias := "Dr. Stone (2019) {tmdb-1429}/Season 02/Dr. Stone - S02E01 [release-abc].mkv"
	if err := s.store.PutAnimeMapping(ctx, metadata.AnimeMapping{ItemID: 1, FileID: 0, OriginalPath: raw, Path: alias, TMDBID: 1429, Season: 2, Episodes: []int{1}}); err != nil {
		t.Fatal(err)
	}
	request := func(method, p string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, p, nil)
		r.Header.Set("Range", "bytes=0-3")
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w
	}
	for _, root := range []string{"/http/", "/webdav/", "/infuse/"} {
		method := "PROPFIND"
		if root == "/http/" {
			method = "GET"
		}
		w := request(method, root)
		if !strings.Contains(w.Body.String(), "anime-plex/") {
			t.Fatalf("missing mount %s: %s", root, w.Body.String())
		}
	}
	for _, root := range []string{"/webdav/", "/infuse/"} {
		w := request("PROPFIND", root+"anime-plex/"+encodeDAVHref("Dr. Stone (2019) {tmdb-1429}/Season 02/"))
		var ms struct {
			Responses []struct{} `xml:"response"`
		}
		if w.Code != 207 || xml.Unmarshal(w.Body.Bytes(), &ms) != nil || len(ms.Responses) != 2 || !strings.Contains(w.Body.String(), "S02E01") {
			t.Fatalf("bad listing: %s", w.Body.String())
		}
		w = request("HEAD", root+"anime-plex/"+encodeDAVHref(alias))
		if w.Code != 200 || w.Header().Get("Content-Length") != "8" {
			t.Fatalf("HEAD %d %s", w.Code, w.Body.String())
		}
		w = request("GET", root+"anime-plex/"+encodeDAVHref(alias))
		if w.Code != 206 || w.Body.String() != "test" {
			t.Fatalf("stream %d %s", w.Code, w.Body.String())
		}
		if w = request("GET", root+"anime-plex/"+encodeDAVHref(raw)); w.Code != 404 {
			t.Fatal("derived view exposed raw path")
		}
	}
	w := request("GET", "/http/anime-plex/"+encodeDAVHref(alias))
	if w.Code != 206 || w.Body.String() != "test" {
		t.Fatalf("HTTP stream %d %s", w.Code, w.Body.String())
	}
	w = request("GET", "/http/anime-plex/")
	if !strings.Contains(w.Body.String(), "tmdb-1429") || strings.Contains(w.Body.String(), "SubsPlease") {
		t.Fatal(w.Body.String())
	}
	// Original views retain their original names and membership.
	for _, name := range []string{"anime", "movies", "tv", "__all__"} {
		w = request("GET", "/http/"+name+"/")
		if strings.Contains(w.Body.String(), "tmdb-1429") {
			t.Fatalf("derived name leaked into %s", name)
		}
		if name == "anime" && !strings.Contains(w.Body.String(), "SubsPlease") {
			t.Fatal("original anime changed")
		}
	}
	// Raw aliases are never installed in the global file table.
	if f, _ := s.store.GetFileByPath(alias); f != nil {
		t.Fatal("alias mutated global store")
	}
}

func TestAnimeDuplicateFallbackUsesAliasCandidates(t *testing.T) {
	s := testServer(t)
	v, err := anime.New(s.store, config.LibraryConfig{Anime: config.AnimeConfig{Source: "anime", Name: "anime-plex"}, VirtualPaths: []config.VirtualPathConfig{{Name: "anime"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.DerivedView = v
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.queue.Start(ctx)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("torrent_id") != "2" || r.URL.Query().Get("file_id") != "0" {
			t.Errorf("wrong fallback identity: %s", r.URL.String())
		}
		fmt.Fprint(w, `{"success":true,"data":"https://cdn.example/alternate"}`)
	}))
	defer api.Close()
	s.torBox = torbox.NewClient("test")
	s.torBox.SetBaseURL(api.URL)
	for i := int64(1); i <= 2; i++ {
		raw := fmt.Sprintf("batch%d/episode.mkv", i)
		if err := s.store.UpsertFile(metadata.FileRecord{ItemID: i, FileID: 0, Path: raw, Name: "episode.mkv"}); err != nil {
			t.Fatal(err)
		}
		if err := s.store.PutAnimeMapping(ctx, metadata.AnimeMapping{ItemID: i, FileID: 0, OriginalPath: raw, Path: "Show/Season 01/Show - S01E01.mkv"}); err != nil {
			t.Fatal(err)
		}
	}
	file, err := v.Resolve(ctx, "Show/Season 01/Show - S01E01.mkv")
	if err != nil || file == nil {
		t.Fatal(file, err)
	}
	u, err := s.fallbackFile(ctx, file)
	if err != nil || u != "https://cdn.example/alternate" {
		t.Fatal(u, err)
	}
}

func TestAnimeFallbackDoesNotCrossVariantAliases(t *testing.T) {
	s := testServer(t)
	v, err := anime.New(s.store, config.LibraryConfig{Anime: config.AnimeConfig{Source: "anime", Name: "anime-plex"}, VirtualPaths: []config.VirtualPathConfig{{Name: "anime"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.DerivedView = v
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.queue.Start(ctx)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("fallback requested a different variant")
		fmt.Fprint(w, `{"success":true,"data":"https://cdn.example/wrong-variant"}`)
	}))
	defer api.Close()
	s.torBox = torbox.NewClient("test")
	s.torBox.SetBaseURL(api.URL)
	const raw = "batch/episode.mkv"
	const alias = "Show/Season 01/Show - S01E01 [release-small].mkv"
	for i, size := range []int64{1000, 2000} {
		item := int64(i + 1)
		if err := s.store.UpsertFile(metadata.FileRecord{ItemID: item, FileID: 0, Path: raw, Name: "episode.mkv", Size: size}); err != nil {
			t.Fatal(err)
		}
		vp := alias
		if i == 1 {
			vp = strings.Replace(alias, "small", "large", 1)
		}
		if err := s.store.PutAnimeMapping(ctx, metadata.AnimeMapping{ItemID: item, FileID: 0, OriginalPath: raw, Path: vp}); err != nil {
			t.Fatal(err)
		}
	}
	file, err := v.Resolve(ctx, alias)
	if err != nil || file == nil {
		t.Fatal(file, err)
	}
	if file.Size != 1000 {
		t.Fatalf("wrong requested variant: %+v", file)
	}
	if u, err := s.fallbackFile(ctx, file); err == nil || u != "" {
		t.Fatalf("crossed alias boundary: %q %v", u, err)
	}
}

func TestAnimeRootListsDirectoriesWithUnresolvedMappings(t *testing.T) {
	s := testServer(t)
	v, err := anime.New(s.store, config.LibraryConfig{Anime: config.AnimeConfig{Source: "anime", Name: "anime-plex"}, VirtualPaths: []config.VirtualPathConfig{{Name: "anime"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.DerivedView = v
	ctx := context.Background()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("opening the derived root attempted playback")
		w.Header().Set("Content-Type", "video/x-matroska")
		fmt.Fprint(w, "wrong video")
	}))
	defer cdn.Close()
	for i, alias := range []string{"", "Dr. Stone (2019) {tmdb-1429}/Season 02/Dr. Stone - S02E01.mkv"} {
		item := int64(i + 1)
		raw := fmt.Sprintf("batch/episode-%d.mkv", item)
		if err := s.store.UpsertFile(metadata.FileRecord{ItemID: item, FileID: 0, Path: raw, Name: "episode.mkv", Size: 11, MimeType: "video/x-matroska"}); err != nil {
			t.Fatal(err)
		}
		f, err := s.store.GetFileByPath(raw)
		if err != nil || f == nil {
			t.Fatal(f, err)
		}
		if err := s.store.SetCDNURL(f.ID, cdn.URL, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := s.store.PutAnimeMapping(ctx, metadata.AnimeMapping{ItemID: item, FileID: 0, OriginalPath: raw, Path: alias}); err != nil {
			t.Fatal(err)
		}
	}
	for _, url := range []string{"/http/anime-plex", "/http/anime-plex/"} {
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), "tmdb-1429") {
			t.Fatalf("%s did not list show folders: %d %s %s", url, w.Code, w.Header().Get("Content-Type"), w.Body.String())
		}
	}
}
