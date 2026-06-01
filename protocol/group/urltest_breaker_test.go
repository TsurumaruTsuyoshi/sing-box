package group

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutboundBreakerTripsAndRecovers(t *testing.T) {
	var breaker outboundBreaker
	now := time.Unix(123, 0).UnixNano()

	for i := int32(1); i < urlTestBreakerThreshold; i++ {
		if breaker.recordFailure(now) {
			t.Fatalf("breaker tripped after %d failures", i)
		}
	}
	if !breaker.recordFailure(now) {
		t.Fatal("breaker did not trip at threshold")
	}
	expectedUntil := time.Unix(0, now).Add(urlTestBreakerCooldown).UnixNano()
	if until := breaker.until(); until != expectedUntil {
		t.Fatalf("unexpected cooldown deadline: got %d, want %d", until, expectedUntil)
	}
	if !breaker.tripped(now) {
		t.Fatal("breaker is not tripped during cooldown")
	}

	if breaker.recordFailure(now + time.Second.Nanoseconds()) {
		t.Fatal("failure during cooldown tripped breaker again")
	}
	if until := breaker.until(); until != expectedUntil {
		t.Fatalf("failure during cooldown extended deadline: got %d, want %d", until, expectedUntil)
	}

	breaker.recordSuccess()
	if breaker.tripped(now) {
		t.Fatal("success did not clear cooldown")
	}
	for i := int32(1); i < urlTestBreakerThreshold; i++ {
		if breaker.recordFailure(now) {
			t.Fatalf("failure count was not reset by success; tripped after %d failures", i)
		}
	}
}

func TestURLTestGroupIgnoresCallerCancellation(t *testing.T) {
	breaker := &outboundBreaker{}
	group := URLTestGroup{breakersTCP: map[string]*outboundBreaker{"test": breaker}}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer deadlineCancel()

	for _, test := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"canceled caller", canceledCtx, context.Canceled},
		{"expired caller deadline", deadlineCtx, context.DeadlineExceeded},
		{"internal cancellation", context.Background(), context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			for range urlTestBreakerThreshold {
				group.recordFailure(test.ctx, "tcp", "test", test.err)
			}
			if breaker.failures != 0 || breaker.until() != 0 {
				t.Fatalf("cancellation changed breaker state: failures=%d, until=%d", breaker.failures, breaker.until())
			}
		})
	}
}

func TestOutboundBreakerConcurrentFailuresTripOnce(t *testing.T) {
	var breaker outboundBreaker
	now := time.Unix(123, 0).UnixNano()
	const goroutines = 100
	var trips atomic.Int32
	var waitGroup sync.WaitGroup
	waitGroup.Add(goroutines)
	for range goroutines {
		go func() {
			defer waitGroup.Done()
			if breaker.recordFailure(now) {
				trips.Add(1)
			}
		}()
	}
	waitGroup.Wait()

	if got := trips.Load(); got != 1 {
		t.Fatalf("concurrent failures produced %d trips, want 1", got)
	}
	expectedUntil := time.Unix(0, now).Add(urlTestBreakerCooldown).UnixNano()
	if until := breaker.until(); until != expectedUntil {
		t.Fatalf("concurrent failures changed cooldown deadline: got %d, want %d", until, expectedUntil)
	}
}
