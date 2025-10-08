package template

import (
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKubeletConfigDirParameter verifies that the --config-dir parameter is correctly configured
// in kubelet.service systemd unit files for worker nodes only.
// This test ensures that:
// - Worker nodes have --config-dir=/etc/openshift/kubelet.conf.d in their kubelet.service
// - Master and arbiter nodes do NOT have the --config-dir parameter
//
// The --config-dir parameter allows the kubelet to load additional configuration from a directory,
// which is useful for dynamic kubelet configuration updates on worker nodes.
func TestKubeletConfigDirParameter(t *testing.T) {
	testCases := []struct {
		name               string
		platform           configv1.PlatformType
		controllerConfig   string
		expectedWorkerFlag bool
		expectedMasterFlag bool
	}{
		{
			name:               "AWS",
			platform:           configv1.AWSPlatformType,
			controllerConfig:   "./test_data/controller_config_aws.yaml",
			expectedWorkerFlag: true,
			expectedMasterFlag: false,
		},
		{
			name:               "BareMetal",
			platform:           configv1.BareMetalPlatformType,
			controllerConfig:   "./test_data/controller_config_baremetal.yaml",
			expectedWorkerFlag: true,
			expectedMasterFlag: false,
		},
		{
			name:               "GCP",
			platform:           configv1.GCPPlatformType,
			controllerConfig:   "./test_data/controller_config_gcp.yaml",
			expectedWorkerFlag: true,
			expectedMasterFlag: false,
		},
		{
			name:               "None",
			platform:           configv1.NonePlatformType,
			controllerConfig:   "./test_data/controller_config_none.yaml",
			expectedWorkerFlag: true,
			expectedMasterFlag: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			controllerConfig, err := controllerConfigFromFile(tc.controllerConfig)
			require.NoError(t, err, "Failed to load controller config for %s", tc.name)

			cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, templateDir)
			require.NoError(t, err, "Failed to generate machine configs for %s", tc.name)

			kubeletConfigs := make(map[string]*string)

			for _, cfg := range cfgs {
				role, ok := cfg.Labels[mcfgv1.MachineConfigRoleLabelKey]
				require.True(t, ok, "Machine config missing role label")

				ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
				require.NoError(t, err, "Failed to parse Ignition config for %s/%s", tc.name, role)

				for _, unit := range ign.Systemd.Units {
					if unit.Name == "kubelet.service" && unit.Contents != nil {
						kubeletConfigs[role] = unit.Contents
						break
					}
				}
			}

			for role, kubeletUnit := range kubeletConfigs {

				hasConfigDirFlag := strings.Contains(*kubeletUnit, "--config-dir=/etc/openshift/kubelet.conf.d")

				switch role {
				case workerRole:
					if tc.expectedWorkerFlag {
						assert.True(t, hasConfigDirFlag,
							"Worker node should have --config-dir parameter for platform %s", tc.name)
					} else {
						assert.False(t, hasConfigDirFlag,
							"Worker node should not have --config-dir parameter for platform %s", tc.name)
					}
				case masterRole:
					if tc.expectedMasterFlag {
						assert.True(t, hasConfigDirFlag,
							"Master node should have --config-dir parameter for platform %s", tc.name)
					} else {
						assert.False(t, hasConfigDirFlag,
							"Master node should not have --config-dir parameter for platform %s", tc.name)
					}
				case arbiterRole:
					if tc.expectedMasterFlag {
						assert.True(t, hasConfigDirFlag,
							"Arbiter node should have --config-dir parameter for platform %s", tc.name)
					} else {
						assert.False(t, hasConfigDirFlag,
							"Arbiter node should not have --config-dir parameter for platform %s", tc.name)
					}
				}

				if hasConfigDirFlag {
					assert.Contains(t, *kubeletUnit, "--config-dir=/etc/openshift/kubelet.conf.d",
						"--config-dir should point to /etc/openshift/kubelet.conf.d for %s/%s", tc.name, role)
				}
			}
		})
	}
}

// TestKubeletConfigDirParameterSpecific tests the exact location and format of the --config-dir parameter
func TestKubeletConfigDirParameterSpecific(t *testing.T) {
	controllerConfig, err := controllerConfigFromFile("./test_data/controller_config_aws.yaml")
	require.NoError(t, err, "Failed to load AWS controller config")

	cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, templateDir)
	require.NoError(t, err, "Failed to generate machine configs")

	var kubeletUnit *string
	for _, cfg := range cfgs {
		if role, ok := cfg.Labels[mcfgv1.MachineConfigRoleLabelKey]; ok && role == workerRole {
			ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
			require.NoError(t, err, "Failed to parse worker Ignition config")

			for _, unit := range ign.Systemd.Units {
				if unit.Name == "kubelet.service" && unit.Contents != nil {
					kubeletUnit = unit.Contents
					break
				}
			}
			if kubeletUnit != nil {
				break
			}
		}
	}

	require.NotNil(t, kubeletUnit, "kubelet.service unit not found in worker config")

	lines := strings.Split(*kubeletUnit, "\n")
	var execStartLines []string

	inExecStart := false
	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "ExecStart=") {
			inExecStart = true
			execStartLines = append(execStartLines, trimmedLine)
		} else if inExecStart && strings.HasSuffix(trimmedLine, "\\") {
			execStartLines = append(execStartLines, trimmedLine)
		} else if inExecStart && !strings.HasSuffix(trimmedLine, "\\") {
			execStartLines = append(execStartLines, trimmedLine)
			break
		}
	}

	require.NotEmpty(t, execStartLines, "ExecStart lines not found in kubelet.service")

	fullExecStart := strings.Join(execStartLines, " ")

	assert.Contains(t, fullExecStart, "--config-dir=/etc/openshift/kubelet.conf.d",
		"Worker kubelet.service should contain --config-dir=/etc/openshift/kubelet.conf.d")

	configIndex := strings.Index(fullExecStart, "--config=/etc/kubernetes/kubelet.conf")
	configDirIndex := strings.Index(fullExecStart, "--config-dir=/etc/openshift/kubelet.conf.d")
	bootstrapIndex := strings.Index(fullExecStart, "--bootstrap-kubeconfig=/etc/kubernetes/kubeconfig")

	assert.Greater(t, configDirIndex, configIndex,
		"--config-dir should appear after --config parameter")
	assert.Less(t, configDirIndex, bootstrapIndex,
		"--config-dir should appear before --bootstrap-kubeconfig parameter")

	t.Logf("Worker kubelet.service ExecStart:\n%s", fullExecStart)
}
