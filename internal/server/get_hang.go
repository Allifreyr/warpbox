// CDN hang/poll mode: hold the client connection while recovering CDN data.
package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mainlink0435/warpbox/internal/metadata"
)

// enterCDNHang marks per-item data cooldown, invalidates the CDN URL cache,
// and enters hang/poll. Caller must already have released the CDN semaphore
// and closed any proxy response body.
//
// attrs are alternating key/value pairs for slog (e.g. "status", 429).
func (s *Server) enterCDNHang(w http.ResponseWriter, r *http.Request, file *metadata.FileRecord, msg string, attrs ...any) {
	base := []any{
		"path", file.Path,
		"source", file.Source,
		"item_id", file.ItemID,
		"file_id", file.FileID,
	}
	slog.Warn(msg, append(base, attrs...)...)
	s.markCDNDataCooldown(file.ItemID, cdnPollInterval)
	s.invalidateCDNURLCache(file)
	s.handleGetCDNHang(w, r, file)
}

// cdnPollInterval is how long to wait between CDN recovery attempts when
// the CDN is unavailable and we are hanging the connection open. Also used
// as the default per-item data cooldown after a CDN data 429.
// Variable (not const) so tests can shorten the interval.
var cdnPollInterval = 15 * time.Second

const maxCDNPollInterval = 5 * time.Minute

// doublePollInterval doubles d and caps at maxCDNPollInterval.
func doublePollInterval(d time.Duration) time.Duration {
	d *= 2
	if d > maxCDNPollInterval {
		return maxCDNPollInterval
	}
	return d
}

// sleepOrDone waits for d or until ctx is cancelled.
func sleepOrDone(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// handleGetCDNHang is entered when the CDN URL cannot be fetched, or when CDN
// *data* returns a transient error, and we want to avoid returning an error
// (which rclone counts toward maxErrorCount=10).
//
// It sends success HTTP headers immediately, then:
//  1. Waits for any per-item CDN data cooldown (lets TorBox drain connections)
//  2. Fetches a CDN URL (with exponential backoff on requestdl 429)
//  3. Proxies range data; on data 429/5xx/text-error, extends cooldown,
//     backs off, and retries — never streaming error bodies to the client
//
// If the client disconnects (context cancelled), we clean up and exit.
// If rclone's --timeout (default 5m) fires, the connection drops and rclone
// counts one error, but at 1 per 5 minutes it would take 50+ to hit
// maxErrorCount=10.
func (s *Server) handleGetCDNHang(w http.ResponseWriter, r *http.Request, file *metadata.FileRecord) {
	// Parse the byte range, if present.
	rangeHeader := r.Header.Get("Range")
	var srvRange *httpRange
	var hasRange bool
	if rangeHeader != "" {
		var parseErr error
		srvRange, parseErr = parseRange(rangeHeader, file.Size)
		if parseErr != nil {
			slog.Error("GET (hang): invalid range", "range", rangeHeader, "path", file.Path, "error", parseErr)
			http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		hasRange = true
	} else {
		// Synthesize a full-file range.
		srvRange = &httpRange{
			Start:  0,
			End:    file.Size - 1,
			Length: file.Size,
		}
	}

	mime := file.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}

	// Send success headers immediately so rclone sees a successful connection.
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.FormatInt(srvRange.Length, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	if hasRange {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", srvRange.Start, srvRange.End, file.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	pollInterval := cdnPollInterval
	proxyClient := &http.Client{Timeout: 30 * time.Second}

	for {
		// Wait before fetch: shared per-item data cooldown (drain window).
		if err := s.waitCDNDataCooldown(r.Context(), file.ItemID); err != nil {
			slog.Debug("client disconnected while waiting for CDN data cooldown",
				"path", file.Path,
			)
			return
		}

		cdnURL, fetchErr := s.fetchCDNURL(file.Source, file.ItemID, file.FileID)
		if fetchErr != nil {
			cdnURL, fetchErr = s.tryCDNFallback(file.Path)
		}
		if fetchErr != nil {
			// Exponential backoff when rate-limited by TorBox's per-item
			// requestdl limit. Keep doubling until max cap.
			if strings.Contains(fetchErr.Error(), "unexpected status 429") {
				pollInterval = doublePollInterval(pollInterval)
				slog.Warn("GET (hang): rate-limited on requestdl, increasing poll backoff",
					"path", file.Path,
					"source", file.Source,
					"item_id", file.ItemID,
					"file_id", file.FileID,
					"next_poll", pollInterval,
					"error", fetchErr,
				)
			} else {
				slog.Debug("GET (hang): CDN URL still unavailable",
					"path", file.Path, "error", fetchErr, "next_poll", pollInterval,
				)
			}
			if err := sleepOrDone(r.Context(), pollInterval); err != nil {
				slog.Debug("client disconnected while waiting for CDN", "path", file.Path)
				return
			}
			continue
		}

		// Cache the recovered CDN URL.
		if s.cfg.CDNTtlMinutes > 0 {
			expiry := time.Now().Add(time.Duration(s.cfg.CDNTtlMinutes) * time.Minute)
			if err := s.store.SetCDNURL(file.ID, cdnURL, expiry); err != nil {
				slog.Error("GET (hang): failed to cache CDN URL after recovery", "path", file.Path, "error", err)
			}
		}

		slog.Info("CDN URL recovered, attempting data proxy",
			"path", file.Path,
			"source", file.Source,
			"item_id", file.ItemID,
			"file_id", file.FileID,
		)

		// Wait again before data Do so concurrent hangers that extended
		// cooldown during requestdl are still respected.
		if err := s.waitCDNDataCooldown(r.Context(), file.ItemID); err != nil {
			slog.Debug("client disconnected while waiting for CDN data cooldown", "path", file.Path)
			return
		}

		s.AcquireCDNConn()
		proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cdnURL, http.NoBody)
		if err != nil {
			s.ReleaseCDNConn()
			slog.Error("GET (hang): failed to create CDN proxy request", "path", file.Path, "error", err)
			return
		}
		proxyReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", srvRange.Start, srvRange.End))

		proxyResp, err := proxyClient.Do(proxyReq)
		if err != nil {
			s.ReleaseCDNConn()
			slog.Warn("GET (hang): CDN proxy request failed, will retry",
				"path", file.Path, "error", err, "next_poll", pollInterval,
			)
			if err := sleepOrDone(r.Context(), pollInterval); err != nil {
				return
			}
			continue
		}

		status := proxyResp.StatusCode
		ct := proxyResp.Header.Get("Content-Type")

		switch classifyCDNDataResponse(status, ct) {
		case cdnDataPermanent:
			// Headers already sent — stop hang. Negative-cache only for
			// known-dead cases (403/404 / 4xx+text), matching pre-refactor hang.
			proxyResp.Body.Close()
			s.ReleaseCDNConn()
			if isCDNPermanentDataFailure(status, ct) {
				s.invalidateCDNURLCache(file)
				key := cdnCacheKey(file.Source, file.ItemID, file.FileID)
				ttl := time.Duration(s.cfg.NegativeCacheTTLSeconds) * time.Second
				if ttl <= 0 {
					ttl = 30 * time.Second
				}
				s.negativeCacheMu.Lock()
				s.negativeCache[key] = &negativeCacheEntry{
					err:       fmt.Errorf("CDN permanent failure status=%d", status),
					expiresAt: time.Now().Add(ttl),
				}
				s.negativeCacheMu.Unlock()
				slog.Warn("GET (hang): CDN permanent failure, giving up",
					"path", file.Path,
					"status", status,
					"content_type", ct,
					"source", file.Source,
					"item_id", file.ItemID,
					"file_id", file.FileID,
				)
			} else {
				slog.Error("GET (hang): CDN returned non-success after recovery",
					"path", file.Path, "status", status,
				)
			}
			return
		case cdnDataTransient:
			// Do not stream error body; cool down and retry.
			proxyResp.Body.Close()
			s.ReleaseCDNConn()
			s.markCDNDataCooldown(file.ItemID, pollInterval)
			pollInterval = doublePollInterval(pollInterval)
			slog.Warn("GET (hang): CDN data still unavailable, backing off",
				"path", file.Path,
				"status", status,
				"content_type", ct,
				"source", file.Source,
				"item_id", file.ItemID,
				"file_id", file.FileID,
				"next_poll", pollInterval,
			)
			s.invalidateCDNURLCache(file)
			if err := sleepOrDone(r.Context(), pollInterval); err != nil {
				return
			}
			continue
		case cdnDataOK:
			// fall through to stream
		}

		// Success — stream and exit.
		written, copyErr := io.Copy(w, proxyResp.Body)
		proxyResp.Body.Close()
		s.ReleaseCDNConn()
		if copyErr != nil {
			slog.Debug("GET (hang): error streaming CDN data", "path", file.Path, "written", written, "error", copyErr)
		} else {
			slog.Debug("GET (hang): finished streaming", "path", file.Path, "bytes", written)
		}
		return
	}
}
