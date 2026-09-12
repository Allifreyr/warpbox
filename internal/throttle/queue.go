// Package throttle implements a blocking request queue with a token-bucket rate limiter.
//
// Warpbox never fails fast. Burst traffic from Plex is queued and trickled
// to the TorBox API at a safe rate below 300 requests per minute.
package throttle

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const queueBufferSize = 1024

// Request represents a queued API call.
type Request struct {
	Label   string
	Execute func(ctx context.Context) error
}

// Limiter paces the start of API calls. Multiple queues can share one Limiter
// so the aggregate start rate never exceeds requestsPerMinute, while each
// queue still runs its own processLoop. 429s are counted here (once, globally)
// rather than on each queue.
type Limiter struct {
	mu       sync.Mutex
	rate     time.Duration
	lastCall time.Time
	http429  int64
}

// NewLimiter creates a shared rate limiter that paces call starts.
func NewLimiter(requestsPerMinute int) *Limiter {
	return &Limiter{
		rate: time.Minute / time.Duration(requestsPerMinute),
	}
}

// Wait blocks until a start slot is available, then reserves it.
// Consecutive starts (across all queues sharing this limiter) are spaced
// at least rate apart. Returns promptly on context cancellation.
func (l *Limiter) Wait(ctx context.Context) {
	for {
		l.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(l.lastCall)
		if elapsed >= l.rate {
			l.lastCall = now
			l.mu.Unlock()
			return
		}
		wait := l.rate - elapsed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Record429 increments the global 429 counter.
func (l *Limiter) Record429() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.http429++
}

// HTTP429Calls returns the lifetime 429 count.
func (l *Limiter) HTTP429Calls() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.http429
}

// Queue is a rate-limited, blocking queue for TorBox API requests.
type Queue struct {
	mu      sync.Mutex
	items   chan Request
	limiter *Limiter
	rate    time.Duration

	totalCalls      int64
	callWindow      []time.Time
	successfulCalls int64
	failedCalls     int64
}

// Stats returns throttle statistics for the landing page.
type Stats struct {
	TotalCalls        int64
	SuccessfulCalls   int64
	FailedCalls       int64
	HTTP429Calls      int64
	CallsLastMinute   int
	RequestsPerMinute int
}

// NewQueue creates a self-paced throttle queue (private limiter).
// requestsPerMinute sets the maximum sustained call rate.
func NewQueue(requestsPerMinute int) *Queue {
	return NewQueueWithLimiter(requestsPerMinute, nil)
}

// NewQueueWithLimiter creates a throttle queue paced by the given limiter.
// If limiter is nil, the queue gets its own private limiter. Sharing a
// limiter between queues enforces the collective requestsPerMinute.
func NewQueueWithLimiter(requestsPerMinute int, shared *Limiter) *Queue {
	if shared == nil {
		shared = NewLimiter(requestsPerMinute)
	}
	return &Queue{
		items:   make(chan Request, queueBufferSize),
		limiter: shared,
		rate:    time.Minute / time.Duration(requestsPerMinute),
	}
}

// Stats returns current throttle statistics.
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Count calls in the last 60 seconds.
	now := time.Now()
	cutoff := now.Add(-60 * time.Second)
	recent := 0
	for _, t := range q.callWindow {
		if t.After(cutoff) {
			recent++
		}
	}

	return Stats{
		TotalCalls:        q.totalCalls,
		SuccessfulCalls:   q.successfulCalls,
		FailedCalls:       q.failedCalls,
		HTTP429Calls:      q.limiter.HTTP429Calls(),
		CallsLastMinute:   recent,
		RequestsPerMinute: int(time.Minute / q.rate),
	}
}

// Enqueue adds a request to the blocking queue.
// If the queue buffer is full, Enqueue blocks until space is available.
func (q *Queue) Enqueue(r Request) {
	q.items <- r
}

// processLoop runs in a goroutine, receiving and executing requests at the
// configured rate. It blocks on the channel when the queue is empty and
// exits cleanly when the context is cancelled.
func (q *Queue) processLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-q.items:
			// Pace the start through the (possibly shared) limiter. A long
			// Execute on this queue does not delay another queue sharing
			// the same limiter.
			q.limiter.Wait(ctx)
			if ctx.Err() != nil {
				return
			}

			err := r.Execute(ctx)

			q.mu.Lock()
			q.totalCalls++
			if err != nil {
				q.failedCalls++
				// Log throttle-level failure at DEBUG only. The caller (e.g. handleGet)
				// owns the ERROR-level log with full context (torrent_id, file_id, etc.).
				slog.Debug("throttle request failed", "label", r.Label, "error", err)
			} else {
				q.successfulCalls++
			}
			now := time.Now()
			q.callWindow = append(q.callWindow, now)
			// Keep the window trimmed to roughly the last 60 seconds.
			cutoff := now.Add(-60 * time.Second)
			for len(q.callWindow) > 0 && q.callWindow[0].Before(cutoff) {
				q.callWindow = q.callWindow[1:]
			}
			q.mu.Unlock()
		}
	}
}

// Record429 increments the 429 counter on the (possibly shared) limiter.
func (q *Queue) Record429() {
	q.limiter.Record429()
}

// Start launches the processing loop in a background goroutine.
func (q *Queue) Start(ctx context.Context) {
	go q.processLoop(ctx)
}
