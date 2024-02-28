package pinnedimageset

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemco "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
)

var (
	noResyncPeriodFunc       = func() time.Duration { return 0 }
	noSpecMachineConfigPools map[string]string
)

func TestEnsurePinnedImageSetNoMachineConfigPool(t *testing.T) {
	require := require.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pinnedImageSet := fakePinnedImageSet(
		"worker",
		fakePinnedImageSetUID1,
		noSpecMachineConfigPools,
		[]mcfgv1.PinnedImageRef{},
	)
	machineConfigPools := []runtime.Object{}
	wantErr := errNoMatchingPool
	fakeMCOClient := fakemco.NewSimpleClientset(machineConfigPools...)
	sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
	mcpInformer := sharedInformers.Machineconfiguration().V1().MachineConfigPools()
	sharedInformers.Start(ctx.Done())
	sharedInformers.WaitForCacheSync(ctx.Done())
	for _, mcp := range machineConfigPools {
		err := mcpInformer.Informer().GetIndexer().Add(mcp)
		require.NoError(err)
	}

	ctrl := Controller{
		mcpLister: mcpInformer.Lister(),
	}
	err := ctrl.ensurePinnedImageSet(pinnedImageSet)
	require.ErrorAs(err, &wantErr)
}

func TestEnsureNodeImagePrefetch(t *testing.T) {
	tests := []struct {
		name           string
		nodes          []runtime.Object
		pinnedImageSet *mcfgv1.PinnedImageSet
		machineConfigPools []runtime.Object
		wantErr        error
		wantRequeue	 bool
	}{
		{
			name: "node with empty annotations",
			nodes: []runtime.Object{
				fakeNode(
					"worker",
					"",
					"",
					"",
				),
			},
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", map[string]string{
					"pools.operator.machineconfiguration.openshift.io/worker": "",
				}),
			},
			pinnedImageSet: fakePinnedImageSet(
				"worker",
				fakePinnedImageSetUID1,
				map[string]string{
				"pools.operator.machineconfiguration.openshift.io/worker": "",
				},
				[]mcfgv1.PinnedImageRef{},
			),
			wantRequeue: true,
			wantErr: errNodePrefetchNotComplete,
		},
		{
			name: "node complete",
			nodes: []runtime.Object{
				fakeNode(
					"worker",
					fakePinnedImageSetUID1,
					fakePinnedImageSetUID1,
					"",
				),
			},
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", map[string]string{
					"pools.operator.machineconfiguration.openshift.io/worker": "",
				}),
			},
			pinnedImageSet: fakePinnedImageSet(
				"worker",
				fakePinnedImageSetUID1,
				map[string]string{
				"pools.operator.machineconfiguration.openshift.io/worker": "",
				},
				[]mcfgv1.PinnedImageRef{},
			),
		},
		{
			name: "node in progress",
			nodes: []runtime.Object{
				fakeNode(
					"worker",
					fakePinnedImageSetUID1,
					fakePinnedImageSetUID2,
					"",
				),
			},
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", map[string]string{
					"pools.operator.machineconfiguration.openshift.io/worker": "",
				}),
			},
			pinnedImageSet: fakePinnedImageSet(
				"worker",
				fakePinnedImageSetUID1,
				map[string]string{
				"pools.operator.machineconfiguration.openshift.io/worker": "",
				},
				[]mcfgv1.PinnedImageRef{},
			),
			wantRequeue: true,
			wantErr: errNodePrefetchNotComplete,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeMCOClient := fakemco.NewSimpleClientset(tt.machineConfigPools...)
			fakeClient := fake.NewSimpleClientset(tt.nodes...)
			sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
			informerFactory := informers.NewSharedInformerFactory(fakeClient, noResyncPeriodFunc())
			mcpInformer := sharedInformers.Machineconfiguration().V1().MachineConfigPools()
			nodeInformer := informerFactory.Core().V1().Nodes()

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())
			sharedInformers.Start(ctx.Done())
			sharedInformers.WaitForCacheSync(ctx.Done())

			for _, mcp := range tt.machineConfigPools {
				err := mcpInformer.Informer().GetIndexer().Add(mcp)
				require.NoError(err)
			}

			for _, node := range tt.nodes {
				err := nodeInformer.Informer().GetIndexer().Add(node)
				require.NoError(err)
			}

			ctrl := Controller{
				mcpLister: mcpInformer.Lister(),
				nodeLister: nodeInformer.Lister(),
			}
			requeue, err := ctrl.ensureNodeImagePrefetch(tt.pinnedImageSet)
			require.Equal(tt.wantRequeue, requeue)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

func TestGetNodesForPinnedImageSet(t *testing.T) {
	tests := []struct {
		name               string
		nodes              []runtime.Object
		pinnedImageSet     *mcfgv1.PinnedImageSet
		machineConfigPools []runtime.Object
		wantNodeCount      int
	}{
		{
			name: "worker pool matches pinnedimageset machineConfigPoolSelector",
			nodes: []runtime.Object{
				fakeWorkerNodePrefetchInProgress,
				fakeMasterNodePrefetchSynced,
			},
			pinnedImageSet: fakePinnedImageSet(
				"worker",
				fakePinnedImageSetUID1,
				map[string]string{
					"pools.operator.machineconfiguration.openshift.io/worker": "",
				},
				[]mcfgv1.PinnedImageRef{},
			),
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", map[string]string{
					"pools.operator.machineconfiguration.openshift.io/worker": "",
				}),
				fakeMachineConfigPool("master", map[string]string{
					"pools.operator.machineconfiguration.openshift.io/master": "",
				}),
			},
			wantNodeCount: 1,
		},
		{
			name: "both pools matches pinnedimageset machineConfigPoolSelector",
			nodes: []runtime.Object{
				fakeWorkerNodePrefetchInProgress,
				fakeMasterNodePrefetchSynced,
			},
			pinnedImageSet: fakePinnedImageSet(
				"master",
				fakePinnedImageSetUID1,
				map[string]string{
					"pinned-image-set-opt-in": "true",
				},
				[]mcfgv1.PinnedImageRef{},
			),
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", map[string]string{
					"pinned-image-set-opt-in": "true",
				}),
				fakeMachineConfigPool("master", map[string]string{
					"pinned-image-set-opt-in": "true",
				}),
			},
			wantNodeCount: 2,
		},
		{
			name: "no matching pool",
			nodes: []runtime.Object{
				fakeWorkerNodePrefetchInProgress,
				fakeMasterNodePrefetchSynced,
			},
			pinnedImageSet: fakePinnedImageSet(
				"master",
				fakePinnedImageSetUID1,
				map[string]string{
					"no-matching-label": "",
				},
				[]mcfgv1.PinnedImageRef{},
			),
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", map[string]string{
					"pinned-image-set-opt-in": "true",
				}),
				fakeMachineConfigPool("master", map[string]string{
					"pinned-image-set-opt-in": "true",
				}),
			},
			wantNodeCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeMCOClient := fakemco.NewSimpleClientset(tt.machineConfigPools...)
			fakeClient := fake.NewSimpleClientset(tt.nodes...)
			sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
			informerFactory := informers.NewSharedInformerFactory(fakeClient, noResyncPeriodFunc())
			imageSetInformer := sharedInformers.Machineconfiguration().V1().PinnedImageSets()
			mcpInformer := sharedInformers.Machineconfiguration().V1().MachineConfigPools()
			nodeInformer := informerFactory.Core().V1().Nodes()

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())
			sharedInformers.Start(ctx.Done())
			sharedInformers.WaitForCacheSync(ctx.Done())

			for _, mcp := range tt.machineConfigPools {
				err := mcpInformer.Informer().GetIndexer().Add(mcp)
				require.NoError(err)
			}

			for _, node := range tt.nodes {
				err := nodeInformer.Informer().GetIndexer().Add(node)
				require.NoError(err)
			}

			err := imageSetInformer.Informer().GetStore().Add(tt.pinnedImageSet)
			require.NoError(err)
			ctrl := Controller{
				mcpLister:  mcpInformer.Lister(),
				nodeLister: nodeInformer.Lister(),
			}
			nodes, err := ctrl.getNodesForPinnedImageSet(tt.pinnedImageSet)
			require.NoError(err)
			require.Len(nodes, tt.wantNodeCount)
		})
	}
}

func fakePinnedImageSet(role, uid string, labels map[string]string, images []mcfgv1.PinnedImageRef) *mcfgv1.PinnedImageSet {
	return &mcfgv1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("fake-%s-pinned-images", role),
			UID:  types.UID(uid),
		},
		Spec: mcfgv1.PinnedImageSetSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			PinnedImages: images,
		},
	}
}

const (
	fakePinnedImageSetUID1 = "50db511e-9959-4045-9852-f80afaf90a01"
	fakePinnedImageSetUID2 = "f77c5a67-ade7-4a67-bf55-bf40025e8a18"
)

var (
	fakeWorkerNodePrefetchInProgress = fakeNode(
		"worker",
		fakePinnedImageSetUID1,
		fakePinnedImageSetUID2,
		"in progress",
	)
	fakeMasterNodePrefetchSynced = fakeNode(
		"master",
		fakePinnedImageSetUID1,
		fakePinnedImageSetUID1,
		"",
	)
)

func fakeNode(
	role,
	currentPinnedImageSetUUID,
	desiredPinnedImageSetUUID,
	reason string,
) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-" + role,
			Labels: map[string]string{
				fmt.Sprintf("node-role.kubernetes.io/%s", role): "",
			},
			Annotations: map[string]string{
				constants.MachineConfigDaemonPinnedImageSetPrefetchCurrentAnnotationKey: currentPinnedImageSetUUID,
				constants.MachineConfigDaemonPinnedImageSetPrefetchDesiredAnnotationKey: desiredPinnedImageSetUUID,
				constants.MachineConfigDaemonPinnedImageSetPrefetchReasonAnnotationKey:  reason,
			},
		},
	}
}

func fakeMachineConfigPool(name string, labels map[string]string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			MachineConfigSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"machineconfiguration.openshift.io/role": name,
				},
			},
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					fmt.Sprintf("node-role.kubernetes.io/%s", name): "",
				},
			},
		},
	}
}
