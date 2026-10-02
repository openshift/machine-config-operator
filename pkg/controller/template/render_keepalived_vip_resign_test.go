package template

import (
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/vincent-petithory/dataurl"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// TestKeepalivedVipResignRendering verifies that the keepalived-vip-resign
// systemd unit and its script are rendered for on-prem platforms, that the
// unit is only enabled when the cluster uses the OpenShift-managed load
// balancer and VIPs are configured, and that all API and ingress VIPs end
// up in the script, for both master and worker roles.
// See https://issues.redhat.com/browse/OCPBUGS-109633
func TestKeepalivedVipResignRendering(t *testing.T) {
	controllerConfig, err := controllerConfigFromFile("test_data/controller_config_baremetal.yaml")
	if err != nil {
		t.Fatalf("failed to get controllerconfig config: %v", err)
	}

	tests := []struct {
		name          string
		apiVIPs       []string
		ingressVIPs   []string
		userManagedLB bool
		wantEnabled   bool
		wantVIPS      string
	}{
		{
			name:        "no VIPs",
			wantEnabled: false,
			wantVIPS:    `VIPS=""`,
		},
		{
			name:        "single stack",
			apiVIPs:     []string{"10.0.0.1"},
			ingressVIPs: []string{"10.0.0.2"},
			wantEnabled: true,
			wantVIPS:    `VIPS="10.0.0.1 10.0.0.2"`,
		},
		{
			name:        "dual stack",
			apiVIPs:     []string{"10.0.0.1", "fd00::1"},
			ingressVIPs: []string{"10.0.0.2", "fd00::2"},
			wantEnabled: true,
			wantVIPS:    `VIPS="10.0.0.1 fd00::1 10.0.0.2 fd00::2"`,
		},
		{
			name:        "ingress VIPs only",
			ingressVIPs: []string{"10.0.0.2"},
			wantEnabled: true,
			wantVIPS:    `VIPS=" 10.0.0.2"`,
		},
		{
			name:          "user-managed load balancer",
			apiVIPs:       []string{"10.0.0.1"},
			ingressVIPs:   []string{"10.0.0.2"},
			userManagedLB: true,
			wantEnabled:   false,
			wantVIPS:      `VIPS="10.0.0.1 10.0.0.2"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cc := controllerConfig.DeepCopy()
			cc.Spec.Infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs = tc.apiVIPs
			cc.Spec.Infra.Status.PlatformStatus.BareMetal.IngressIPs = tc.ingressVIPs
			if tc.userManagedLB {
				cc.Spec.Infra.Status.PlatformStatus.BareMetal.LoadBalancer = &configv1.BareMetalPlatformLoadBalancer{
					Type: configv1.LoadBalancerTypeUserManaged,
				}
			}

			cfgs, err := generateTemplateMachineConfigs(&RenderConfig{&cc.Spec, `{"dummy":"dummy"}`, "dummy", nil, nil}, templateDir)
			if err != nil {
				t.Fatalf("failed to generate machine configs: %v", err)
			}

			foundUnit := map[string]bool{}
			foundScript := map[string]bool{}
			for _, cfg := range cfgs {
				role := cfg.Labels["machineconfiguration.openshift.io/role"]
				ign, err := ctrlcommon.ParseAndConvertConfig(cfg.Spec.Config.Raw)
				if err != nil {
					t.Fatalf("failed to parse Ignition config for %s: %v", cfg.Name, err)
				}
				for _, u := range ign.Systemd.Units {
					if u.Name != "keepalived-vip-resign.service" {
						continue
					}
					foundUnit[role] = true
					if u.Enabled == nil || *u.Enabled != tc.wantEnabled {
						t.Errorf("%s: unit enabled = %v, want %v", cfg.Name, u.Enabled, tc.wantEnabled)
					}
				}
				for _, f := range ign.Storage.Files {
					if f.Path != "/usr/local/bin/keepalived-vip-resign.sh" {
						continue
					}
					foundScript[role] = true
					contents, err := dataurl.DecodeString(*f.Contents.Source)
					if err != nil {
						t.Fatalf("failed to decode script contents: %v", err)
					}
					if !strings.Contains(string(contents.Data), tc.wantVIPS) {
						t.Errorf("%s: script does not contain %q", cfg.Name, tc.wantVIPS)
					}
				}
			}
			for _, role := range []string{"master", "worker"} {
				if !foundUnit[role] {
					t.Errorf("keepalived-vip-resign.service unit not rendered for role %s", role)
				}
				if !foundScript[role] {
					t.Errorf("keepalived-vip-resign.sh script not rendered for role %s", role)
				}
			}
		})
	}
}
