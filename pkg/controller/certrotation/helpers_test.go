package certrotationcontroller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	arov1alpha1 "github.com/Azure/ARO-RP/pkg/operator/apis/aro.openshift.io/v1alpha1"
)

func getGoodMAOSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MachineAPINamespace,
		},
		Data: map[string][]byte{"disableTemplating": []byte("true"), "userData": []byte(`{"ignition":{"config":{"merge":[{"source":"https://test-cluster-api:22623/config/worker"}]},"security":{"tls":{"certificateAuthorities":[{"source":"data:text/plain;charset=utf-8;base64,LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tClJPT1QgQ0EgREFUQQotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="}]}},"version":"3.2.0"}}`)},
	}
}

func getBadMAOSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MachineAPINamespace,
		},
		Data: map[string][]byte{"disableTemplating": []byte("true"), "userData": []byte(`bad data`)},
	}
}

func getMachineSet(name string) *machinev1beta1.MachineSet {
	return &machinev1beta1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MachineAPINamespace,
		},
	}
}

func getAROCluster(apiIntIP string) *arov1alpha1.Cluster {
	return &arov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: arov1alpha1.ClusterSpec{
			APIIntIP: apiIntIP,
		},
	}
}

func getIRIClusterResource() *mcfgv1alpha1.InternalReleaseImage {
	return &mcfgv1alpha1.InternalReleaseImage{
		ObjectMeta: metav1.ObjectMeta{
			Name: ctrlcommon.InternalReleaseImageInstanceName,
		},
	}
}

// createIRISecret creates an empty IRI TLS secret, simulating what the IRI controller
// would create during cluster bootstrap.
func (f *fixture) createIRISecret(t *testing.T) {
	t.Helper()
	_, err := f.kubeClient.CoreV1().Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.InternalReleaseImageTLSSecretName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       {},
			corev1.TLSPrivateKeyKey: {},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
}
