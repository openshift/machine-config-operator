package internalreleaseimage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// IRIBaseUsername is the base username for IRI registry authentication (generation 0).
// During credential rotation, a generation counter is appended to form successive
// usernames: "openshift" (generation 0), "openshift1" (generation 1),
// "openshift2" (generation 2), etc.
const IRIBaseUsername = "openshift"

// MergeIRIAuthIntoPullSecret merges IRI registry authentication credentials
// into a dockerconfigjson pull secret using the default username (generation 0).
// It adds an auth entry for the IRI registry host (api-int.<baseDomain>:22625)
// so that kubelet can pull from it.
//
// This must be called during both bootstrap and in-cluster rendering to ensure
// the pull secret content is consistent, avoiding a rendered MachineConfig
// hash mismatch between bootstrap and in-cluster.
func MergeIRIAuthIntoPullSecret(pullSecretRaw []byte, password, baseDomain string) ([]byte, error) {
	return MergeIRIAuthIntoPullSecretWithUsername(pullSecretRaw, IRIBaseUsername, password, baseDomain)
}

// MergeIRIAuthIntoPullSecretWithUsername merges IRI registry authentication credentials
// into a dockerconfigjson pull secret using the specified username. This is used during
// credential rotation to update the pull secret with the new generation username
// (e.g., "openshift1") after all nodes have been updated with the new htpasswd.
func MergeIRIAuthIntoPullSecretWithUsername(pullSecretRaw []byte, username, password, baseDomain string) ([]byte, error) {
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

	authValue := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))

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

	// Validate that the only change is the IRI registry entry. All other
	// auth entries must be preserved with identical values. This prevents a
	// bug in the merge logic from silently dropping or modifying existing
	// registry credentials, which would brick the cluster.
	if err := validatePullSecretAuthsPreserved(pullSecretRaw, mergedBytes, iriRegistryHost); err != nil {
		return nil, fmt.Errorf("pull secret validation failed after IRI merge: %w", err)
	}

	return mergedBytes, nil
}

// ExtractIRICredentialsFromPullSecret extracts the current IRI registry username
// and password from a dockerconfigjson pull secret. It looks for the auth entry
// keyed by api-int.<baseDomain>:22625 and decodes the credentials from the
// base64 "username:password" auth value.
// Returns empty strings if the entry is not found or cannot be parsed.
func ExtractIRICredentialsFromPullSecret(pullSecretRaw []byte, baseDomain string) (username, password string) {
	iriRegistryHost := fmt.Sprintf("api-int.%s:22625", baseDomain)

	var dockerConfig map[string]interface{}
	if err := json.Unmarshal(pullSecretRaw, &dockerConfig); err != nil {
		return "", ""
	}

	auths, ok := dockerConfig["auths"].(map[string]interface{})
	if !ok {
		return "", ""
	}

	entry, ok := auths[iriRegistryHost].(map[string]interface{})
	if !ok {
		return "", ""
	}

	authValue, ok := entry["auth"].(string)
	if !ok {
		return "", ""
	}

	decoded, err := base64.StdEncoding.DecodeString(authValue)
	if err != nil {
		return "", ""
	}

	// Auth format is "username:password"
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", ""
	}

	return parts[0], parts[1]
}

// NextIRIUsername computes the next generation username for credential rotation.
// "openshift" (generation 0) becomes "openshift1" (generation 1),
// "openshift1" becomes "openshift2", and so on.
func NextIRIUsername(currentUsername string) string {
	if currentUsername == IRIBaseUsername {
		return IRIBaseUsername + "1"
	}

	suffix := strings.TrimPrefix(currentUsername, IRIBaseUsername)
	gen, err := strconv.Atoi(suffix)
	if err != nil {
		// Unrecognized format, start at generation 1
		return IRIBaseUsername + "1"
	}

	return fmt.Sprintf("%s%d", IRIBaseUsername, gen+1)
}

// HtpasswdHasValidEntry checks whether the htpasswd content contains an entry
// for the given username whose bcrypt hash matches the given password.
func HtpasswdHasValidEntry(htpasswd, username, password string) bool {
	for _, line := range strings.Split(strings.TrimSpace(htpasswd), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == username {
			if bcrypt.CompareHashAndPassword([]byte(parts[1]), []byte(password)) == nil {
				return true
			}
		}
	}
	return false
}

// GenerateHtpasswdEntry generates a single htpasswd entry in the format
// "username:bcrypt-hash\n".
func GenerateHtpasswdEntry(username, password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("failed to generate bcrypt hash: %w", err)
	}
	return fmt.Sprintf("%s:%s\n", username, string(hash)), nil
}

// GenerateDualHtpasswd generates an htpasswd file with two entries: one for the
// current credentials (from the pull secret) and one for the new credentials
// (from the auth secret). This is used during credential rotation to ensure
// that both old and new passwords are accepted during the MachineConfig rollout.
//
// The distribution registry's htpasswd implementation uses a map keyed by
// username, so the two entries MUST have different usernames. The rotation
// scheme uses generation-numbered usernames: "openshift" (generation 0),
// "openshift1" (generation 1), "openshift2" (generation 2), etc.
func GenerateDualHtpasswd(currentUsername, currentPassword, newUsername, newPassword string) (string, error) {
	if currentUsername == newUsername {
		return "", fmt.Errorf("current and new htpasswd usernames must differ to support dual credentials")
	}

	currentEntry, err := GenerateHtpasswdEntry(currentUsername, currentPassword)
	if err != nil {
		return "", fmt.Errorf("failed to generate current htpasswd entry: %w", err)
	}

	newEntry, err := GenerateHtpasswdEntry(newUsername, newPassword)
	if err != nil {
		return "", fmt.Errorf("failed to generate new htpasswd entry: %w", err)
	}

	return currentEntry + newEntry, nil
}

// validatePullSecretAuthsPreserved verifies that the only change between the
// original and merged pull secrets is the IRI registry entry. All other auth
// entries must be preserved with identical values. This prevents a bug in the
// merge logic from silently dropping or modifying existing registry credentials,
// which would brick the cluster.
func validatePullSecretAuthsPreserved(original, merged []byte, iriRegistryHost string) error {
	var origConfig, mergedConfig map[string]interface{}
	if err := json.Unmarshal(original, &origConfig); err != nil {
		return fmt.Errorf("could not parse original pull secret: %w", err)
	}
	if err := json.Unmarshal(merged, &mergedConfig); err != nil {
		return fmt.Errorf("could not parse merged pull secret: %w", err)
	}

	origAuths, _ := origConfig["auths"].(map[string]interface{})
	mergedAuths, _ := mergedConfig["auths"].(map[string]interface{})

	for host, origEntry := range origAuths {
		mergedEntry, ok := mergedAuths[host]
		if !ok {
			return fmt.Errorf("registry auth entry %q was present in original pull secret but missing after merge", host)
		}
		// The IRI entry is expected to change during merge
		if host == iriRegistryHost {
			continue
		}
		origJSON, _ := json.Marshal(origEntry)
		mergedJSON, _ := json.Marshal(mergedEntry)
		if !bytes.Equal(origJSON, mergedJSON) {
			return fmt.Errorf("registry auth entry %q was modified during IRI merge; only the IRI entry (%s) should change", host, iriRegistryHost)
		}
	}
	return nil
}
