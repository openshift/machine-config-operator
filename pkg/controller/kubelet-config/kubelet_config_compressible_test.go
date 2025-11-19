package kubeletconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestNewCompressibleMachineConfig(t *testing.T) {
	kubeletContents := []byte("apiVersion: kubelet.config.k8s.io/v1beta1\nkind: KubeletConfiguration")

	mc, err := newCompressibleMachineConfig("worker", kubeletContents)

	require.NoError(t, err)
	assert.Equal(t, "50-worker-compressible-kubelet-override", mc.Name)
	assert.Contains(t, mc.ObjectMeta.Annotations, "openshift-patch-reference")

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	require.NoError(t, err)
	require.Len(t, ignCfg.Storage.Files, 1)
	assert.Equal(t, KubeletConfPath, ignCfg.Storage.Files[0].Path)

	contents, err := ctrlcommon.DecodeIgnitionFileContents(ignCfg.Storage.Files[0].Contents.Source, ignCfg.Storage.Files[0].Contents.Compression)
	require.NoError(t, err)
	assert.Equal(t, kubeletContents, contents)
}

func TestRunCompressibleBootstrap(t *testing.T) {
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.AWSPlatformType)
	pools := []*mcfgv1.MachineConfigPool{
		helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
		helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
	}
	fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{}, nil)

	mcs, err := RunCompressibleBootstrap(pools, cc, "../../../templates", nil, fgHandler)

	require.NoError(t, err)
	require.Len(t, mcs, 2)

	assert.Equal(t, "50-master-compressible-kubelet-override", mcs[0].Name)
	assert.Equal(t, "50-worker-compressible-kubelet-override", mcs[1].Name)

	for _, mc := range mcs {
		ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
		require.NoError(t, err)
		require.Len(t, ignCfg.Storage.Files, 1)
		assert.Equal(t, KubeletConfPath, ignCfg.Storage.Files[0].Path)
	}
}

// This test ensures that the template replacement works as expected
func TestCompressibleMachineConfigTemplateReplacement(t *testing.T) {
	cc := newControllerConfig(ctrlcommon.ControllerConfigName, configv1.AWSPlatformType)
	cc.Spec.ClusterDNSIP = "10.96.0.10"

	fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{}, nil)

	_, kubeletContents, err := generateOriginalKubeletConfigWithFeatureGates(cc, "../../../templates", "master", fgHandler, nil)
	require.NoError(t, err)

	mc, err := newCompressibleMachineConfig("master", kubeletContents)
	require.NoError(t, err)

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	require.NoError(t, err)
	require.Len(t, ignCfg.Storage.Files, 1)

	contents, err := ctrlcommon.DecodeIgnitionFileContents(ignCfg.Storage.Files[0].Contents.Source, ignCfg.Storage.Files[0].Contents.Compression)
	require.NoError(t, err)

	contentsStr := string(contents)
	assert.Contains(t, contentsStr, "10.96.0.10")
	assert.NotContains(t, contentsStr, "{{.ClusterDNSIP}}")
}
