package runtimeassets

import (
	"fmt"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
)

func TestRevertService(t *testing.T) {
	mcoImagePullspec := "mco.image.pullspec"

	proxyContents := "EnvironmentFile=/etc/mco/proxy.env"

	alwaysExpectedContents := []string{
		fmt.Sprintf("ConditionPathExists=%s", RevertServiceMachineConfigFile),
		fmt.Sprintf("podman run --authfile=/var/lib/kubelet/config.json --rm --privileged --net=host -v /:/rootfs  --entrypoint machine-config-daemon '%s' firstboot-complete-machineconfig --persist-nics --machineconfig-file %s", mcoImagePullspec, RevertServiceMachineConfigFile),
		fmt.Sprintf("podman run --authfile=/var/lib/kubelet/config.json --rm --privileged --pid=host --net=host -v /:/rootfs  --entrypoint machine-config-daemon '%s' firstboot-complete-machineconfig --machineconfig-file %s", mcoImagePullspec, RevertServiceMachineConfigFile),
	}

	testCases := []struct {
		name               string
		ctrlcfg            *mcfgv1.ControllerConfig
		expectedContents   []string
		unexpectedContents []string
		errExpected        bool
	}{
		{
			name: "no proxy",
			ctrlcfg: &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Images: map[string]string{
						"machineConfigOperator": mcoImagePullspec,
					},
				},
			},
			expectedContents: alwaysExpectedContents,
			unexpectedContents: []string{
				proxyContents,
			},
		},
		{
			name: "with proxy",
			ctrlcfg: &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Proxy: &configv1.ProxyStatus{},
					Images: map[string]string{
						"machineConfigOperator": mcoImagePullspec,
					},
				},
			},
			expectedContents: append(alwaysExpectedContents, proxyContents),
		},
		{
			name:        "no mco image found",
			ctrlcfg:     &mcfgv1.ControllerConfig{},
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			rs, err := NewRevertService(testCase.ctrlcfg)
			if testCase.errExpected {
				assert.Error(t, err)
				assert.Nil(t, rs)
				return
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, rs)
			}

			ign, err := rs.Ignition()
			assert.NoError(t, err)
			assert.NotNil(t, ign)

			unit := ign.Systemd.Units[0]

			for _, item := range testCase.expectedContents {
				assert.Contains(t, *unit.Contents, item)
			}

			for _, item := range testCase.unexpectedContents {
				assert.NotContains(t, *unit.Contents, item)
			}

			assert.Equal(t, unit.Name, RevertServiceName)
			assert.True(t, *unit.Enabled)
		})
	}
}
