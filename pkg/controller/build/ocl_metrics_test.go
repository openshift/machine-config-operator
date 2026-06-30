package build

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// resetMetric deletes all label combinations for the given GaugeVec so tests
// don't bleed into each other.
func resetGaugeVec(g *prometheus.GaugeVec, labels prometheus.Labels) {
	g.DeletePartialMatch(labels)
}

func TestRecordImagePushStarted(t *testing.T) {
	t.Parallel()

	resetGaugeVec(oclImagePushState, prometheus.Labels{"pool": "worker"})

	RecordImagePushStarted("worker")

	v := testutil.ToFloat64(oclImagePushState.WithLabelValues("worker", StatePushing))
	if v != 1 {
		t.Errorf("expected ocl_image_push_state{state=%q} = 1, got %v", StatePushing, v)
	}
}

func TestRecordImagePushCompleted(t *testing.T) {
	t.Parallel()

	resetGaugeVec(oclImagePushState, prometheus.Labels{"pool": "worker2"})

	RecordImagePushStarted("worker2")
	RecordImagePushCompleted("worker2")

	gauge := testutil.ToFloat64(oclImagePushState.WithLabelValues("worker2", StateSucceeded))
	if gauge != 1 {
		t.Errorf("expected ocl_image_push_state{state=%q} = 1, got %v", StateSucceeded, gauge)
	}

	// pushing state should be cleared
	pushing := testutil.ToFloat64(oclImagePushState.WithLabelValues("worker2", StatePushing))
	if pushing != 0 {
		t.Errorf("expected ocl_image_push_state{state=%q} = 0 after completion, got %v", StatePushing, pushing)
	}
}

func TestRecordImagePushFailed(t *testing.T) {
	t.Parallel()

	resetGaugeVec(oclImagePushState, prometheus.Labels{"pool": "worker3"})

	RecordImagePushStarted("worker3")
	RecordImagePushFailed("worker3")

	gauge := testutil.ToFloat64(oclImagePushState.WithLabelValues("worker3", StateFailed))
	if gauge != 1 {
		t.Errorf("expected ocl_image_push_state{state=%q} = 1, got %v", StateFailed, gauge)
	}

	// pushing state should be cleared
	pushing := testutil.ToFloat64(oclImagePushState.WithLabelValues("worker3", StatePushing))
	if pushing != 0 {
		t.Errorf("expected ocl_image_push_state{state=%q} = 0 after failure, got %v", StatePushing, pushing)
	}
}

func TestImagePushTotalCounters(t *testing.T) {
	t.Parallel()

	// Use unique pool name to avoid counter bleed from other tests
	pool := "counter-test-pool"

	before := testutil.ToFloat64(oclImagePushTotal.WithLabelValues(pool, StateSucceeded))
	RecordImagePushCompleted(pool)
	after := testutil.ToFloat64(oclImagePushTotal.WithLabelValues(pool, StateSucceeded))
	if after-before != 1 {
		t.Errorf("expected ocl_image_push_total{state=%q} to increment by 1, got %v -> %v", StateSucceeded, before, after)
	}

	before = testutil.ToFloat64(oclImagePushTotal.WithLabelValues(pool, StateFailed))
	RecordImagePushFailed(pool)
	after = testutil.ToFloat64(oclImagePushTotal.WithLabelValues(pool, StateFailed))
	if after-before != 1 {
		t.Errorf("expected ocl_image_push_total{state=%q} to increment by 1, got %v -> %v", StateFailed, before, after)
	}
}

func TestUpdateOCLRolloutCounts(t *testing.T) {
	t.Parallel()

	pool := "rollout-pool"

	UpdateOCLRolloutCounts(pool, 3, 5)

	updated := testutil.ToFloat64(oclRolloutUpdatedNodes.WithLabelValues(pool))
	if updated != 3 {
		t.Errorf("expected ocl_rollout_updated_nodes = 3, got %v", updated)
	}

	total := testutil.ToFloat64(oclRolloutTotalNodes.WithLabelValues(pool))
	if total != 5 {
		t.Errorf("expected ocl_rollout_total_nodes = 5, got %v", total)
	}
}

func TestUpdateOCLRolloutCountsUpdates(t *testing.T) {
	t.Parallel()

	pool := "rollout-pool2"

	UpdateOCLRolloutCounts(pool, 2, 10)
	UpdateOCLRolloutCounts(pool, 8, 10)

	updated := testutil.ToFloat64(oclRolloutUpdatedNodes.WithLabelValues(pool))
	if updated != 8 {
		t.Errorf("expected ocl_rollout_updated_nodes to reflect latest value 8, got %v", updated)
	}
}

func TestActiveBuildsGauge(t *testing.T) {
	t.Parallel()

	pool := "active-builds-pool"
	oclActiveBuilds.WithLabelValues(pool).Set(0)

	RecordBuildStarted(pool)
	if v := testutil.ToFloat64(oclActiveBuilds.WithLabelValues(pool)); v != 1 {
		t.Errorf("expected ocl_active_builds = 1 after start, got %v", v)
	}

	RecordBuildStarted(pool)
	if v := testutil.ToFloat64(oclActiveBuilds.WithLabelValues(pool)); v != 2 {
		t.Errorf("expected ocl_active_builds = 2 after second start, got %v", v)
	}

	RecordBuildCompleted(pool, time.Now())
	if v := testutil.ToFloat64(oclActiveBuilds.WithLabelValues(pool)); v != 1 {
		t.Errorf("expected ocl_active_builds = 1 after completion, got %v", v)
	}

	RecordBuildFailed(pool, time.Now())
	if v := testutil.ToFloat64(oclActiveBuilds.WithLabelValues(pool)); v != 0 {
		t.Errorf("expected ocl_active_builds = 0 after failure, got %v", v)
	}
}

func TestActiveBuildsDecrementsOnInterrupted(t *testing.T) {
	t.Parallel()

	pool := "interrupted-pool"
	oclActiveBuilds.WithLabelValues(pool).Set(0)

	RecordBuildStarted(pool)
	RecordBuildInterrupted(pool)

	if v := testutil.ToFloat64(oclActiveBuilds.WithLabelValues(pool)); v != 0 {
		t.Errorf("expected ocl_active_builds = 0 after interruption, got %v", v)
	}
}

func TestBuildQueueDuration(t *testing.T) {
	t.Parallel()

	pool := "queue-pool"
	queuedAt := time.Now().Add(-30 * time.Second)

	before := testutil.CollectAndCount(oclBuildQueueDuration)
	RecordBuildQueueDuration(pool, queuedAt)
	after := testutil.CollectAndCount(oclBuildQueueDuration)

	if after <= before {
		t.Errorf("expected ocl_build_queue_duration_seconds to have more observations after recording, got count %v -> %v", before, after)
	}
}

func TestImagePushDurationRecorded(t *testing.T) {
	t.Parallel()

	pool := "push-duration-pool"

	before := testutil.CollectAndCount(oclImagePushDuration)
	RecordImagePushStarted(pool)
	time.Sleep(5 * time.Millisecond)
	RecordImagePushCompleted(pool)
	after := testutil.CollectAndCount(oclImagePushDuration)

	if after <= before {
		t.Errorf("expected ocl_image_push_duration_seconds to have more observations after push completed, got count %v -> %v", before, after)
	}
}

func TestImagePushDurationOnFailure(t *testing.T) {
	t.Parallel()

	pool := "push-duration-fail-pool"

	before := testutil.CollectAndCount(oclImagePushDuration)
	RecordImagePushStarted(pool)
	time.Sleep(5 * time.Millisecond)
	RecordImagePushFailed(pool)
	after := testutil.CollectAndCount(oclImagePushDuration)

	if after <= before {
		t.Errorf("expected ocl_image_push_duration_seconds to have more observations after push failed, got count %v -> %v", before, after)
	}
}
