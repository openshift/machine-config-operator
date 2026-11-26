package globalpullsecret

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func entry(name, hostname, user, pass string) CredentialEntry {
	return CredentialEntry{Name: name, Hostname: hostname, Username: user, Password: pass}
}

func TestMergeCredentials_NoEntries_ReturnsBaselineCopy(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"rh"}}}`)

	result, err := MergeCredentials(baseline, nil)
	require.NoError(t, err)

	assert.JSONEq(t, string(baseline), string(result.MergedJSON))
	assert.Empty(t, result.Statuses)
}

func TestMergeCredentials_NewHostnameIsMerged(t *testing.T) {
	result, err := MergeCredentials([]byte(`{"auths":{}}`), []CredentialEntry{
		entry("quay", "quay.io", "user", "pass"),
	})
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))
	assert.JSONEq(t, `{"auth":"dXNlcjpwYXNz"}`, string(merged.Auths["quay.io"]))
	assert.Equal(t, credentialStatus{
		Applied: true,
		Reason:  "Merged",
		Message: "credential for \"quay.io\" merged into pull-secret-merged",
	}, result.Statuses["quay"])
}

func TestMergeCredentials_BaselineHostnameWins(t *testing.T) {
	result, err := MergeCredentials(
		[]byte(`{"auths":{"registry.redhat.io":{"auth":"baseline"}}}`),
		[]CredentialEntry{entry("rh", "registry.redhat.io", "user", "pass")},
	)
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))
	assert.JSONEq(t, `{"auth":"baseline"}`, string(merged.Auths["registry.redhat.io"]))
	assert.Equal(t, credentialStatus{
		Reason:  "BaselinePrecedence",
		Message: "baseline pull-secret credential for \"registry.redhat.io\" is used instead",
	}, result.Statuses["rh"])
}

func TestMergeCredentials_DuplicateHostname_ReturnsError(t *testing.T) {
	_, err := MergeCredentials([]byte(`{"auths":{}}`), []CredentialEntry{
		entry("first", "quay.io", "user", "pass"),
		entry("second", "quay.io", "other", "secret"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate hostname")
}

func TestMergeCredentials_PasswordMayContainColon(t *testing.T) {
	result, err := MergeCredentials([]byte(`{"auths":{}}`), []CredentialEntry{
		entry("quay", "quay.io", "user", "pass:word"),
	})
	require.NoError(t, err)

	var merged dockerConfigJSON
	require.NoError(t, json.Unmarshal(result.MergedJSON, &merged))
	assert.JSONEq(t, `{"auth":"`+encodeAuth("user", "pass:word")+`"}`, string(merged.Auths["quay.io"]))
}

func TestMergeCredentials_PreservesExtraFields(t *testing.T) {
	baseline := []byte(`{"auths":{"registry.redhat.io":{"auth":"rh"}},"HttpHeaders":{"X-Custom":"value"}}`)

	result, err := MergeCredentials(baseline, nil)
	require.NoError(t, err)

	assert.Contains(t, string(result.MergedJSON), `"HttpHeaders":{"X-Custom":"value"}`)
}

func TestMergeCredentials_DeterministicOutput(t *testing.T) {
	baseline := []byte(`{"auths":{"z.io":{"auth":"z"},"a.io":{"auth":"a"}}}`)
	entries := []CredentialEntry{entry("mid", "m.io", "m", "token")}

	first, err := MergeCredentials(baseline, entries)
	require.NoError(t, err)
	second, err := MergeCredentials(baseline, entries)
	require.NoError(t, err)

	assert.Equal(t, string(first.MergedJSON), string(second.MergedJSON))
	assert.Regexp(t, `"a.io".*"m.io".*"z.io"`, string(first.MergedJSON))
}

func TestMergeCredentials_InvalidBaselineJSON(t *testing.T) {
	_, err := MergeCredentials([]byte("not-json"), nil)
	assert.Error(t, err)
}
