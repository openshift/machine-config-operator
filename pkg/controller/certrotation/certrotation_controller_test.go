package certrotationcontroller

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/certrotation"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	kubeinformers "k8s.io/client-go/informers"

	fakearoclientset "github.com/Azure/ARO-RP/pkg/operator/clientset/versioned/fake"
	fakeconfigv1client "github.com/openshift/client-go/config/clientset/versioned/fake"
	fakemachineclientset "github.com/openshift/client-go/machine/clientset/versioned/fake"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

var (
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	kubeClient    *fake.Clientset
	configClient  *fakeconfigv1client.Clientset
	machineClient *fakemachineclientset.Clientset
	aroClient     *fakearoclientset.Clientset

	maoSecretLister    []*corev1.Secret
	mcoSecretLister    []*corev1.Secret
	mcoConfigMapLister []*corev1.ConfigMap

	objects        []runtime.Object
	configObjects  []runtime.Object
	machineObjects []runtime.Object
	aroObjects     []runtime.Object
	k8sI           kubeinformers.SharedInformerFactory

	controller *CertRotationController
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.configObjects = []runtime.Object{}
	f.machineObjects = []runtime.Object{}
	f.aroObjects = []runtime.Object{}
	return f
}

func (f *fixture) newController() *CertRotationController {

	// Only set platform to Azure for the ARO test case
	var platformStatus *configv1.PlatformStatus
	if len(f.aroObjects) > 0 {
		platformStatus = &configv1.PlatformStatus{
			Type: configv1.AzurePlatformType,
		}
	}

	f.configObjects = append(f.configObjects, &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			PlatformStatus:       platformStatus,
			APIServerInternalURL: "test-url"},
	})

	f.kubeClient = fake.NewSimpleClientset(f.objects...)
	f.configClient = fakeconfigv1client.NewSimpleClientset(f.configObjects...)
	f.machineClient = fakemachineclientset.NewSimpleClientset(f.machineObjects...)
	f.aroClient = fakearoclientset.NewSimpleClientset(f.aroObjects...)
	f.k8sI = kubeinformers.NewSharedInformerFactory(f.kubeClient, noResyncPeriodFunc())

	for _, secret := range f.maoSecretLister {
		f.k8sI.Core().V1().Secrets().Informer().GetIndexer().Add(secret)
	}

	for _, secret := range f.mcoSecretLister {
		f.k8sI.Core().V1().Secrets().Informer().GetIndexer().Add(secret)
	}

	for _, configMap := range f.mcoConfigMapLister {
		f.k8sI.Core().V1().ConfigMaps().Informer().GetIndexer().Add(configMap)
	}

	c, err := New(f.kubeClient, f.configClient, f.machineClient, f.aroClient, f.k8sI.Core().V1().Secrets(), f.k8sI.Core().V1().Secrets(), f.k8sI.Core().V1().ConfigMaps())
	require.NoError(f.t, err)

	c.StartInformers()
	require.NoError(f.t, err)

	return c
}

func (f *fixture) runController() {

	err := f.controller.Sync()
	require.NoError(f.t, err)

	f.controller.reconcileUserDataSecrets()
}

func (f *fixture) verifyUserDataSecretUpdateCount(expectedCount int) {
	count := 0
	for _, action := range f.kubeClient.Actions() {
		f.t.Log(action.GetVerb(), action.GetResource(), action.GetNamespace(), action.GetSubresource())
		if action.GetVerb() == "update" && action.GetResource().Resource == "secrets" && action.GetNamespace() == ctrlcommon.MachineAPINamespace {
			count++
		}
	}
	require.Equal(f.t, expectedCount, count)
}

func (f *fixture) verifyAROIPInTLSCertificate(t *testing.T, expectedIP string) {
	// Get the TLS secret that should contain the certificate with ARO IP
	tlsSecret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerTLSSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, tlsSecret)

	// Get the certificate data from the secret
	certData, exists := tlsSecret.Data["tls.crt"]
	require.True(t, exists, "TLS certificate should exist in secret")
	require.NotEmpty(t, certData, "TLS certificate data should not be empty")

	// Decode PEM block to get the certificate
	block, _ := pem.Decode(certData)
	require.NotNil(t, block, "Should be able to decode PEM certificate")
	require.Equal(t, "CERTIFICATE", block.Type, "PEM block should be a certificate")

	// Parse the certificate
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err, "Should be able to parse TLS certificate")

	// Verify the ARO IP is present in the certificate's Subject Alternative Names
	expectedIPAddr := net.ParseIP(expectedIP)
	require.NotNil(t, expectedIPAddr, "Expected ARO IP should be valid")

	found := false
	for _, ip := range cert.IPAddresses {
		if ip.Equal(expectedIPAddr) {
			found = true
			break
		}
	}
	require.True(t, found, "ARO IP %s should be present in certificate SAN IP addresses", expectedIP)
	t.Logf("Successfully verified ARO IP %s is present in TLS certificate", expectedIP)
}

func TestMCSCARotation(t *testing.T) {
	tests := []struct {
		name                      string
		forceRotation             bool
		machineObjects            []runtime.Object
		maoSecrets                []runtime.Object
		aroObjects                []runtime.Object
		expectedSecretUpdateCount int
		expectedAROIP             string
	}{
		{
			name:                      "Creation and no rotation expected",
			maoSecrets:                []runtime.Object{getGoodMAOSecret("test-user-data")},
			machineObjects:            []runtime.Object{getMachineSet("test-machine")},
			forceRotation:             false,
			expectedSecretUpdateCount: 1,
		},
		{
			name:                      "Creation and rotation expected",
			maoSecrets:                []runtime.Object{getGoodMAOSecret("test-user-data")},
			machineObjects:            []runtime.Object{getMachineSet("test-machine")},
			forceRotation:             true,
			expectedSecretUpdateCount: 2,
		},
		{
			name:                      "user-data secret in bad format, no user data update expected",
			maoSecrets:                []runtime.Object{getBadMAOSecret("bad-user-data")},
			machineObjects:            []runtime.Object{getMachineSet("test-machine")},
			forceRotation:             false,
			expectedSecretUpdateCount: 0,
		},
		{
			name:                      "no machine-api objects, no user data update expected",
			maoSecrets:                []runtime.Object{getGoodMAOSecret("test-user-data")},
			machineObjects:            []runtime.Object{},
			forceRotation:             false,
			expectedSecretUpdateCount: 0,
		},
		{
			name:                      "ARO cluster with APIIntIP, creation and no rotation expected",
			maoSecrets:                []runtime.Object{getGoodMAOSecret("test-user-data")},
			machineObjects:            []runtime.Object{getMachineSet("test-machine")},
			aroObjects:                []runtime.Object{getAROCluster("10.0.0.4")},
			forceRotation:             false,
			expectedSecretUpdateCount: 1,
			expectedAROIP:             "10.0.0.4",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(fmt.Sprintf("case %s", test.name), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.machineObjects = append(f.machineObjects, test.machineObjects...)
			f.aroObjects = append(f.aroObjects, test.aroObjects...)
			f.objects = append(f.objects, test.maoSecrets...)
			for _, obj := range test.maoSecrets {
				f.maoSecretLister = append(f.maoSecretLister, obj.(*corev1.Secret))
			}
			f.controller = f.newController()

			f.runController()

			if test.forceRotation {
				t.Log("Forcing rotation")
				// Update the CA secret annotation to an expired time to force rotation
				secret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerCAName, metav1.GetOptions{})
				require.NoError(t, err)
				newSecret := secret.DeepCopy()
				newSecret.Annotations[certrotation.CertificateNotAfterAnnotation] = time.Now().Add(-time.Hour).Format(time.RFC3339)
				_, err = f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(context.TODO(), newSecret, metav1.UpdateOptions{})
				require.NoError(t, err)
			}

			// No need to sync listers if there are no machine-api objects
			if len(f.machineObjects) > 0 {
				f.syncListers(t)
			}
			f.runController()

			f.verifyUserDataSecretUpdateCount(test.expectedSecretUpdateCount)

			// Special verification for ARO IP inclusion in TLS certificate
			// TODO: add an e2e for this when ARO prow jobs are implemented
			if test.expectedAROIP != "" {
				f.verifyAROIPInTLSCertificate(t, test.expectedAROIP)
			}

		})
	}
}

// Update the controller's indexers to capture the new secrets and configmaps
func (f *fixture) syncListers(t *testing.T) {
	signingSecret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerCAName, metav1.GetOptions{})
	require.NoError(t, err)
	f.k8sI.Core().V1().Secrets().Informer().GetIndexer().Add(signingSecret)

	configMap, err := f.kubeClient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerCAName, metav1.GetOptions{})
	require.NoError(t, err)
	f.k8sI.Core().V1().ConfigMaps().Informer().GetIndexer().Add(configMap)

	tlsSecret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerTLSSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	f.k8sI.Core().V1().Secrets().Informer().GetIndexer().Add(tlsSecret)

}
