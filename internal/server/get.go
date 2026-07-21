// WebDAV GET handler — serves file content via throttle → CDN pipeline.
//
// Handles byte-range requests for partial content delivery (used by rclone
// for metadata scanning and media server streaming). CDN URLs are cached in
// the SQLite store with configurable TTL to minimise TorBox API calls.
package server

import (
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mainlink0435/warpbox/internal/library"
	"github.com/mainlink0435/warpbox/internal/metadata"
)

// ---------------------------------------------------------------------------
// GET handler
// ---------------------------------------------------------------------------

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	// Extract filter from context (set by virtual path handlers).
	libFilter, _ := r.Context().Value(filterKey).(*library.Filter)

	// Resolve virtual path using the appropriate root (mount or s.root).
	root := s.rootForRequest(r)
	virtualPath := strings.TrimPrefix(r.URL.Path, root)
	virtualPath = strings.TrimPrefix(virtualPath, "/")

	if virtualPath == "" {
		s.serveDirListing(w, r.URL.Path, "1", libFilter, root)
		return
	}

	slog.Debug("GET", "path", virtualPath, "range", r.Header.Get("Range"))

	// Look up the file in the SQLite store.
	file, err := s.store.GetFileByPath(virtualPath)
	if err != nil {
		slog.Error("GET: store lookup failed", "path", virtualPath, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if file == nil {
		// Not a file — check if it's a virtual directory with children.
		records, listErr := s.store.ListDir(virtualPath)
		if listErr != nil {
			slog.Error("GET: ListDir failed", "prefix", virtualPath, "error", listErr)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if len(records) > 0 {
			s.serveDirListing(w, r.URL.Path, "1", libFilter, root)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// For WebDAV, if there's no Range header, redirect directly to the CDN
	// rather than proxying (preserves the existing behaviour and avoids
	// consuming a CDN connection slot for full-file downloads).
	// The /http/ endpoint (handleHTTP) always proxies, even without Range,
	// because browsers need proper Content-Type headers for inline playback.
	if r.Header.Get("Range") == "" {
		cdnURL, cdnErr := s.store.GetCDNURL(file.ID)
		if cdnErr == nil && cdnURL != "" {
			slog.Debug("GET: redirecting to CDN", "id", file.ID)
			http.Redirect(w, r, cdnURL, http.StatusFound)
			return
		}
		// If we don't have a cached CDN URL, fall through to streamFileContent
		// which will fetch one and proxy.
	}

	s.streamFileContent(w, r, file)
}

// streamFileContent serves file bytes through the CDN proxy pipeline.
// Used by both handleGet (WebDAV) and handleHTTP (direct streaming).
// It handles CDN URL resolution (with retry, hang/poll, negative cache,
// circuit breaker), byte-range requests, and streaming the response to the
// client via a proxy from the CDN.
func (s *Server) streamFileContent(w http.ResponseWriter, r *http.Request, file *metadata.FileRecord) {
	// Invalid TorBox file ids never get a working CDN link — fail fast (no hang).
	if file.FileID <= 0 {
		slog.Warn("GET: refusing stream for unusable file_id",
			"path", file.Path, "item_id", file.ItemID, "file_id", file.FileID)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Get or refresh the CDN URL.
	cdnURL, err := s.store.GetCDNURL(file.ID)
	if err != nil {
		slog.Error("GET: CDN URL lookup failed", "id", file.ID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if cdnURL == "" {
		// No cached CDN URL — fetch one via the throttle queue.
		cdnURL, err = s.fetchCDNURL(file.Source, file.ItemID, file.FileID)
		if err != nil {
			// Primary fetch failed — try alternatives (same path, different TorBox item).
			cdnURL, err = s.tryCDNFallback(file.Path)
			if err != nil {
				// All alternatives also failed. Instead of returning an error (which
				// rclone counts toward maxErrorCount=10, causing Plex to trash the file),
				// send success headers immediately and hold the connection while polling
				// for the CDN URL. This looks like a slow spinning disk to Plex.
				slog.Warn("GET: CDN URL unavailable (primary + alternatives), entering hang/poll mode",
					"path", file.Path,
					"source", file.Source,
					"item_id", file.ItemID,
					"file_id", file.FileID,
					"alternatives", file.Path != "",
					"error", err,
				)
				s.handleGetCDNHang(w, r, file)
				return
			}
		}

		// Cache the CDN URL if TTL > 0.
		if s.cfg.CDNTtlMinutes > 0 {
			expiry := time.Now().Add(time.Duration(s.cfg.CDNTtlMinutes) * time.Minute)
			if err := s.store.SetCDNURL(file.ID, cdnURL, expiry); err != nil {
				slog.Error("GET: failed to cache CDN URL", "path", file.Path, "error", err)
			}
		}
	}

	// Determine if the client requested a byte range.
	rangeHeader := r.Header.Get("Range")
	var srvRange *httpRange
	var isRange bool
	if rangeHeader != "" {
		var parseErr error
		srvRange, parseErr = parseRange(rangeHeader, file.Size)
		if parseErr != nil {
			slog.Error("stream: invalid range", "range", rangeHeader, "error", parseErr)
			http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		isRange = true
	} else {
		// No range — serve the full file.
		srvRange = &httpRange{
			Start:  0,
			End:    file.Size - 1,
			Length: file.Size,
		}
	}

	// Fetch the data through a proxied request to the CDN URL.
	// If the CDN returns 403/404, the URL may be stale. Automatically re-fetch
	// a fresh URL via the throttle queue and retry, up to cdn_url_repair_retries.
	// Acquire the CDN semaphore *before* client.Do so max_cdn_connections
	// actually limits concurrent TorBox CDN opens (D-016 / D-015c).
	slog.Debug("GET: proxying from CDN", "id", file.ID, "offset", srvRange.Start)

	client := &http.Client{Timeout: 30 * time.Second}
	maxAttempts := s.cfg.CDNURLRepairRetries + 1

	for attempt := 0; attempt < maxAttempts; attempt++ {
		s.AcquireCDNConn()

		proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cdnURL, http.NoBody)
		if err != nil {
			s.ReleaseCDNConn()
			slog.Error("GET: failed to create CDN request", "error", err)
			http.Error(w, "Failed to create upstream request", http.StatusInternalServerError)
			return
		}
		proxyReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", srvRange.Start, srvRange.End))

		proxyResp, err := client.Do(proxyReq)
		if err != nil {
			s.ReleaseCDNConn()
			slog.Error("GET: CDN proxy request failed", "error", err)
			// Network error — do not retry.
			http.Error(w, "CDN proxy error", http.StatusBadGateway)
			return
		}

		// Check for stale CDN URL.
		if (proxyResp.StatusCode == http.StatusForbidden || proxyResp.StatusCode == http.StatusNotFound) &&
			s.cfg.CDNURLAutoRepair && attempt < maxAttempts-1 {

			proxyResp.Body.Close()
			s.ReleaseCDNConn()
			slog.Warn("stale CDN URL detected, refreshing",
				"path", file.Path,
				"attempt", attempt+1,
				"max_retries", s.cfg.CDNURLRepairRetries,
				"status", proxyResp.StatusCode,
			)

			newURL, refreshErr := s.fetchCDNURL(file.Source, file.ItemID, file.FileID)
			if refreshErr != nil {
				// Primary refresh failed — try alternatives.
				newURL, refreshErr = s.tryCDNFallback(file.Path)
				if refreshErr != nil {
					slog.Error("GET: CDN URL refresh failed (primary + alternatives)",
						"path", file.Path,
						"attempt", attempt+1,
						"error", refreshErr,
					)
					http.Error(w, "CDN URL refresh failed", http.StatusBadGateway)
					return
				}
				slog.Info("CDN URL refresh succeeded via alternative item",
					"path", file.Path,
				)
			}

			// Update the cached CDN URL.
			cdnURL = newURL
			if s.cfg.CDNTtlMinutes > 0 {
				expiry := time.Now().Add(time.Duration(s.cfg.CDNTtlMinutes) * time.Minute)
				if err := s.store.SetCDNURL(file.ID, cdnURL, expiry); err != nil {
					slog.Error("GET: failed to update cached CDN URL", "path", file.Path, "error", err)
				}
			}
			continue // retry with the fresh URL
		}

		status := proxyResp.StatusCode
		ct := proxyResp.Header.Get("Content-Type")
		switch classifyCDNDataResponse(status, ct) {
		case cdnDataTransient:
			// 429/5xx or 200+text disguised rate-limit: hang instead of 502.
			proxyResp.Body.Close()
			s.ReleaseCDNConn()
			if isCDNDisguisedErrorBody(ct) && (status == http.StatusOK || status == http.StatusPartialContent) {
				s.enterCDNHang(w, r, file,
					"GET: CDN returned a text/error body on a 2xx data response (disguised rate-limit/error) — not streaming, entering hang/poll",
					"content_type", ct,
					"status", status,
				)
			} else {
				s.enterCDNHang(w, r, file, "GET: CDN transient error, entering hang/poll mode",
					"status", status,
				)
			}
			return
		case cdnDataPermanent:
			// Non-success that will not recover by hang (or other permanent class).
			proxyResp.Body.Close()
			s.ReleaseCDNConn()
			if (status == http.StatusForbidden || status == http.StatusNotFound) &&
				attempt == maxAttempts-1 {
				key := cdnCacheKey(file.Source, file.ItemID, file.FileID)
				ttl := time.Duration(s.cfg.NegativeCacheTTLSeconds) * time.Second
				s.negativeCacheMu.Lock()
				s.negativeCache[key] = &negativeCacheEntry{
					err:       fmt.Errorf("CDN returned %d after %d repair attempts", status, maxAttempts),
					expiresAt: time.Now().Add(ttl),
				}
				s.negativeCacheMu.Unlock()
				slog.Warn("CDN proxy exhausted, caching failure in negative cache",
					"path", file.Path,
					"status", status,
					"attempts", maxAttempts,
					"negative_cache_ttl", ttl.Seconds(),
				)
			}
			slog.Error("GET: CDN returned non-success",
				"path", file.Path,
				"status", status,
			)
			http.Error(w, fmt.Sprintf("CDN returned status %d", status), http.StatusBadGateway)
			return
		case cdnDataOK:
			// fall through to stream
		}

		// Stream the CDN response directly to the client.
		// Slot stays held until stream completes (release on return).
		mime := file.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Length", strconv.FormatInt(srvRange.Length, 10))
		w.Header().Set("Accept-Ranges", "bytes")
		if isRange {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", srvRange.Start, srvRange.End, file.Size))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		defer s.ReleaseCDNConn()
		defer proxyResp.Body.Close()

		// Stream from CDN → client.
		written, copyErr := io.Copy(w, proxyResp.Body)

		if copyErr != nil {
			// context canceled / broken pipe / connection reset are normal
			// client-side disconnects (Plex seeking, buffering, switching
			// streams). Only show at DEBUG level — they're not actionable.
			slog.Debug("GET: error streaming CDN data",
				"path", file.Path,
				"written", written,
				"error", copyErr,
			)
			return
		}
		return
	}

	// All attempts exhausted without success.
	http.Error(w, "CDN proxy error after retries", http.StatusBadGateway)
}

type httpRange struct {
	Start  int64
	End    int64
	Length int64
}

// parseRange parses a "bytes=start-end" Range header and returns the computed
// range bounds. Only a single range is supported (rclone uses single ranges).
func parseRange(rang string, fileSize int64) (*httpRange, error) {
	if rang == "" {
		return nil, fmt.Errorf("empty range")
	}

	if !strings.HasPrefix(rang, "bytes=") {
		return nil, fmt.Errorf("invalid range prefix")
	}

	rangeVal := strings.TrimPrefix(rang, "bytes=")
	parts := strings.SplitN(rangeVal, "-", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid range format")
	}

	startStr := strings.TrimSpace(parts[0])
	endStr := strings.TrimSpace(parts[1])

	var start, end int64

	if startStr == "" {
		// Suffix range: "bytes=-N" means last N bytes.
		suffixSize, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid suffix range: %w", err)
		}
		if suffixSize >= fileSize {
			start = 0
			end = fileSize - 1
		} else {
			start = fileSize - suffixSize
			end = fileSize - 1
		}
	} else {
		var parseErr error
		start, parseErr = strconv.ParseInt(startStr, 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid start in range: %w", parseErr)
		}

		if endStr == "" {
			end = fileSize - 1
		} else {
			end, parseErr = strconv.ParseInt(endStr, 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid end in range: %w", parseErr)
			}
		}

		if start > end || start < 0 || end >= fileSize {
			return nil, fmt.Errorf("range out of bounds: start=%d end=%d fileSize=%d", start, end, fileSize)
		}
	}

	return &httpRange{
		Start:  start,
		End:    end,
		Length: end - start + 1,
	}, nil
}

// ---------------------------------------------------------------------------
// HEAD handler (same as GET but no body)
// ---------------------------------------------------------------------------

func (s *Server) handleHead(w http.ResponseWriter, r *http.Request) {
	// Resolve virtual path using the appropriate root (mount or s.root).
	root := s.rootForRequest(r)
	virtualPath := strings.TrimPrefix(r.URL.Path, root)
	virtualPath = strings.TrimPrefix(virtualPath, "/")

	if virtualPath == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	slog.Debug("HEAD", "path", virtualPath)

	// Look up the file to get metadata.
	file, err := s.store.GetFileByPath(virtualPath)
	if err != nil {
		slog.Error("HEAD: store lookup failed", "path", virtualPath, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if file == nil {
		// Not a file — head is for files only; return not found.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	mime := file.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
}

// ---------------------------------------------------------------------------
// Directory listing (WebDAV-style Multi-Status for GET on directory paths)
// ---------------------------------------------------------------------------

// serveDirListing responds to a GET request on a virtual directory path with
// a WebDAV Multi-Status XML document listing the directory contents.
// This matches the behaviour of zurg and other standards-compliant WebDAV servers
// so that Chrome and other browsers render a browsable directory listing.
func (s *Server) serveDirListing(w http.ResponseWriter, reqPath string, depth string, f *library.Filter, root string) {
	slog.Debug("directory listing", "path", reqPath, "depth", depth)

	// Normalise the path.
	normalised := strings.TrimRight(reqPath, "/")
	if normalised == "" {
		normalised = "/"
	}

	// Build the virtual prefix: strip the root (s.root or mount) from the path.
	prefix := strings.TrimPrefix(normalised, root)
	prefix = strings.TrimPrefix(prefix, "/")

	// List files from SQLite matching this prefix.
	records, err := s.store.ListDir(prefix)
	if err != nil {
		slog.Error("directory listing: ListDir failed", "prefix", prefix, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Apply library filter if provided.
	if f != nil {
		records = f.Apply(records)
	}

	// Build a set of virtual paths for the response.
	seen := map[string]bool{}
	var responses []response

	// Always include the requested directory itself.
	dirHref := normalised
	if !strings.HasSuffix(dirHref, "/") {
		dirHref += "/"
	}
	responses = appendResponse(responses, dirHref, true, 0, "", "", "", &seen)

	// Add immediate children based on depth.
	if depth == "1" || depth == "infinity" {

		// At the root level (/webdav/) with virtual paths configured,
		// show synthetic directory entries instead of real files.
		if prefix == "" && root == webdavRoot {
			baseHref := strings.TrimRight(normalised, "/") + "/"
			responses = appendResponse(responses, baseHref+"__all__/", true, 0, "", "", "", &seen)
			for _, vf := range s.virtualFilters {
				name := strings.TrimPrefix(vf.Mount, "/")
				responses = appendResponse(responses, baseHref+name+"/", true, 0, "", "", "", &seen)
			}
		} else {
			// Track immediate children of the requested directory.
			type childInfo struct {
				isDir     bool
				size      int64
				name      string
				mime      string
				createdAt string
			}
			immediate := map[string]childInfo{}

			for _, rec := range records {
				relPath := strings.TrimPrefix(rec.Path, prefix)
				relPath = strings.TrimPrefix(relPath, "/")

				parts := strings.SplitN(relPath, "/", 2)
				immediateName := parts[0]

				if _, exists := immediate[immediateName]; exists {
					continue
				}

				if len(parts) > 1 {
					// The file is nested deeper — the immediate child is a directory.
					immediate[immediateName] = childInfo{isDir: true}
				} else {
					// Direct file in the requested directory.
					mime := rec.MimeType
					if mime == "" {
						mime = "application/octet-stream"
					}
					immediate[immediateName] = childInfo{
						isDir:     false,
						size:      rec.Size,
						name:      rec.Name,
						mime:      mime,
						createdAt: rec.CreatedAt,
					}
				}
			}

			// Build response entries from the immediate children map.
			baseHref := strings.TrimRight(normalised, "/") + "/"
			for name, info := range immediate {
				childHref := baseHref + name
				if info.isDir {
					childHref += "/"
					responses = appendResponse(responses, childHref, true, 0, "", "", "", &seen)
				} else {
					responses = appendResponse(responses, childHref, false, info.size, info.name, info.mime, info.createdAt, &seen)
				}
			}
		} // close else block
	} // close depth block

	// Build the XML response.
	ms := multiStatus{
		XmlnsD:    davNamespace,
		Responses: responses,
	}

	output, err := xml.MarshalIndent(ms, "", "  ")
	if err != nil {
		slog.Error("directory listing: XML marshal failed", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Prepend XML declaration.
	body := append([]byte(xml.Header), output...)

	w.Header().Set("DAV", "1")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	w.Write(body)
}
