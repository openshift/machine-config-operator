package pinnedimageset

import (
	"context"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	fakemco "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
)

func TestSyncHandler(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name              string
		machineConfigPool runtime.Object
		pis               runtime.Object
		pinnedImageSets   []mcfgv1.PinnedImageSetRef
		wantErr           error
	}{
		{
			name:              "happy path",
			machineConfigPool: masterPool,
			pis:               masterPis,
			pinnedImageSets:   []mcfgv1.PinnedImageSetRef{{Name: masterPis.Name}},
		},
		{
			name:              "pool mismatch",
			machineConfigPool: workerPool,
			pis:               masterPis,
			pinnedImageSets:   nil,
		},
		{
			name:              "no pinned image sets",
			machineConfigPool: masterPool,
			pis:               nil,
			pinnedImageSets:   nil,
		},
		{
			name:              "custom infra pool",
			machineConfigPool: infraPool,
			pis:               infraPis,
			pinnedImageSets:   []mcfgv1.PinnedImageSetRef{{Name: infraPis.Name}},
		},
		{
			name:              "custom infra pool also gets worker pinned image sets",
			machineConfigPool: infraPool,
			pis:               workerPis,
			pinnedImageSets:   []mcfgv1.PinnedImageSetRef{{Name: workerPis.Name}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fakeClient := fake.NewSimpleClientset()
			fakeMCOClient := fakemco.NewSimpleClientset(tt.machineConfigPool)
			sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
			mcpInformer := sharedInformers.Machineconfiguration().V1().MachineConfigPools()
			imageSetInformer := sharedInformers.Machineconfiguration().V1().PinnedImageSets()

			sharedInformers.Start(ctx.Done())
			sharedInformers.WaitForCacheSync(ctx.Done())

			err := mcpInformer.Informer().GetIndexer().Add(tt.machineConfigPool)
			require.NoError(err)

			if tt.pis != nil {
				err = imageSetInformer.Informer().GetIndexer().Add(tt.pis)
				require.NoError(err)
			}

			c := New(imageSetInformer, mcpInformer, fakeClient, fakeMCOClient)
			mcp, ok := tt.machineConfigPool.(*mcfgv1.MachineConfigPool)
			require.True(ok)

			err = c.syncHandler(mcp.Name)
			require.NoError(err)

			mcpNew, err := c.client.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcp.Name, metav1.GetOptions{})
			require.NoError(err)
			require.Equal(mcpNew.Spec.PinnedImageSets, tt.pinnedImageSets)
		})
	}
}

func fakePinnedImageSet(name, image string, labels map[string]string) *mcfgv1.PinnedImageSet {
	return &mcfgv1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: mcfgv1.PinnedImageSetSpec{
			PinnedImages: []mcfgv1.PinnedImageRef{
				{
					Name: mcfgv1.ImageDigestFormat(image),
				},
			},
		},
	}
}

var (
	noResyncPeriodFunc = func() time.Duration { return 0 }
	masterPoolSelector = metav1.AddLabelToSelector(&metav1.LabelSelector{}, "machineconfiguration.openshift.io/role", "master")
	workerPoolSelector = metav1.AddLabelToSelector(&metav1.LabelSelector{}, "machineconfiguration.openshift.io/role", "worker")
	masterPis          = fakePinnedImageSet("master-set", "image1", map[string]string{"machineconfiguration.openshift.io/role": "master"})
	infraPis           = fakePinnedImageSet("infra-set", "image1", map[string]string{"machineconfiguration.openshift.io/role": "infra"})
	workerPis          = fakePinnedImageSet("worker-set", "image1", map[string]string{"machineconfiguration.openshift.io/role": "worker"})
	masterPool         = helpers.NewMachineConfigPool("master", masterPoolSelector, helpers.MasterSelector, "")
	workerPool         = helpers.NewMachineConfigPool("worker", workerPoolSelector, helpers.WorkerSelector, "")
	infraPool          = helpers.NewMachineConfigPool("infra", infraPoolSelector, helpers.InfraSelector, "")
	infraPoolSelector  = &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "machineconfiguration.openshift.io/role",
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{"worker", "infra"},
			},
		},
	}
)
