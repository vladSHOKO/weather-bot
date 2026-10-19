package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
	"weather-sum/internal/metrics"
)

func TestSharedLoadSurvivesCanceledWaiter(t *testing.T) {
	c := New[int](2, time.Minute, time.Second, &metrics.Metrics{})
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	load := func(ctx context.Context) (int, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return 42, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := c.Get(ctx, "key", load); first <- err }()
	<-started
	second := make(chan int, 1)
	go func() { v, _ := c.Get(context.Background(), "key", load); second <- v }()
	cancel()
	if !errors.Is(<-first, context.Canceled) {
		t.Fatal("first waiter not canceled")
	}
	close(release)
	if <-second != 42 || calls.Load() != 1 {
		t.Fatal("shared request was canceled or repeated")
	}
}
func TestBoundedTTLAndFailures(t *testing.T) {
	c := New[int](1, 5*time.Millisecond, time.Second, &metrics.Metrics{})
	calls := 0
	load := func(context.Context) (int, error) { calls++; return calls, nil }
	_, _ = c.Get(context.Background(), "a", load)
	_, _ = c.Get(context.Background(), "b", load)
	if len(c.items) != 1 {
		t.Fatal("cache unbounded")
	}
	time.Sleep(10 * time.Millisecond)
	_, _ = c.Get(context.Background(), "b", load)
	if calls != 3 {
		t.Fatal("expired data used")
	}
	bad := func(context.Context) (int, error) { calls++; return 0, errors.New("down") }
	_, _ = c.Get(context.Background(), "error", bad)
	_, _ = c.Get(context.Background(), "error", bad)
	if calls != 5 {
		t.Fatal("failure cached")
	}
}
