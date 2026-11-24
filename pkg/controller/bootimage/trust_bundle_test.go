package bootimage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	archtranslater "github.com/coreos/stream-metadata-go/arch"
	"github.com/coreos/stream-metadata-go/stream"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	bootimagecache "github.com/openshift/machine-config-operator/pkg/controller/bootimage/cache"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func newTestControllerConfig(name string, bundle []byte) *mcfgv1.ControllerConfig {
	return &mcfgv1.ControllerConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       mcfgv1.ControllerConfigSpec{AdditionalTrustBundle: bundle},
	}
}

// trustTestCA is an independently generated certificate authority for trust bundle tests.
type trustTestCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTrustTestCA(t *testing.T, commonName string) *trustTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &trustTestCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// serveOVA starts an HTTPS server on the loopback address, with a certificate signed by the CA, that serves content
// for any path.
func (ca *trustTestCA) serveOVA(t *testing.T, content []byte) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "ova mirror"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestMachineSetSyncDownloadsOVAWithCurrentTrustBundle verifies that a vSphere MachineSet sync passes the additional
// trust bundle that the ControllerConfig has at the time of the sync down to the OVA download: no extra CA before the
// ControllerConfig exists, its CA once it is created and only the new CA after a rotation. The template creation is
// replaced because it needs a live vCenter before it downloads the OVA, so its own call to cache.DownloadOva is not
// covered here; the replacement performs the same download with the arguments it receives.
func TestMachineSetSyncDownloadsOVAWithCurrentTrustBundle(t *testing.T) {
	content := []byte("not really an OVA")
	checksum := sha256.Sum256(content)
	oldCA := newTrustTestCA(t, "old trust bundle CA")
	newCA := newTrustTestCA(t, "new trust bundle CA")
	oldServer := oldCA.serveOVA(t, content)
	newServer := newCA.serveOVA(t, content)

	// Stand in for the vCenter steps of the template creation, which need a live vCenter, but keep the OVA download
	// they perform with the arguments that the MachineSet sync passed down.
	original := createVSphereVMTemplate
	t.Cleanup(func() { createVSphereVMTemplate = original })
	createVSphereVMTemplate = func(streamData *stream.Stream, _ *machinev1beta1.VSphereMachineProviderSpec, _ *osconfigv1.Infrastructure, _ *corev1.Secret, _ clientset.Interface, arch, _ string, additionalTrustBundle []byte) (string, bool, bool, error) {
		ova, err := streamData.QueryDisk(arch, "vmware", "ova")
		if err != nil {
			return "", false, false, err
		}
		path, err := bootimagecache.DownloadOva(ova, additionalTrustBundle)
		if err == nil {
			t.Cleanup(func() { _ = os.Remove(path) })
		}
		return "", false, false, err
	}

	cvIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, cvIndexer.Add(&osconfigv1.ClusterVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "version"},
		Status:     osconfigv1.ClusterVersionStatus{History: []osconfigv1.UpdateHistory{{State: osconfigv1.CompletedUpdate, Version: "4.22.0"}}},
	}))
	infraIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, infraIndexer.Add(&osconfigv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       osconfigv1.InfrastructureSpec{PlatformSpec: osconfigv1.PlatformSpec{VSphere: &osconfigv1.VSpherePlatformSpec{}}},
		Status:     osconfigv1.InfrastructureStatus{PlatformStatus: &osconfigv1.PlatformStatus{Type: osconfigv1.VSpherePlatformType}},
	}))
	ccIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	ctrl := &Controller{
		kubeClient: fake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.VSphereCredentialsSecretName, Namespace: ctrlcommon.MCONamespace},
		}),
		clusterVersionLister: configlistersv1.NewClusterVersionLister(cvIndexer),
		infraLister:          configlistersv1.NewInfrastructureLister(infraIndexer),
		ccLister:             mcfglistersv1.NewControllerConfigLister(ccIndexer),
		mapiBootImageState:   map[string]BootImageState{},
		fgHandler:            ctrlcommon.NewFeatureGatesHardcodedHandler(nil, nil),
	}
	machineSet := &machinev1beta1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{Name: "vsphere-machineset"},
		Spec: machinev1beta1.MachineSetSpec{Template: machinev1beta1.MachineTemplateSpec{Spec: machinev1beta1.MachineSpec{
			ProviderSpec: machinev1beta1.ProviderSpec{Value: &runtime.RawExtension{Raw: []byte("{}")}},
		}}},
	}

	downloads := 0
	syncFrom := func(server *httptest.Server) error {
		t.Helper()
		// Use a new OVA name for every sync so that the download is never served from the boot image cache.
		downloads++
		name := fmt.Sprintf("trust-bundle-test-%d-%d.ova", time.Now().UnixNano(), downloads)
		streamData := stream.Stream{
			Stream: "rhcos",
			Architectures: map[string]stream.Arch{archtranslater.CurrentRpmArch(): {
				Artifacts: map[string]stream.PlatformArtifacts{"vmware": {
					Release: "9.6.20260101-0",
					Formats: map[string]stream.ImageFormat{"ova": {Disk: &stream.Artifact{
						Location: server.URL + "/" + name,
						Sha256:   hex.EncodeToString(checksum[:]),
					}}},
				}},
			}},
		}
		data, err := json.Marshal(streamData)
		require.NoError(t, err)
		configMap := &corev1.ConfigMap{Data: map[string]string{StreamConfigMapKey: string(data)}}

		_, _, err = ctrl.syncMAPIMachineSet(machineSet, configMap)
		return err
	}
	requireUnknownAuthority := func(err error) {
		t.Helper()
		var unknownAuthority x509.UnknownAuthorityError
		require.ErrorAs(t, err, &unknownAuthority)
	}

	// Without a ControllerConfig, only the system roots are trusted.
	requireUnknownAuthority(syncFrom(oldServer))

	// Once the ControllerConfig is created, its CA is trusted.
	require.NoError(t, ccIndexer.Add(newTestControllerConfig(ctrlcommon.ControllerConfigName, oldCA.pem)))
	require.NoError(t, syncFrom(oldServer))

	// After a rotation, the next sync trusts the new CA and no longer the old one.
	require.NoError(t, ccIndexer.Update(newTestControllerConfig(ctrlcommon.ControllerConfigName, newCA.pem)))
	require.NoError(t, syncFrom(newServer))
	requireUnknownAuthority(syncFrom(oldServer))
}

// TestWithAdditionalTrustBundleForwardsArguments verifies that the vSphere reconcile
// adapter passes the trust bundle, and all other arguments and results, through.
func TestWithAdditionalTrustBundleForwardsArguments(t *testing.T) {
	bundle := []byte("ca-bundle")
	streamData := &stream.Stream{}
	infra := &osconfigv1.Infrastructure{}
	providerSpec := &machinev1beta1.VSphereMachineProviderSpec{}
	kubeClient := fake.NewClientset()
	newProviderSpec := &machinev1beta1.VSphereMachineProviderSpec{Template: "new-template"}

	called := false
	reconcile := withAdditionalTrustBundle(bundle, func(gotStream *stream.Stream, gotArch string, gotInfra *osconfigv1.Infrastructure, gotProviderSpec *machinev1beta1.VSphereMachineProviderSpec, gotName string, gotClient clientset.Interface, gotBundle []byte) (bool, bool, *machinev1beta1.VSphereMachineProviderSpec, string, error) {
		called = true
		assert.Same(t, streamData, gotStream)
		assert.Equal(t, "x86_64", gotArch)
		assert.Same(t, infra, gotInfra)
		assert.Same(t, providerSpec, gotProviderSpec)
		assert.Equal(t, "test-machineset", gotName)
		assert.Equal(t, clientset.Interface(kubeClient), gotClient)
		assert.Equal(t, bundle, gotBundle)
		return true, false, newProviderSpec, "", nil
	})

	patchRequired, reconcileSkipped, gotProviderSpec, rhcosVersion, err := reconcile(streamData, "x86_64", infra, providerSpec, "test-machineset", kubeClient)
	require.NoError(t, err)
	require.True(t, called)
	assert.True(t, patchRequired)
	assert.False(t, reconcileSkipped)
	assert.Same(t, newProviderSpec, gotProviderSpec)
	assert.Empty(t, rhcosVersion)
}
