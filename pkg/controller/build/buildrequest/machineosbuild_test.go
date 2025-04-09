package buildrequest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
)

// Tests that a MachineOSBuild can be constructed correctly.
func TestMachineOSBuild(t *testing.T) {
	t.Parallel()

	poolName := "worker"

	getMachineOSConfig := func() *mcfgv1.MachineOSConfig {
		return testhelpers.NewMachineOSConfigBuilder(poolName).WithMachineConfigPool(poolName).MachineOSConfig()
	}

	getMachineConfigPool := func() *mcfgv1.MachineConfigPool {
		return testhelpers.NewMachineConfigPoolBuilder(poolName).MachineConfigPool()
	}

	// Some of the test cases expect the hash name to be the same. This is that hash value.
	expectedCommonHashName := "worker-55592464e51104dcc274a300565fec9e"

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
			expectedName: expectedCommonHashName,
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
				OSImageURLConfig:  fixtures.OSImageURLConfig(),
			},
		},
		// These cases ensure that pausing the MachineConfigPool does not affect the hash.
		{
			name:         "Unpaused MachineConfigPool",
			expectedName: expectedCommonHashName,
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: getMachineConfigPool(),
				OSImageURLConfig:  fixtures.OSImageURLConfig(),
			},
		},
		{
			name:         "Paused MachineConfigPool",
			expectedName: expectedCommonHashName,
			opts: MachineOSBuildOpts{
				MachineOSConfig:   getMachineOSConfig(),
				MachineConfigPool: testhelpers.NewMachineConfigPoolBuilder(poolName).WithPaused().MachineConfigPool(),
				OSImageURLConfig:  fixtures.OSImageURLConfig(),
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if testCase.opts.MachineOSConfig != nil {
				testCase.opts.MachineOSConfig.Spec.RenderedImagePushSpec = "registry.hostname.com/org/repo:latest"
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
			assert.Equal(t, expectedPullspec, string(mosb.Spec.RenderedImagePushSpec))
			assert.Equal(t, testCase.opts.MachineConfigPool.Spec.Configuration.Name, mosb.Spec.MachineConfig.Name)
			assert.NotNil(t, mosb.Status.BuildStart)

			assert.True(t, utils.MachineOSBuildSelector(testCase.opts.MachineOSConfig, testCase.opts.MachineConfigPool, testCase.opts.MachineConfig).Matches(labels.Set(mosb.Labels)))
			assert.Equal(t, utils.GetMachineOSBuildLabels(testCase.opts.MachineOSConfig, testCase.opts.MachineConfigPool, testCase.opts.MachineConfig), mosb.Labels)
		})
	}
}

// Ensures that the labels are consistent between NewMachineOSBuild and the
// test fixture given the same input data. This is required because it would
// cause a circular import for the fixtures package to use the
// utils.GetMachineOSBuildLabels() function..
func TestMachineOSBuildLabelConsistency(t *testing.T) {
	t.Parallel()

	obj := fixtures.NewObjectsForTest("worker")

	mosb, err := NewMachineOSBuild(MachineOSBuildOpts{
		MachineConfigPool: obj.MachineConfigPool,
		MachineOSConfig:   obj.MachineOSConfig,
		OSImageURLConfig:  fixtures.OSImageURLConfig(),
	})

	assert.NoError(t, err)

	assert.True(t, utils.MachineOSBuildSelector(obj.MachineOSConfig, obj.MachineConfigPool).Matches(labels.Set(mosb.Labels)))
	assert.Equal(t, mosb.Labels, utils.GetMachineOSBuildLabels(obj.MachineOSConfig, obj.MachineConfigPool))
	assert.Equal(t, obj.MachineOSBuild.Labels, mosb.Labels)
}
