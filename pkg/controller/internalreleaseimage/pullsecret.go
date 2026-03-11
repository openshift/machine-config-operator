package internalreleaseimage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// MergeIRIAuthIntoPullSecret merges IRI registry authentication credentials
// into a dockerconfigjson pull secret. It adds an auth entry for the IRI
// registry host (api-int.<baseDomain>:22625) so that kubelet can pull from it.
//
// This must be called during both bootstrap and in-cluster rendering to ensure
// the pull secret content is consistent, avoiding a rendered MachineConfig
// hash mismatch between bootstrap and in-cluster.
func MergeIRIAuthIntoPullSecret(pullSecretRaw []byte, password string, baseDomain string) ([]byte, error) {
	if password == "" {
		return pullSecretRaw, nil
	}

	iriRegistryHost := fmt.Sprintf("api-int.%s:22625", baseDomain)

	var dockerConfig map[string]interface{}
	if err := json.Unmarshal(pullSecretRaw, &dockerConfig); err != nil {
		return nil, fmt.Errorf("could not parse pull secret: %w", err)
	}

	auths, ok := dockerConfig["auths"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("pull secret missing 'auths' field")
	}

	authValue := base64.StdEncoding.EncodeToString([]byte("openshift:" + password))

	// Check if IRI entry already exists and is current
	if existing, ok := auths[iriRegistryHost].(map[string]interface{}); ok {
		if existing["auth"] == authValue {
			return pullSecretRaw, nil
		}
	}

	auths[iriRegistryHost] = map[string]interface{}{
		"auth": authValue,
	}

	mergedBytes, err := json.Marshal(dockerConfig)
	if err != nil {
		return nil, fmt.Errorf("could not marshal merged pull secret: %w", err)
	}

	return mergedBytes, nil
}
