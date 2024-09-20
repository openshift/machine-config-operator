package common

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"sync/atomic"
)

func TestWrappedQueue(t *testing.T) {
	t.Parallel()

	testTimeout := time.Second * 5

	// We construct our own wrappedQueue for testing purposes since exponential
	// backoff is undesirable here since it will take significantly longer to run.
	wq := NewWrappedQueueForTesting(t)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	t.Cleanup(wq.Queue().ShutDown)

	wq.Start(ctx, 5)

	wg := sync.WaitGroup{}

	// Enqueues a function for the test and gives it an atomic counter which is
	// incremented on each call.
	enqueueFuncForTest := func(name string, f func(*atomic.Uint64) error) *atomic.Uint64 {
		var counter atomic.Uint64

		wq.EnqueueWithNameAndContext(ctx, name, func(_ context.Context) error {
			wg.Add(1)
			defer wg.Done()
			counter.Add(1)
			return f(&counter)
		})

		return &counter
	}

	// Enqueues a function for the test and gives it an atomic counter which is
	// incremented on each call.
	enqueueFuncForTestAfterDelay := func(name string, delay time.Duration, f func(*atomic.Uint64) error) *atomic.Uint64 {
		var counter atomic.Uint64

		wq.EnqueueAfterWithNameAndContext(ctx, name, delay, func(_ context.Context) error {
			wg.Add(1)
			defer wg.Done()
			counter.Add(1)
			return f(&counter)
		})

		return &counter
	}

	for i := 0; i <= 10; i++ {
		name := fmt.Sprintf("func-%d", i)
		enqueueFuncForTest(name, func(count *atomic.Uint64) error {
			if count.Load() > 1 {
				t.Fatalf("function %s should only execute once if no error is returned", name)
			}

			return nil
		})
	}

	retryableCount := enqueueFuncForTest("retryable error", func(count *atomic.Uint64) error {
		if count.Load() != 3 {
			return fmt.Errorf("retryable error")
		}

		return nil
	})

	pathologicallyFailingCount := enqueueFuncForTest("pathologically failing", func(_ *atomic.Uint64) error {
		return fmt.Errorf("pathologically failing")
	})

	shouldBeDroppedFromQueue := enqueueFuncForTest("should be dropped from queue", func(count *atomic.Uint64) error {
		if count.Load() > 1 {
			t.Fatalf("call count for should be dropped from queue exceeded expected value")
		}

		return fmt.Errorf("i should be dropped from the execution queue: %w", ErrDropFromQueue)
	})

	enqueuedTime := time.Now()
	delay := time.Millisecond * 10

	executesAfterDelay := enqueueFuncForTestAfterDelay("delayed execution", delay, func(count *atomic.Uint64) error {
		since := time.Since(enqueuedTime)

		if since < delay {
			t.Fatalf("should have executed after %s but executed after only %s", delay, since)
		}

		return nil
	})

	eventually := func(c func() bool) {
		assert.Eventually(t, c, time.Second*10, time.Millisecond)
	}

	eventually(func() bool {
		return executesAfterDelay.Load() == 1
	})

	eventually(func() bool {
		return wq.Queue().Len() == 0
	})

	eventually(func() bool {
		return retryableCount.Load() == 3
	})

	eventually(func() bool {
		return pathologicallyFailingCount.Load() > 10
	})

	eventually(func() bool {
		return shouldBeDroppedFromQueue.Load() == 1
	})

	wg.Wait()
}
