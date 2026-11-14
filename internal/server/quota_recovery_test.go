package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Allifreyr/warpbox/internal/anime"
	"github.com/Allifreyr/warpbox/internal/config"
	"github.com/Allifreyr/warpbox/internal/metadata"
	"github.com/Allifreyr/warpbox/internal/torbox"
)

func TestQuotaRecoveryRotatesDuplicatesOneAttemptPerCheck(t *testing.T) {
	for _, mode := range []string{"torrent", "usenet", "derived"} {
		t.Run(mode, func(t *testing.T) {
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "video/x-matroska")
				w.WriteHeader(206)
				io.WriteString(w, "data")
			}))
			defer cdn.Close()
			healthy, primary := int64(1), int64(3)
			source, param := metadata.SourceTorrent, "torrent_id"
			if mode == "derived" {
				healthy, primary = 3, 1 // Derived aliases order sources by item ID.
			} else if mode == "usenet" {
				source, param = metadata.SourceUsenet, "usenet_id"
			}
			var calls atomic.Int64
			s, limiter := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				id, _ := strconv.ParseInt(r.URL.Query().Get(param), 10, 64)
				switch id {
				case primary:
					w.WriteHeader(404)
					io.WriteString(w, `{"error":"ITEM_NOT_FOUND"}`)
				case healthy:
					fmt.Fprintf(w, `{"success":true,"data":%q}`, cdn.URL)
				default:
					io.WriteString(w, `{"success":false,"error":"FILE_NOT_FOUND","data":{}}`)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			const raw = "Show/same.mkv"
			const alias = "Show/Season 01/Show - S01E01 [release-small].mkv"
			for id := int64(1); id <= 3; id++ {
				if err := s.store.UpsertFile(metadata.FileRecord{Source: source, ItemID: id, FileID: 0,
					Path: raw, Name: "same.mkv", Size: 4, MimeType: "video/x-matroska"}); err != nil {
					t.Fatal(err)
				}
				if mode == "derived" {
					if err := s.store.PutAnimeMapping(ctx, metadata.AnimeMapping{ItemID: id, FileID: 0, OriginalPath: raw, Path: alias}); err != nil {
						t.Fatal(err)
					}
				}
			}
			file, err := s.store.GetFileByPath(raw)
			if mode == "derived" {
				view, viewErr := anime.New(s.store, config.LibraryConfig{Anime: config.AnimeConfig{Source: "anime", Name: "anime-plex"}, VirtualPaths: []config.VirtualPathConfig{{Name: "anime"}}})
				if viewErr != nil {
					t.Fatal(viewErr)
				}
				s.cfg.DerivedView = view
				file, err = view.Resolve(ctx, alias)
			}
			if err != nil || file == nil || file.ItemID != primary {
				t.Fatal(file, err)
			}
			read := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/webdav/__all__/Show/same.mkv", nil).WithContext(ctx)
				r.Header.Set("Range", "bytes=0-3")
				w := httptest.NewRecorder()
				s.streamFileContent(w, r, file)
				return w
			}
			if before := read(); before.Code != 206 || before.Body.String() != "data" || calls.Load() != 3 {
				t.Fatal("baseline duplicate fallback failed", before.Code, before.Body.String(), calls.Load())
			}
			clock := newQuotaClock(s.quota)
			s.quota.observe(quotaRefusal())
			results := make(chan *httptest.ResponseRecorder, 8)
			for i := 0; i < cap(results); i++ {
				go func() { results <- read() }()
			}
			eventuallyQuota(t, func() bool { return clock.Pending() >= cap(results) })
			for want := int64(4); want <= 5; want++ {
				clock.Advance(5 * time.Minute)
				eventuallyQuota(t, func() bool {
					snap := s.quota.snapshot()
					return calls.Load() == want && !snap.Probing && snap.Next.Equal(clock.Now().Add(5*time.Minute))
				})
				select {
				case resp := <-results:
					t.Fatal("reader stopped before trying its eligible duplicate", resp.Code, resp.Body.String())
				default:
				}
			}
			clock.Advance(5 * time.Minute)
			for i := 0; i < cap(results); i++ {
				select {
				case resp := <-results:
					if resp.Code != 206 || resp.Body.String() != "data" {
						t.Fatal(resp.Code, resp.Body.String())
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if calls.Load() != 6 || limiter.RequestStats().TotalCalls != 6 || s.quota.snapshot().Paused {
				t.Fatal("probe attempts were not bounded/shared", calls.Load(), limiter.RequestStats(), s.quota.snapshot())
			}
			if cached, err := s.store.GetCDNURL(file.ID); err != nil || cached != cdn.URL {
				t.Fatal("alternate probe URL was not cached on the requested record", cached, err)
			}
			if len(s.negativeCache) != 2 || len(s.torrentFailures) != 2 || len(s.cdnSem) != cap(s.cdnSem) {
				t.Fatal("quota probes added file failures or leaked slots", len(s.negativeCache), len(s.torrentFailures), len(s.cdnSem))
			}
		})
	}
}

func TestQuotaCandidateRotationSurvivesReaderDisconnect(t *testing.T) {
	var primaryCalls, alternativeCalls atomic.Int64
	s, _ := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("torrent_id") == "2" {
			primaryCalls.Add(1)
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"ITEM_NOT_FOUND"}`)
			return
		}
		alternativeCalls.Add(1)
		io.WriteString(w, `{"success":true,"data":"https://cdn.example/recovered"}`)
	})
	for _, id := range []int64{1, 2} {
		if err := s.store.UpsertFile(metadata.FileRecord{ItemID: id, FileID: 0, Path: "Show/same.mkv", Name: "same.mkv", Size: 4}); err != nil {
			t.Fatal(err)
		}
	}
	file, err := s.store.GetFileByPath("Show/same.mkv")
	if err != nil || file == nil {
		t.Fatal(file, err)
	}
	clock := newQuotaClock(s.quota)
	s.quota.observe(quotaRefusal())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := s.waitForQuota(ctx, file); first <- err }()
	clock.Advance(5 * time.Minute)
	eventuallyQuota(t, func() bool { return primaryCalls.Load() == 1 && !s.quota.snapshot().Probing })
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("reader failed instead of waiting for the duplicate", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader cancellation stalled")
	}
	live, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	result := make(chan error, 1)
	go func() {
		u, err := s.waitForQuota(live, file)
		if err == nil && u != "https://cdn.example/recovered" {
			err = fmt.Errorf("unexpected recovered URL %q", u)
		}
		result <- err
	}()
	clock.Advance(5 * time.Minute)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-live.Done():
		t.Fatal(live.Err())
	}
	if primaryCalls.Load() != 1 || alternativeCalls.Load() != 1 || s.quota.snapshot().Paused || len(s.negativeCache) != 0 || len(s.torrentFailures) != 0 {
		t.Fatal(primaryCalls.Load(), alternativeCalls.Load(), s.quota.snapshot())
	}
}

func TestQuotaRecoveryHonorsRetryAfterOnOtherFailures(t *testing.T) {
	for _, code := range []string{torbox.DailyBandwidthLimitExceeded, "RATE_LIMITED", "DATABASE_ERROR"} {
		t.Run(code, func(t *testing.T) {
			var calls atomic.Int64
			s, _ := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", "3600")
					status := 429
					if code == "DATABASE_ERROR" {
						status = 503
					}
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"error":%q}`, code)
					return
				}
				io.WriteString(w, `{"success":true,"data":"https://cdn.example/recovered"}`)
			})
			file := quotaTestFile(t, s, 1)
			clock := newQuotaClock(s.quota)
			initial := quotaRefusal()
			limit, usage := int64(300), int64(301)
			initial.LimitBytes, initial.UsageBytes = &limit, &usage
			s.quota.observe(initial)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := s.waitForQuota(ctx, file); result <- err }()
			clock.Advance(5 * time.Minute)
			eventuallyQuota(t, func() bool { return calls.Load() == 1 && !s.quota.snapshot().Probing })
			snap := s.quota.snapshot()
			// Retry-After is parsed against the real receive time; the fake
			// scheduler has already advanced five minutes since it was created.
			if snap.Next.Sub(clock.Now()) < 54*time.Minute {
				t.Fatal("provider delay was shortened", snap.Next.Sub(clock.Now()))
			}
			if code != torbox.DailyBandwidthLimitExceeded && (snap.Limit == nil || *snap.Limit != limit || snap.Usage == nil || *snap.Usage != usage) {
				t.Fatal("non-quota error replaced the last reported quota values", snap)
			}
			clock.Advance(snap.Next.Sub(clock.Now()) - time.Second)
			if calls.Load() != 1 {
				t.Fatal("recovery checked before Retry-After")
			}
			clock.Advance(time.Second)
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if calls.Load() != 2 || s.quota.snapshot().Paused {
				t.Fatal(calls.Load(), s.quota.snapshot())
			}
		})
	}
}

func TestQuotaRecoveryDoesNotCrossAnimeVariantAliases(t *testing.T) {
	var calls atomic.Int64
	s, _ := newQuotaPlaybackServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("torrent_id") != "1" {
			t.Error("quota recovery requested a different release variant")
			io.WriteString(w, `{"success":true,"data":"https://cdn.example/wrong-variant"}`)
			return
		}
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"ITEM_NOT_FOUND"}`)
	})
	view, err := anime.New(s.store, config.LibraryConfig{Anime: config.AnimeConfig{Source: "anime", Name: "anime-plex"}, VirtualPaths: []config.VirtualPathConfig{{Name: "anime"}}})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.DerivedView = view
	const raw = "batch/episode.mkv"
	const alias = "Show/Season 01/Show - S01E01 [release-small].mkv"
	for i, size := range []int64{4, 8} {
		id := int64(i + 1)
		if err := s.store.UpsertFile(metadata.FileRecord{ItemID: id, FileID: 0, Path: raw, Name: "episode.mkv", Size: size}); err != nil {
			t.Fatal(err)
		}
		vp := alias
		if i == 1 {
			vp = "Show/Season 01/Show - S01E01 [release-large].mkv"
		}
		if err := s.store.PutAnimeMapping(context.Background(), metadata.AnimeMapping{ItemID: id, FileID: 0, OriginalPath: raw, Path: vp}); err != nil {
			t.Fatal(err)
		}
	}
	file, err := view.Resolve(context.Background(), alias)
	if err != nil || file == nil {
		t.Fatal(file, err)
	}
	clock := newQuotaClock(s.quota)
	s.quota.observe(quotaRefusal())
	for i := int64(1); i <= 2; i++ {
		clock.Advance(5 * time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		u, err := s.waitForQuota(ctx, file)
		cancel()
		if err == nil || u != "" || calls.Load() != i || !s.quota.snapshot().Paused {
			t.Fatal("quota recovery crossed the alias boundary or issued extra attempts", u, err, calls.Load(), s.quota.snapshot())
		}
	}
}
