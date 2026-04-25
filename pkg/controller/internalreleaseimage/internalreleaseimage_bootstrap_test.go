package internalreleaseimage

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
)

func TestRunInternalReleaseImageBootstrap(t *testing.T) {
	configs, err := RunInternalReleaseImageBootstrap(&mcfgv1alpha1.InternalReleaseImage{}, iriCertSecret().obj, cconfig().obj, nil)
	assert.NoError(t, err)
	verifyAllInternalReleaseImageMachineConfigs(t, configs)
}

func TestRunInternalReleaseImageBootstrap_WithTLSProfile(t *testing.T) {
	modernProfile := &configv1.TLSSecurityProfile{
		Type: configv1.TLSProfileModernType,
	}
	configs, err := RunInternalReleaseImageBootstrap(&mcfgv1alpha1.InternalReleaseImage{}, iriCertSecret().obj, cconfig().obj, modernProfile)
	assert.NoError(t, err)
	assert.Len(t, configs, 2)

	// Master config should have TLS 1.3 minimum version from the Modern profile.
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(configs[0].Spec.Config.Raw)
	assert.NoError(t, err)
	assert.Len(t, ignCfg.Systemd.Units, 1)
	assert.Contains(t, *ignCfg.Systemd.Units[0].Contents, "REGISTRY_HTTP_TLS_MINIMUMTLS=tls1.3")
	// Modern profile uses TLS 1.3 which has no configurable cipher suites.
	assert.NotContains(t, *ignCfg.Systemd.Units[0].Contents, "REGISTRY_HTTP_TLS_CIPHERSUITES")
}
