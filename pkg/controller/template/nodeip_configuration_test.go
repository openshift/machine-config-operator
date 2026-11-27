package template

import (
	"regexp"
	"strings"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNodeIPConfigurationStackFlags checks the node-ip stack flags of each nodeip-configuration unit per IP family.
func TestNodeIPConfigurationStackFlags(t *testing.T) {
	units := map[string]string{
		"none":      "nodeip-configuration.service",
		"baremetal": "nodeip-configuration.service",
		"kubevirt":  "nodeip-configuration.service",
		"vsphere":   "nodeip-configuration-vsphere-upi.service",
	}
	families := []struct {
		family                             mcfgv1.IPFamiliesType
		vips                               []string
		preferIPv6, dualStack, singleStack bool
	}{
		{mcfgv1.IPFamiliesIPv4, []string{"192.0.2.1"}, false, false, false},
		{mcfgv1.IPFamiliesIPv6, []string{"2001:db8::1"}, true, false, true},
		{mcfgv1.IPFamiliesDualStack, []string{"192.0.2.1", "2001:db8::1"}, false, true, false},
		{mcfgv1.IPFamiliesDualStackIPv6Primary, []string{"2001:db8::1", "192.0.2.1"}, true, true, false},
	}

	for platform, unit := range units {
		for _, f := range families {
			t.Run(platform+"/"+string(f.family), func(t *testing.T) {
				cc, err := controllerConfigFromFile(configs[platform])
				require.NoError(t, err)
				cc.Spec.IPFamilies = f.family
				// The on-prem unit is only enabled when the cluster has VIPs.
				if bm := cc.Spec.Infra.Status.PlatformStatus.BareMetal; bm != nil {
					bm.APIServerInternalIPs = f.vips
					bm.IngressIPs = f.vips
				}

				args := nodeIPSetArgs(t, &cc.Spec, unit)
				assert.Equal(t, f.preferIPv6, args["--prefer-ipv6"], "--prefer-ipv6")
				assert.Equal(t, f.dualStack, args["--dual-stack"], "--dual-stack")
				assert.Equal(t, f.singleStack, args["--single-stack"], "--single-stack")
			})
		}
	}
}

// nodeIPSetCmd captures the arguments of "node-ip set" up to the end of the command.
var nodeIPSetCmd = regexp.MustCompile(`node-ip\s+\\\s+set\s+\\([^;]*);`)

// nodeIPSetArgs returns the "node-ip set" flags of an enabled unit that must match for master and worker.
func nodeIPSetArgs(t *testing.T, spec *mcfgv1.ControllerConfigSpec, unit string) map[string]bool {
	t.Helper()
	cfgs, err := generateTemplateMachineConfigs(&RenderConfig{ControllerConfigSpec: spec, PullSecret: `{"dummy":"dummy"}`, TLSMinVersion: "dummy"}, templateDir)
	require.NoError(t, err)

	byRole := map[string]map[string]bool{}
	for _, cfg := range cfgs {
		role := cfg.Labels[mcfgv1.MachineConfigRoleLabelKey]
		ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
		require.NoError(t, err)
		for _, u := range ign.Systemd.Units {
			if u.Name != unit || u.Contents == nil {
				continue
			}
			require.True(t, u.Enabled != nil && *u.Enabled, "%s in %s is not enabled", unit, cfg.Name)
			m := nodeIPSetCmd.FindStringSubmatch(*u.Contents)
			require.NotNil(t, m, "%s in %s has no node-ip set command", unit, cfg.Name)
			args := map[string]bool{}
			for _, f := range strings.Fields(m[1]) {
				if strings.HasPrefix(f, "--") {
					args[f] = true
				}
			}
			require.NotContains(t, byRole, role, "%s rendered twice for %s", unit, role)
			byRole[role] = args
		}
	}
	require.Contains(t, byRole, masterRole, "%s not rendered for master", unit)
	require.Contains(t, byRole, workerRole, "%s not rendered for worker", unit)
	require.Equal(t, byRole[masterRole], byRole[workerRole], "%s differs between master and worker", unit)
	return byRole[masterRole]
}
