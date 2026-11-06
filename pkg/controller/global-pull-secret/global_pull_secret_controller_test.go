package globalpullsecret

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/openshift/machine-config-operator/pkg/controller/common"
)

type stubFeatureGateHandler struct{ enabled bool }

func (s *stubFeatureGateHandler) Connect(context.Context) error { return nil }

func (s *stubFeatureGateHandler) Enabled(configv1.FeatureGateName) bool     { return s.enabled }
func (s *stubFeatureGateHandler) Exists(configv1.FeatureGateName) bool      { return true }
func (s *stubFeatureGateHandler) KnownFeatures() []configv1.FeatureGateName { return nil }

func basePullSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.GlobalPullSecretName,
			Namespace: common.OpenshiftConfigNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.redhat.io":{"auth":"baseline-rh"}}}`),
		},
	}
}

func emptyMergedSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.GlobalPullSecretMergedName,
			Namespace: common.OpenshiftConfigNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}
}

func globalPullCredential(name, hostname, username, password string) *unstructured.Unstructured {
	credential := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "config.openshift.io/v1alpha1",
		"kind":       "GlobalPullCredential",
		"metadata": map[string]interface{}{
			"name":       name,
			"generation": int64(1),
		},
		"spec": map[string]interface{}{
			"hostname": hostname,
			"auth": map[string]interface{}{
				"username": base64.StdEncoding.EncodeToString([]byte(username)),
				"password": base64.StdEncoding.EncodeToString([]byte(password)),
			},
		},
	}}
	credential.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   globalPullCredentialGVR.Group,
		Version: globalPullCredentialGVR.Version,
		Kind:    "GlobalPullCredential",
	})
	return credential
}

func setupController(t *testing.T, enabled bool, kubeObjects []runtime.Object, credentials ...runtime.Object) (*Controller, *k8sfake.Clientset, *fake.FakeDynamicClient) {
	t.Helper()

	kubeClient := k8sfake.NewSimpleClientset(kubeObjects...)
	secretFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, 0, informers.WithNamespace(common.OpenshiftConfigNamespace))

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(globalPullCredentialGVR.GroupVersion().WithKind("GlobalPullCredential"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(globalPullCredentialGVR.GroupVersion().WithKind("GlobalPullCredentialList"), &unstructured.UnstructuredList{})
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{globalPullCredentialGVR: "GlobalPullCredentialList"},
		credentials...,
	)

	ctrl := New(secretFactory.Core().V1().Secrets(), dynamicClient, kubeClient, &stubFeatureGateHandler{enabled: enabled})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	secretFactory.Start(ctx.Done())
	if enabled {
		ctrl.dynamicFactory.Start(ctx.Done())
		require.True(t, cache.WaitForCacheSync(ctx.Done(), ctrl.secretListerSynced, ctrl.credentialListerSynced))
		return ctrl, kubeClient, dynamicClient
	}
	require.True(t, cache.WaitForCacheSync(ctx.Done(), ctrl.secretListerSynced))

	return ctrl, kubeClient, dynamicClient
}

func getSecret(t *testing.T, client *k8sfake.Clientset, name string) *corev1.Secret {
	t.Helper()
	secret, err := client.CoreV1().Secrets(common.OpenshiftConfigNamespace).Get(context.TODO(), name, metav1.GetOptions{})
	require.NoError(t, err)
	return secret
}

func TestController_Sync_PullSecretMissing_NoMergedSecretCreated(t *testing.T) {
	ctrl, client, _ := setupController(t, true, nil)

	require.NoError(t, ctrl.sync(context.Background()))

	_, err := client.CoreV1().Secrets(common.OpenshiftConfigNamespace).Get(context.TODO(), common.GlobalPullSecretMergedName, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}

func TestController_Sync_CreatesMergedCopyOfBaseline(t *testing.T) {
	ctrl, client, _ := setupController(t, true, []runtime.Object{basePullSecret(), emptyMergedSecret()})

	require.NoError(t, ctrl.sync(context.Background()))

	merged := getSecret(t, client, common.GlobalPullSecretMergedName)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, merged.Type)
	assert.Equal(t, managedByValue, merged.Labels[managedByLabel])
	assert.JSONEq(t, `{"auths":{"registry.redhat.io":{"auth":"baseline-rh"}}}`, string(merged.Data[corev1.DockerConfigJsonKey]))

	var applyAction k8stesting.PatchActionImpl
	for _, action := range client.Actions() {
		patch, ok := action.(k8stesting.PatchActionImpl)
		if ok && patch.GetNamespace() == common.OpenshiftConfigNamespace && patch.GetName() == common.GlobalPullSecretMergedName {
			applyAction = patch
			break
		}
	}
	require.NotNil(t, applyAction)
	assert.Equal(t, types.ApplyPatchType, applyAction.GetPatchType())
	assert.Equal(t, fieldManager, applyAction.PatchOptions.FieldManager)

	baseline := getSecret(t, client, common.GlobalPullSecretName)
	assert.JSONEq(t, `{"auths":{"registry.redhat.io":{"auth":"baseline-rh"}}}`, string(baseline.Data[corev1.DockerConfigJsonKey]))
}

func TestController_Sync_MergesCredentialAndUpdatesStatus(t *testing.T) {
	credential := globalPullCredential("quay", "quay.io", "user", "pass")
	ctrl, client, dynamicClient := setupController(t, true, []runtime.Object{basePullSecret(), emptyMergedSecret()}, credential)

	require.NoError(t, ctrl.sync(context.Background()))

	merged := getSecret(t, client, common.GlobalPullSecretMergedName)
	assert.Contains(t, string(merged.Data[corev1.DockerConfigJsonKey]), `"quay.io"`)

	updated, err := dynamicClient.Resource(globalPullCredentialGVR).Get(context.TODO(), "quay", metav1.GetOptions{})
	require.NoError(t, err)
	conditions, found, err := unstructured.NestedSlice(updated.Object, "status", "conditions")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, conditions, 1)

	condition, ok := conditions[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "Applied", condition["type"])
	assert.Equal(t, "True", condition["status"])
	assert.Equal(t, "Merged", condition["reason"])
	assert.Equal(t, int64(1), condition["observedGeneration"])
	assert.Contains(t, condition["message"], "merged into pull-secret-merged")
}

func TestController_Sync_BaselineWinsAndUpdatesStatus(t *testing.T) {
	credential := globalPullCredential("rh", "registry.redhat.io", "user", "pass")
	ctrl, client, dynamicClient := setupController(t, true, []runtime.Object{basePullSecret(), emptyMergedSecret()}, credential)

	require.NoError(t, ctrl.sync(context.Background()))

	merged := getSecret(t, client, common.GlobalPullSecretMergedName)
	assert.JSONEq(t, `{"auths":{"registry.redhat.io":{"auth":"baseline-rh"}}}`, string(merged.Data[corev1.DockerConfigJsonKey]))

	updated, err := dynamicClient.Resource(globalPullCredentialGVR).Get(context.TODO(), "rh", metav1.GetOptions{})
	require.NoError(t, err)
	conditions, found, err := unstructured.NestedSlice(updated.Object, "status", "conditions")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, conditions, 1)
	condition, ok := conditions[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "False", condition["status"])
	assert.Equal(t, "BaselinePrecedence", condition["reason"])
	assert.Contains(t, condition["message"], "baseline pull-secret credential")
}

func TestController_Sync_MalformedBase64_Fails(t *testing.T) {
	credential := globalPullCredential("malformed", "quay.io", "user", "pass")
	require.NoError(t, unstructured.SetNestedField(credential.Object, "not-base64!", "spec", "auth", "username"))
	ctrl, _, dynamicClient := setupController(t, true, []runtime.Object{basePullSecret(), emptyMergedSecret()}, credential)

	err := ctrl.sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding username")

	updated, getErr := dynamicClient.Resource(globalPullCredentialGVR).Get(context.TODO(), "malformed", metav1.GetOptions{})
	require.NoError(t, getErr)
	conditions, found, condErr := unstructured.NestedSlice(updated.Object, "status", "conditions")
	require.NoError(t, condErr)
	require.True(t, found)
	require.Len(t, conditions, 1)
	condition, ok := conditions[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "Applied", condition["type"])
	assert.Equal(t, "False", condition["status"])
	assert.Equal(t, "ReconcileError", condition["reason"])
}

func TestController_Sync_DecodedUsernameWithColon_Fails(t *testing.T) {
	credential := globalPullCredential("colon", "quay.io", "user:name", "pass")
	ctrl, _, dynamicClient := setupController(t, true, []runtime.Object{basePullSecret(), emptyMergedSecret()}, credential)

	err := ctrl.sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "username must not contain a colon")

	updated, getErr := dynamicClient.Resource(globalPullCredentialGVR).Get(context.TODO(), "colon", metav1.GetOptions{})
	require.NoError(t, getErr)
	conditions, found, condErr := unstructured.NestedSlice(updated.Object, "status", "conditions")
	require.NoError(t, condErr)
	require.True(t, found)
	require.Len(t, conditions, 1)
	condition, ok := conditions[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "Applied", condition["type"])
	assert.Equal(t, "False", condition["status"])
	assert.Equal(t, "ReconcileError", condition["reason"])
}

func TestController_Sync_DuplicateHostname_SetsAppliedFalse(t *testing.T) {
	credentials := []runtime.Object{
		globalPullCredential("first", "quay.io", "user", "pass"),
		globalPullCredential("second", "quay.io", "other", "secret"),
	}
	ctrl, _, dynamicClient := setupController(t, true, []runtime.Object{basePullSecret(), emptyMergedSecret()}, credentials...)

	err := ctrl.sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate hostname")

	for _, name := range []string{"first", "second"} {
		updated, getErr := dynamicClient.Resource(globalPullCredentialGVR).Get(context.TODO(), name, metav1.GetOptions{})
		require.NoError(t, getErr)
		conditions, found, condErr := unstructured.NestedSlice(updated.Object, "status", "conditions")
		require.NoError(t, condErr)
		require.True(t, found)
		require.Len(t, conditions, 1)
		condition, ok := conditions[0].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "Applied", condition["type"])
		assert.Equal(t, "False", condition["status"])
		assert.Equal(t, "ReconcileError", condition["reason"])
	}
}

func TestController_Run_FeatureGateDisabled_DoesNotStart(t *testing.T) {
	ctrl, client, _ := setupController(t, false, []runtime.Object{basePullSecret()})

	done := make(chan struct{})
	go func() {
		ctrl.Run(context.Background(), 1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return when the feature gate is disabled")
	}

	_, err := client.CoreV1().Secrets(common.OpenshiftConfigNamespace).Get(context.TODO(), common.GlobalPullSecretMergedName, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err))
}
