package build

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
)

// Event types for OCL processes
const (
	// Build lifecycle events
	EventBuildStarted     = "BuildStarted"
	EventBuildPreparing   = "BuildPreparing"
	EventBuildBuilding    = "BuildBuilding"
	EventBuildCompleted   = "BuildCompleted"
	EventBuildFailed      = "BuildFailed"
	EventBuildInterrupted = "BuildInterrupted"
	EventBuildDeleted     = "BuildDeleted"

	// Job events
	EventJobCreated   = "JobCreated"
	EventJobStarted   = "JobStarted"
	EventJobCompleted = "JobCompleted"
	EventJobFailed    = "JobFailed"
	EventJobDeleted   = "JobDeleted"

	// Config events
	EventConfigReconciled      = "ConfigReconciled"
	EventConfigReconcileFailed = "ConfigReconcileFailed"
	EventRebuildRequested      = "RebuildRequested"

	// Config deletion events
	EventConfigDeleted = "ConfigDeleted"

	// MachineConfigPool events
	EventPoolConfigChanged = "PoolConfigChanged"

	// Degradation events
	EventBuildDegraded  = "BuildDegraded"
	EventBuildRecovered = "BuildRecovered"
)

// OCLEventRecorder wraps the Kubernetes event recorder with OCL-specific event helpers
type OCLEventRecorder struct {
	recorder record.EventRecorder
}

// NewOCLEventRecorder creates a new OCL event recorder
func NewOCLEventRecorder(recorder record.EventRecorder) *OCLEventRecorder {
	return &OCLEventRecorder{
		recorder: recorder,
	}
}

// Build lifecycle event recording

// RecordBuildStarted records when a build starts
func (r *OCLEventRecorder) RecordBuildStarted(mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildStarted,
		fmt.Sprintf("Started build for pool %q with config %q",
			mosc.Spec.MachineConfigPool.Name, mosb.Spec.MachineConfig.Name))
}

// RecordBuildPreparing records when a build is in the preparing phase
func (r *OCLEventRecorder) RecordBuildPreparing(mosb *mcfgv1.MachineOSBuild, message string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildPreparing,
		fmt.Sprintf("Preparing build: %s", message))
}

// RecordBuildBuilding records when a build transitions to building state
func (r *OCLEventRecorder) RecordBuildBuilding(mosb *mcfgv1.MachineOSBuild) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildBuilding,
		"Build is now in progress")
}

// RecordBuildCompleted records when a build completes successfully
func (r *OCLEventRecorder) RecordBuildCompleted(mosb *mcfgv1.MachineOSBuild, imagePullspec string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildCompleted,
		fmt.Sprintf("Build completed successfully, image: %s", imagePullspec))
}

// RecordBuildFailed records when a build fails
func (r *OCLEventRecorder) RecordBuildFailed(mosb *mcfgv1.MachineOSBuild) {
	r.recorder.Event(mosb, corev1.EventTypeWarning, EventBuildFailed,
		fmt.Sprintf("Build failed; see MachineOSBuild %q status conditions for details", mosb.Name))
}

// RecordBuildInterrupted records when a build is interrupted
func (r *OCLEventRecorder) RecordBuildInterrupted(mosb *mcfgv1.MachineOSBuild, reason string) {
	r.recorder.Event(mosb, corev1.EventTypeWarning, EventBuildInterrupted,
		fmt.Sprintf("Build interrupted: %s", reason))
}

// RecordBuildDeleted records when a build is deleted
func (r *OCLEventRecorder) RecordBuildDeleted(mosb *mcfgv1.MachineOSBuild, reason string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventBuildDeleted,
		fmt.Sprintf("Build deleted: %s", reason))
}

// Job event recording

// RecordJobCreated records when a build job is created
func (r *OCLEventRecorder) RecordJobCreated(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobCreated,
		fmt.Sprintf("Created build job: %s", job.Name))
}

// RecordJobStarted records when a build job starts
func (r *OCLEventRecorder) RecordJobStarted(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobStarted,
		fmt.Sprintf("Build job started: %s", job.Name))
}

// RecordJobCompleted records when a build job completes
func (r *OCLEventRecorder) RecordJobCompleted(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobCompleted,
		fmt.Sprintf("Build job completed: %s", job.Name))
}

// RecordJobFailed records when a build job fails
func (r *OCLEventRecorder) RecordJobFailed(mosb *mcfgv1.MachineOSBuild, job *batchv1.Job) {
	r.recorder.Event(mosb, corev1.EventTypeWarning, EventJobFailed,
		fmt.Sprintf("Build job %q failed; see MachineOSBuild %q status conditions for details", job.Name, mosb.Name))
}

// RecordJobDeleted records when a build job is deleted
func (r *OCLEventRecorder) RecordJobDeleted(mosb *mcfgv1.MachineOSBuild, jobName string) {
	r.recorder.Event(mosb, corev1.EventTypeNormal, EventJobDeleted,
		fmt.Sprintf("Build job deleted: %s", jobName))
}

// Config event recording

// RecordConfigReconciled records when a MachineOSConfig spec change is detected and acted on.
func (r *OCLEventRecorder) RecordConfigReconciled(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventConfigReconciled,
		"MachineOSConfig spec change detected and reconciled: new build created or reused")
}

// RecordConfigReconcileFailed records when a MachineOSConfig spec change could not be reconciled.
func (r *OCLEventRecorder) RecordConfigReconcileFailed(mosc *mcfgv1.MachineOSConfig, err error) {
	r.recorder.Event(mosc, corev1.EventTypeWarning, EventConfigReconcileFailed,
		fmt.Sprintf("Failed to reconcile MachineOSConfig spec change: %v", err))
}

// RecordRebuildRequested records when a rebuild is requested
func (r *OCLEventRecorder) RecordRebuildRequested(mosc *mcfgv1.MachineOSConfig, reason string) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventRebuildRequested,
		fmt.Sprintf("Rebuild requested: %s", reason))
}

// RecordConfigDeleted records when a MachineOSConfig is deleted
func (r *OCLEventRecorder) RecordConfigDeleted(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventConfigDeleted,
		fmt.Sprintf("MachineOSConfig %q deleted, removing associated builds", mosc.Name))
}

// Degradation event recording

// RecordBuildDegraded records when a build enters degraded state
func (r *OCLEventRecorder) RecordBuildDegraded(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeWarning, EventBuildDegraded,
		fmt.Sprintf("Build for pool %q degraded; see MachineOSBuild status conditions for details", mosc.Spec.MachineConfigPool.Name))
}

// RecordBuildRecovered records when a build recovers from degraded state
func (r *OCLEventRecorder) RecordBuildRecovered(mosc *mcfgv1.MachineOSConfig) {
	r.recorder.Event(mosc, corev1.EventTypeNormal, EventBuildRecovered,
		fmt.Sprintf("Build for pool %q recovered from degraded state", mosc.Spec.MachineConfigPool.Name))
}

// MachineConfigPool events

// RecordPoolConfigChanged records when a pool's config changes
func (r *OCLEventRecorder) RecordPoolConfigChanged(mcp *mcfgv1.MachineConfigPool, oldConfig, newConfig string) {
	r.recorder.Event(mcp, corev1.EventTypeNormal, EventPoolConfigChanged,
		fmt.Sprintf("Rendered config changed from %s to %s", oldConfig, newConfig))
}
