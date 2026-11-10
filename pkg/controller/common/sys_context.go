package common

import (
	"bytes"
	"fmt"

	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	operatorlistersv1alpha1 "github.com/openshift/client-go/operator/listers/operator/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

// NewSysContextFactory builds a SysContextFactory that resolves pull secrets,
// ControllerConfig, and registry mirror rules from informer-backed listers.
//
// secretLister reads the user-owned global pull secret from openshift-config.
// iriMerger supplies the InternalReleaseImage registry credentials, which are
// deliberately never written back into that global pull secret: it is
// user-owned, and writing to it would both create an ownership conflict and
// open a window where 00-master renders without credentials. Instead every
// consumer merges them at the point of use. Pass nil when there is no IRI
// merger to apply.
func NewSysContextFactory(
	ccLister mcfglistersv1.ControllerConfigLister,
	secretLister corelistersv1.SecretLister,
	iriMerger *IRISecretMerger,
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
		// On a cluster using an InternalReleaseImage, every image reference is
		// mirrored to the on-node IRI registry, which requires htpasswd auth.
		// Without this merge the registry answers 401 and image inspection
		// fails on any cluster with no route to the upstream registries.
		secret, err = mergeIRICredentialsIntoSecret(secret, iriMerger)
		if err != nil {
			return nil, err
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

// mergeIRICredentialsIntoSecret returns the pull secret with IRI registry
// credentials merged in, or the original secret unchanged when IRI is not in
// use. The secret is normalised to dockerconfigjson first so that a legacy
// dockercfg-typed pull secret merges correctly rather than silently skipping.
func mergeIRICredentialsIntoSecret(secret *corev1.Secret, merger *IRISecretMerger) (*corev1.Secret, error) {
	if merger == nil {
		return secret, nil
	}

	is, err := secrets.NewImageRegistrySecret(secret)
	if err != nil {
		return nil, fmt.Errorf("could not read pull secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	raw, err := is.JSONBytes(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return nil, fmt.Errorf("could not normalize pull secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}

	merged, err := merger.Merge(raw)
	if err != nil {
		return nil, fmt.Errorf("could not merge IRI registry credentials into pull secret: %w", err)
	}
	if bytes.Equal(merged, raw) {
		return secret, nil
	}

	// Copy rather than mutate: the input belongs to the informer cache.
	out := secret.DeepCopy()
	out.Type = corev1.SecretTypeDockerConfigJson
	out.Data = map[string][]byte{corev1.DockerConfigJsonKey: merged}
	return out, nil
}
