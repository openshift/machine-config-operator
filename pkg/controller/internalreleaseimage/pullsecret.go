package internalreleaseimage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/klog/v2"
)

// MergeIRIAuthIntoPullSecret merges IRI registry authentication credentials
// into a dockerconfigjson pull secret. It adds an auth entry for the IRI
// registry host (api-int.<baseDomain>:<IRIRegistryPort>) so that kubelet can
// pull from it. Returns the merged bytes, a boolean indicating whether the
// pull secret was changed, and any error.
//
// This must be called during both bootstrap and in-cluster rendering to ensure
// the pull secret content is consistent, avoiding a rendered MachineConfig
// hash mismatch between bootstrap and in-cluster.
func MergeIRIAuthIntoPullSecret(pullSecretRaw []byte, password, baseDomain string) ([]byte, bool, error) {
	if password == "" {
		return nil, false, fmt.Errorf("IRI registry password must not be empty")
	}

	if strings.TrimSpace(baseDomain) == "" {
		return nil, false, fmt.Errorf("baseDomain must not be empty")
	}

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
