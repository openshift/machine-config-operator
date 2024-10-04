package fixtures

import (
	"context"
	"fmt"
	"testing"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// Sets the provided pod phase on a given pod under test. If successful, it will also insert the digestfile ConfigMap.
func SetPodPhase(ctx context.Context, t *testing.T, kubeclient clientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, phase corev1.PodPhase) {
	podName := fmt.Sprintf("build-%s", mosb.Name)
	digestName := fmt.Sprintf("digest-%s", mosb.Name)

	p, err := kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, podName, metav1.GetOptions{})
	require.NoError(t, err)

	p.Status.Phase = phase
	_, err = kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).UpdateStatus(ctx, p, metav1.UpdateOptions{})
	require.NoError(t, err)

	if phase == corev1.PodSucceeded {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      digestName,
				Namespace: ctrlcommon.MCONamespace,
			},
			Data: map[string]string{
				"digest": "sha256:628e4e8f0a78d91015c6cebeee95931ae2e8defe5dfb4ced4a82830e08937573",
			},
		}
		_, cmErr := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
		require.NoError(t, cmErr)
	}
}
