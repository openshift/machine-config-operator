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

type errDropFromQueue struct{}

func (e *errDropFromQueue) Error() string {
	return fmt.Sprint("drop from queue")
}

type errNotPathologicalFailure struct{}

func (e *errNotPathologicalFailure) Error() string {
	return fmt.Sprintf("no pathological failure")
}

// Any function which should not pathologically fail should return this error
// or have it someplace within the returned error chain.
var ErrPathological error = &errNotPathologicalFailure{}

// Any function that should stop execution and be dropped from the queue should
// return this error or have it someplace within the returned error chain.
var ErrDropFromQueue error = &errDropFromQueue{}

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
	queue workqueue.RateLimitingInterface
}

type WrappedQueueOpts struct {
	Name        string
	MaxRetries  int
	Period      time.Duration
	RetryAfter  time.Duration
	RateLimiter workqueue.RateLimiter
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
// unused testing.T instance to enforce the fact that it should not be used in
// production code.
func NewWrappedQueueForTesting(_ *testing.T, name string) *WrappedQueue {
	return NewWrappedQueueWithOpts(WrappedQueueOpts{
		Name:        name,
		MaxRetries:  5,
		Period:      time.Millisecond,
		RetryAfter:  time.Millisecond,
		RateLimiter: workqueue.NewWithMaxWaitRateLimiter(workqueue.DefaultControllerRateLimiter(), time.Millisecond),
	})
}

func NewWrappedQueueWithOpts(o WrappedQueueOpts) *WrappedQueue {
	rateLimiter := o.RateLimiter
	if rateLimiter == nil {
		rateLimiter = workqueue.DefaultControllerRateLimiter()
	}

	if o.Period == 0 {
		o.Period = time.Second
	}

	if o.RetryAfter == 0 {
		o.RetryAfter = time.Minute
	}

	return &WrappedQueue{
		WrappedQueueOpts: o,
		queue:            workqueue.NewNamedRateLimitingQueue(rateLimiter, o.Name),
	}
}

// Starts the workqueue process with the specified number of concurrent workers.
func (w *WrappedQueue) Start(ctx context.Context, workers int) {
	for i := 0; i <= workers; i++ {
		go wait.Until(w.worker, w.Period, ctx.Done())
	}
}

// Returns the queue object for situations where more nuanced usage is desired.
func (w *WrappedQueue) Queue() workqueue.RateLimitingInterface {
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
	// If no parent context was provided, use the background context.
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	// Create a child context from the provided context.
	ctx, cancel := context.WithCancel(parentCtx)

	if name == "" {
		name = fmt.Sprintf("<unnamed function in %s>", w.Name) // TODO: Figure out how to get original function name, if available.
	}

	exec := &executable{
		name: name,
		run: func() error {
			// Cancel the context when execution is complete.
			defer cancel()

			// Pass the child context into the provided function.
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
	key, quit := w.queue.Get()
	if quit {
		return false
	}

	// Regardless of error status, we want to mark the item as Done. We will
	// requeue it in the future if needed. The work queue object will keep track
	// of it.
	defer w.queue.Done(key)

	exec := key.(*executable)

	err := exec.run()
	w.handleErr(err, exec)
	return true
}

// Handles any errors associated with the execution of a work queue item. This
// Will pathologically retry execution whenever an error is returned by the
// worker function.
func (w *WrappedQueue) handleErr(err error, exec *executable) {
	// If no error has occurred, forget the item and move on.
	if err == nil {
		w.queue.Forget(exec)
		return
	}

	errsToDrop := map[string]error{
		"context cancellation":           context.Canceled,
		"context deadline exceeded":      context.DeadlineExceeded,
		"should not pathologically fail": ErrPathological,
		"dropped from queue":             ErrDropFromQueue,
	}

	for reason, errToDrop := range errsToDrop {
		if errors.Is(err, errToDrop) {
			utilruntime.HandleError(err)
			klog.Infof("Dropping item %s out of queue %s due to %s : %v", exec.name, w.Name, reason, err)
			w.queue.Forget(exec)
			return
		}
	}

	// If we have not hit the maximum number of retries yet, re-add the object so
	// that it will be retried later.
	if w.queue.NumRequeues(exec) < w.MaxRetries {
		klog.Infof("Error syncing %q in queue %s: %v", exec.name, w.Name, err)
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
