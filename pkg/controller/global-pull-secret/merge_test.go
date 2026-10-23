package globalpullsecret

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// helper: build a labeled modular Secret.
func newModularSecret(name string, priority string, auths map[string]interface{}) *corev1.Secret {
	authsBytes, _ := json.Marshal(map[string]interface{}{"auths": auths})
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "openshift-config",
			Labels:    map[string]string{GlobalPullSecretLabel: "true"},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: authsBytes,
		},
	}
	if priority != "" {
		s.Annotations = map[string]string{GlobalPullSecretPriorityAnnotation: priority}
	}
	return s
}

func cred(user, token string) map[string]interface{} {
	return map[string]interface{}{"username": user, "auth": token}
}

func TestMergeGlobalPullSecret_BaselineWins(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"baseline-rh"},"cloud.openshift.com":{"auth":"baseline-cloud"}}}`)

	modular := []*corev1.Secret{
		newModularSecret("team-private", "", map[string]interface{}{
			"registry.redhat.io":   cred("attacker", "bad"),
			"registry.example.com": cred("user", "good"),
		}),
	}

	result, err := MergeGlobalPullSecret(baseline, modular)
	require.NoError(t, err)
	assert.Empty(t, result.InvalidSources)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))

	// Baseline keys are untouched.
	assert.JSONEq(t, `{"auth":"baseline-rh"}`, string(merged.Auths["registry.redhat.io"]))
	assert.JSONEq(t, `{"auth":"baseline-cloud"}`, string(merged.Auths["cloud.openshift.com"]))
	// Modular-only key is added.
	assert.Contains(t, merged.Auths, "registry.example.com")
}

func TestMergeGlobalPullSecret_PriorityTieBreakByName(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"rh"}}}`)

	modular := []*corev1.Secret{
		newModularSecret("team-a-quay", "10", map[string]interface{}{
			"quay.io": cred("team-a", "cred-a"),
		}),
		newModularSecret("team-b-quay", "10", map[string]interface{}{
			"quay.io": cred("team-b", "cred-b"),
		}),
	}

	result, err := MergeGlobalPullSecret(baseline, modular)
	require.NoError(t, err)
	assert.Empty(t, result.InvalidSources)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))

	// Same priority: name ascending → team-a-quay < team-b-quay.
	// Iterate low-to-high precedence: team-b-quay applied first, team-a-quay second.
	// team-a-quay should NOT overwrite because it has same priority and
	// sorts before team-b-quay (lower name = lower precedence).
	// Actually per EP: "sort by priority desc, name asc" → among equal priority,
	// name ascending = team-a first, team-b second. Iterate low-to-high →
	// team-a applied first, team-b applied last → team-b wins.
	assert.JSONEq(t, `{"username":"team-b","auth":"cred-b"}`, string(merged.Auths["quay.io"]))
}

func TestMergeGlobalPullSecret_HigherPriorityWins(t *testing.T) {
	baseline := []byte(`{"auths":{}}`)

	modular := []*corev1.Secret{
		newModularSecret("low-prio", "1", map[string]interface{}{
			"quay.io": cred("low", "low-token"),
		}),
		newModularSecret("high-prio", "10", map[string]interface{}{
			"quay.io": cred("high", "high-token"),
		}),
	}

	result, err := MergeGlobalPullSecret(baseline, modular)
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))

	assert.JSONEq(t, `{"username":"high","auth":"high-token"}`, string(merged.Auths["quay.io"]))
}

func TestMergeGlobalPullSecret_MissingPriorityTreatedAsZero(t *testing.T) {
	baseline := []byte(`{"auths":{}}`)

	modular := []*corev1.Secret{
		newModularSecret("no-annotation", "", map[string]interface{}{
			"quay.io": cred("default", "def-tok"),
		}),
		newModularSecret("has-priority", "5", map[string]interface{}{
			"quay.io": cred("priority", "pri-tok"),
		}),
	}

	result, err := MergeGlobalPullSecret(baseline, modular)
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))

	// Priority 5 > 0 (missing) → has-priority wins.
	assert.JSONEq(t, `{"username":"priority","auth":"pri-tok"}`, string(merged.Auths["quay.io"]))
}

func TestMergeGlobalPullSecret_SkipInvalidSecrets(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"rh"}}}`)

	modular := []*corev1.Secret{
		// Valid
		newModularSecret("good-secret", "", map[string]interface{}{
			"registry.example.com": cred("good", "tok"),
		}),
		// Invalid: wrong type
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-type", Namespace: "openshift-config"},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`)},
		},
		// Invalid: bad JSON
		{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-json", Namespace: "openshift-config"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`not-json`)},
		},
		// Invalid: missing auths key
		{
			ObjectMeta: metav1.ObjectMeta{Name: "no-auths", Namespace: "openshift-config"},
			Type:       corev1.SecretTypeDockerConfigJson,
			Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"something":"else"}`)},
		},
	}

	result, err := MergeGlobalPullSecret(baseline, modular)
	require.NoError(t, err)

	// Three invalid Secrets reported.
	assert.Len(t, result.InvalidSources, 3)
	invalidNames := make([]string, len(result.InvalidSources))
	for i, v := range result.InvalidSources {
		invalidNames[i] = v.SecretName
	}
	assert.Contains(t, invalidNames, "bad-type")
	assert.Contains(t, invalidNames, "bad-json")
	assert.Contains(t, invalidNames, "no-auths")

	// Valid Secret still merged.
	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))
	assert.Contains(t, merged.Auths, "registry.example.com")
	assert.Contains(t, merged.Auths, "registry.redhat.io")
}

func TestMergeGlobalPullSecret_DeleteModularSecret(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"rh"}}}`)

	// First merge: one modular Secret.
	result1, err := MergeGlobalPullSecret(baseline, []*corev1.Secret{
		newModularSecret("team-quay", "5", map[string]interface{}{
			"quay.io": cred("team", "tok"),
		}),
	})
	require.NoError(t, err)
	var m1 dockerConfigJSON
	require.NoError(t, json.Unmarshal(result1.MergedJSON, &m1))
	assert.Contains(t, m1.Auths, "quay.io")

	// Second merge: modular Secret removed (empty list).
	result2, err := MergeGlobalPullSecret(baseline, []*corev1.Secret{})
	require.NoError(t, err)
	var m2 dockerConfigJSON
	require.NoError(t, json.Unmarshal(result2.MergedJSON, &m2))
	assert.NotContains(t, m2.Auths, "quay.io")
	assert.Contains(t, m2.Auths, "registry.redhat.io")
}

func TestMergeGlobalPullSecret_SortedOutputStable(t *testing.T) {
	baseline := []byte(`{"auths":{"z-registry.io":{"auth":"z"},"a-registry.io":{"auth":"a"}}}`)

	result, err := MergeGlobalPullSecret(baseline, []*corev1.Secret{
		newModularSecret("mid", "", map[string]interface{}{
			"m-registry.io": cred("m", "m-tok"),
		}),
	})
	require.NoError(t, err)

	// Run twice: output must be identical (deterministic).
	result2, err := MergeGlobalPullSecret(baseline, []*corev1.Secret{
		newModularSecret("mid", "", map[string]interface{}{
			"m-registry.io": cred("m", "m-tok"),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, string(result.MergedJSON), string(result2.MergedJSON))

	// Keys must be sorted: a-registry.io, m-registry.io, z-registry.io.
	assert.Contains(t, string(result.MergedJSON), `"a-registry.io"`)
	assert.Less(t,
		indexOf(string(result.MergedJSON), "a-registry.io"),
		indexOf(string(result.MergedJSON), "m-registry.io"),
	)
	assert.Less(t,
		indexOf(string(result.MergedJSON), "m-registry.io"),
		indexOf(string(result.MergedJSON), "z-registry.io"),
	)
}

func TestMergeGlobalPullSecret_PreservesBaselineHttpHeaders(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"rh"}},"HttpHeaders":{"X-Custom":"value"}}`)

	result, err := MergeGlobalPullSecret(baseline, []*corev1.Secret{
		newModularSecret("team", "", map[string]interface{}{
			"quay.io": cred("t", "tok"),
		}),
	})
	require.NoError(t, err)

	// HttpHeaders from baseline must survive in merged output.
	assert.Contains(t, string(result.MergedJSON), `"HttpHeaders"`)
	assert.Contains(t, string(result.MergedJSON), `"X-Custom"`)
}

func TestMergeGlobalPullSecret_InvalidPriorityAnnotation(t *testing.T) {
	baseline := []byte(`{"auths":{}}`)

	modular := []*corev1.Secret{
		newModularSecret("bad-priority", "not-a-number", map[string]interface{}{
			"quay.io": cred("u", "t"),
		}),
	}

	result, err := MergeGlobalPullSecret(baseline, modular)
	require.NoError(t, err)
	assert.Len(t, result.InvalidSources, 1)
	assert.Equal(t, "bad-priority", result.InvalidSources[0].SecretName)
}

func TestMergeGlobalPullSecret_EmptyModularList(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"rh"}}}`)

	result, err := MergeGlobalPullSecret(baseline, nil)
	require.NoError(t, err)
	assert.Empty(t, result.InvalidSources)
	assert.JSONEq(t, `{"auths":{"registry.redhat.io":{"auth":"rh"}}}`, string(result.MergedJSON))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
