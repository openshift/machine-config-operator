package fixtures

import (
	"context"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMachineOSBuildStatusSubresourceSemantics(t *testing.T) {
	t.Run("empty clients", func(t *testing.T) {
		_, mcfgclient, _ := GetEmptyClientsForTest(t)
		assertMachineOSBuildStatusSubresourceSemantics(t, mcfgclient)
	})

	t.Run("default clients", func(t *testing.T) {
		_, mcfgclient, _, _, _, _ := GetClientsForTest(t)
		assertMachineOSBuildStatusSubresourceSemantics(t, mcfgclient)
	})
}

func assertMachineOSBuildStatusSubresourceSemantics(t *testing.T, mcfgclient *fakeclientmachineconfigv1.Clientset) {
	t.Helper()
	ctx := context.Background()
	builds := mcfgclient.MachineconfigurationV1().MachineOSBuilds()

	creationTime := metav1.NewTime(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	deletionTime := metav1.NewTime(time.Date(2026, time.February, 3, 4, 5, 6, 0, time.UTC))
	managedFieldsTime := metav1.NewTime(time.Date(2026, time.January, 2, 3, 5, 0, 0, time.UTC))
	initial := &mcfgv1.MachineOSBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "status-subresource-test",
			UID:               types.UID("status-subresource-test-uid"),
			ResourceVersion:   "23",
			Generation:        7,
			CreationTimestamp: creationTime,
			DeletionTimestamp: &deletionTime,
			Finalizers:        []string{"machineconfiguration.openshift.io/test-finalizer"},
			ManagedFields: []metav1.ManagedFieldsEntry{{
				Manager:    "status-subresource-test-manager",
				Operation:  metav1.ManagedFieldsOperationUpdate,
				APIVersion: mcfgv1.GroupVersion.String(),
				Time:       &managedFieldsTime,
				FieldsType: "FieldsV1",
				FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:labels":{}}}`)},
			}},
		},
		Spec: mcfgv1.MachineOSBuildSpec{
			MachineConfig:         mcfgv1.MachineConfigReference{Name: "rendered-worker-test"},
			MachineOSConfig:       mcfgv1.MachineOSConfigReference{Name: "worker-os-config"},
			RenderedImagePushSpec: "registry.example.com/example/image:latest",
		},
	}
	require.NoError(t, mcfgclient.Tracker().Add(initial))

	stored, err := builds.Get(ctx, initial.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assertServerManagedMetadata(t, initial, stored)
	assertManagedFieldManagers(t, stored, "status-subresource-test-manager")

	// Capture the metadata writer's stale object before the controller writes
	// Prepared through the status subresource. JobImageBuilder follows this
	// ordering when it adds the Job UID annotation.
	staleMetadataUpdate := stored.DeepCopy()
	statusUpdate := stored.DeepCopy()
	statusUpdate.Status.Conditions = []metav1.Condition{{
		Type:   string(mcfgv1.MachineOSBuildPrepared),
		Status: metav1.ConditionTrue,
	}}
	returnedStatus, err := builds.UpdateStatus(ctx, statusUpdate, metav1.UpdateOptions{FieldManager: "build-controller"})
	require.NoError(t, err)
	expectedAfterStatus := initial.DeepCopy()
	expectedAfterStatus.Status = statusUpdate.Status
	expectedAfterStatus.ManagedFields = returnedStatus.ManagedFields
	require.Equal(t, expectedAfterStatus, returnedStatus, "UpdateStatus must return the server representation")
	stored, err = builds.Get(ctx, initial.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, expectedAfterStatus, stored, "UpdateStatus must persist the returned representation")
	assertServerManagedMetadata(t, initial, returnedStatus)
	assertServerManagedMetadata(t, initial, stored)
	assertManagedFieldManagers(t, returnedStatus, "status-subresource-test-manager", "build-controller")
	assertManagedFieldManagers(t, stored, "status-subresource-test-manager", "build-controller")
	assertManagedFieldEntry(t, initial.ManagedFields[0], returnedStatus)
	assertManagedFieldEntry(t, initial.ManagedFields[0], stored)

	staleMetadataUpdate.Annotations = map[string]string{"machineconfiguration.openshift.io/job-uid": "job-uid"}
	staleMetadataUpdate.Labels = map[string]string{"machineconfiguration.openshift.io/test-label": "updated"}
	returnedUpdate, err := builds.Update(ctx, staleMetadataUpdate, metav1.UpdateOptions{FieldManager: "metadata-writer"})
	require.NoError(t, err)

	expectedAfterUpdate := staleMetadataUpdate.DeepCopy()
	expectedAfterUpdate.Status = statusUpdate.Status
	expectedAfterUpdate.ManagedFields = returnedUpdate.ManagedFields
	require.Equal(t, expectedAfterUpdate, returnedUpdate, "Update must return the server representation")
	stored, err = builds.Get(ctx, initial.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, expectedAfterUpdate, stored, "Update must persist the returned representation")
	assertServerManagedMetadata(t, initial, returnedUpdate)
	assertServerManagedMetadata(t, initial, stored)
	assertManagedFieldManagers(t, returnedUpdate, "metadata-writer")
	assertManagedFieldManagers(t, stored, "metadata-writer")

	// Conversely, a status-subresource update based on stale metadata/spec may
	// update status only; it must not roll back the stored main resource.
	staleStatusUpdate := statusUpdate.DeepCopy()
	staleStatusUpdate.Status.Conditions[0].Type = string(mcfgv1.MachineOSBuilding)
	returnedStatus, err = builds.UpdateStatus(ctx, staleStatusUpdate, metav1.UpdateOptions{FieldManager: "build-controller"})
	require.NoError(t, err)

	expectedAfterStatus = expectedAfterUpdate.DeepCopy()
	expectedAfterStatus.Status = staleStatusUpdate.Status
	expectedAfterStatus.ManagedFields = returnedStatus.ManagedFields
	require.Equal(t, expectedAfterStatus, returnedStatus, "UpdateStatus must return current main-resource fields with the new status")
	stored, err = builds.Get(ctx, initial.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, expectedAfterStatus, stored, "UpdateStatus must persist the returned representation")
	assertServerManagedMetadata(t, initial, returnedStatus)
	assertServerManagedMetadata(t, initial, stored)
	assertManagedFieldManagers(t, returnedStatus, "metadata-writer", "build-controller")
	assertManagedFieldManagers(t, stored, "metadata-writer", "build-controller")
	assertManagedFieldEntry(t, returnedUpdate.ManagedFields[0], returnedStatus)
	assertManagedFieldEntry(t, returnedUpdate.ManagedFields[0], stored)
}

func assertServerManagedMetadata(t *testing.T, expected, actual *mcfgv1.MachineOSBuild) {
	t.Helper()
	require.Equal(t, expected.UID, actual.UID)
	require.Equal(t, expected.ResourceVersion, actual.ResourceVersion)
	require.Equal(t, expected.Generation, actual.Generation)
	require.Equal(t, expected.CreationTimestamp, actual.CreationTimestamp)
	require.Equal(t, expected.DeletionTimestamp, actual.DeletionTimestamp)
}

func assertManagedFieldManagers(t *testing.T, build *mcfgv1.MachineOSBuild, expected ...string) {
	t.Helper()
	managers := make([]string, 0, len(build.ManagedFields))
	for _, entry := range build.ManagedFields {
		managers = append(managers, entry.Manager)
	}
	require.ElementsMatch(t, expected, managers)
}

func assertManagedFieldEntry(t *testing.T, expected metav1.ManagedFieldsEntry, build *mcfgv1.MachineOSBuild) {
	t.Helper()
	for _, actual := range build.ManagedFields {
		if actual.Manager == expected.Manager {
			require.Equal(t, expected, actual)
			return
		}
	}
	require.Failf(t, "managed field entry not found", "manager %q is absent from %#v", expected.Manager, build.ManagedFields)
}
