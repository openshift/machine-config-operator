package fixtures

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
)

// JobStatus is used to set the active, succeeded, and failed fields for the k8s Job
// Status so that we can parse those to imitate Job phases for testing purposes
type JobStatus struct {
	Active                        int32
	Succeeded                     int32
	Failed                        int32
	UncountedTerminatedPodsFailed string
}

// Sets the provided job status on a given job under test. If successful, it will also insert the digestfile ConfigMap.
func SetJobStatus(ctx context.Context, t *testing.T, kubeclient clientset.Interface, mosb *mcfgv1.MachineOSBuild, jobStatus JobStatus) {
	require.NoError(t, setJobStatusFields(ctx, kubeclient, mosb, jobStatus))

	if jobStatus.Succeeded == 1 {
		require.NoError(t, createDigestfileConfigMap(ctx, kubeclient, mosb))
	} else {
		require.NoError(t, deleteDigestfileConfigMap(ctx, kubeclient, mosb))
	}
}

func SetJobDeletionTimestamp(ctx context.Context, t *testing.T, kubeclient clientset.Interface, mosb *mcfgv1.MachineOSBuild, timestamp *metav1.Time) {
	jobName := fmt.Sprintf("build-%s", mosb.Name)

	j, err := kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Get(ctx, jobName, metav1.GetOptions{})
	require.NoError(t, err)

	j.SetDeletionTimestamp(timestamp)

	_, err = kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).UpdateStatus(ctx, j, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func setJobStatusFields(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1.MachineOSBuild, jobStatus JobStatus) error {
	jobName := fmt.Sprintf("build-%s", mosb.Name)

	j, err := kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	j.Status.Active = jobStatus.Active
	j.Status.Succeeded = jobStatus.Succeeded
	j.Status.Failed = jobStatus.Failed
	if jobStatus.UncountedTerminatedPodsFailed != "" {
		j.Status.UncountedTerminatedPods = &batchv1.UncountedTerminatedPods{Failed: []apitypes.UID{apitypes.UID(jobStatus.UncountedTerminatedPodsFailed)}}
	}
	_, err = kubeclient.BatchV1().Jobs(ctrlcommon.MCONamespace).UpdateStatus(ctx, j, metav1.UpdateOptions{})
	return err
}

func createDigestfileConfigMap(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1.MachineOSBuild) error {
	return createDigestfileConfigMapWithDigest(ctx, kubeclient, mosb, getDigest(mosb.Name))
}

func createDigestfileConfigMapWithDigest(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1.MachineOSBuild, digest string) error {
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

func deleteDigestfileConfigMap(ctx context.Context, kubeclient clientset.Interface, mosb *mcfgv1.MachineOSBuild) error {
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
