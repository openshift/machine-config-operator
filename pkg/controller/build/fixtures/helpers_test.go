package fixtures

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSetJobStatusCreatesDigestConfigMapBeforeMarkingJobSucceeded(t *testing.T) {
	ctx := context.Background()
	mosb := &mcfgv1.MachineOSBuild{ObjectMeta: metav1.ObjectMeta{Name: "test-build"}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "build-test-build",
		Namespace: ctrlcommon.MCONamespace,
	}}
	client := fake.NewSimpleClientset(job)

	SetJobStatus(ctx, t, client, mosb, JobStatus{Succeeded: 1})

	digestConfigMapCreate := -1
	jobStatusUpdate := -1
	for i, action := range client.Actions() {
		switch {
		case action.GetVerb() == "create" && action.GetResource().Resource == "configmaps":
			digestConfigMapCreate = i
		case action.GetVerb() == "update" && action.GetResource().Resource == "jobs" && action.GetSubresource() == "status":
			jobStatusUpdate = i
		}
	}

	require.GreaterOrEqual(t, digestConfigMapCreate, 0)
	require.Greater(t, jobStatusUpdate, digestConfigMapCreate)
}
