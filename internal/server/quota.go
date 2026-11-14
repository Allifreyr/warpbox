package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Allifreyr/warpbox/internal/metadata"
	"github.com/Allifreyr/warpbox/internal/torbox"
)

var errQuotaPaused = errors.New("TorBox daily bandwidth quota is paused")
var errQuotaNextCandidate = errors.New("quota recovery will try another eligible source")

// Rotation hints live only for the current pause. Bound their memory even if
// clients keep requesting unavailable files throughout a prolonged outage.
const maxQuotaProbeCursors = 1024

type quotaProbeCursor struct {
	candidate string
	order     uint64
}

func isQuotaPause(err error) bool {
	return errors.Is(err, errQuotaPaused) || torbox.IsDailyBandwidthLimit(err)
}

// quotaController coordinates demand-driven checks. No lock is held while
// waiting, accessing SQLite, or making a request. It owns no background poller.
type quotaController struct {
	mu                         sync.Mutex
	paused, probing, closed    bool
	interval, initial, maximum time.Duration
	probeTimeout               time.Duration
	next                       time.Time
	last                       *torbox.APIError
	changed                    chan struct{}
	done                       <-chan struct{}
	life                       context.Context
	stop                       context.CancelFunc
	now                        func() time.Time
	after                      func(time.Duration) <-chan time.Time
	probeCursors               map[string]quotaProbeCursor
	probeSequence              uint64
}

type quotaSnapshot struct {
	Paused, Probing bool
	Next            time.Time
	Limit, Usage    *int64
}

func newQuotaController() *quotaController {
	life, stop := context.WithCancel(context.Background())
	return &quotaController{initial: 5 * time.Minute, maximum: 30 * time.Minute, probeTimeout: 2 * time.Minute,
		changed: make(chan struct{}), done: life.Done(), life: life, stop: stop, now: time.Now, after: time.After}
}

func (q *quotaController) snapshot() quotaSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := quotaSnapshot{Paused: q.paused, Probing: q.probing, Next: q.next}
	if q.last != nil {
		s.Limit, s.Usage = q.last.LimitBytes, q.last.UsageBytes
	}
	return s
}

func (q *quotaController) check() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return context.Canceled
	}
	if q.paused {
		return errQuotaPaused
	}
	return nil
}

func (q *quotaController) signalLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func (q *quotaController) scheduleLocked(err error) {
	q.next = q.now().Add(q.interval)
	var apiErr *torbox.APIError
	if errors.As(err, &apiErr) {
		if torbox.IsDailyBandwidthLimit(apiErr) {
			q.last = apiErr
		}
		if apiErr.RetryAt.After(q.next) {
			q.next = apiErr.RetryAt
		}
	}
}

func (q *quotaController) observe(err error) {
	if !torbox.IsDailyBandwidthLimit(err) {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.paused {
		return
	}
	q.paused = true
	q.interval = q.initial
	clear(q.probeCursors)
	q.probeSequence = 0
	q.scheduleLocked(err)
	q.signalLocked()
	slog.Warn("TorBox daily bandwidth quota exhausted; pausing new downloads", "next_check", q.next)
}

func (q *quotaController) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		q.stop()
		q.signalLocked()
	}
}

// wait elects one caller to check recovery. Other callers wait on state changes
// or the next eligible check, and never occupy an API worker or CDN slot.
func (q *quotaController) wait(ctx context.Context, probe func(context.Context) (string, error)) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return "", context.Canceled
		}
		if !q.paused {
			q.mu.Unlock()
			return "", nil
		}
		if !q.probing && !q.now().Before(q.next) {
			q.probing = true
			q.signalLocked()
			q.mu.Unlock()
			result := make(chan quotaProbeResult, 1)
			go func() { result <- q.runProbe(probe) }()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-q.done:
				return "", context.Canceled
			case res := <-result:
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if res.err == nil {
					return res.url, nil
				}
				if !isQuotaPause(res.err) && !torbox.IsRetryable(res.err) && !errors.Is(res.err, errQuotaNextCandidate) {
					return "", res.err // A bad candidate must not keep winning probe ownership.
				}
			}
			continue
		}
		changed, done := q.changed, q.done
		var timer <-chan time.Time
		if !q.probing {
			timer = q.after(q.next.Sub(q.now()))
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-done:
			return "", context.Canceled
		case <-changed:
		case <-timer:
		}
	}
}

type quotaProbeResult struct {
	url string
	err error
}

// A check starts only with demand, but is shared by the account rather than
// owned by a particular HTTP reader. Otherwise a five-minute client timeout can
// repeatedly abort the five-minute recovery check just before it completes.
// It may finish after readers disconnect; it never schedules another idle check.
func (q *quotaController) runProbe(probe func(context.Context) (string, error)) quotaProbeResult {
	slog.Info("checking TorBox daily bandwidth quota recovery")
	ctx, cancel := context.WithTimeout(q.life, q.probeTimeout)
	defer cancel()
	u, err := probe(ctx)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && u == "" {
		err = errors.New("TorBox recovery check returned an empty download URL")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.probing = false
	if q.closed {
		return quotaProbeResult{"", context.Canceled}
	}
	if err == nil {
		q.paused = false
		q.next = time.Time{}
		clear(q.probeCursors)
		slog.Info("TorBox daily bandwidth quota recovered; resuming downloads")
	} else {
		if torbox.IsDailyBandwidthLimit(err) {
			q.interval *= 2
			if q.interval > q.maximum {
				q.interval = q.maximum
			}
		}
		q.scheduleLocked(err)
		slog.Warn("TorBox quota recovery check did not restore downloads", "next_check", q.next,
			"quota_exhausted", torbox.IsDailyBandwidthLimit(err))
	}
	q.signalLocked()
	return quotaProbeResult{u, err}
}

func (s *Server) playbackContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if s.playbackCtx == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(s.playbackCtx, cancel)
	return ctx, func() { stop(); cancel() }
}

func (s *Server) waitForQuota(ctx context.Context, file *metadata.FileRecord) (string, error) {
	return s.quota.wait(ctx, func(ctx context.Context) (string, error) {
		// Enumerate only this read's allowed sources, and only after it wins
		// the account's probe slot. Never run a fallback loop within a slot.
		candidates := []metadata.FileRecord{*file}
		alts, err := s.alternativeFiles(ctx, file)
		if err != nil {
			// A local lookup failure must not prevent checking the primary.
			slog.Debug("quota recovery alternatives unavailable", "path", file.Path, "error", err)
		}
		seen := map[string]bool{cdnCacheKey(file.Source, file.ItemID, file.FileID): true}
		for _, alt := range alts {
			key := cdnCacheKey(alt.Source, alt.ItemID, alt.FileID)
			if alt.FileID >= 0 && !seen[key] {
				candidates = append(candidates, alt)
				seen[key] = true
			}
		}
		scope := "raw:" + file.Path
		if file.ViewPath != "" {
			scope = "derived:" + file.ViewPath
		}
		candidate, hasNext := s.quota.probeCandidate(scope, candidates)
		u, err := s.downloadAttempt(ctx, candidate.Source, candidate.ItemID, candidate.FileID, true)
		if err == nil && u == "" {
			err = errors.New("TorBox recovery check returned an empty download URL")
		}
		if err == nil && u != "" && ctx.Err() == nil {
			s.cacheCDNURL(file, u)
		} else if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) &&
			!isQuotaPause(err) && !torbox.IsRetryable(err) {
			s.quota.advanceProbeCandidate(scope, candidate)
			if hasNext {
				// Keep this reader waiting for the next scheduled check. The
				// hint also survives if the reader disconnects before then.
				err = fmt.Errorf("%w: %w", errQuotaNextCandidate, err)
			}
		}
		return u, err
	})
}

func (q *quotaController) probeCandidate(scope string, candidates []metadata.FileRecord) (metadata.FileRecord, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	next := 0
	if cursor, ok := q.probeCursors[scope]; ok {
		for i, candidate := range candidates {
			if cdnCacheKey(candidate.Source, candidate.ItemID, candidate.FileID) == cursor.candidate {
				next = (i + 1) % len(candidates)
				break
			}
		}
	}
	return candidates[next], next < len(candidates)-1
}

func (q *quotaController) advanceProbeCandidate(scope string, candidate metadata.FileRecord) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || !q.paused {
		return
	}
	if q.probeCursors == nil {
		q.probeCursors = make(map[string]quotaProbeCursor)
	}
	if _, exists := q.probeCursors[scope]; !exists && len(q.probeCursors) >= maxQuotaProbeCursors {
		var oldestScope string
		var oldestOrder uint64 = ^uint64(0)
		for key, cursor := range q.probeCursors {
			if cursor.order < oldestOrder {
				oldestScope, oldestOrder = key, cursor.order
			}
		}
		delete(q.probeCursors, oldestScope)
	}
	q.probeSequence++
	q.probeCursors[scope] = quotaProbeCursor{candidate: cdnCacheKey(candidate.Source, candidate.ItemID, candidate.FileID), order: q.probeSequence}
}

func (s *Server) cacheCDNURL(file *metadata.FileRecord, u string) {
	if s.cfg.CDNTtlMinutes > 0 && u != "" {
		if err := s.store.SetCDNURL(file.ID, u, time.Now().Add(time.Duration(s.cfg.CDNTtlMinutes)*time.Minute)); err != nil {
			slog.Error("failed to cache CDN URL", "path", file.Path, "error", err)
		}
	}
}
