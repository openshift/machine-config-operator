package globalpullsecret

import (
	"context"
	"encoding/json"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	fake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// stubFeatureGateHandler is a minimal test stub that enables all gates.
type stubFeatureGateHandler struct{}

func (s *stubFeatureGateHandler) Connect(_ context.Context) error           { return nil }
func (s *stubFeatureGateHandler) Enabled(_ configv1.FeatureGateName) bool   { return true }
func (s *stubFeatureGateHandler) Exists(_ configv1.FeatureGateName) bool    { return true }
func (s *stubFeatureGateHandler) KnownFeatures() []configv1.FeatureGateName { return nil }

func basePullSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.GlobalPullSecretName,
			Namespace: ctrlcommon.OpenshiftConfigNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.redhat.io":{"auth":"baseline-rh"}}}`),
		},
	}
}

func labeledSecret(name, priority string, auths map[string]interface{}) *corev1.Secret {
	data, _ := json.Marshal(map[string]interface{}{"auths": auths})
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.OpenshiftConfigNamespace,
			Labels:    map[string]string{GlobalPullSecretLabel: "true"},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: data},
	}
	if priority != "" {
		s.Annotations = map[string]string{GlobalPullSecretPriorityAnnotation: priority}
	}
	return s
}

// setupController creates a Controller backed by fake clients for testing.
func setupController(t *testing.T, objects ...runtime.Object) (*Controller, *fake.Clientset) {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	factory := informers.NewSharedInformerFactoryWithOptions(client, 0,
		informers.WithNamespace(ctrlcommon.OpenshiftConfigNamespace),
	)
	secretInformer := factory.Core().V1().Secrets()

	ctrl := New(secretInformer, client, &stubFeatureGateHandler{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factory.Start(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), ctrl.secretListerSynced)

	return ctrl, client
}

func TestController_Sync_CapturesBaseline(t *testing.T) {
	ctrl, _ := setupController(t, basePullSecret())

	require.NoError(t, ctrl.sync())
	assert.NotNil(t, ctrl.baseline)
	assert.Contains(t, string(ctrl.baseline), "baseline-rh")
}

func TestController_Sync_MergesModularSecret(t *testing.T) {
	mod := labeledSecret("team-quay", "5", map[string]interface{}{
		"quay.io": map[string]interface{}{"auth": "quay-tok"},
	})

	ctrl, client := setupController(t, basePullSecret(), mod)

	require.NoError(t, ctrl.sync())

	// Fetch updated pull-secret from fake client.
	updated, err := client.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(
		context.TODO(), ctrlcommon.GlobalPullSecretName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(updated.Data[corev1.DockerConfigJsonKey], &merged))
	assert.Contains(t, merged.Auths, "quay.io")
	assert.Contains(t, merged.Auths, "registry.redhat.io")
}

func TestController_Sync_NoModular_NoUpdate(t *testing.T) {
	ctrl, client := setupController(t, basePullSecret())

	require.NoError(t, ctrl.sync())

	// Read back: should still be baseline, no extra keys.
	updated, err := client.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(
		context.TODO(), ctrlcommon.GlobalPullSecretName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(updated.Data[corev1.DockerConfigJsonKey], &merged))
	assert.Len(t, merged.Auths, 1)
	assert.Contains(t, merged.Auths, "registry.redhat.io")
}

func TestController_Sync_BaselineNeverOverwritten(t *testing.T) {
	mod := labeledSecret("evil", "", map[string]interface{}{
		"registry.redhat.io": map[string]interface{}{"auth": "evil-tok"},
	})
	ctrl, client := setupController(t, basePullSecret(), mod)

	require.NoError(t, ctrl.sync())

	updated, err := client.CoreV1().Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(
		context.TODO(), ctrlcommon.GlobalPullSecretName, metav1.GetOptions{},
	)
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(updated.Data[corev1.DockerConfigJsonKey], &merged))

	// Baseline key must keep its original value.
	assert.JSONEq(t, `{"auth":"baseline-rh"}`, string(merged.Auths["registry.redhat.io"]))
}
