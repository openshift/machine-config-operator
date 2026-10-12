package bootimage

import (
	"testing"

	opv1 "github.com/openshift/api/operator/v1"
	fakemcopclient "github.com/openshift/client-go/operator/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestVSphereCredentialsSecretEventHandlers(t *testing.T) {
	targetSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.VSphereCredentialsSecretName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string][]byte{"vcenter.example.com.username": []byte("old-user")},
	}
	unrelatedSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated",
			Namespace: ctrlcommon.MCONamespace,
		},
	}
	updatedSecret := targetSecret.DeepCopy()
	updatedSecret.Data["vcenter.example.com.username"] = []byte("new-user")
	metadataOnlyUpdate := targetSecret.DeepCopy()
	metadataOnlyUpdate.Annotations = map[string]string{"example.com/changed": "true"}

	tests := []struct {
		name          string
		handle        func(*Controller)
		expectEnqueue bool
	}{
		{
			name:          "target Secret add enqueues",
			handle:        func(ctrl *Controller) { ctrl.addVSphereCredentialsSecret(targetSecret) },
			expectEnqueue: true,
		},
		{
			name:          "unrelated Secret add is ignored",
			handle:        func(ctrl *Controller) { ctrl.addVSphereCredentialsSecret(unrelatedSecret) },
			expectEnqueue: false,
		},
		{
			name:          "target Secret data update enqueues",
			handle:        func(ctrl *Controller) { ctrl.updateVSphereCredentialsSecret(targetSecret, updatedSecret) },
			expectEnqueue: true,
		},
		{
			name:          "target Secret metadata-only update is ignored",
			handle:        func(ctrl *Controller) { ctrl.updateVSphereCredentialsSecret(targetSecret, metadataOnlyUpdate) },
			expectEnqueue: false,
		},
		{
			name: "unrelated Secret update is ignored",
			handle: func(ctrl *Controller) {
				ctrl.updateVSphereCredentialsSecret(unrelatedSecret, unrelatedSecret.DeepCopy())
			},
			expectEnqueue: false,
		},
		{
			name:          "target Secret delete enqueues",
			handle:        func(ctrl *Controller) { ctrl.deleteVSphereCredentialsSecret(targetSecret) },
			expectEnqueue: true,
		},
		{
			name: "target Secret tombstone delete enqueues",
			handle: func(ctrl *Controller) {
				ctrl.deleteVSphereCredentialsSecret(cache.DeletedFinalStateUnknown{
					Key: ctrlcommon.MCONamespace + "/" + ctrlcommon.VSphereCredentialsSecretName,
					Obj: targetSecret,
				})
			},
			expectEnqueue: true,
		},
		{
			name:          "unrelated Secret delete is ignored",
			handle:        func(ctrl *Controller) { ctrl.deleteVSphereCredentialsSecret(unrelatedSecret) },
			expectEnqueue: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &Controller{queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())}
			t.Cleanup(ctrl.queue.ShutDown)

			tc.handle(ctrl)
			assert.Equal(t, tc.expectEnqueue, ctrl.queue.Len() == 1)
		})
	}
}

func TestVSphereCredentialsSecretCorrectionClearsDegraded(t *testing.T) {
	conditions := getDefaultConditions()
	for i := range conditions {
		if conditions[i].Type == opv1.MachineConfigurationBootImageUpdateDegraded {
			conditions[i].Status = metav1.ConditionTrue
			conditions[i].Reason = "VSphereCredentialsSecretAdded"
			conditions[i].Message = "vSphere credentials were rejected"
		}
	}
	mcop := &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.MCOOperatorKnobsObjectName},
		Status:     opv1.MachineConfigurationStatus{Conditions: conditions},
	}
	oldSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.VSphereCredentialsSecretName, Namespace: ctrlcommon.MCONamespace},
		Data: map[string][]byte{
			"vcenter.example.com.username": []byte("wrong-user"),
			"vcenter.example.com.password": []byte("wrong-password"),
		},
	}
	correctedSecret := oldSecret.DeepCopy()
	correctedSecret.Data["vcenter.example.com.username"] = []byte("correct-user")
	correctedSecret.Data["vcenter.example.com.password"] = []byte("correct-password")

	ctrl := &Controller{
		mcopClient: fakemcopclient.NewSimpleClientset(mcop),
		queue:      workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
	}
	t.Cleanup(ctrl.queue.ShutDown)
	syncCalled := false
	ctrl.syncHandler = func(event string) error {
		syncCalled = true
		username, password := getVSphereCredentialsForServer(correctedSecret, "vcenter.example.com")
		require.Equal(t, "correct-user", username)
		require.Equal(t, "correct-password", password)
		ctrl.updateConditions(event, nil, opv1.MachineConfigurationBootImageUpdateDegraded)
		return nil
	}

	ctrl.updateVSphereCredentialsSecret(oldSecret, correctedSecret)
	require.Equal(t, 1, ctrl.queue.Len(), "credential correction must enqueue reconciliation")
	require.True(t, ctrl.processNextWorkItem())
	require.True(t, syncCalled)

	updated, err := ctrl.mcopClient.OperatorV1().MachineConfigurations().Get(t.Context(), ctrlcommon.MCOOperatorKnobsObjectName, metav1.GetOptions{})
	require.NoError(t, err)
	for _, condition := range updated.Status.Conditions {
		if condition.Type == opv1.MachineConfigurationBootImageUpdateDegraded {
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, "VSphereCredentialsSecretUpdated", condition.Reason)
			return
		}
	}
	t.Fatal("degraded condition not found")
}
