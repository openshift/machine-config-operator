package common

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	mcfglistersv1alpha1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	corelistersv1 "k8s.io/client-go/listers/core/v1"
)

// errIRIDisabled is returned by resolve when the NoRegistryClusterInstall
// feature gate is off or the InternalReleaseImage resource is absent.
// Merge treats it as a skip signal rather than an error.
var errIRIDisabled = errors.New("IRI not enabled or not present")

// IRISecretMerger merges IRI registry credentials into a pull secret.
// Construct via NewIRISecretMerger (controller use) or NewIRISecretMergerFromObjects
// (bootstrap use); then call Merge for each pull secret that needs updating.
type IRISecretMerger struct {
	// resolve returns the password and baseDomain needed for merging, or
	// errIRIDisabled when IRI is not in use on this cluster.
	resolve func() (password, baseDomain string, err error)
}

// NewIRISecretMerger creates an IRISecretMerger that resolves the feature gate,
// IRI resource, credentials secret, and ControllerConfig from the informer cache
// at merge time. Use this in controllers where informers are available.
// fgHandler must not be nil.
func NewIRISecretMerger(
	secretLister corelistersv1.SecretLister,
	ccLister mcfglistersv1.ControllerConfigLister,
	iriLister mcfglistersv1alpha1.InternalReleaseImageLister,
	fgHandler FeatureGatesHandler,
) *IRISecretMerger {
	return &IRISecretMerger{
		resolve: func() (string, string, error) {
			if !fgHandler.Enabled(features.FeatureGateNoRegistryClusterInstall) {
				return "", "", errIRIDisabled
			}
			_, err := iriLister.Get(InternalReleaseImageInstanceName)
			if apierrors.IsNotFound(err) {
				return "", "", errIRIDisabled
			}
			if err != nil {
				return "", "", fmt.Errorf("could not get InternalReleaseImage: %w", err)
			}
			secret, err := secretLister.Secrets(MCONamespace).Get(InternalReleaseImageAuthSecretName)
			if err != nil {
				return "", "", fmt.Errorf("could not get IRI auth secret: %w", err)
			}
			cconfig, err := ccLister.Get(ControllerConfigName)
			if err != nil {
				return "", "", fmt.Errorf("could not get ControllerConfig: %w", err)
			}
			return extractIRICredentials(secret, cconfig)
		},
	}
}

// NewIRISecretMergerFromObjects creates an IRISecretMerger from pre-fetched objects.
// Use this during bootstrap where informer caches are not yet available.
// The feature gate and iri checks are deferred to Merge time so the constructor
// never returns an error; if either check fails, Merge skips and logs.
func NewIRISecretMergerFromObjects(
	secret *corev1.Secret,
	cconfig *mcfgv1.ControllerConfig,
	fgHandler FeatureGatesHandler,
	iri *mcfgv1alpha1.InternalReleaseImage,
) *IRISecretMerger {
	return &IRISecretMerger{
		resolve: func() (string, string, error) {
			if fgHandler == nil || !fgHandler.Enabled(features.FeatureGateNoRegistryClusterInstall) {
				return "", "", errIRIDisabled
			}
			if iri == nil {
				return "", "", errIRIDisabled
			}
			return extractIRICredentials(secret, cconfig)
		},
	}
}

// Merge merges IRI registry credentials into pullSecretRaw, adding auth entries
// for api-int.<baseDomain>:<IRIRegistryPort> (all nodes) and
// localhost:<IRIRegistryPort> (masters, where the registry runs locally).
// If the feature gate is disabled or the InternalReleaseImage resource is absent,
// Merge logs and returns pullSecretRaw unchanged.
func (m *IRISecretMerger) Merge(pullSecretRaw []byte) ([]byte, error) {
	password, baseDomain, err := m.resolve()
	if errors.Is(err, errIRIDisabled) {
		klog.V(4).Info("Skipping IRI registry credential merge: IRI not enabled or not present")
		return pullSecretRaw, nil
	}
	if err != nil {
		return nil, err
	}
	merged, changed, err := mergeIRIRegistryCredentialsIntoPullSecret(pullSecretRaw, password, baseDomain)
	if err != nil {
		return nil, err
	}
	if changed {
		klog.V(4).Info("Merged IRI registry credentials into pull secret")
	}
	return merged, nil
}

// extractIRICredentials validates and extracts the password and baseDomain from
// the IRI credentials secret and ControllerConfig.
func extractIRICredentials(secret *corev1.Secret, cconfig *mcfgv1.ControllerConfig) (password, baseDomain string, err error) {
	if secret == nil {
		return "", "", fmt.Errorf("IRI registry credentials secret must not be nil")
	}
	if cconfig == nil {
		return "", "", fmt.Errorf("ControllerConfig must not be nil")
	}
	if cconfig.Spec.DNS == nil {
		return "", "", fmt.Errorf("ControllerConfig DNS spec must not be nil")
	}
	pw, ok := secret.Data["password"]
	if !ok || len(pw) == 0 {
		return "", "", fmt.Errorf("IRI registry credentials secret missing or empty \"password\" field")
	}
	bd := cconfig.Spec.DNS.Spec.BaseDomain
	if strings.TrimSpace(bd) == "" {
		return "", "", fmt.Errorf("ControllerConfig baseDomain must not be empty")
	}
	return string(pw), bd, nil
}

// mergeIRIRegistryCredentialsIntoPullSecret merges IRI registry authentication
// credentials into a dockerconfigjson pull secret. It adds auth entries for
// api-int.<baseDomain>:<IRIRegistryPort> (all nodes) and
// localhost:<IRIRegistryPort> (masters, where the registry runs locally).
// Returns the merged bytes, a boolean indicating whether the pull secret was
// changed, and any error.
func mergeIRIRegistryCredentialsIntoPullSecret(pullSecretRaw []byte, password, baseDomain string) ([]byte, bool, error) {
	// The IRI registry is reachable via api-int on all nodes, and also via
	// localhost on master nodes where it runs locally. registries.conf mirror
	// rules on masters use localhost:22625, so credentials must be present for
	// both hostnames to avoid authentication failures.
	iriRegistryAPIIntHost := fmt.Sprintf("api-int.%s:%d", baseDomain, IRIRegistryPort)
	iriRegistryLocalHost := fmt.Sprintf("localhost:%d", IRIRegistryPort)

	var dockerConfig map[string]interface{}
	if err := json.Unmarshal(pullSecretRaw, &dockerConfig); err != nil {
		return nil, false, fmt.Errorf("could not parse pull secret: %w", err)
	}

	auths, ok := dockerConfig["auths"].(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("pull secret missing 'auths' field")
	}

	authValue := base64.StdEncoding.EncodeToString([]byte(IRIRegistryUsername + ":" + password))

	// Check if both IRI entries already exist and are current — no update needed.
	if pullSecretHasAuth(auths, iriRegistryAPIIntHost, authValue) && pullSecretHasAuth(auths, iriRegistryLocalHost, authValue) {
		return pullSecretRaw, false, nil
	}

	klog.V(4).Infof("Merging IRI auth credentials into pull secret for %s and %s", iriRegistryAPIIntHost, iriRegistryLocalHost)
	auths[iriRegistryAPIIntHost] = map[string]interface{}{
		"auth": authValue,
	}
	auths[iriRegistryLocalHost] = map[string]interface{}{
		"auth": authValue,
	}

	mergedBytes, err := json.Marshal(dockerConfig)
	if err != nil {
		return nil, false, fmt.Errorf("could not marshal merged pull secret: %w", err)
	}

	return mergedBytes, true, nil
}

// pullSecretHasAuth returns true if auths[host] exists and its "auth" field
// matches expected.
func pullSecretHasAuth(auths map[string]interface{}, host, expected string) bool {
	e, ok := auths[host].(map[string]interface{})
	return ok && e["auth"] == expected
}
