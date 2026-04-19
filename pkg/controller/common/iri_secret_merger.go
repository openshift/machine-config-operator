package common

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// IRISecretMerger merges IRI registry credentials into a pull secret.
// Construct via NewIRISecretMerger; then call Merge for each pull secret.
type IRISecretMerger struct {
	password   string
	baseDomain string
}

// NewIRISecretMerger creates an IRISecretMerger from the IRI credentials secret
// and the ControllerConfig. Returns an error if either is nil or incomplete.
func NewIRISecretMerger(secret *corev1.Secret, cconfig *mcfgv1.ControllerConfig) (*IRISecretMerger, error) {
	if secret == nil {
		return nil, fmt.Errorf("IRI registry credentials secret must not be nil")
	}
	if cconfig.Spec.DNS == nil {
		return nil, fmt.Errorf("ControllerConfig DNS spec must not be nil")
	}
	password, ok := secret.Data["password"]
	if !ok || len(password) == 0 {
		return nil, fmt.Errorf("IRI registry credentials secret missing or empty \"password\" field")
	}
	baseDomain := cconfig.Spec.DNS.Spec.BaseDomain
	if strings.TrimSpace(baseDomain) == "" {
		return nil, fmt.Errorf("ControllerConfig baseDomain must not be empty")
	}
	return &IRISecretMerger{
		password:   string(password),
		baseDomain: baseDomain,
	}, nil
}

// Merge merges IRI registry credentials into pullSecretRaw, adding auth entries
// for api-int.<baseDomain>:<IRIRegistryPort> (all nodes) and
// localhost:<IRIRegistryPort> (masters, where the registry runs locally).
// Returns the merged bytes and any error.
func (m *IRISecretMerger) Merge(pullSecretRaw []byte) ([]byte, error) {
	merged, changed, err := mergeIRIRegistryCredentialsIntoPullSecret(pullSecretRaw, m.password, m.baseDomain)
	if err != nil {
		return nil, err
	}
	if changed {
		klog.V(4).Info("Merged IRI registry credentials into pull secret")
	}
	return merged, nil
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
