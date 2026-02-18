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
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/factory"
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
	infraLister        []*configv1.Infrastructure

	objects        []runtime.Object
	configObjects  []runtime.Object
	machineObjects []runtime.Object
	aroObjects     []runtime.Object
	k8sI           kubeinformers.SharedInformerFactory
	infraInformer  configinformers.SharedInformerFactory

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
			APIServerInternalURL: "https://10.0.0.1:6443"},
	})

	f.kubeClient = fake.NewSimpleClientset(f.objects...)
	f.configClient = fakeconfigv1client.NewSimpleClientset(f.configObjects...)
	f.machineClient = fakemachineclientset.NewSimpleClientset(f.machineObjects...)
	f.aroClient = fakearoclientset.NewSimpleClientset(f.aroObjects...)
	f.k8sI = kubeinformers.NewSharedInformerFactory(f.kubeClient, noResyncPeriodFunc())
	f.infraInformer = configinformers.NewSharedInformerFactory(f.configClient, noResyncPeriodFunc())

	for _, secret := range f.maoSecretLister {
		f.k8sI.Core().V1().Secrets().Informer().GetIndexer().Add(secret)
	}

	for _, secret := range f.mcoSecretLister {
		f.k8sI.Core().V1().Secrets().Informer().GetIndexer().Add(secret)
	}

	for _, configMap := range f.mcoConfigMapLister {
		f.k8sI.Core().V1().ConfigMaps().Informer().GetIndexer().Add(configMap)
	}

	for _, infra := range f.configObjects {
		f.infraInformer.Config().V1().Infrastructures().Informer().GetIndexer().Add(infra)
		f.infraLister = append(f.infraLister, infra.(*configv1.Infrastructure))
	}

	c, err := New(f.kubeClient, f.configClient, f.machineClient, f.aroClient, f.k8sI.Core().V1().Secrets(), f.k8sI.Core().V1().Secrets(), f.k8sI.Core().V1().ConfigMaps(), f.infraInformer.Config().V1().Infrastructures())
	require.NoError(f.t, err)

	c.StartInformers()
	require.NoError(f.t, err)

	return c
}

func (f *fixture) sync() error {
	syncCtx := factory.NewSyncContext("mco-cert-rotation-sync", f.controller.recorder)

	if err := f.controller.syncHostnames(); err != nil {
		return err
	}

	for _, certRotator := range f.controller.certRotators {
		if err := certRotator.Sync(context.TODO(), syncCtx); err != nil {
			return err
		}
	}
	return nil

}

func (f *fixture) runController() {

	err := f.sync()
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

func TestInfraUpdateTriggersCertResync(t *testing.T) {
	f := newFixture(t)
	f.objects = append(f.objects, getGoodMAOSecret("test-user-data"))
	f.maoSecretLister = append(f.maoSecretLister, getGoodMAOSecret("test-user-data"))
	f.machineObjects = append(f.machineObjects, getMachineSet("test-machine"))

	f.controller = f.newController()

	// Perform initial sync to create initial certificates
	f.runController()

	// Update the Infrastructure object with a new APIServerInternalURL
	infraObj := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			APIServerInternalURL: "https://10.0.0.2:6443", // Changed from 10.0.0.1 to 10.0.0.2
		},
	}

	// Update the Infrastructure object
	_, err := f.configClient.ConfigV1().Infrastructures().Update(context.TODO(), infraObj, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Update the informer with the new Infrastructure object
	f.infraInformer.Config().V1().Infrastructures().Informer().GetIndexer().Update(infraObj)

	// Trigger the sync after Infrastructure update
	f.syncListers(t)
	f.runController()

	// Verify that the TLS certificate was regenerated with the new hostname
	tlsSecret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerTLSSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, tlsSecret)

	// Verify certificate contains new hostname
	certData, exists := tlsSecret.Data["tls.crt"]
	require.True(t, exists, "TLS certificate should exist in secret")
	require.NotEmpty(t, certData, "TLS certificate data should not be empty")

	// Decode and parse certificate
	block, _ := pem.Decode(certData)
	require.NotNil(t, block, "Should be able to decode PEM certificate")

	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err, "Should be able to parse TLS certificate")

	// Verify the new hostname is in the certificate's DNS names
	expectedHostname := "10.0.0.2"
	found := false
	for _, dnsName := range cert.DNSNames {
		if dnsName == expectedHostname {
			found = true
			break
		}
	}
	require.True(t, found, "New hostname %s should be present in certificate DNS names", expectedHostname)
	t.Logf("Successfully verified hostname %s is present in TLS certificate after Infrastructure update", expectedHostname)

	// Verify that user data secrets were updated (should be 1 total update)
	f.verifyUserDataSecretUpdateCount(1)
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

func TestIRICertificateRotation(t *testing.T) {
	tests := []struct {
		name           string
		forceRotation  bool
		machineObjects []runtime.Object
		maoSecrets     []runtime.Object
		preCreateIRI   bool // whether to pre-create the IRI secret before reconciliation
	}{
		{
			name:           "IRI secret is created on initial sync",
			maoSecrets:     []runtime.Object{getGoodMAOSecret("test-user-data")},
			machineObjects: []runtime.Object{getMachineSet("test-machine")},
			forceRotation:  false,
			preCreateIRI:   false,
		},
		{
			name:           "IRI secret is updated on CA rotation",
			maoSecrets:     []runtime.Object{getGoodMAOSecret("test-user-data")},
			machineObjects: []runtime.Object{getMachineSet("test-machine")},
			forceRotation:  true,
			preCreateIRI:   false,
		},
		{
			name:           "IRI secret is updated when it already exists",
			maoSecrets:     []runtime.Object{getGoodMAOSecret("test-user-data")},
			machineObjects: []runtime.Object{getMachineSet("test-machine")},
			forceRotation:  false,
			preCreateIRI:   true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.machineObjects = append(f.machineObjects, test.machineObjects...)
			f.objects = append(f.objects, test.maoSecrets...)
			for _, obj := range test.maoSecrets {
				f.maoSecretLister = append(f.maoSecretLister, obj.(*corev1.Secret))
			}
			f.controller = f.newController()

			// Initial sync to create CA and MCS TLS cert
			f.runController()

			if test.preCreateIRI {
				// Pre-create an IRI secret with dummy data to test the update path
				dummySecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      ctrlcommon.InternalReleaseImageTLSSecretName,
						Namespace: ctrlcommon.MCONamespace,
					},
					Type: corev1.SecretTypeTLS,
					Data: map[string][]byte{
						corev1.TLSCertKey:       []byte("dummy-cert"),
						corev1.TLSPrivateKeyKey: []byte("dummy-key"),
					},
				}
				_, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), dummySecret, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			if test.forceRotation {
				t.Log("Forcing CA rotation")
				secret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerCAName, metav1.GetOptions{})
				require.NoError(t, err)
				newSecret := secret.DeepCopy()
				newSecret.Annotations[certrotation.CertificateNotAfterAnnotation] = time.Now().Add(-time.Hour).Format(time.RFC3339)
				_, err = f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Update(context.TODO(), newSecret, metav1.UpdateOptions{})
				require.NoError(t, err)
				f.syncListers(t)
				f.runController()
			}

			// Reconcile the IRI certificate
			f.controller.reconcileIRICertificate()

			// Verify the IRI TLS secret was created/updated
			iriSecret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.InternalReleaseImageTLSSecretName, metav1.GetOptions{})
			require.NoError(t, err, "IRI TLS secret should exist after reconciliation")
			require.Equal(t, corev1.SecretTypeTLS, iriSecret.Type, "IRI secret should be of type TLS")

			// Parse the IRI certificate
			iriCertData := iriSecret.Data[corev1.TLSCertKey]
			require.NotEmpty(t, iriCertData, "IRI certificate data should not be empty")

			block, _ := pem.Decode(iriCertData)
			require.NotNil(t, block, "Should be able to decode IRI PEM certificate")

			iriCert, err := x509.ParseCertificate(block.Bytes)
			require.NoError(t, err, "Should be able to parse IRI certificate")

			// Get the MCS CA certificate to verify the IRI cert is signed by it
			caSecret, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.MachineConfigServerCAName, metav1.GetOptions{})
			require.NoError(t, err)
			caCertData := caSecret.Data[corev1.TLSCertKey]
			require.NotEmpty(t, caCertData, "CA certificate data should not be empty")

			caBlock, _ := pem.Decode(caCertData)
			require.NotNil(t, caBlock, "Should be able to decode CA PEM certificate")
			caCert, err := x509.ParseCertificate(caBlock.Bytes)
			require.NoError(t, err, "Should be able to parse CA certificate")

			// Verify the IRI cert is signed by the MCS CA
			err = iriCert.CheckSignatureFrom(caCert)
			require.NoError(t, err, "IRI certificate should be signed by the MCS CA")

			// Verify the IRI cert has the correct SANs (hostnames from hostnamesRotation)
			expectedHostnames := f.controller.hostnamesRotation.GetHostnames()
			require.NotEmpty(t, expectedHostnames, "Expected hostnames should not be empty")

			for _, hostname := range expectedHostnames {
				ip := net.ParseIP(hostname)
				if ip != nil {
					found := false
					for _, certIP := range iriCert.IPAddresses {
						if certIP.Equal(ip) {
							found = true
							break
						}
					}
					require.True(t, found, "IP %s should be present in IRI certificate SAN IP addresses", hostname)
				} else {
					found := false
					for _, dnsName := range iriCert.DNSNames {
						if dnsName == hostname {
							found = true
							break
						}
					}
					require.True(t, found, "Hostname %s should be present in IRI certificate SAN DNS names", hostname)
				}
			}

			t.Logf("Successfully verified IRI certificate: signed by MCS CA, correct SANs")
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
