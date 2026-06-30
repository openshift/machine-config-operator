package build

import (
	"fmt"
	"sync"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/prometheus/client_golang/prometheus"
)

// pushStartTimes stores the time each image push began, keyed by "pool/buildName".
// Used to compute push duration across separate AddJob and UpdateJob reconciler events.
var pushStartTimes sync.Map

// OCL Build Metrics for tracking On-Cluster Layering processes
var (
	// oclBuildState tracks the current state of OCL builds per pool
	oclBuildState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_build_state",
			Help: "Current state of OCL build for a pool; gauge is 1 for the active state label, 0 otherwise",
		}, []string{"pool", "state"})

	// oclBuildDuration tracks how long OCL builds take to complete
	oclBuildDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ocl_build_duration_seconds",
			Help:    "Duration of OCL build processes in seconds",
			Buckets: []float64{60, 180, 300, 600, 900, 1200, 1800, 2400, 3000, 3600}, // 1m to 1h
		}, []string{"pool", "state"})

	// oclBuildStartTime tracks when a build started
	oclBuildStartTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_build_start_timestamp_seconds",
			Help: "Timestamp when OCL build started",
		}, []string{"pool"})

	// oclBuildEndTime tracks when a build completed/failed
	oclBuildEndTime = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_build_end_timestamp_seconds",
			Help: "Timestamp when OCL build completed or failed",
		}, []string{"pool", "state"})

	// oclBuildTotal counts total number of builds by state
	oclBuildTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ocl_build_total",
			Help: "Total number of OCL builds by final state",
		}, []string{"pool", "state"})

	// oclBuildJobState tracks the state of build jobs
	oclBuildJobState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_build_job_state",
			Help: "State of OCL build job; gauge is 1 for the active state label, 0 otherwise",
		}, []string{"pool", "state"})

	// oclConfigChangeTotal counts config changes triggering builds
	oclConfigChangeTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ocl_config_change_total",
			Help: "Total number of config changes triggering OCL builds",
		}, []string{"pool"})

	// oclBuildRetries tracks build retry attempts
	oclBuildRetries = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ocl_build_retries_total",
			Help: "Total number of OCL build retry attempts",
		}, []string{"pool"})

	// oclLayeredNodesCount tracks number of nodes using layered images
	oclLayeredNodesCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_layered_nodes_count",
			Help: "Number of nodes currently using OCL layered images",
		}, []string{"pool"})

	// oclImagePushState tracks the state of image push operations
	oclImagePushState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_image_push_state",
			Help: "State of OCL image push operations; gauge is 1 for the active state label, 0 otherwise",
		}, []string{"pool", "state"})

	// oclImagePushTotal counts total image push attempts by final state
	oclImagePushTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ocl_image_push_total",
			Help: "Total number of OCL image push operations by final state",
		}, []string{"pool", "state"})

	// oclRolloutUpdatedNodes tracks how many nodes have adopted the current layered image
	oclRolloutUpdatedNodes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_rollout_updated_nodes",
			Help: "Number of nodes in an OCL pool that have adopted the current layered image",
		}, []string{"pool"})

	// oclRolloutTotalNodes tracks total node count in an OCL pool
	oclRolloutTotalNodes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_rollout_total_nodes",
			Help: "Total number of nodes in an OCL pool",
		}, []string{"pool"})

	// oclBuildQueueDuration tracks time between build creation and job going active
	oclBuildQueueDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ocl_build_queue_duration_seconds",
			Help:    "Time in seconds between OCL build creation and the build job becoming active",
			Buckets: []float64{5, 15, 30, 60, 120, 300, 600},
		}, []string{"pool"})

	// oclImagePushDuration tracks how long image push operations take
	oclImagePushDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "ocl_image_push_duration_seconds",
			Help:    "Duration of OCL image push operations in seconds",
			Buckets: []float64{10, 30, 60, 120, 180, 300, 600},
		}, []string{"pool", "state"})

	// oclActiveBuilds tracks the number of builds currently in progress per pool
	oclActiveBuilds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ocl_active_builds",
			Help: "Number of OCL builds currently in progress per pool",
		}, []string{"pool"})
)

// Build state constants for consistent labeling
const (
	StateNone        = "none"
	StatePending     = "pending"
	StateBuilding    = "building"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateInterrupted = "interrupted"
	StatePushing     = "pushing"
)

// RegisterOCLMetrics registers all OCL-related Prometheus metrics
func RegisterOCLMetrics() error {
	err := ctrlcommon.RegisterMetrics([]prometheus.Collector{
		oclBuildState,
		oclBuildDuration,
		oclBuildStartTime,
		oclBuildEndTime,
		oclBuildTotal,
		oclBuildJobState,
		oclConfigChangeTotal,
		oclBuildRetries,
		oclLayeredNodesCount,
		oclImagePushState,
		oclImagePushTotal,
		oclRolloutUpdatedNodes,
		oclRolloutTotalNodes,
		oclBuildQueueDuration,
		oclImagePushDuration,
		oclActiveBuilds,
	})

	if err != nil {
		return fmt.Errorf("could not register OCL metrics: %w", err)
	}

	// Initialize GaugeVecs to ensure metrics are accessible even without values
	oclBuildState.WithLabelValues("init", StateNone).Set(0)
	oclBuildJobState.WithLabelValues("init", StateNone).Set(0)
	oclLayeredNodesCount.WithLabelValues("init").Set(0)
	oclImagePushState.WithLabelValues("init", StateNone).Set(0)
	oclRolloutUpdatedNodes.WithLabelValues("init").Set(0)
	oclRolloutTotalNodes.WithLabelValues("init").Set(0)
	oclActiveBuilds.WithLabelValues("init").Set(0)

	return nil
}

// RecordBuildStarted records when a build starts
func RecordBuildStarted(pool string) {
	now := float64(time.Now().Unix())

	// Clear previous states for this pool
	oclBuildState.DeletePartialMatch(prometheus.Labels{"pool": pool})

	// Set new state
	oclBuildState.WithLabelValues(pool, StatePending).Set(1)
	oclBuildStartTime.WithLabelValues(pool).Set(now)
	oclActiveBuilds.WithLabelValues(pool).Inc()
}

// RecordBuildBuilding records when a build transitions to building state
func RecordBuildBuilding(pool string) {
	// Clear pending state
	oclBuildState.DeletePartialMatch(prometheus.Labels{"pool": pool})

	// Set building state
	oclBuildState.WithLabelValues(pool, StateBuilding).Set(1)
}

// RecordBuildCompleted records when a build completes successfully
func RecordBuildCompleted(pool string, startTime time.Time) {
	now := time.Now()
	duration := now.Sub(startTime).Seconds()

	// Clear previous states
	oclBuildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	pushStartTimes.Delete(pool)

	// Set succeeded state
	oclBuildState.WithLabelValues(pool, StateSucceeded).Set(1)
	oclBuildEndTime.WithLabelValues(pool, StateSucceeded).Set(float64(now.Unix()))
	oclBuildDuration.WithLabelValues(pool, StateSucceeded).Observe(duration)
	oclBuildTotal.WithLabelValues(pool, StateSucceeded).Inc()
	oclActiveBuilds.WithLabelValues(pool).Dec()
}

// RecordBuildFailed records when a build fails
func RecordBuildFailed(pool string, startTime time.Time) {
	now := time.Now()
	duration := now.Sub(startTime).Seconds()

	// Clear previous states
	oclBuildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	pushStartTimes.Delete(pool)

	// Set failed state
	oclBuildState.WithLabelValues(pool, StateFailed).Set(1)
	oclBuildEndTime.WithLabelValues(pool, StateFailed).Set(float64(now.Unix()))
	oclBuildDuration.WithLabelValues(pool, StateFailed).Observe(duration)
	oclBuildTotal.WithLabelValues(pool, StateFailed).Inc()
	oclActiveBuilds.WithLabelValues(pool).Dec()
}

// RecordBuildInterrupted records when a build is interrupted
func RecordBuildInterrupted(pool string) {
	// Clear previous states
	oclBuildState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	pushStartTimes.Delete(pool)

	// Set interrupted state
	oclBuildState.WithLabelValues(pool, StateInterrupted).Set(1)
	oclBuildEndTime.WithLabelValues(pool, StateInterrupted).Set(float64(time.Now().Unix()))
	oclBuildTotal.WithLabelValues(pool, StateInterrupted).Inc()
	oclActiveBuilds.WithLabelValues(pool).Dec()
}

// RecordBuildJobState records the state of a build job
func RecordBuildJobState(pool, state string) {
	oclBuildJobState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	oclBuildJobState.WithLabelValues(pool, state).Set(1)
}

// RecordConfigChange records when a config change triggers a build
func RecordConfigChange(pool string) {
	oclConfigChangeTotal.WithLabelValues(pool).Inc()
}

// RecordBuildRetry records a build retry attempt
func RecordBuildRetry(pool string) {
	oclBuildRetries.WithLabelValues(pool).Inc()
}

// UpdateLayeredNodesCount updates the count of nodes using layered images
func UpdateLayeredNodesCount(pool string, count int) {
	oclLayeredNodesCount.WithLabelValues(pool).Set(float64(count))
}

// RecordImagePushStarted records when a build job becomes active (image push begins).
func RecordImagePushStarted(pool string) {
	oclImagePushState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	oclImagePushState.WithLabelValues(pool, StatePushing).Set(1)
	pushStartTimes.Store(pool, time.Now())
}

// RecordImagePushCompleted records a successful image push.
func RecordImagePushCompleted(pool string) {
	oclImagePushState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	oclImagePushState.WithLabelValues(pool, StateSucceeded).Set(1)
	oclImagePushTotal.WithLabelValues(pool, StateSucceeded).Inc()
	if start, ok := pushStartTimes.LoadAndDelete(pool); ok {
		oclImagePushDuration.WithLabelValues(pool, StateSucceeded).Observe(time.Since(start.(time.Time)).Seconds())
	}
}

// RecordImagePushFailed records a failed image push.
func RecordImagePushFailed(pool string) {
	oclImagePushState.DeletePartialMatch(prometheus.Labels{"pool": pool})
	oclImagePushState.WithLabelValues(pool, StateFailed).Set(1)
	oclImagePushTotal.WithLabelValues(pool, StateFailed).Inc()
	if start, ok := pushStartTimes.LoadAndDelete(pool); ok {
		oclImagePushDuration.WithLabelValues(pool, StateFailed).Observe(time.Since(start.(time.Time)).Seconds())
	}
}

// RecordBuildQueueDuration records how long a build waited before its job went active.
// queuedAt should be the MachineOSBuild's CreationTimestamp.
func RecordBuildQueueDuration(pool string, queuedAt time.Time) {
	oclBuildQueueDuration.WithLabelValues(pool).Observe(time.Since(queuedAt).Seconds())
}

// UpdateOCLRolloutCounts updates the OCL rollout node counts from MachineConfigPool status.
func UpdateOCLRolloutCounts(pool string, updatedNodes, totalNodes int32) {
	oclRolloutUpdatedNodes.WithLabelValues(pool).Set(float64(updatedNodes))
	oclRolloutTotalNodes.WithLabelValues(pool).Set(float64(totalNodes))
}
