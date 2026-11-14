package throttle

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequestAdmissionCancellationAndCounters(t *testing.T) {
	l := NewLimiter(600) // 100ms between attempts.
	ctx := context.Background()
	if err := l.BeginRequest(ctx); err != nil {
		t.Fatal(err)
	}
	l.EndRequest(nil)
	first := l.lastCall
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := l.BeginRequest(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !l.lastCall.Equal(first) {
		t.Fatal("cancelled admission consumed a slot")
	}
	waiting, stop := context.WithTimeout(ctx, 5*time.Millisecond)
	defer stop()
	if err := l.BeginRequest(waiting); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !l.lastCall.Equal(first) {
		t.Fatal("expired admission consumed a slot")
	}
	if stats := l.RequestStats(); stats.TotalCalls != 1 || stats.SuccessfulCalls != 1 || stats.FailedCalls != 0 {
		t.Fatal(stats)
	}
	if err := l.BeginRequest(ctx); err != nil {
		t.Fatal(err)
	}
	l.EndRequest(errors.New("HTTP failure"))
	l.Record429()
	stats := l.RequestStats()
	if stats.TotalCalls != 2 || stats.SuccessfulCalls != 1 || stats.FailedCalls != 1 || stats.HTTP429Calls != 1 || stats.CallsLastMinute != 2 || stats.RequestsPerMinute != 600 {
		t.Fatal(stats)
	}
}

func TestRequestWindowExpiresWithoutNewRequests(t *testing.T) {
	l := NewLimiter(250)
	l.totalRequests = 2
	l.requestWindow = []time.Time{time.Now().Add(-2 * time.Minute), time.Now()}
	if stats := l.RequestStats(); stats.CallsLastMinute != 1 || stats.TotalCalls != 2 {
		t.Fatal(stats)
	}
	l.requestWindow[0] = time.Now().Add(-2 * time.Minute)
	if stats := l.RequestStats(); stats.CallsLastMinute != 0 || stats.TotalCalls != 2 {
		t.Fatal(stats)
	}
}

func TestExecutionQueueDoesNotChargeLimiter(t *testing.T) {
	l := NewLimiter(10) // A paced second operation would wait six seconds.
	l.lastCall = time.Now()
	q := NewExecutionQueue(10, l)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		q.Enqueue(Request{Execute: func(context.Context) error { done <- struct{}{}; return nil }})
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("execution queue charged an extra limiter slot")
		}
	}
	if stats := l.RequestStats(); stats.TotalCalls != 0 {
		t.Fatal("operations counted as HTTP requests", stats)
	}
}

func TestExecutionQueueSerializesAndSkipsCancelledWork(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	q := NewExecutionQueue(250, NewLimiter(250))
	q.Start(ctx)
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	entered, finished := make(chan struct{}), make(chan struct{})
	if err := q.EnqueueContext(request, Request{Execute: func(ctx context.Context) error { close(entered); <-ctx.Done(); close(finished); return ctx.Err() }}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first operation did not start")
	}
	cancelled, abort := context.WithCancel(ctx)
	abort()
	// Enqueue directly to exercise cancellation at execution as well as admission.
	q.Enqueue(Request{Context: cancelled, Execute: func(context.Context) error { t.Error("cancelled operation executed"); return nil }})
	done := make(chan bool, 1)
	q.Enqueue(Request{Execute: func(context.Context) error {
		select {
		case <-finished:
			done <- true
		default:
			done <- false
		}
		return nil
	}})
	select {
	case <-done:
		t.Fatal("operations executed concurrently")
	default:
	}
	cancel()
	select {
	case serial := <-done:
		if !serial {
			t.Fatal("operation overtook active request")
		}
	case <-time.After(time.Second):
		t.Fatal("queue stalled after cancellation")
	}
}
