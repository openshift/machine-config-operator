package template

import (
	"strconv"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/require"
)

func TestIsSNOCoreDNSEnabledTemplateFunc(t *testing.T) {
	cc := newControllerConfig("test-cluster")
	cc.Spec.Infra.Status.PlatformStatus.Type = configv1.NonePlatformType
	cc.Spec.Infra.Status.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
	on := ctrlcommon.NewFeatureGatesHardcodedHandler([]configv1.FeatureGateName{features.FeatureGateUnifiedClusterManagedDNSAndLB}, nil)
	off := ctrlcommon.NewFeatureGatesHardcodedHandler(nil, nil)
	for _, tc := range []struct {
		name    string
		config  *mcfgv1.ControllerConfigSpec
		handler ctrlcommon.FeatureGatesHandler
		want    bool
	}{
		{"enabled", &cc.Spec, on, true},
		{"disabled", &cc.Spec, off, false},
		{"no handler", &cc.Spec, nil, false},
		{"empty config", nil, on, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderTemplate(RenderConfig{
				ControllerConfigSpec: tc.config,
				FeatureGatesHandler:  tc.handler,
			}, tc.name, []byte(`{{if isSNOCoreDNSEnabled}}true{{else}}false{{end}}`))
			require.NoError(t, err)
			require.Equal(t, strconv.FormatBool(tc.want), string(got))
		})
	}
}
