package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Allifreyr/warpbox/internal/metadata"
)

const cdnErrorBodyLimit = 16 * 1024

var cdnErrorHeading = regexp.MustCompile(`(?is)<(?:h1|title)\b[^>]*>(.*?)</(?:h1|title)>`)
var cdnErrorTags = regexp.MustCompile(`<[^>]*>`)

func tokenErrorText(s string) string {
	s = html.UnescapeString(cdnErrorTags.ReplaceAllString(s, " "))
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func invalidPresignedToken(body []byte) bool {
	var env struct{ Error, Message string }
	if json.Unmarshal(body, &env) == nil {
		return strings.EqualFold(env.Error, "INVALID_PRESIGNED_TOKEN") || tokenErrorText(env.Error) == "invalid presigned token" || tokenErrorText(env.Message) == "invalid presigned token"
	}
	text := string(body)
	for _, heading := range cdnErrorHeading.FindAllStringSubmatch(text, -1) {
		if tokenErrorText(heading[1]) == "invalid presigned token" {
			return true
		}
	}
	if strings.Contains(text, "<") {
		return false
	}
	if tokenErrorText(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0]) == "invalid presigned token" {
		return true
	}
	normalized := tokenErrorText(text)
	return normalized == "invalid presigned token" || strings.HasPrefix(normalized, "invalid presigned token:") || strings.HasPrefix(normalized, "invalid presigned token -")
}

// Inspect only failures. Media bytes are never consumed here or logged.
func inspectCDN400(resp *http.Response, req *http.Request, file *metadata.FileRecord) bool {
	if resp.StatusCode != http.StatusBadRequest {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cdnErrorBodyLimit+1))
	truncated := len(body) > cdnErrorBodyLimit
	if truncated {
		body = body[:cdnErrorBodyLimit]
	}
	known := err == nil && invalidPresignedToken(body)
	reason := "unrecognized_bad_request"
	if known {
		reason = "invalid_presigned_token"
	}
	requestID := resp.Header.Get("X-Request-ID")
	if len(requestID) > 128 {
		requestID = requestID[:128]
	}
	slog.Warn("CDN bad request", "path", file.Path, "item_id", file.ItemID, "file_id", file.FileID,
		"source", file.Source, "status", resp.StatusCode, "reason", reason,
		"content_type", resp.Header.Get("Content-Type"), "cdn_host", req.URL.Hostname(),
		"range", req.Header.Get("Range"), "request_id", requestID, "body_truncated", truncated, "body_read_failed", err != nil)
	return known
}

type cdnRepairFlight struct {
	done      chan struct{}
	url       string
	err       error
	abandoned bool
}
type repairBudget struct{ remaining int }
type repairBudgetKey struct{}

func (s *Server) withRepairBudget(r *http.Request) *http.Request {
	if r.Context().Value(repairBudgetKey{}) != nil {
		return r
	}
	remaining := s.cfg.CDNURLRepairRetries
	if remaining < 0 {
		remaining = 0
	}
	return r.WithContext(context.WithValue(r.Context(), repairBudgetKey{}, &repairBudget{remaining: remaining}))
}

func (s *Server) takeRepair(r *http.Request) bool {
	b, _ := r.Context().Value(repairBudgetKey{}).(*repairBudget)
	if !s.cfg.CDNURLAutoRepair || b == nil || b.remaining == 0 {
		return false
	}
	b.remaining--
	return true
}

// refreshCDNURL coalesces repair only, preserving ordinary URL-fetch behavior.
// A cancelled leader releases ownership; live followers can elect a new one.
func (s *Server) refreshCDNURL(ctx context.Context, file *metadata.FileRecord, failedURL string) (string, error) {
	key := cdnCacheKey(file.Source, file.ItemID, file.FileID)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		s.repairMu.Lock()
		if flight := s.repairs[key]; flight != nil {
			s.repairMu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-flight.done:
				if flight.abandoned && ctx.Err() == nil {
					continue
				}
				return flight.url, flight.err
			}
		}
		flight := &cdnRepairFlight{done: make(chan struct{})}
		s.repairs[key] = flight
		s.repairMu.Unlock()
		u, err := s.refreshCDNURLOnce(ctx, file, failedURL)
		s.repairMu.Lock()
		flight.url, flight.err = u, err
		flight.abandoned = ctx.Err() != nil
		delete(s.repairs, key)
		close(flight.done)
		s.repairMu.Unlock()
		return u, err
	}
}

func (s *Server) refreshCDNURLOnce(ctx context.Context, file *metadata.FileRecord, failedURL string) (string, error) {
	if err := s.quota.check(); err != nil {
		return "", err
	}
	current, err := s.store.GetCDNURL(file.ID)
	if err != nil {
		return "", err
	}
	if current != "" && current != failedURL {
		return current, nil
	}
	if _, err := s.store.InvalidateCDNURLIfCurrent(file.ID, failedURL); err != nil {
		return "", err
	}
	u, err := s.fetchWithFallback(ctx, file)
	if err == nil {
		s.cacheCDNURL(file, u)
	}
	return u, err
}

func (s *Server) exhaustedTokenRepair(file *metadata.FileRecord, failedURL string) {
	if !s.cfg.CDNURLAutoRepair {
		return
	}
	if _, err := s.store.InvalidateCDNURLIfCurrent(file.ID, failedURL); err != nil {
		slog.Error("failed to invalidate rejected CDN URL", "path", file.Path, "error", err)
		return
	}
	// A concurrently published replacement must not be poisoned by an old 400.
	if current, err := s.store.GetCDNURL(file.ID); err != nil || current != "" {
		return
	}
	ttl := time.Duration(s.cfg.NegativeCacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	s.negativeCacheMu.Lock()
	s.negativeCache[cdnCacheKey(file.Source, file.ItemID, file.FileID)] = &negativeCacheEntry{
		err: fmt.Errorf("CDN invalid signed link after bounded repair"), expiresAt: time.Now().Add(ttl),
	}
	s.negativeCacheMu.Unlock()
}
