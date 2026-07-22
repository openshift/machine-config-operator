package common

import (
	"fmt"

	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	operatorlistersv1alpha1 "github.com/openshift/client-go/operator/listers/operator/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

// NewSysContextFactory builds a SysContextFactory that resolves pull secrets,
// ControllerConfig, and registry mirror rules from informer-backed listers.
func NewSysContextFactory(
	ccLister mcfglistersv1.ControllerConfigLister,
	secretLister corelistersv1.SecretLister,
	imgLister configlistersv1.ImageLister,
	icspLister operatorlistersv1alpha1.ImageContentSourcePolicyLister,
	idmsLister configlistersv1.ImageDigestMirrorSetLister,
	itmsLister configlistersv1.ImageTagMirrorSetLister,
) imageutils.SysContextFactory {
	return func() (*imageutils.SysContext, error) {
		cc, err := ccLister.Get(ControllerConfigName)
		if err != nil {
			return nil, fmt.Errorf("failed to get controller config: %w", err)
		}
		secret, err := secretLister.Secrets(OpenshiftConfigNamespace).Get(GlobalPullSecretName)
		if err != nil {
			return nil, fmt.Errorf("failed to get pull secret: %w", err)
		}

		builder := imageutils.NewSysContextBuilder().
			WithSecret(secret).
			WithControllerConfig(cc)

		imgCfg, err := imgLister.Get("cluster")
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get cluster image config: %w", err)
		}
		icspRules, err := icspLister.List(labels.Everything())
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to list ICSP rules: %w", err)
		}
		idmsRules, err := idmsLister.List(labels.Everything())
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to list IDMS rules: %w", err)
		}
		itmsRules, err := itmsLister.List(labels.Everything())
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("failed to list ITMS rules: %w", err)
		}

		if imgCfg != nil || len(icspRules) != 0 || len(idmsRules) != 0 || len(itmsRules) != 0 {
			registriesConf, err := imageutils.GenerateRegistriesConfig(imgCfg, icspRules, idmsRules, itmsRules)
			if err != nil {
				return nil, fmt.Errorf("failed to generate registries config: %w", err)
			}
			builder.WithRegistriesConfig(registriesConf)
		}

		return builder.Build()
	}
}
