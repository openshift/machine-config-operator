package bootimage

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testAWSRoleARN        = "arn:aws:iam::123456789012:role/test-machine-config-controller"
	testAWSTokenFile      = "/var/run/secrets/openshift/serviceaccount/token"
	testAWSAccessKeyID    = "AKIATESTACCESSKEYID1"
	testAWSSecretKey      = "test/secret+access+key+value0123456789ab"
	testAWSOtherAccessKey = "AKIATESTOTHERACCESSK"
	testAWSOtherSecretKey = "other/secret+access+key+value0123456789"
)

// testAWSSecretValues are credential values that must never appear in error messages.
var testAWSSecretValues = []string{testAWSAccessKeyID, testAWSSecretKey, testAWSOtherAccessKey, testAWSOtherSecretKey}

// ccoctlSTSProfile matches the "credentials" value that ccoctl renders into its Secret manifests.
const ccoctlSTSProfile = "[default]\n" +
	"sts_regional_endpoints = regional\n" +
	"role_arn = " + testAWSRoleARN + "\n" +
	"web_identity_token_file = " + testAWSTokenFile

// ccoStaticProfile matches the "credentials" value that the Cloud Credential Operator writes in
// mint and passthrough modes alongside the top-level static keys.
const ccoStaticProfile = "[default]\n" +
	"aws_access_key_id = " + testAWSAccessKeyID + "\n" +
	"aws_secret_access_key = " + testAWSSecretKey

func newTestAWSCredentialsSecret(data map[string]string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "aws-cloud-credentials", Namespace: "openshift-machine-config-operator"},
		Data:       map[string][]byte{},
	}
	for k, v := range data {
		secret.Data[k] = []byte(v)
	}
	return secret
}

func assertNoAWSSecretValues(t *testing.T, msg string) {
	t.Helper()
	for _, value := range testAWSSecretValues {
		assert.NotContains(t, msg, value, "error message must not disclose credential values")
	}
}

func TestResolveAWSCredentials(t *testing.T) {
	stsCreds := awsCredentials{roleARN: testAWSRoleARN, webIdentityTokenFile: testAWSTokenFile}
	staticCreds := awsCredentials{accessKeyID: testAWSAccessKeyID, secretAccessKey: testAWSSecretKey}

	tests := []struct {
		name           string
		data           map[string]string
		expected       awsCredentials
		errContains    []string
		errNotContains []string
	}{
		{
			name:     "ccoctl STS credentials profile",
			data:     map[string]string{"credentials": ccoctlSTSProfile},
			expected: stsCreds,
		},
		{
			name: "STS credentials profile with indentation, CRLF line endings and comments",
			data: map[string]string{"credentials": "# generated\r\n    [default]\r\n    ; regional STS\r\n" +
				"    sts_regional_endpoints = regional\r\n\r\n    role_arn=" + testAWSRoleARN + "\r\n" +
				"    web_identity_token_file =" + testAWSTokenFile + "\r\n"},
			expected: stsCreds,
		},
		{
			name: "Cloud Credential Operator mint or passthrough Secret",
			data: map[string]string{
				"aws_access_key_id":     testAWSAccessKeyID,
				"aws_secret_access_key": testAWSSecretKey,
				"credentials":           ccoStaticProfile,
			},
			expected: staticCreds,
		},
		{
			name:     "static credentials profile only",
			data:     map[string]string{"credentials": ccoStaticProfile},
			expected: staticCreds,
		},
		{
			name: "manually created top-level static keys with surrounding whitespace",
			data: map[string]string{
				"aws_access_key_id":     testAWSAccessKeyID + "\n",
				"aws_secret_access_key": " " + testAWSSecretKey + "\n",
			},
			expected: staticCreds,
		},
		{
			name: "top-level STS keys",
			data: map[string]string{
				"role_arn":                testAWSRoleARN,
				"web_identity_token_file": testAWSTokenFile,
			},
			expected: stsCreds,
		},
		{
			name: "STS credentials take precedence over static keys",
			data: map[string]string{
				"credentials":           ccoctlSTSProfile,
				"aws_access_key_id":     testAWSAccessKeyID,
				"aws_secret_access_key": testAWSSecretKey,
			},
			expected: stsCreds,
		},
		{
			name: "credentials profile takes precedence over top-level static keys",
			data: map[string]string{
				"credentials":           ccoStaticProfile,
				"aws_access_key_id":     testAWSOtherAccessKey,
				"aws_secret_access_key": testAWSOtherSecretKey,
			},
			expected: staticCreds,
		},
		{
			name: "complete top-level STS keys are used when the profile is partial",
			data: map[string]string{
				"credentials":             "[default]\nrole_arn = " + testAWSRoleARN,
				"role_arn":                testAWSRoleARN,
				"web_identity_token_file": testAWSTokenFile,
			},
			expected: stsCreds,
		},
		{
			name: "unparseable profile falls back to top-level static keys",
			data: map[string]string{
				"credentials":           "[default]\naws_access_key_id " + testAWSOtherAccessKey,
				"aws_access_key_id":     testAWSAccessKeyID,
				"aws_secret_access_key": testAWSSecretKey,
			},
			expected: staticCreds,
		},
		{
			name: "profile without a default section falls back to top-level static keys",
			data: map[string]string{
				"credentials":           "[other]\n" + "aws_access_key_id = " + testAWSOtherAccessKey + "\naws_secret_access_key = " + testAWSOtherSecretKey,
				"aws_access_key_id":     testAWSAccessKeyID,
				"aws_secret_access_key": testAWSSecretKey,
			},
			expected: staticCreds,
		},
		{
			name:        "empty Secret",
			data:        map[string]string{},
			errContains: []string{"openshift-machine-config-operator/aws-cloud-credentials", "no usable AWS credentials"},
		},
		{
			name:        "empty credentials key",
			data:        map[string]string{"credentials": " \n"},
			errContains: []string{"no usable AWS credentials"},
		},
		{
			name: "role_arn without web_identity_token_file is rejected even with static keys",
			data: map[string]string{
				"credentials":           "[default]\nrole_arn = " + testAWSRoleARN,
				"aws_access_key_id":     testAWSAccessKeyID,
				"aws_secret_access_key": testAWSSecretKey,
			},
			errContains: []string{"incomplete STS web identity credentials", `the [default] profile of the "credentials" key`, "role_arn", "web_identity_token_file"},
		},
		{
			name:        "top-level web_identity_token_file without role_arn",
			data:        map[string]string{"web_identity_token_file": testAWSTokenFile},
			errContains: []string{"incomplete STS web identity credentials", "the top-level data keys"},
		},
		{
			name:        "access key ID without secret access key",
			data:        map[string]string{"aws_access_key_id": testAWSAccessKeyID},
			errContains: []string{"incomplete static credentials", "aws_access_key_id", "aws_secret_access_key"},
		},
		{
			name:        "secret access key without access key ID in the profile",
			data:        map[string]string{"credentials": "[default]\naws_secret_access_key = " + testAWSSecretKey},
			errContains: []string{"incomplete static credentials", `the [default] profile of the "credentials" key`},
		},
		{
			name:           "relative web identity token path",
			data:           map[string]string{"credentials": "[default]\nrole_arn = " + testAWSRoleARN + "\nweb_identity_token_file = relative-token-marker"},
			errContains:    []string{"openshift-machine-config-operator/aws-cloud-credentials", "web_identity_token_file must be an absolute path"},
			errNotContains: []string{"relative-token-marker"},
		},
		{
			name: "token text placed in the top-level web_identity_token_file key",
			data: map[string]string{
				"role_arn":                testAWSRoleARN,
				"web_identity_token_file": "eyJhbGciOiJSUzI1NiJ9.token-text-marker.signature",
			},
			errContains:    []string{"openshift-machine-config-operator/aws-cloud-credentials", "web_identity_token_file must be an absolute path"},
			errNotContains: []string{"token-text-marker", "eyJhbGciOiJSUzI1NiJ9"},
		},
		{
			name:        "unparseable profile without other credentials",
			data:        map[string]string{"credentials": "[default]\naws_access_key_id = " + testAWSAccessKeyID + "\naws_secret_access_key " + testAWSSecretKey},
			errContains: []string{"no usable AWS credentials", `the "credentials" key could not be parsed`, "line 3"},
		},
		{
			name:        "inline comment in a credential value",
			data:        map[string]string{"credentials": "[default]\naws_access_key_id = " + testAWSAccessKeyID + " # test\naws_secret_access_key = " + testAWSSecretKey},
			errContains: []string{"could not be parsed", "line 2", "aws_access_key_id contains whitespace"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			creds, err := resolveAWSCredentials(newTestAWSCredentialsSecret(tc.data))
			if len(tc.errContains) > 0 {
				require.Error(t, err)
				for _, s := range tc.errContains {
					assert.Contains(t, err.Error(), s)
				}
				for _, s := range tc.errNotContains {
					assert.NotContains(t, err.Error(), s)
				}
				assertNoAWSSecretValues(t, err.Error())
				assert.Equal(t, awsCredentials{}, creds)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, creds)
			assert.Equal(t, tc.expected.roleARN != "", creds.usesWebIdentity())
		})
	}
}

func TestParseAWSCredentialsProfile(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		expected    map[string]string
		errContains string
	}{
		{
			name: "default profile among other profiles",
			data: "[other]\nrole_arn = arn:aws:iam::111111111111:role/other\n[default]\nregion = us-east-1\nrole_arn = " + testAWSRoleARN + "\n[third]\nkey = value with spaces",
			expected: map[string]string{
				"region":   "us-east-1",
				"role_arn": testAWSRoleARN,
			},
		},
		{
			name:     "value containing an equals sign",
			data:     "[default]\naws_secret_access_key = abc=def",
			expected: map[string]string{"aws_secret_access_key": "abc=def"},
		},
		{
			name:     "empty value",
			data:     "[default]\nrole_arn =",
			expected: map[string]string{"role_arn": ""},
		},
		{
			name:        "no default section",
			data:        "[profile default]\nrole_arn = " + testAWSRoleARN,
			errContains: "no [default] section found",
		},
		{
			name:        "duplicate default section",
			data:        "[default]\nrole_arn = " + testAWSRoleARN + "\n[default]\nweb_identity_token_file = " + testAWSTokenFile,
			errContains: "line 3: duplicate [default] section",
		},
		{
			name:        "duplicate key",
			data:        "[default]\naws_access_key_id = " + testAWSAccessKeyID + "\naws_access_key_id = " + testAWSOtherAccessKey,
			errContains: "line 3: duplicate key in the [default] section",
		},
		{
			name:        "line without separator",
			data:        "[default]\naws_secret_access_key " + testAWSSecretKey,
			errContains: `line 2: expected a "key = value" pair`,
		},
		{
			name:        "key before any section",
			data:        "aws_access_key_id = " + testAWSAccessKeyID + "\n[default]",
			errContains: "line 1: key/value pair appears before any section header",
		},
		{
			name:        "missing key name",
			data:        "[default]\n= " + testAWSSecretKey,
			errContains: "line 2: missing key name",
		},
		{
			name:        "malformed section header",
			data:        "[default\nrole_arn = " + testAWSRoleARN,
			errContains: "line 1: malformed section header",
		},
		{
			name:        "empty section name",
			data:        "[ ]\nrole_arn = " + testAWSRoleARN,
			errContains: "line 1: empty section name",
		},
		{
			name:        "quoted credential value",
			data:        "[default]\naws_secret_access_key = \"" + testAWSSecretKey + " \"",
			errContains: "line 2: the value of aws_secret_access_key contains whitespace",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profile, err := parseAWSCredentialsProfile([]byte(tc.data))
			if tc.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errContains)
				assertNoAWSSecretValues(t, err.Error())
				assert.Nil(t, profile)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.expected, profile)
		})
	}
}

// checkWebIdentityTokenFileWithin runs checkWebIdentityTokenFile and fails the test if the
// check blocks, for example on opening a FIFO that has no writer.
func checkWebIdentityTokenFileWithin(t *testing.T, path string) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- checkWebIdentityTokenFile(path) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("checkWebIdentityTokenFile(%q) blocked", path)
		return nil
	}
}

// makeTestFIFO creates a FIFO and, on cleanup, opens it for writing without blocking so that
// a reader blocked in open (a regression) is released.
func makeTestFIFO(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	t.Cleanup(func() {
		if w, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			if err := w.Close(); err != nil {
				t.Errorf("close FIFO writer: %v", err)
			}
		}
	})
}

// writeProjectedTokenLayout mimics the kubelet atomic writer layout used for projected service
// account tokens: token -> ..data/token and ..data -> a timestamped directory.
func writeProjectedTokenLayout(t *testing.T, dir, version, token string) {
	t.Helper()
	versionDir := filepath.Join(dir, version)
	require.NoError(t, os.MkdirAll(versionDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(versionDir, "token"), []byte(token), 0o600))
	tmpLink := filepath.Join(dir, "..data_tmp")
	require.NoError(t, os.Symlink(version, tmpLink))
	require.NoError(t, os.Rename(tmpLink, filepath.Join(dir, "..data")))
	if _, err := os.Lstat(filepath.Join(dir, "token")); os.IsNotExist(err) {
		require.NoError(t, os.Symlink(filepath.Join("..data", "token"), filepath.Join(dir, "token")))
	}
}

func TestCheckWebIdentityTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("header.payload.signature"), 0o600))

	t.Run("readable regular token file", func(t *testing.T) {
		assert.NoError(t, checkWebIdentityTokenFileWithin(t, tokenFile))
	})

	t.Run("custom absolute path through a symlink to a regular file", func(t *testing.T) {
		link := filepath.Join(dir, "custom-token-link")
		require.NoError(t, os.Symlink(tokenFile, link))
		assert.NoError(t, checkWebIdentityTokenFileWithin(t, link))
	})

	t.Run("projected token layout and rotation", func(t *testing.T) {
		projected := filepath.Join(t.TempDir(), "serviceaccount")
		require.NoError(t, os.MkdirAll(projected, 0o755))
		writeProjectedTokenLayout(t, projected, "..2026_11_12_00_00_00.000000001", "first.token.value")
		path := filepath.Join(projected, "token")
		require.NoError(t, checkWebIdentityTokenFileWithin(t, path))

		// The SDK retriever used for the client re-reads the path on every refresh, so a
		// kubelet rotation that swaps ..data is picked up without rebuilding the client.
		retriever := stscreds.IdentityTokenFile(path)
		token, err := retriever.GetIdentityToken()
		require.NoError(t, err)
		assert.Equal(t, "first.token.value", string(token))

		writeProjectedTokenLayout(t, projected, "..2026_11_12_00_50_00.000000002", "rotated.token.value")
		require.NoError(t, checkWebIdentityTokenFileWithin(t, path))
		token, err = retriever.GetIdentityToken()
		require.NoError(t, err)
		assert.Equal(t, "rotated.token.value", string(token))
	})

	t.Run("missing token file", func(t *testing.T) {
		missing := filepath.Join(dir, "missing")
		err := checkWebIdentityTokenFileWithin(t, missing)
		require.Error(t, err)
		assert.Contains(t, err.Error(), missing)
		assert.Contains(t, err.Error(), "must mount the projected service account token")
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("dangling symlink", func(t *testing.T) {
		link := filepath.Join(dir, "dangling")
		require.NoError(t, os.Symlink(filepath.Join(dir, "does-not-exist"), link))
		err := checkWebIdentityTokenFileWithin(t, link)
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("directory instead of token file", func(t *testing.T) {
		err := checkWebIdentityTokenFileWithin(t, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a regular file")
	})

	t.Run("symlink to a directory", func(t *testing.T) {
		link := filepath.Join(dir, "dir-link")
		require.NoError(t, os.Symlink(dir, link))
		err := checkWebIdentityTokenFileWithin(t, link)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a regular file")
	})

	t.Run("FIFO is rejected without blocking", func(t *testing.T) {
		fifo := filepath.Join(t.TempDir(), "token-fifo")
		makeTestFIFO(t, fifo)
		err := checkWebIdentityTokenFileWithin(t, fifo)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a regular file")
	})

	t.Run("symlink to a FIFO is rejected without blocking", func(t *testing.T) {
		fifoDir := t.TempDir()
		fifo := filepath.Join(fifoDir, "token-fifo")
		makeTestFIFO(t, fifo)
		link := filepath.Join(fifoDir, "token")
		require.NoError(t, os.Symlink(fifo, link))
		err := checkWebIdentityTokenFileWithin(t, link)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a regular file")
	})

	t.Run("character device is rejected without being read", func(t *testing.T) {
		// /dev/null is used because it is present on Linux and reading it is bounded; the
		// check must reject it from its file type alone.
		info, err := os.Stat(os.DevNull)
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			t.Skipf("%s is not a character device here", os.DevNull)
		}
		err = checkWebIdentityTokenFileWithin(t, os.DevNull)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a regular file")
	})

	t.Run("unreadable token file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("file permissions are not enforced for root")
		}
		unreadable := filepath.Join(dir, "unreadable")
		require.NoError(t, os.WriteFile(unreadable, []byte("header.payload.signature"), 0o000))
		err := checkWebIdentityTokenFileWithin(t, unreadable)
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrPermission)
		assert.NotContains(t, err.Error(), "header.payload.signature")
	})
}
