package pinnedimageset

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
	masterPoolSelector = metav1.AddLabelToSelector(&metav1.LabelSelector{}, "machineconfiguration.openshift.io/role", "master")
)

type fixture struct {
	t *testing.T

	client *fake.Clientset

	mcpLister      []*mcfgv1.MachineConfigPool
	imageSetLister []*mcfgv1alpha1.PinnedImageSet
	mcpIndexer     cache.Indexer

	objects []runtime.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	return f
}

func (f *fixture) newController() *Controller {
	f.client = fake.NewSimpleClientset(f.objects...)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())

	pisInformer := i.Machineconfiguration().V1alpha1().PinnedImageSets()
	f.mcpIndexer = pisInformer.Informer().GetIndexer()

	c := New(
		pisInformer,
		i.Machineconfiguration().V1().MachineConfigPools(),
		k8sfake.NewSimpleClientset(),
		f.client,
	)

	c.mcpListerSynced = alwaysReady
	c.mcpListerSynced = alwaysReady
	c.imageSetSynced = alwaysReady
	c.eventRecorder = ctrlcommon.NamespacedEventRecorder(&record.FakeRecorder{})

	stopCh := make(chan struct{})
	defer close(stopCh)
	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)

	for _, c := range f.mcpLister {
		i.Machineconfiguration().V1().MachineConfigPools().Informer().GetIndexer().Add(c)
	}
	for _, p := range f.imageSetLister {
		f.mcpIndexer.Add(p)
	}

	return c
}

func (f *fixture) updatePinnedImageSet(pis *mcfgv1alpha1.PinnedImageSet) error {
	return f.mcpIndexer.Update(pis)
}

func (f *fixture) deletePinnedImageSet(pis *mcfgv1alpha1.PinnedImageSet) error {
	return f.mcpIndexer.Delete(pis)
}

func TestUpdateSpecNewPinnedImageSet(t *testing.T) {
	require := require.New(t)
	f := newFixture(t)
	mcp := helpers.NewMachineConfigPool("test-cluster-master", masterPoolSelector, helpers.MasterSelector, "")
	pis := fakePinnedImageSet("test-pinned-image-set", "master", map[string]string{"machineconfiguration.openshift.io/role": "master"})

	f.mcpLister = append(f.mcpLister, mcp)
	f.objects = append(f.objects, mcp)
	f.imageSetLister = append(f.imageSetLister, pis)
	f.objects = append(f.objects, pis)

	c := f.newController()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tests := []struct {
		name         string
		pis          *mcfgv1alpha1.PinnedImageSet
		deletePis    *mcfgv1alpha1.PinnedImageSet
		wantSpecPis  []mcfgv1.PinnedImageSetRef
		wantErr      error
		wantImageErr error
	}{
		{
			name: "happy path",
			pis:  pis,
			wantSpecPis: []mcfgv1.PinnedImageSetRef{
				{
					Name: pis.Name,
				},
			},
		},
		{
			name:        "update labels to worker",
			pis:         fakePinnedImageSet("test-pinned-image-set", "master", map[string]string{"machineconfiguration.openshift.io/role": "worker"}),
			wantSpecPis: nil,
		},
		{
			name: "multiple labels single match",
			pis: fakePinnedImageSet("test-pinned-image-set", "infr", map[string]string{
				"machineconfiguration.openshift.io/role": "master",
				"test-label":                             "",
			}),
			wantSpecPis: []mcfgv1.PinnedImageSetRef{
				{
					Name: pis.Name,
				},
			},
		},
		{
			name:        "delete pinned image set",
			deletePis:   pis,
			wantSpecPis: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.deletePis != nil {
				err := f.deletePinnedImageSet(tt.deletePis)
				require.NoError(err)
			} else {
				err := f.updatePinnedImageSet(tt.pis)
				require.NoError(err)
			}
			err := c.syncHandler(mcp.Name)
			require.NoError(err)

			mcp, err = c.client.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcp.Name, metav1.GetOptions{})
			require.NoError(err)
			require.Equal(mcp.Spec.PinnedImageSets, tt.wantSpecPis)
		})
	}
}

func fakePinnedImageSet(name, image string, labels map[string]string) *mcfgv1alpha1.PinnedImageSet {
	return &mcfgv1alpha1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: mcfgv1alpha1.PinnedImageSetSpec{
			PinnedImages: []mcfgv1alpha1.PinnedImageRef{
				{
					Name: image,
				},
			},
		},
	}
}
