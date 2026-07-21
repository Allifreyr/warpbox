// CDN URL fetch, cache, circuit breaker, and response classification for GET.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mainlink0435/warpbox/internal/metadata"
	"github.com/mainlink0435/warpbox/internal/throttle"
	"github.com/mainlink0435/warpbox/internal/torbox"
)

// cdnDataClass is the outcome of classifying a CDN *data* response
// (after requestdl already returned a URL).
type cdnDataClass int

const (
	cdnDataOK        cdnDataClass = iota // stream media bytes
	cdnDataTransient                     // hang/retry (429, 5xx, disguised rate-limit body)
	cdnDataPermanent                     // stop hang / fail (404, 403, other dead 4xx+text)
)

// isCDNDisguisedErrorBody reports whether a CDN Content-Type looks like an
// error page rather than binary media (TorBox sometimes returns 200 with
// text rate-limit bodies).
func isCDNDisguisedErrorBody(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "html") || strings.Contains(ct, "json")
}

// isCDNTransientStatus reports CDN HTTP statuses that should enter hang/poll
// rather than fail the client immediately (status-only; CT checked separately).
func isCDNTransientStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// isCDNPermanentDataFailure reports CDN data responses that are known-dead
// (403/404, or other 4xx with a text/HTML error body). Used for negative-cache
// side effects; hang still stops on any non-OK/non-transient via classify.
func isCDNPermanentDataFailure(status int, contentType string) bool {
	if status == http.StatusNotFound || status == http.StatusForbidden {
		return true
	}
	if status >= 400 && status < 500 && status != http.StatusTooManyRequests &&
		isCDNDisguisedErrorBody(contentType) {
		return true
	}
	return false
}

// classifyCDNDataResponse decides how stream and hang paths treat a CDN data response.
//   - 200/206 + binary CT → OK
//   - 200/206 + text/html/json → Transient (disguised rate limit)
//   - 429 or >=500 → Transient (via isCDNTransientStatus)
//   - any other non-success → Permanent (stop hang / fail stream)
func classifyCDNDataResponse(status int, contentType string) cdnDataClass {
	if status == http.StatusOK || status == http.StatusPartialContent {
		if isCDNDisguisedErrorBody(contentType) {
			return cdnDataTransient
		}
		return cdnDataOK
	}
	if isCDNTransientStatus(status) {
		return cdnDataTransient
	}
	return cdnDataPermanent
}

// invalidateCDNURLCache clears any cached CDN URL for the file so hang/poll
// re-fetches via requestdl.
func (s *Server) invalidateCDNURLCache(file *metadata.FileRecord) {
	if s.cfg.CDNTtlMinutes <= 0 {
		return
	}
	expiry := time.Now().Add(-1 * time.Hour)
	if err := s.store.SetCDNURL(file.ID, "", expiry); err != nil {
		slog.Error("GET: failed to invalidate CDN URL cache", "path", file.Path, "error", err)
	}
}

// cdnCacheKey builds a map key from source, item_id, and file_id.
func cdnCacheKey(source metadata.FileSource, itemID, fileID int64) string {
	src := "torrent"
	if source == metadata.SourceUsenet {
		src = "usenet"
	}
	return fmt.Sprintf("%s:%d:%d", src, itemID, fileID)
}

// isTorrentStale checks whether a torrent has been marked stale by the circuit
// breaker. Stale torrents skip API calls entirely.
func (s *Server) isTorrentStale(itemID int64) bool {
	s.torrentFailuresMu.Lock()
	defer s.torrentFailuresMu.Unlock()

	tracker, exists := s.torrentFailures[itemID]
	if !exists {
		return false
	}
	if !tracker.staleUntil.IsZero() {
		if time.Now().Before(tracker.staleUntil) {
			slog.Warn("circuit breaker: item marked stale, skipping CDN URL fetch",
				"item_id", itemID,
				"stale_until", tracker.staleUntil.Format(time.RFC3339),
			)
			return true
		}
		// Stale period expired — remove the tracker so we try again.
		delete(s.torrentFailures, itemID)
		slog.Info("circuit breaker: item stale period expired, will retry",
			"item_id", itemID,
		)
	}
	return false
}

// recordTorrentFailure records a failure for the given item (torrent or usenet).
// If the failure count exceeds cfg.CircuitBreakerFailures within
// cfg.CircuitBreakerWindowSec, the item is marked stale for
// cfg.CircuitBreakerStaleMin minutes.
func (s *Server) recordTorrentFailure(itemID int64) {
	s.torrentFailuresMu.Lock()
	defer s.torrentFailuresMu.Unlock()

	now := time.Now()
	tracker, exists := s.torrentFailures[itemID]
	if !exists {
		tracker = &torrentFailureTracker{}
		s.torrentFailures[itemID] = tracker
	}

	// Prune failures outside the sliding window.
	window := time.Duration(s.cfg.CircuitBreakerWindowSec) * time.Second
	cutoff := now.Add(-window)
	var active []time.Time
	for _, t := range tracker.failures {
		if t.After(cutoff) {
			active = append(active, t)
		}
	}
	active = append(active, now)
	tracker.failures = active

	if len(active) >= s.cfg.CircuitBreakerFailures {
		staleDur := time.Duration(s.cfg.CircuitBreakerStaleMin) * time.Minute
		tracker.staleUntil = now.Add(staleDur)
		slog.Warn("circuit breaker: item exceeded failure threshold, marking stale",
			"item_id", itemID,
			"failures", len(active),
			"window_seconds", window.Seconds(),
			"threshold", s.cfg.CircuitBreakerFailures,
			"stale_duration_minutes", s.cfg.CircuitBreakerStaleMin,
			"stale_until", tracker.staleUntil.Format(time.RFC3339),
		)
	}
}

// getCDNURLWithRetry enqueues a TorBox requestdl call through the throttle
// queue and returns the fresh CDN URL. Routes to the torrent or usenet
// endpoint based on source. On failure it retries with exponential backoff
// (cfg.CDNURLRetryBackoff * 1s, * 2s, * 4s, etc.) for up to
// cfg.CDNURLRetryCount attempts. 429 responses use a 5s backoff instead.
func (s *Server) getCDNURLWithRetry(source metadata.FileSource, itemID, fileID int64) (string, error) {
	maxRetries := s.cfg.CDNURLRetryCount
	baseBackoff := time.Duration(s.cfg.CDNURLRetryBackoff) * time.Second

	type result struct {
		url string
		err error
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		resCh := make(chan result, 1)

		s.queue.Enqueue(throttle.Request{
			Label: fmt.Sprintf("fetch CDN URL for file %d (attempt %d/%d)", fileID, attempt+1, maxRetries+1),
			Execute: func(ctx context.Context) error {
				var url string
				var err error
				if source == metadata.SourceUsenet {
					url, err = s.torBox.GetUsenetDownloadURL(ctx, itemID, fileID, false)
				} else {
					url, err = s.torBox.GetDownloadURL(ctx, itemID, fileID, false)
				}
				resCh <- result{url, err}
				return err
			},
		})

		res := <-resCh

		if res.err == nil {
			return res.url, nil
		}

		// Check if the error is retryable. 429, 5xx, timeouts, HTML
		// responses, and network errors can all be transient.
		isRetryable := torbox.IsRetryable(res.err)

		if !isRetryable || attempt >= maxRetries {
			// Non-retryable or out of attempts — record and return.
			s.recordTorrentFailure(itemID)
			slog.Warn("CDN URL fetch failed, non-retryable or exhausted",
				"item_id", itemID,
				"file_id", fileID,
				"source", source,
				"attempt", attempt+1,
				"max_attempts", maxRetries+1,
				"retry_backoff_base", s.cfg.CDNURLRetryBackoff,
				"error", res.err,
			)
			return "", res.err
		}

		// Exponential backoff: base * 2^attempt
		wait := baseBackoff * (1 << attempt)
		// 429 rate-limit errors get a long 30s backoff. Once TorBox rate-limits,
		// we need to give it breathing room rather than retrying aggressively.
		if strings.Contains(res.err.Error(), "unexpected status 429") {
			wait = 30 * time.Second
		}
		slog.Warn("CDN URL fetch failed, retrying with backoff",
			"item_id", itemID,
			"file_id", fileID,
			"source", source,
			"attempt", attempt+1,
			"max_attempts", maxRetries+1,
			"backoff_seconds", wait.Seconds(),
			"error", res.err,
		)
		time.Sleep(wait)
	}

	return "", fmt.Errorf("torbox: CDN URL fetch exhausted after %d retries", maxRetries)
}

// fetchCDNURL is the public entry point for handleGet to obtain a CDN URL.
// It checks the negative cache and circuit breaker before making any API calls.
func (s *Server) fetchCDNURL(source metadata.FileSource, itemID, fileID int64) (string, error) {
	key := cdnCacheKey(source, itemID, fileID)

	// 1. Check negative cache for a recent failure on this exact file.
	s.negativeCacheMu.Lock()
	entry, found := s.negativeCache[key]
	if found {
		if time.Now().Before(entry.expiresAt) {
			s.negativeCacheMu.Unlock()
			slog.Debug("negative cache hit, skipping CDN URL fetch",
				"source", source,
				"item_id", itemID,
				"file_id", fileID,
				"error", entry.err,
			)
			return "", entry.err
		}
		// Expired — clean up.
		delete(s.negativeCache, key)
	}
	s.negativeCacheMu.Unlock()

	// 2. Check circuit breaker for this item.
	if s.isTorrentStale(itemID) {
		return "", fmt.Errorf("item %d is marked stale by circuit breaker", itemID)
	}

	// 3. Attempt the API call with retry.
	cdnURL, err := s.getCDNURLWithRetry(source, itemID, fileID)
	if err != nil {
		// Cache the error in the negative cache so subsequent requests for the
		// same file fail fast without hitting the API.
		ttl := time.Duration(s.cfg.NegativeCacheTTLSeconds) * time.Second
		s.negativeCacheMu.Lock()
		s.negativeCache[key] = &negativeCacheEntry{
			err:       err,
			expiresAt: time.Now().Add(ttl),
		}
		s.negativeCacheMu.Unlock()
		return "", err
	}

	return cdnURL, nil
}

// tryCDNFallback queries alternative TorBox items sharing the same virtual
// path and tries to fetch a CDN URL from each in turn. Returns the first
// successful URL. If none succeed, returns the last error.
// This provides resilience when the primary item has been deleted from
// TorBox but alternative duplicates still exist in the database.
func (s *Server) tryCDNFallback(path string) (string, error) {
	alternatives, err := s.store.GetFileAlternatives(path)
	if err != nil {
		return "", fmt.Errorf("querying alternatives: %w", err)
	}
	if len(alternatives) == 0 {
		return "", fmt.Errorf("no alternatives for path %q", path)
	}

	var lastErr error
	for _, alt := range alternatives {
		altURL, altErr := s.fetchCDNURL(alt.Source, alt.ItemID, alt.FileID)
		if altErr == nil {
			slog.Info("CDN URL obtained from alternative item",
				"path", path,
				"alt_source", alt.Source,
				"alt_item_id", alt.ItemID,
				"alt_file_id", alt.FileID,
			)
			return altURL, nil
		}
		lastErr = altErr
	}
	return "", fmt.Errorf("all alternatives failed: %w", lastErr)
}
