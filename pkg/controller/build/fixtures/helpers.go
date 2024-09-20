package fixtures

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// Sets the provided pod phase on a given pod under test. If successful, it will also insert the digestfile ConfigMap.
func SetPodPhase(ctx context.Context, t *testing.T, kubeclient clientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, phase corev1.PodPhase) {
	require.NoError(t, setPodPhase(ctx, kubeclient, mosb, phase))

	if phase == corev1.PodSucceeded {
		require.NoError(t, createDigestfileConfigMap(ctx, kubeclient, mosb))
	} else {
		require.NoError(t, deleteDigestfileConfigMap(ctx, kubeclient, mosb))
	}
}

func SetPodDeletionTimestamp(ctx context.Context, t *testing.T, kubeclient clientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, timestamp *metav1.Time) {
	podName := fmt.Sprintf("build-%s", mosb.Name)

	p, err := kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, podName, metav1.GetOptions{})
	require.NoError(t, err)

	p.SetDeletionTimestamp(timestamp)

	_, err = kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).UpdateStatus(ctx, p, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func setPodPhase(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, phase corev1.PodPhase) error {
	podName := fmt.Sprintf("build-%s", mosb.Name)

	p, err := kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	p.Status.Phase = phase
	_, err = kubeclient.CoreV1().Pods(ctrlcommon.MCONamespace).UpdateStatus(ctx, p, metav1.UpdateOptions{})
	return err
}

func createDigestfileConfigMap(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) error {
	return createDigestfileConfigMapWithDigest(ctx, kubeclient, mosb, getDigest(mosb.Name))
}

func createDigestfileConfigMapWithDigest(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, digest string) error {
	digestName := fmt.Sprintf("digest-%s", mosb.Name)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      digestName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string]string{
			"digest": digest,
		},
	}

	_, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

func deleteDigestfileConfigMap(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild) error {
	digestName := fmt.Sprintf("digest-%s", mosb.Name)
	err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Delete(ctx, digestName, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}

	return nil
}

func getDigest(in string) string {
	h := sha256.New()
	h.Write([]byte(in))
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
