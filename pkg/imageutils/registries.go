package imageutils

import (
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	apicfgv1 "github.com/openshift/api/config/v1"
	configv1 "github.com/openshift/api/config/v1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/runtime-utils/pkg/registries"
)

// GenerateRegistriesConfig builds a container runtime registries configuration by consolidating
// cluster image policies, registry mirrors (ICSP/IDMS/ITMS), and registry access controls.
func GenerateRegistriesConfig(
	image *configv1.Image,
	icspRules []*apioperatorsv1alpha1.ImageContentSourcePolicy,
	idmsRules []*apicfgv1.ImageDigestMirrorSet,
	itmsRules []*apicfgv1.ImageTagMirrorSet) (*sysregistriesv2.V2RegistriesConf, error) {

	// TODO: Consume this values from templates
	// Tracked by https://issues.redhat.com/browse/MCO-2060
	tomlConf := &sysregistriesv2.V2RegistriesConf{
		UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
	}

	var insecureScopes []string
	var blockedScopes []string
	if image != nil {
		insecureScopes = image.Spec.RegistrySources.InsecureRegistries
		blockedScopes = image.Spec.RegistrySources.BlockedRegistries
	}
	if err := registries.EditRegistriesConfig(tomlConf, insecureScopes, blockedScopes, icspRules, idmsRules, itmsRules); err != nil {
		return nil, err
	}
	return tomlConf, nil
}
