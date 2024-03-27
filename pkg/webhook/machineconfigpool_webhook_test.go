package webhook

import (
	"context"
	"fmt"
	"testing"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	fakemco "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

func TestMachineConfigPoolValidatorHandler(t *testing.T) {
	tests := []struct {
		name               string
		pinnedImageSets    []runtime.Object
		machineConfigPools []runtime.Object
		wantErrMsg         string
		wantRequeue        bool
	}{

		{
			name: "create machineconfigpool with valid pinnedImageSet",
			pinnedImageSets: []runtime.Object{
				fakePinnedImageSet("valid-pinned-images"),
			},
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", []mcfgv1.PinnedImageSetRef{
					{
						Name: "valid-pinned-images",
					},
				}),
			},
		},
		{
			name: "create machineconfigpool with invalid pinnedImageSet",
			pinnedImageSets: []runtime.Object{
				fakePinnedImageSet("valid-pinned-images"),
			},
			machineConfigPools: []runtime.Object{
				fakeMachineConfigPool("worker", []mcfgv1.PinnedImageSetRef{
					{
						Name: "invalid-pinned-images",
					},
				}),
			},
			wantErrMsg: "pinnedImageSet not found: invalid-pinned-images",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeMCOClient := fakemco.NewSimpleClientset(tt.machineConfigPools...)
			sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
			pinnedImageInformer := sharedInformers.Machineconfiguration().V1alpha1().PinnedImageSets()

			sharedInformers.Start(ctx.Done())
			sharedInformers.WaitForCacheSync(ctx.Done())

			for _, set := range tt.pinnedImageSets {
				err := pinnedImageInformer.Informer().GetIndexer().Add(set)
				require.NoError(err)
			}

			handler := &machineConfigPoolValidatorHandler{
				admissionHandler: &admissionHandler{
					admissionConfig: &admissionConfig{
						imageSetLister: pinnedImageInformer.Lister(),
					},
				},
			}

			warning, err := handler.ValidateCreate(context.Background(), tt.machineConfigPools[0])
			if tt.wantErrMsg != "" {
				require.Error(err)
				require.Contains(err.Error(), tt.wantErrMsg)
			}
			require.Nil(warning)

			warning, err = handler.ValidateUpdate(context.Background(), tt.machineConfigPools[0], tt.machineConfigPools[0])
			require.NoError(err)
			require.Nil(warning)
		})
	}
}

func fakePinnedImageSet(name string) *mcfgv1alpha1.PinnedImageSet {
	return &mcfgv1alpha1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: mcfgv1alpha1.PinnedImageSetSpec{
			PinnedImages: []mcfgv1alpha1.PinnedImageRef{
				{
					Name: "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:fake",
				},
			},
		},
	}
}

func fakeMachineConfigPool(name string, sets []mcfgv1.PinnedImageSetRef) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			PinnedImageSets: sets,
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
