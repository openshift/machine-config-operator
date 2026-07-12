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

// TestKubeletConfigDirParameter verifies that the --config-dir parameter is present
// in kubelet.service systemd unit files for all node roles (worker, master, arbiter).
func TestKubeletConfigDirParameter(t *testing.T) {
	testCases := []struct {
		name             string
		platform         configv1.PlatformType
		controllerConfig string
	}{
		{
			name:             "AWS",
			platform:         configv1.AWSPlatformType,
			controllerConfig: "./test_data/controller_config_aws.yaml",
		},
		{
			name:             "BareMetal",
			platform:         configv1.BareMetalPlatformType,
			controllerConfig: "./test_data/controller_config_baremetal.yaml",
		},
		{
			name:             "GCP",
			platform:         configv1.GCPPlatformType,
			controllerConfig: "./test_data/controller_config_gcp.yaml",
		},
		{
			name:             "None",
			platform:         configv1.NonePlatformType,
			controllerConfig: "./test_data/controller_config_none.yaml",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			controllerConfig, err := controllerConfigFromFile(tc.controllerConfig)
			require.NoError(t, err, "Failed to load controller config for %s", tc.name)

			cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, "dummy", nil, "", "", nil}, templateDir)
			require.NoError(t, err, "Failed to generate machine configs for %s", tc.name)

			for _, cfg := range cfgs {
				role, ok := cfg.Labels[mcfgv1.MachineConfigRoleLabelKey]
				require.True(t, ok, "Machine config missing role label")

				ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
				require.NoError(t, err, "Failed to parse Ignition config for %s/%s", tc.name, role)

				for _, unit := range ign.Systemd.Units {
					if unit.Name == "kubelet.service" && unit.Contents != nil {
						assert.Contains(t, *unit.Contents, "--config-dir=/etc/openshift/kubelet.conf.d",
							"%s node should have --config-dir for platform %s", role, tc.name)
						break
					}
				}
			}
		})
	}
}

// TestKubeletConfigDirParameterSpecific tests the exact location and format of the --config-dir parameter
func TestKubeletConfigDirParameterSpecific(t *testing.T) {
	controllerConfig, err := controllerConfigFromFile("./test_data/controller_config_aws.yaml")
	require.NoError(t, err, "Failed to load AWS controller config")

	cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&controllerConfig.Spec, `{"dummy":"dummy"}`, "dummy", nil, "", "", nil}, templateDir)
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
