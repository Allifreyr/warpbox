package throttle

import (
	"context"
	"testing"
	"time"
)

func TestEnqueueContextFullQueue(t *testing.T) {
	q := NewQueue(60)
	for i := 0; i < queueBufferSize; i++ {
		q.Enqueue(Request{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := q.EnqueueContext(ctx, Request{}); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
}

func TestCanceledQueueRequestNeverExecutes(t *testing.T) {
	q := NewQueue(99999)
	ctx, cancel := context.WithCancel(context.Background())
	called := make(chan bool, 1)
	if err := q.EnqueueContext(ctx, Request{Execute: func(context.Context) error { called <- true; return nil }}); err != nil {
		t.Fatal(err)
	}
	cancel()
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	q.Start(parent)
	done := make(chan bool, 1)
	q.Enqueue(Request{Execute: func(context.Context) error { done <- true; return nil }})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queue stalled")
	}
	select {
	case <-called:
		t.Fatal("canceled request executed")
	default:
	}
}

func TestLegacyQueueRequestKeepsWorkerContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(99999)
	result := make(chan bool, 1)
	q.Enqueue(Request{Execute: func(ctx context.Context) error { result <- ctx == parent; return nil }})
	q.Start(parent)
	select {
	case same := <-result:
		if !same {
			t.Fatal("legacy callback context changed")
		}
	case <-time.After(time.Second):
		t.Fatal("queue stalled")
	}
}

func TestQueuePreservesRequestDeadlineAndCancellation(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	request, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	deadline, _ := request.Deadline()
	q := NewQueue(99999)
	started := make(chan bool, 1)
	done := make(chan bool, 1)
	q.EnqueueContext(request, Request{Execute: func(ctx context.Context) error {
		got, ok := ctx.Deadline()
		started <- ok && got.Equal(deadline)
		<-ctx.Done()
		done <- true
		return ctx.Err()
	}})
	q.Start(parent)
	select {
	case valid := <-started:
		if !valid {
			t.Fatal("request deadline lost")
		}
	case <-time.After(time.Second):
		t.Fatal("queue stalled")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request cancellation ignored")
	}
}
