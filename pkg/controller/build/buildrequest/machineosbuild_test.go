package buildrequest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
)

// Tests that a MachineOSBuild can be constructed correctly.
func TestMachineOSBuild(t *testing.T) {
	t.Parallel()

	poolName := "worker"

	getMachineOSConfig := func() *mcfgv1alpha1.MachineOSConfig {
		return testhelpers.NewMachineOSConfigBuilder(poolName).WithMachineConfigPool(poolName).MachineOSConfig()
	}

	getMachineConfigPool := func() *mcfgv1.MachineConfigPool {
		return testhelpers.NewMachineConfigPoolBuilder(poolName).MachineConfigPool()
	}

	getOSImageURLConfig := func() *ctrlcommon.OSImageURLConfig {
		return &ctrlcommon.OSImageURLConfig{
			BaseOSContainerImage:           "registry.hostname.com/org@sha256:220a60ecd4a3c32c282622a625a54db9ba0ff55b5ba9c29c7064a2bc358b6a3e",
			BaseOSExtensionsContainerImage: "registry.hostname.com/org@sha256:5fb4ba1a651bae8057ec6b5cdafc93fa7e0b7d944d6f02a4b751de4e15464def",
			OSImageURL:                     "registry.hostname.com/org@sha256:5be476dce1f7c1fbaf41bf9c0097e1725d7d26b74ea93543989d1a2b76fef4a5",
			ReleaseVersion:                 "release-version",
		}
	}

	testCases := []struct {
		name         string
		opts         MachineOSBuildOpts
		expectedName string
		errExpected  bool
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
			errExpected:  true,
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
			},
		},
		// These cases ensure that the hashed name remains stable regardless of
		// which source of truth is used for the base OS image, extensions image,
		// and / or release version. In these cases, the source of truth can either
		// be the value from the MachineOSConfig or the OSImageURLConfig struct.
		{
			name:         "All values from OSImageURLConfig",
			expectedName: "worker-60211aafc6404066c9696898483f320c",
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
				OSImageURLConfig:  getOSImageURLConfig(),
			},
		},
		{
			name:         "Base OS image pullspec provided by MachineOSConfig equal to OSImageURLConfig",
			expectedName: "worker-60211aafc6404066c9696898483f320c",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithBaseOSImagePullspec(getOSImageURLConfig().BaseOSContainerImage).
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
			},
		},
		{
			name:         "Extensions image provided by provided by MachineOSConfig equal to OSImageURLConfig",
			expectedName: "worker-60211aafc6404066c9696898483f320c",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithExtensionsImagePullspec(getOSImageURLConfig().BaseOSExtensionsContainerImage).
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
			},
		},
		{
			name:         "Release version provided by MachineOSConfig equal to OSImageURLConfig",
			expectedName: "worker-60211aafc6404066c9696898483f320c",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithReleaseVersion(getOSImageURLConfig().ReleaseVersion).
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
			},
		},
		{
			name:         "All values provided by MachineOSConfig equal to OSImageURLConfig values",
			expectedName: "worker-60211aafc6404066c9696898483f320c",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithBaseOSImagePullspec(getOSImageURLConfig().BaseOSContainerImage).
					WithExtensionsImagePullspec(getOSImageURLConfig().BaseOSExtensionsContainerImage).
					WithReleaseVersion(getOSImageURLConfig().ReleaseVersion).
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
			},
		},
		// These cases ensure that should the value on the MachineOSConfig differ
		// from what is in the OSImageURLConfig (provided it is not empty!), the
		// hash will change.
		{
			name:         "Custom base OS image pullspec provided by MachineOSConfig",
			expectedName: "worker-6cbd9c6ea5335474c48493851ce4988c",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithBaseOSImagePullspec("registry.hostname.com/org/repo:custom-os-image").
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
			},
		},
		{
			name:         "Custom extensions image provided by provided by MachineOSConfig",
			expectedName: "worker-c40e8ead3bf2407887c4e9bbe46c8f9d",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithExtensionsImagePullspec("registry.hostname.com/org/repo:custom-extensions-image").
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
			},
		},
		{
			name:         "Custom release version provided by MachineOSConfig",
			expectedName: "worker-d24d98601f32491fa9d741fe7c4e0f39",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithReleaseVersion("custom-release-version").
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
			},
		},
		{
			name:         "All custom values provided by MachineOSConfig",
			expectedName: "worker-d66000275f70748faaddf769bff055fd",
			opts: MachineOSBuildOpts{
				MachineConfigPool: getMachineConfigPool(),
				MachineOSConfig: testhelpers.NewMachineOSConfigBuilder(poolName).
					WithMachineConfigPool(poolName).
					WithBaseOSImagePullspec("registry.hostname.com/org/repo:custom-os-image").
					WithExtensionsImagePullspec("registry.hostname.com/org/repo:custom-extensions-image").
					WithReleaseVersion("custom-release-version").
					MachineOSConfig(),
				OSImageURLConfig: getOSImageURLConfig(),
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

			assert.True(t, utils.MachineOSBuildSelector(testCase.opts.MachineOSConfig, testCase.opts.MachineConfigPool).Matches(labels.Set(mosb.Labels)))
			assert.NotNil(t, mosb.Labels)
		})
	}
}
