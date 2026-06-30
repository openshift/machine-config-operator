package build

import (
	"strings"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
)

func newTestOCLRecorder(bufSize int) (*OCLEventRecorder, *record.FakeRecorder) {
	fake := record.NewFakeRecorder(bufSize)
	return NewOCLEventRecorder(fake), fake
}

func assertEvent(t *testing.T, events chan string, wantType, wantReason string) {
	t.Helper()
	select {
	case event := <-events:
		if !strings.HasPrefix(event, wantType+" ") {
			t.Errorf("expected event type %q, got: %s", wantType, event)
		}
		if !strings.Contains(event, wantReason) {
			t.Errorf("expected event reason %q, got: %s", wantReason, event)
		}
	default:
		t.Errorf("expected event %q %q but none was recorded", wantType, wantReason)
	}
}

func assertNoEvent(t *testing.T, events chan string) {
	t.Helper()
	select {
	case event := <-events:
		t.Errorf("expected no event but got: %s", event)
	default:
	}
}

func testMOSB(name string) *mcfgv1.MachineOSBuild {
	return &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineOSBuildSpec{
			MachineConfig: mcfgv1.MachineConfigReference{Name: "rendered-worker-abc"},
		},
	}
}

func testMOSC(name, pool string) *mcfgv1.MachineOSConfig {
	return &mcfgv1.MachineOSConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineOSConfigSpec{
			MachineConfigPool: mcfgv1.MachineConfigPoolReference{Name: pool},
		},
	}
}

func testMCP(name string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func testJob(name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func TestBuildLifecycleEvents(t *testing.T) {
	t.Parallel()

	mosb := testMOSB("test-build")
	mosc := testMOSC("test-config", "worker")

	tests := []struct {
		name       string
		record     func(*OCLEventRecorder)
		wantType   string
		wantReason string
	}{
		{"BuildStarted", func(r *OCLEventRecorder) { r.RecordBuildStarted(mosb, mosc) }, "Normal", EventBuildStarted},
		{"BuildPreparing", func(r *OCLEventRecorder) { r.RecordBuildPreparing(mosb, "creating job") }, "Normal", EventBuildPreparing},
		{"BuildBuilding", func(r *OCLEventRecorder) { r.RecordBuildBuilding(mosb) }, "Normal", EventBuildBuilding},
		{"BuildCompleted", func(r *OCLEventRecorder) { r.RecordBuildCompleted(mosb, "quay.io/test@sha256:abc") }, "Normal", EventBuildCompleted},
		{"BuildFailed", func(r *OCLEventRecorder) { r.RecordBuildFailed(mosb) }, "Warning", EventBuildFailed},
		{"BuildInterrupted", func(r *OCLEventRecorder) { r.RecordBuildInterrupted(mosb, "job deleted") }, "Warning", EventBuildInterrupted},
		{"BuildRetrying", func(r *OCLEventRecorder) { r.RecordBuildRetrying(mosb) }, "Normal", EventBuildRetrying},
		{"BuildDeleted", func(r *OCLEventRecorder) { r.RecordBuildDeleted(mosb, "removed") }, "Normal", EventBuildDeleted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder, fake := newTestOCLRecorder(1)
			tt.record(recorder)
			assertEvent(t, fake.Events, tt.wantType, tt.wantReason)
			assertNoEvent(t, fake.Events)
		})
	}
}

func TestJobEvents(t *testing.T) {
	t.Parallel()

	mosb := testMOSB("test-build")
	job := testJob("test-job")

	tests := []struct {
		name       string
		record     func(*OCLEventRecorder)
		wantType   string
		wantReason string
	}{
		{"JobCreated", func(r *OCLEventRecorder) { r.RecordJobCreated(mosb, job) }, "Normal", EventJobCreated},
		{"JobStarted", func(r *OCLEventRecorder) { r.RecordJobStarted(mosb, job) }, "Normal", EventJobStarted},
		{"JobCompleted", func(r *OCLEventRecorder) { r.RecordJobCompleted(mosb, job) }, "Normal", EventJobCompleted},
		{"JobFailed", func(r *OCLEventRecorder) { r.RecordJobFailed(mosb, job) }, "Warning", EventJobFailed},
		{"JobDeleted", func(r *OCLEventRecorder) { r.RecordJobDeleted(mosb, job.Name) }, "Normal", EventJobDeleted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder, fake := newTestOCLRecorder(1)
			tt.record(recorder)
			assertEvent(t, fake.Events, tt.wantType, tt.wantReason)
			assertNoEvent(t, fake.Events)
		})
	}
}

func TestConfigEvents(t *testing.T) {
	t.Parallel()

	mosc := testMOSC("test-config", "worker")

	tests := []struct {
		name       string
		record     func(*OCLEventRecorder)
		wantType   string
		wantReason string
	}{
		{"ConfigChanged", func(r *OCLEventRecorder) { r.RecordConfigChanged(mosc, "rendered-worker-old", "rendered-worker-new") }, "Normal", EventConfigChanged},
		{"ConfigReconciling", func(r *OCLEventRecorder) { r.RecordConfigReconciling(mosc) }, "Normal", EventConfigReconciling},
		{"ConfigReconciled", func(r *OCLEventRecorder) { r.RecordConfigReconciled(mosc) }, "Normal", EventConfigReconciled},
		{"RebuildRequested", func(r *OCLEventRecorder) { r.RecordRebuildRequested(mosc, "annotation applied") }, "Normal", EventRebuildRequested},
		{"ConfigDeleted", func(r *OCLEventRecorder) { r.RecordConfigDeleted(mosc) }, "Normal", EventConfigDeleted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder, fake := newTestOCLRecorder(1)
			tt.record(recorder)
			assertEvent(t, fake.Events, tt.wantType, tt.wantReason)
			assertNoEvent(t, fake.Events)
		})
	}
}

func TestCleanupAndDegradationEvents(t *testing.T) {
	t.Parallel()

	mosb := testMOSB("test-build")
	mosc := testMOSC("test-config", "worker")
	mcp := testMCP("worker")

	tests := []struct {
		name       string
		record     func(*OCLEventRecorder)
		wantType   string
		wantReason string
	}{
		{"CleanupStarted", func(r *OCLEventRecorder) { r.RecordCleanupStarted(mosb) }, "Normal", EventCleanupStarted},
		{"CleanupCompleted", func(r *OCLEventRecorder) { r.RecordCleanupCompleted(mosb) }, "Normal", EventCleanupCompleted},
		{"CleanupFailed", func(r *OCLEventRecorder) { r.RecordCleanupFailed(mosb) }, "Warning", EventCleanupFailed},
		{"BuildDegraded", func(r *OCLEventRecorder) { r.RecordBuildDegraded(mosc, mcp) }, "Warning", EventBuildDegraded},
		{"BuildRecovered", func(r *OCLEventRecorder) { r.RecordBuildRecovered(mosc, mcp) }, "Normal", EventBuildRecovered},
		{"PoolConfigChanged", func(r *OCLEventRecorder) { r.RecordPoolConfigChanged(mcp, "rendered-worker-old", "rendered-worker-new") }, "Normal", EventConfigChanged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder, fake := newTestOCLRecorder(1)
			tt.record(recorder)
			assertEvent(t, fake.Events, tt.wantType, tt.wantReason)
			assertNoEvent(t, fake.Events)
		})
	}
}

// TestBuildStartedMessageContent verifies the message includes the pool and rendered config name.
func TestBuildStartedMessageContent(t *testing.T) {
	t.Parallel()

	mosb := testMOSB("test-build")
	mosc := testMOSC("test-config", "worker")

	recorder, fake := newTestOCLRecorder(1)
	recorder.RecordBuildStarted(mosb, mosc)

	event := <-fake.Events
	if !strings.Contains(event, "worker") {
		t.Errorf("expected event to contain pool name %q, got: %s", "worker", event)
	}
	if !strings.Contains(event, "rendered-worker-abc") {
		t.Errorf("expected event to contain config name %q, got: %s", "rendered-worker-abc", event)
	}
}

// TestConfigChangedMessageContent verifies old and new config names appear in the message.
func TestConfigChangedMessageContent(t *testing.T) {
	t.Parallel()

	mosc := testMOSC("test-config", "worker")
	recorder, fake := newTestOCLRecorder(1)
	recorder.RecordConfigChanged(mosc, "rendered-worker-old", "rendered-worker-new")

	event := <-fake.Events
	if !strings.Contains(event, "rendered-worker-old") {
		t.Errorf("expected event to contain old config name, got: %s", event)
	}
	if !strings.Contains(event, "rendered-worker-new") {
		t.Errorf("expected event to contain new config name, got: %s", event)
	}
}
