package utils

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
)

// Tests that a MachineOSBuild can be constructed correctly.
func TestMachineOSBuild(t *testing.T) {
	t.Parallel()

	getMachineOSConfig := func() *mcfgv1alpha1.MachineOSConfig {
		return testhelpers.NewMachineOSConfigBuilder("worker").WithMachineConfigPool("worker").MachineOSConfig()
	}

	getMachineConfigPool := func() *mcfgv1.MachineConfigPool {
		return testhelpers.NewMachineConfigPoolBuilder("worker").MachineConfigPool()
	}

	getOSImageURLConfig := func() *ctrlcommon.OSImageURLConfig {
		return &ctrlcommon.OSImageURLConfig{
			BaseOSContainerImage:           "registry.hostname.com/org@sha256:220a60ecd4a3c32c282622a625a54db9ba0ff55b5ba9c29c7064a2bc358b6a3e",
			BaseOSExtensionsContainerImage: "registry.hostname.com/org@sha256:5fb4ba1a651bae8057ec6b5cdafc93fa7e0b7d944d6f02a4b751de4e15464def",
			OSImageURL:                     "registry.hostname.com/org@sha256:5be476dce1f7c1fbaf41bf9c0097e1725d7d26b74ea93543989d1a2b76fef4a5",
			ReleaseVersion:                 "release-version",
		}
	}

	getImages := func() *ctrlcommon.Images {
		return &ctrlcommon.Images{
			ReleaseVersion: "release-version",
			RenderConfigImages: ctrlcommon.RenderConfigImages{
				BaseOSContainerImage:           "registry.hostname.com/org@sha256:220a60ecd4a3c32c282622a625a54db9ba0ff55b5ba9c29c7064a2bc358b6a3e",
				BaseOSExtensionsContainerImage: "registry.hostname.com/org@sha256:5fb4ba1a651bae8057ec6b5cdafc93fa7e0b7d944d6f02a4b751de4e15464def",
			},
		}
	}

	testCases := []struct {
		name             string
		opts             MachineOSBuildOpts
		expectedName     string
		expectedPushspec string
		errExpected      bool
	}{
		{
			name:        "Missing MachineConfigPool",
			errExpected: true,
			opts: MachineOSBuildOpts{
				MachineOSConfig: getMachineOSConfig(),
			},
		},
		{
			name:        "Missing MachineOSConfig",
			errExpected: true,
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		{
			name:        "Mismatched MachineConfigPool name and MachineOSConfig",
			errExpected: true,
			opts: MachineOSBuildOpts{
				MachineOSConfig:   testhelpers.NewMachineOSConfigBuilder("worker").WithMachineConfigPool("other-pool").MachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		{
			name:         "Only MachineOSConfig and MachineConfigPool",
			expectedName: "worker-6782c5fc52947bc8fa6d105c9fe62b7d",
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		{
			name:         "With OSImageURLConfig",
			expectedName: "worker-9730a471098020fbd80ef65bba60228e",
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
				OSImageURLConfig:  getOSImageURLConfig(),
			},
		},
		{
			name:         "With Images",
			expectedName: "worker-752c26b0513e719737d73c6b1f5b1090",
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
				Images:            getImages(),
			},
		},
		{
			name:         "With OSImageURLConfig and Images",
			expectedName: "worker-06a4ec82da9f61d05b25e8031057bb68",
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
				OSImageURLConfig:  getOSImageURLConfig(),
				Images:            getImages(),
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if testCase.opts.MachineOSConfig != nil {
				testCase.opts.MachineOSConfig.Spec.BuildInputs.RenderedImagePushspec = "registry.hostname.com/org/repo:latest"
			}

			mosb, err := NewMachineOSBuild(testCase.opts)

			if testCase.errExpected {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, testCase.expectedName, mosb.Name)

			expectedPullspec := fmt.Sprintf("registry.hostname.com/org/repo:%s", testCase.expectedName)
			assert.Equal(t, expectedPullspec, mosb.Spec.RenderedImagePushspec)
			assert.Equal(t, testCase.opts.MachineConfigPool.Spec.Configuration.Name, mosb.Spec.DesiredConfig.Name)
			assert.NotNil(t, mosb.Status.BuildStart)

			assert.True(t, MachineOSBuildSelector(testCase.opts.MachineOSConfig, testCase.opts.MachineConfigPool).Matches(labels.Set(mosb.Labels)))
			assert.NotNil(t, mosb.Labels)
		})
	}
}
