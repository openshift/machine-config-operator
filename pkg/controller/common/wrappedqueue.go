package common

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// Any function that should not be retried when returning an error should
// either return this sentinal error or include it within its error chain,
// e.g., return errors.Join(err, ErrDropFromQueue).
var ErrDropFromQueue = errors.New("drop from queue")

// Functions are not hashable, which is a core requirement of the workqueue
// interface. However, structs are. So WrappedQueue attaches functions to an
// instance of this struct before adding them into the queue.
type executable struct {
	name string
	run  func() error
}

// WrappedQueue serves the following purposes:
// 1. Abstract the minutae of setting up and managing a workqueue away from a
// controller.
// 2. Enable having multiple work queues per controller.
// 3. Enables the addition of functions onto the work queue instead of just
// generic objects which are stored and retrieved using a string key.
type WrappedQueue struct {
	WrappedQueueOpts
	queue workqueue.TypedRateLimitingInterface[*executable]
}

type WrappedQueueOpts struct {
	Name       string
	MaxRetries int
	Period     time.Duration
	RetryAfter time.Duration
}

// Constructs a new wrapped queue instance with the specified number of retries
// and name.
func NewWrappedQueue(name string) *WrappedQueue {
	return NewWrappedQueueWithOpts(WrappedQueueOpts{
		Name: name,
	})
}

// Constructs a new WrappedQueue instance for use in unit / integration tests.
// Its options are tuned specifically for speed. This function accepts an
// unused testing.T instance which it uses to apply the test name to the
// WrappedQueue name.
func NewWrappedQueueForTesting(t *testing.T) *WrappedQueue {
	wq := NewWrappedQueueWithOpts(WrappedQueueOpts{
		MaxRetries: 5,
		Period:     time.Millisecond,
		RetryAfter: time.Millisecond,
		Name:       t.Name(),
	})

	rl := workqueue.NewTypedWithMaxWaitRateLimiter[*executable](
		workqueue.DefaultTypedControllerRateLimiter[*executable](),
		time.Millisecond)

	cfg := workqueue.TypedRateLimitingQueueConfig[*executable]{
		Name: t.Name(),
	}

	wq.queue = workqueue.NewTypedRateLimitingQueueWithConfig[*executable](rl, cfg)

	return wq
}

func NewWrappedQueueWithOpts(o WrappedQueueOpts) *WrappedQueue {
	if o.Period == 0 {
		o.Period = time.Second
	}

	if o.RetryAfter == 0 {
		o.RetryAfter = time.Minute
	}

	cfg := workqueue.TypedRateLimitingQueueConfig[*executable]{
		Name: o.Name,
	}

	rl := workqueue.DefaultTypedControllerRateLimiter[*executable]()

	return &WrappedQueue{
		WrappedQueueOpts: o,
		queue:            workqueue.NewTypedRateLimitingQueueWithConfig[*executable](rl, cfg),
	}
}

// Starts the workqueue process with the specified number of concurrent workers.
func (w *WrappedQueue) Start(ctx context.Context, workers int) {
	for i := 0; i <= workers; i++ {
		go wait.Until(w.worker, w.Period, ctx.Done())
	}
}

// Returns the queue object for situations where more nuanced usage is desired.
func (w *WrappedQueue) Queue() workqueue.TypedRateLimitingInterface[*executable] {
	return w.queue
}

// Shuts down the queue.
func (w *WrappedQueue) ShutDown() {
	w.queue.ShutDown()
}

// Enqueues a function with a name that will be included in any error logs.
func (w *WrappedQueue) EnqueueWithName(name string, toRun func() error) {
	w.enqueueExecutable(context.TODO(), name, 0, func(_ context.Context) error {
		return toRun()
	})
}

// Enqueues a function with a name and a context that will be passed into the
// function. A child context will be created from the provided context and will
// be canceled upon completion.
func (w *WrappedQueue) EnqueueWithNameAndContext(ctx context.Context, name string, toRun func(context.Context) error) {
	w.enqueueExecutable(ctx, name, 0, toRun)
}

// Enqueues a function with a context that will be passed into the function. A
// child context will be created from the provided context and will be canceled
// upon completion.
func (w *WrappedQueue) EnqueueWithContext(ctx context.Context, toRun func(context.Context) error) {
	w.enqueueExecutable(ctx, "", 0, toRun)
}

// Enqueues a function which will be executed as soon as a worker becomes free.
func (w *WrappedQueue) Enqueue(toRun func() error) {
	w.EnqueueWithName("", toRun)
}

// Enqueues a function with a name that will be included in any error logs.
// This will be executed after the specified delay has passed.
func (w *WrappedQueue) EnqueueAfterWithName(name string, delay time.Duration, toRun func() error) {
	w.enqueueExecutable(context.TODO(), name, delay, func(_ context.Context) error {
		return toRun()
	})
}

// Enqueues a function with a name and a context that will be passed into the
// function. A child context will be created from the provided context and will
// be canceled upon completion.
// This will be executed after the specified delay has passed.
func (w *WrappedQueue) EnqueueAfterWithNameAndContext(ctx context.Context, name string, delay time.Duration, toRun func(context.Context) error) {
	w.enqueueExecutable(ctx, name, delay, toRun)
}

// Enqueues a function with a context that will be passed into the function. A
// child context will be created from the provided context and will be canceled
// upon completion.
// This will be executed after the specified delay has passed.
func (w *WrappedQueue) EnqueueAfterWithContext(ctx context.Context, delay time.Duration, toRun func(context.Context) error) {
	w.enqueueExecutable(ctx, "", delay, toRun)
}

// Enqueues a function which will be executed as soon as a worker becomes free.
// This will be executed after the specified delay has passed.
func (w *WrappedQueue) EnqueueAfter(delay time.Duration, toRun func() error) {
	w.EnqueueAfterWithName("", delay, toRun)
}

func (w *WrappedQueue) enqueueExecutable(parentCtx context.Context, name string, delay time.Duration, toRun func(context.Context) error) {
	if name == "" {
		name = fmt.Sprintf("<unnamed function in %s>", w.Name) // TODO: Figure out how to get original function name, if available.
	}

	exec := &executable{
		name: name,
		run: func() error {
			// Create a child context from the provided context. This is done here so
			// that a new child context will be created if toRun() is retried.
			ctx, cancel := context.WithCancel(parentCtx)

			// Cancel the child context when execution is complete.
			defer cancel()

			// Pass the child context into the provided function and run it.
			return toRun(ctx)
		},
	}

	if delay == 0 {
		w.queue.Add(exec)
	} else {
		w.queue.AddAfter(exec, delay)
	}
}

// The main execution loop of the work queue.
func (w *WrappedQueue) worker() {
	for w.processNextWorkItem() {

	}
}

// Executes the next work item, retrying if any errors have occurred.
func (w *WrappedQueue) processNextWorkItem() bool {
	exec, quit := w.queue.Get()
	if quit {
		return false
	}

	// Regardless of error status, we want to mark the item as Done. We will
	// requeue it in the future if needed. The work queue object will keep track
	// of it.
	defer w.queue.Done(exec)

	err := exec.run()
	w.handleErr(err, exec)
	return true
}

// Handles any errors associated with the execution of a work queue item. This
// Will retry execution whenever an error is returned by the worker function
// until the returned error is nil. If this behavior is undesirable, the
// function should return ErrDropFromQueue or include ErrDropFromQueue within
// its returned error chain. Doing so will cause the function to be dropped
// from the queue.
func (w *WrappedQueue) handleErr(err error, exec *executable) {
	// If no error has occurred, forget the item and move on.
	if err == nil {
		w.queue.Forget(exec)
		return
	}

	// If this is (or contains) ErrDropFromQueue, drop it and do not attempt to retry.
	if errors.Is(err, ErrDropFromQueue) {
		utilruntime.HandleError(err)
		klog.Infof("Dropping item %s out of queue %s due to %s : %v", exec.name, w.Name, "ErrDropFromQueue being found in the error chain", err)
		w.queue.Forget(exec)
		return
	}

	// If we have not yet hit the max retries, re-add the object so that it will
	// be retried later.
	if w.queue.NumRequeues(exec) < w.MaxRetries {
		klog.Infof("Error executing %q in queue %s: %v", exec.name, w.Name, err)
		utilruntime.HandleError(err)
		w.queue.AddRateLimited(exec)
		return
	}

	// If we're above the maximum retries, drop the item from the queue and try
	// again later.
	utilruntime.HandleError(err)
	klog.Infof("Dropping item %s out of queue %s: %v", exec.name, w.Name, err)
	w.queue.Forget(exec)
	w.queue.AddAfter(exec, w.RetryAfter)
}
