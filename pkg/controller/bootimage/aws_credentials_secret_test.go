package bootimage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	opv1 "github.com/openshift/api/operator/v1"
	fakemcopclient "github.com/openshift/client-go/operator/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

const testAWSRegion = "us-east-1"

// resetAWSEC2ClientCache clears the package-level EC2 client cache before and after a test.
func resetAWSEC2ClientCache(t *testing.T) {
	t.Helper()
	reset := func() {
		awsEC2ClientCache.mu.Lock()
		defer awsEC2ClientCache.mu.Unlock()
		awsEC2ClientCache.entries = map[string]awsEC2ClientCacheEntry{}
	}
	reset()
	t.Cleanup(reset)
}

func newMCOAWSCredentialsSecret(resourceVersion string, data map[string]string) *corev1.Secret {
	secret := newTestAWSCredentialsSecret(data)
	secret.Name = ctrlcommon.AWSCredentialsSecretName
	secret.Namespace = ctrlcommon.MCONamespace
	secret.ResourceVersion = resourceVersion
	return secret
}

func staticAWSCredentialsData(accessKeyID, secretAccessKey string) map[string]string {
	return map[string]string{
		"aws_access_key_id":     accessKeyID,
		"aws_secret_access_key": secretAccessKey,
		"credentials":           "[default]\naws_access_key_id = " + accessKeyID + "\naws_secret_access_key = " + secretAccessKey,
	}
}

func writeTestTokenFile(t *testing.T) string {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("header.payload.signature"), 0o600))
	return tokenFile
}

func TestNewAWSEC2Client(t *testing.T) {
	t.Run("static credentials", func(t *testing.T) {
		secret := newMCOAWSCredentialsSecret("1", staticAWSCredentialsData(testAWSAccessKeyID, testAWSSecretKey))
		client, err := newAWSEC2Client(testAWSRegion, secret)
		require.NoError(t, err)
		assert.Equal(t, testAWSRegion, client.Options().Region)

		creds, err := client.Options().Credentials.Retrieve(t.Context())
		require.NoError(t, err)
		assert.Equal(t, testAWSAccessKeyID, creds.AccessKeyID)
		assert.Equal(t, testAWSSecretKey, creds.SecretAccessKey)
	})

	t.Run("STS web identity credentials profile", func(t *testing.T) {
		tokenFile := writeTestTokenFile(t)
		secret := newMCOAWSCredentialsSecret("1", map[string]string{
			"credentials": "[default]\nsts_regional_endpoints = regional\nrole_arn = " + testAWSRoleARN + "\nweb_identity_token_file = " + tokenFile,
		})
		client, err := newAWSEC2Client(testAWSRegion, secret)
		require.NoError(t, err)
		assert.Equal(t, testAWSRegion, client.Options().Region)
		assert.IsType(t, &aws.CredentialsCache{}, client.Options().Credentials)
	})

	t.Run("STS web identity token file is not mounted", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing-token")
		secret := newMCOAWSCredentialsSecret("1", map[string]string{
			"credentials": "[default]\nrole_arn = " + testAWSRoleARN + "\nweb_identity_token_file = " + missing,
		})
		_, err := newAWSEC2Client(testAWSRegion, secret)
		require.Error(t, err)
		assert.Contains(t, err.Error(), ctrlcommon.MCONamespace+"/"+ctrlcommon.AWSCredentialsSecretName)
		assert.Contains(t, err.Error(), "must mount the projected service account token")
	})

	t.Run("STS web identity token file is a FIFO", func(t *testing.T) {
		fifo := filepath.Join(t.TempDir(), "token")
		makeTestFIFO(t, fifo)
		secret := newMCOAWSCredentialsSecret("1", map[string]string{
			"credentials": "[default]\nrole_arn = " + testAWSRoleARN + "\nweb_identity_token_file = " + fifo,
		})
		done := make(chan error, 1)
		go func() {
			_, err := newAWSEC2Client(testAWSRegion, secret)
			done <- err
		}()
		select {
		case err := <-done:
			require.Error(t, err)
			assert.Contains(t, err.Error(), ctrlcommon.MCONamespace+"/"+ctrlcommon.AWSCredentialsSecretName)
			assert.Contains(t, err.Error(), "is not a regular file")
		case <-time.After(5 * time.Second):
			t.Fatal("newAWSEC2Client blocked on a FIFO token file")
		}
	})

	t.Run("unusable Secret", func(t *testing.T) {
		_, err := newAWSEC2Client(testAWSRegion, newMCOAWSCredentialsSecret("1", map[string]string{"aws_access_key_id": testAWSAccessKeyID}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "incomplete static credentials")
		assertNoAWSSecretValues(t, err.Error())
	})
}

func TestGetAWSEC2Client(t *testing.T) {
	t.Run("reads only the MCO Secret, caches it and follows rotation, deletion and recreation", func(t *testing.T) {
		resetAWSEC2ClientCache(t)
		ctx := t.Context()
		kubeClient := fakeclientset.NewSimpleClientset(newMCOAWSCredentialsSecret("1", staticAWSCredentialsData(testAWSAccessKeyID, testAWSSecretKey)))

		first, err := getAWSEC2Client(ctx, testAWSRegion, kubeClient)
		require.NoError(t, err)
		creds, err := first.Options().Credentials.Retrieve(ctx)
		require.NoError(t, err)
		assert.Equal(t, testAWSAccessKeyID, creds.AccessKeyID)

		cached, err := getAWSEC2Client(ctx, testAWSRegion, kubeClient)
		require.NoError(t, err)
		assert.Same(t, first, cached, "an unchanged Secret must reuse the cached client")

		// Rotation: a new resourceVersion rebuilds the client with the new keys.
		_, err = kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(ctx,
			newMCOAWSCredentialsSecret("2", staticAWSCredentialsData(testAWSOtherAccessKey, testAWSOtherSecretKey)), metav1.UpdateOptions{})
		require.NoError(t, err)
		rotated, err := getAWSEC2Client(ctx, testAWSRegion, kubeClient)
		require.NoError(t, err)
		assert.NotSame(t, first, rotated)
		creds, err = rotated.Options().Credentials.Retrieve(ctx)
		require.NoError(t, err)
		assert.Equal(t, testAWSOtherAccessKey, creds.AccessKeyID)

		// Deletion: the cached client is not used once the Secret is gone.
		require.NoError(t, kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Delete(ctx, ctrlcommon.AWSCredentialsSecretName, metav1.DeleteOptions{}))
		_, err = getAWSEC2Client(ctx, testAWSRegion, kubeClient)
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err))

		// Recovery: recreating the Secret restores client creation.
		_, err = kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(ctx,
			newMCOAWSCredentialsSecret("3", staticAWSCredentialsData(testAWSAccessKeyID, testAWSSecretKey)), metav1.CreateOptions{})
		require.NoError(t, err)
		recovered, err := getAWSEC2Client(ctx, testAWSRegion, kubeClient)
		require.NoError(t, err)
		creds, err = recovered.Options().Credentials.Retrieve(ctx)
		require.NoError(t, err)
		assert.Equal(t, testAWSAccessKeyID, creds.AccessKeyID)
	})

	t.Run("does not fall back to the Machine API Secret", func(t *testing.T) {
		resetAWSEC2ClientCache(t)
		legacy := newMCOAWSCredentialsSecret("1", staticAWSCredentialsData(testAWSAccessKeyID, testAWSSecretKey))
		legacy.Namespace = ctrlcommon.MachineAPINamespace
		kubeClient := fakeclientset.NewSimpleClientset(legacy)

		_, err := getAWSEC2Client(t.Context(), testAWSRegion, kubeClient)
		require.Error(t, err)
		assert.True(t, apierrors.IsNotFound(err))
		assert.Contains(t, err.Error(), ctrlcommon.MCONamespace+"/"+ctrlcommon.AWSCredentialsSecretName)
	})

	t.Run("invalid credentials are not cached and a corrected Secret recovers", func(t *testing.T) {
		resetAWSEC2ClientCache(t)
		ctx := t.Context()
		kubeClient := fakeclientset.NewSimpleClientset(newMCOAWSCredentialsSecret("1", map[string]string{"aws_access_key_id": testAWSAccessKeyID}))

		_, err := getAWSEC2Client(ctx, testAWSRegion, kubeClient)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "incomplete static credentials")
		assertNoAWSSecretValues(t, err.Error())

		_, err = kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(ctx,
			newMCOAWSCredentialsSecret("2", staticAWSCredentialsData(testAWSAccessKeyID, testAWSSecretKey)), metav1.UpdateOptions{})
		require.NoError(t, err)
		client, err := getAWSEC2Client(ctx, testAWSRegion, kubeClient)
		require.NoError(t, err)
		require.NotNil(t, client)
	})
}

func TestAWSCredentialsSecretEventHandlers(t *testing.T) {
	targetSecret := newMCOAWSCredentialsSecret("1", staticAWSCredentialsData(testAWSAccessKeyID, testAWSSecretKey))
	updatedSecret := newMCOAWSCredentialsSecret("2", staticAWSCredentialsData(testAWSOtherAccessKey, testAWSOtherSecretKey))
	metadataOnlyUpdate := targetSecret.DeepCopy()
	metadataOnlyUpdate.ResourceVersion = "2"
	metadataOnlyUpdate.Annotations = map[string]string{"example.com/changed": "true"}
	unrelatedSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: ctrlcommon.MCONamespace}}
	vsphereSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.VSphereCredentialsSecretName, Namespace: ctrlcommon.MCONamespace}}
	legacySecret := targetSecret.DeepCopy()
	legacySecret.Namespace = ctrlcommon.MachineAPINamespace

	tests := []struct {
		name          string
		handle        func(*Controller)
		expectedEvent string
	}{
		{
			name:          "target Secret add enqueues",
			handle:        func(ctrl *Controller) { ctrl.addAWSCredentialsSecret(targetSecret) },
			expectedEvent: "AWSCredentialsSecretAdded",
		},
		{
			name:   "unrelated Secret add is ignored",
			handle: func(ctrl *Controller) { ctrl.addAWSCredentialsSecret(unrelatedSecret) },
		},
		{
			name:   "vSphere credentials Secret add is ignored",
			handle: func(ctrl *Controller) { ctrl.addAWSCredentialsSecret(vsphereSecret) },
		},
		{
			name:   "same-named Secret in another namespace is ignored",
			handle: func(ctrl *Controller) { ctrl.addAWSCredentialsSecret(legacySecret) },
		},
		{
			name:          "target Secret data update enqueues",
			handle:        func(ctrl *Controller) { ctrl.updateAWSCredentialsSecret(targetSecret, updatedSecret) },
			expectedEvent: "AWSCredentialsSecretUpdated",
		},
		{
			name:   "target Secret metadata-only update is ignored",
			handle: func(ctrl *Controller) { ctrl.updateAWSCredentialsSecret(targetSecret, metadataOnlyUpdate) },
		},
		{
			name:   "unrelated Secret update is ignored",
			handle: func(ctrl *Controller) { ctrl.updateAWSCredentialsSecret(unrelatedSecret, unrelatedSecret.DeepCopy()) },
		},
		{
			name:          "target Secret delete enqueues",
			handle:        func(ctrl *Controller) { ctrl.deleteAWSCredentialsSecret(targetSecret) },
			expectedEvent: "AWSCredentialsSecretDeleted",
		},
		{
			name: "target Secret tombstone delete enqueues",
			handle: func(ctrl *Controller) {
				ctrl.deleteAWSCredentialsSecret(cache.DeletedFinalStateUnknown{
					Key: ctrlcommon.MCONamespace + "/" + ctrlcommon.AWSCredentialsSecretName,
					Obj: targetSecret,
				})
			},
			expectedEvent: "AWSCredentialsSecretDeleted",
		},
		{
			name:   "unrelated Secret delete is ignored",
			handle: func(ctrl *Controller) { ctrl.deleteAWSCredentialsSecret(unrelatedSecret) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &Controller{queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())}
			t.Cleanup(ctrl.queue.ShutDown)

			tc.handle(ctrl)
			if tc.expectedEvent == "" {
				assert.Equal(t, 0, ctrl.queue.Len())
				return
			}
			require.Equal(t, 1, ctrl.queue.Len())
			event, _ := ctrl.queue.Get()
			assert.Equal(t, tc.expectedEvent, event)
			ctrl.queue.Done(event)
		})
	}
}

func TestAWSCredentialsSecretCorrectionClearsDegraded(t *testing.T) {
	resetAWSEC2ClientCache(t)
	conditions := getDefaultConditions()
	for i := range conditions {
		if conditions[i].Type == opv1.MachineConfigurationBootImageUpdateDegraded {
			conditions[i].Status = metav1.ConditionTrue
			conditions[i].Reason = "AWSCredentialsSecretAdded"
			conditions[i].Message = "incomplete static credentials"
		}
	}
	mcop := &opv1.MachineConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.MCOOperatorKnobsObjectName},
		Status:     opv1.MachineConfigurationStatus{Conditions: conditions},
	}
	oldSecret := newMCOAWSCredentialsSecret("1", map[string]string{"aws_access_key_id": testAWSAccessKeyID})
	correctedSecret := newMCOAWSCredentialsSecret("2", staticAWSCredentialsData(testAWSAccessKeyID, testAWSSecretKey))
	kubeClient := fakeclientset.NewSimpleClientset(correctedSecret)

	ctrl := &Controller{
		mcopClient: fakemcopclient.NewSimpleClientset(mcop),
		queue:      workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
	}
	t.Cleanup(ctrl.queue.ShutDown)
	syncCalled := false
	ctrl.syncHandler = func(event string) error {
		syncCalled = true
		client, err := getAWSEC2Client(t.Context(), testAWSRegion, kubeClient)
		require.NoError(t, err)
		creds, err := client.Options().Credentials.Retrieve(t.Context())
		require.NoError(t, err)
		require.Equal(t, testAWSAccessKeyID, creds.AccessKeyID)
		ctrl.updateConditions(event, nil, opv1.MachineConfigurationBootImageUpdateDegraded)
		return nil
	}

	ctrl.updateAWSCredentialsSecret(oldSecret, correctedSecret)
	require.Equal(t, 1, ctrl.queue.Len(), "credential correction must enqueue reconciliation")
	require.True(t, ctrl.processNextWorkItem())
	require.True(t, syncCalled)

	updated, err := ctrl.mcopClient.OperatorV1().MachineConfigurations().Get(t.Context(), ctrlcommon.MCOOperatorKnobsObjectName, metav1.GetOptions{})
	require.NoError(t, err)
	for _, condition := range updated.Status.Conditions {
		if condition.Type == opv1.MachineConfigurationBootImageUpdateDegraded {
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, "AWSCredentialsSecretUpdated", condition.Reason)
			return
		}
	}
	t.Fatal("degraded condition not found")
}
