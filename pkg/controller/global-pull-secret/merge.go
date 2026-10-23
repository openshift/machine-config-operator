package globalpullsecret

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

const (
	// GlobalPullSecretLabel is the label that marks a Secret for aggregation
	// into openshift-config/pull-secret.
	GlobalPullSecretLabel = "config.openshift.io/global-pull-secret"

	// GlobalPullSecretPriorityAnnotation controls merge precedence among
	// modular Secrets. Higher numeric value = higher priority.
	// Secrets without this annotation are treated as priority 0.
	GlobalPullSecretPriorityAnnotation = "config.openshift.io/global-pull-secret-priority"
)

// dockerConfigJSON mirrors the ~/.docker/config.json schema.
// It preserves top-level fields beyond "auths" (e.g. HttpHeaders) so the
// baseline is not silently stripped during merge.
type dockerConfigJSON struct {
	Auths map[string]json.RawMessage `json:"auths,omitempty"`

	// Extra holds all top-level JSON fields that are not "auths".
	// We round-trip them from the baseline so nothing is lost on write.
	Extra map[string]json.RawMessage `json:"-"`
}

// MarshalJSON emits auths plus any extra top-level fields from the baseline.
func (d dockerConfigJSON) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage, len(d.Extra)+1)
	for k, v := range d.Extra {
		m[k] = v
	}
	m["auths"] = marshalSortedAuths(d.Auths)
	return marshalSortedTopLevel(m), nil
}

// UnmarshalJSON parses the full dockerconfigjson, separating "auths" from
// other top-level keys so we can preserve them.
func (d *dockerConfigJSON) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	d.Extra = make(map[string]json.RawMessage)
	for k, v := range raw {
		if k == "auths" {
			var auths map[string]json.RawMessage
			if err := json.Unmarshal(v, &auths); err != nil {
				return fmt.Errorf("parsing auths: %w", err)
			}
			d.Auths = auths
		} else {
			d.Extra[k] = v
		}
	}
	if d.Auths == nil {
		d.Auths = make(map[string]json.RawMessage)
	}
	return nil
}

// modularSource is a validated, parsed modular Secret ready for merging.
type modularSource struct {
	Name     string
	Priority int
	Auths    map[string]json.RawMessage
}

// ValidationError captures one invalid modular Secret.
type ValidationError struct {
	SecretName string
	Reason     string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("secret %q: %s", e.SecretName, e.Reason)
}

// MergeResult is the output of MergeGlobalPullSecret.
type MergeResult struct {
	// MergedJSON is the final dockerconfigjson to write to pull-secret.
	MergedJSON []byte
	// InvalidSources lists modular Secrets that failed validation (skipped).
	InvalidSources []ValidationError
}

// MergeGlobalPullSecret computes a merged pull-secret from a preserved
// baseline and a set of labeled modular Secrets per the EP algorithm:
//
//  1. Parse baseline; baseline auths always win on conflict.
//  2. Validate each modular Secret independently; skip invalid ones.
//  3. Sort valid modular Secrets: priority descending, name ascending.
//  4. Iterate from lowest to highest precedence; for each registry key
//     not in baseline, write to effective map (higher-precedence overwrites).
//  5. Serialize with sorted registry keys for stable output.
//
// baselineRaw is the preserved original pull-secret .dockerconfigjson bytes.
// modularSecrets are the labeled Secrets from openshift-config.
func MergeGlobalPullSecret(baselineRaw []byte, modularSecrets []*corev1.Secret) (*MergeResult, error) {
	var baseline dockerConfigJSON
	if err := json.Unmarshal(baselineRaw, &baseline); err != nil {
		return nil, fmt.Errorf("parsing baseline pull-secret: %w", err)
	}

	// Validate and parse each modular Secret.
	var valid []modularSource
	var invalid []ValidationError
	for _, s := range modularSecrets {
		src, err := validateModularSecret(s)
		if err != nil {
			invalid = append(invalid, *err)
			continue
		}
		valid = append(valid, *src)
	}

	// Sort by increasing precedence (last applied wins):
	// lower priority first; within same priority, name ascending.
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Priority != valid[j].Priority {
			return valid[i].Priority < valid[j].Priority
		}
		return valid[i].Name < valid[j].Name
	})

	// Build effective auths: start from baseline (immutable), layer modular.
	effective := make(map[string]json.RawMessage, len(baseline.Auths))
	for k, v := range baseline.Auths {
		effective[k] = v
	}

	for _, src := range valid {
		for registry, cred := range src.Auths {
			if _, inBaseline := baseline.Auths[registry]; inBaseline {
				continue // baseline always wins
			}
			effective[registry] = cred
		}
	}

	merged := dockerConfigJSON{
		Auths: effective,
		Extra: baseline.Extra, // preserve baseline HttpHeaders etc.
	}

	mergedBytes, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshaling merged pull-secret: %w", err)
	}

	return &MergeResult{
		MergedJSON:     mergedBytes,
		InvalidSources: invalid,
	}, nil
}

// validateModularSecret checks a single labeled Secret per EP rules:
//   - Type must be kubernetes.io/dockerconfigjson
//   - .dockerconfigjson key must exist
//   - Value must be valid JSON with an "auths" map
func validateModularSecret(s *corev1.Secret) (*modularSource, *ValidationError) {
	if s.Type != corev1.SecretTypeDockerConfigJson {
		return nil, &ValidationError{
			SecretName: s.Name,
			Reason:     fmt.Sprintf("type is %q, expected %q", s.Type, corev1.SecretTypeDockerConfigJson),
		}
	}

	raw, ok := s.Data[corev1.DockerConfigJsonKey]
	if !ok || len(raw) == 0 {
		return nil, &ValidationError{
			SecretName: s.Name,
			Reason:     fmt.Sprintf("missing or empty %s key", corev1.DockerConfigJsonKey),
		}
	}

	var parsed struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &ValidationError{
			SecretName: s.Name,
			Reason:     fmt.Sprintf("invalid JSON in %s: %v", corev1.DockerConfigJsonKey, err),
		}
	}
	if parsed.Auths == nil {
		return nil, &ValidationError{
			SecretName: s.Name,
			Reason:     "JSON does not contain an auths map",
		}
	}

	priority := 0
	if v, exists := s.Annotations[GlobalPullSecretPriorityAnnotation]; exists {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, &ValidationError{
				SecretName: s.Name,
				Reason:     fmt.Sprintf("invalid priority annotation value %q: %v", v, err),
			}
		}
		priority = p
	}

	return &modularSource{
		Name:     s.Name,
		Priority: priority,
		Auths:    parsed.Auths,
	}, nil
}

// marshalSortedAuths produces a JSON object with registry keys in sorted order.
func marshalSortedAuths(auths map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(auths))
	for k := range auths {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf := []byte("{")
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		keyBytes, _ := json.Marshal(k)
		buf = append(buf, keyBytes...)
		buf = append(buf, ':')
		buf = append(buf, auths[k]...)
	}
	buf = append(buf, '}')
	return buf
}

// marshalSortedTopLevel produces a JSON object with top-level keys in sorted order.
func marshalSortedTopLevel(m map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	buf := []byte("{")
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		keyBytes, _ := json.Marshal(k)
		buf = append(buf, keyBytes...)
		buf = append(buf, ':')
		buf = append(buf, m[k]...)
	}
	buf = append(buf, '}')
	return buf
}
