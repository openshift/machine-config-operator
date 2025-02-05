package runtimeassets

import (
	"fmt"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
)

func TestRevertService(t *testing.T) {
	mcoImagePullspec := "mco.image.pullspec"

	proxyContents := []string{
		"EnvironmentFile=/etc/mco/proxy.env.backup",
		"ExecStartPost=rm /etc/mco/proxy.env.backup",
	}

	proxyStatus := configv1.ProxyStatus{
		HTTPProxy:  "http://1.2.3.4",
		HTTPSProxy: "https://5.6.7.8",
		NoProxy:    "no.proxy.local",
	}

	alwaysExpectedContents := []string{
		fmt.Sprintf("ConditionPathExists=%s", RevertServiceMachineConfigFile),
		fmt.Sprintf("podman run --authfile=/var/lib/kubelet/config.json --rm --privileged --net=host -v /:/rootfs  --entrypoint machine-config-daemon '%s' firstboot-complete-machineconfig --persist-nics --machineconfig-file %s", mcoImagePullspec, RevertServiceMachineConfigFile),
		fmt.Sprintf("podman run --authfile=/var/lib/kubelet/config.json --rm --privileged --pid=host --net=host -v /:/rootfs  --entrypoint machine-config-daemon '%s' firstboot-complete-machineconfig --machineconfig-file %s", mcoImagePullspec, RevertServiceMachineConfigFile),
		fmt.Sprintf("ExecStartPost=rm %s", RevertServiceMachineConfigFile),
	}

	testCases := []struct {
		name               string
		ctrlcfg            *mcfgv1.ControllerConfig
		mc                 *mcfgv1.MachineConfig
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
			expectedContents:   alwaysExpectedContents,
			unexpectedContents: proxyContents,
		},
		{
			name: "with proxy",
			ctrlcfg: &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Proxy: &proxyStatus,
					Images: map[string]string{
						"machineConfigOperator": mcoImagePullspec,
					},
				},
			},
			expectedContents: append(alwaysExpectedContents, proxyContents...),
		},
		{
			name:        "no mco image found",
			ctrlcfg:     &mcfgv1.ControllerConfig{},
			errExpected: true,
		},
		{
			name: "no proxy file found in MachineConfig despite proxy being enabled",
			mc:   helpers.NewMachineConfig("", map[string]string{}, "", []ign3types.File{}),
			ctrlcfg: &mcfgv1.ControllerConfig{
				Spec: mcfgv1.ControllerConfigSpec{
					Proxy: &proxyStatus,
					Images: map[string]string{
						"machineConfigOperator": mcoImagePullspec,
					},
				},
			},
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			mc := helpers.NewMachineConfig("", map[string]string{}, "", []ign3types.File{ctrlcommon.NewIgnFile("/etc/mco/proxy.env", "proxycontents")})
			if testCase.mc != nil {
				mc = testCase.mc
			}

			rs, err := NewRevertService(testCase.ctrlcfg, mc)
			if testCase.errExpected {
				// If the returned error is nil, try calling the Ignition() method to
				// see if it returns an error.
				if err == nil {
					ign, err := rs.Ignition()
					assert.Error(t, err)
					assert.Nil(t, ign)
					return
				}
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

			if testCase.ctrlcfg.Spec.Proxy != nil {
				assertFileExistsInIgnConfig(t, ign, RevertServiceProxyFile)
			}

			assertFileExistsInIgnConfig(t, ign, RevertServiceMachineConfigFile)
		})
	}
}

func assertFileExistsInIgnConfig(t *testing.T, igncfg *ign3types.Config, filename string) {
	t.Helper()

	file := findFileInIgnitionConfig(igncfg, filename)
	assert.NotNil(t, file, "expected to find file %q in ignition config", filename)

	_, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
	assert.NoError(t, err, "expected %s to be encoded", file.Path)
}
