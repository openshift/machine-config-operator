package internalreleaseimage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
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
		return pullSecretRaw, false, nil
	}

	if strings.TrimSpace(baseDomain) == "" {
		return nil, false, fmt.Errorf("baseDomain must not be empty")
	}

	iriRegistryHost := fmt.Sprintf("api-int.%s:%d", baseDomain, IRIRegistryPort)

	var dockerConfig map[string]interface{}
	if err := json.Unmarshal(pullSecretRaw, &dockerConfig); err != nil {
		return nil, false, fmt.Errorf("could not parse pull secret: %w", err)
	}

	auths, ok := dockerConfig["auths"].(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("pull secret missing 'auths' field")
	}

	authValue := base64.StdEncoding.EncodeToString([]byte(IRIRegistryUsername + ":" + password))

	// Check if IRI entry already exists and is current — no update needed.
	if existing, ok := auths[iriRegistryHost].(map[string]interface{}); ok {
		if existing["auth"] == authValue {
			return pullSecretRaw, false, nil
		}
	}

	auths[iriRegistryHost] = map[string]interface{}{
		"auth": authValue,
	}

	mergedBytes, err := json.Marshal(dockerConfig)
	if err != nil {
		return nil, false, fmt.Errorf("could not marshal merged pull secret: %w", err)
	}

	return mergedBytes, true, nil
}
