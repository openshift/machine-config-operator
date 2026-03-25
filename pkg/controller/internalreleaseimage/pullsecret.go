package internalreleaseimage

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// IRIRegistryUsername is the fixed username used for IRI registry htpasswd authentication.
const IRIRegistryUsername = "openshift"

// GenerateHtpasswdEntry generates an htpasswd-formatted line for the given username
// and password using bcrypt hashing.
func GenerateHtpasswdEntry(username, password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to generate bcrypt hash: %w", err)
	}
	return fmt.Sprintf("%s:%s", username, string(hash)), nil
}

// HtpasswdMatchesPassword reports whether the given htpasswd line matches
// the provided username and password.
func HtpasswdMatchesPassword(htpasswd, username, password string) bool {
	prefix := username + ":"
	if !strings.HasPrefix(htpasswd, prefix) {
		return false
	}
	hash := []byte(strings.TrimPrefix(htpasswd, prefix))
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

// MergeIRIAuthIntoPullSecret merges IRI registry authentication credentials
// into a dockerconfigjson pull secret. It adds an auth entry for the IRI
// registry host (api-int.<baseDomain>:22625) so that kubelet can pull from it.
//
// This must be called during both bootstrap and in-cluster rendering to ensure
// the pull secret content is consistent, avoiding a rendered MachineConfig
// hash mismatch between bootstrap and in-cluster.
func MergeIRIAuthIntoPullSecret(pullSecretRaw []byte, password, baseDomain string) ([]byte, error) {
	if password == "" {
		return pullSecretRaw, nil
	}

	if strings.TrimSpace(baseDomain) == "" {
		return nil, fmt.Errorf("baseDomain must not be empty")
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

	authValue := base64.StdEncoding.EncodeToString([]byte(IRIRegistryUsername + ":" + password))

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
