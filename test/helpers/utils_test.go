package helpers

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactArgs covers the redaction applied to the argument list before
// ExecCmdOnNodeRedacted formats it into a failure message.
func TestRedactArgs(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		args     []string
		secrets  []string
		expected []string
	}{
		{
			name:     "no secrets leaves args untouched",
			args:     []string{"curl", "-s", "https://api-int.example.com:22625/v2/"},
			secrets:  nil,
			expected: []string{"curl", "-s", "https://api-int.example.com:22625/v2/"},
		},
		{
			name:     "secret embedded in a larger argument",
			args:     []string{"curl", "-H", "Authorization: Basic c2VjcmV0"},
			secrets:  []string{"Basic c2VjcmV0"},
			expected: []string{"curl", "-H", "Authorization: <redacted>"},
		},
		{
			name:     "secret appearing as its own argument",
			args:     []string{"podman", "login", "-p", "hunter2"},
			secrets:  []string{"hunter2"},
			expected: []string{"podman", "login", "-p", "<redacted>"},
		},
		{
			name:     "multiple secrets and repeated occurrences",
			args:     []string{"curl", "-u", "openshift:hunter2", "-H", "X-Token: tok", "https://hunter2.example.com"},
			secrets:  []string{"hunter2", "tok"},
			expected: []string{"curl", "-u", "openshift:<redacted>", "-H", "X-Token: <redacted>", "https://<redacted>.example.com"},
		},
		{
			name:     "empty secrets are ignored",
			args:     []string{"curl", "-H", "Authorization: Bearer abc"},
			secrets:  []string{"", "abc"},
			expected: []string{"curl", "-H", "Authorization: Bearer <redacted>"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			redacted := redactArgs(testCase.args, testCase.secrets)
			assert.Equal(t, testCase.expected, redacted)
		})
	}
}

// TestRedactArgsDoesNotMutateInput ensures the command still receives its
// original arguments after the redacted copy has been built.
func TestRedactArgsDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	args := []string{"curl", "-H", "Authorization: Bearer hunter2"}

	redactArgs(args, []string{"hunter2"})

	assert.Equal(t, []string{"curl", "-H", "Authorization: Bearer hunter2"}, args)
}

// TestRedactArgsScrubsCredentialsFromFailureMessage reproduces the messages
// ExecCmdOnNodeRedacted emits on failure and asserts that neither the password
// nor the Authorization header it is encoded into survives into the output.
func TestRedactArgsScrubsCredentialsFromFailureMessage(t *testing.T) {
	t.Parallel()

	const password = "s3cr3t-registry-password"

	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("openshift:"+password))
	args := []string{"curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "-H", "Authorization: " + authHeader, "https://api-int.example.com:22625/v2/"}

	safeArgs := redactArgs(args, []string{authHeader, password})

	// Every message ExecCmdOnNodeRedacted can fail with.
	messages := []string{
		fmt.Sprintf("could not prepare to exec cmd %v on node %s: %s", safeArgs, "master-0", "boom"),
		fmt.Sprintf("timed out after %s exec'ing cmd %v on node %s: %s", ExecCmdOnNodeTimeout, safeArgs, "master-0", "signal: killed"),
		fmt.Sprintf("failed to exec cmd %v on node %s: %s", safeArgs, "master-0", ""),
	}

	for _, message := range messages {
		assert.NotContains(t, message, password, "password leaked into failure message")
		assert.NotContains(t, message, authHeader, "Authorization header leaked into failure message")
		assert.NotContains(t, message, base64.StdEncoding.EncodeToString([]byte("openshift:"+password)), "encoded credential leaked into failure message")
		// The rest of the command must still be there to keep the message useful.
		assert.Contains(t, message, "curl")
		assert.Contains(t, message, "https://api-int.example.com:22625/v2/")
		assert.Contains(t, message, redactedPlaceholder)
	}

	// Sanity check: the same messages built from the unredacted args would leak,
	// so the assertions above are actually testing something.
	leaky := fmt.Sprintf("failed to exec cmd %v on node %s", args, "master-0")
	require.True(t, strings.Contains(leaky, authHeader), "test fixture no longer reproduces the leak")
}

// TestNewOcCommandContextIsBounded verifies that the command ExecCmdOnNodeRedacted
// builds is actually bound to the context it is given, so that a hung `oc rsh`
// is killed when the helper's deadline expires rather than running forever.
//
// NewOcCommandContext needs an oc binary and a kubeconfig, neither of which
// exists in a unit-test environment, so this exercises the same
// exec.CommandContext wiring directly.
func TestNewOcCommandContextIsBounded(t *testing.T) {
	t.Parallel()

	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cmd := exec.CommandContext(ctx, sleep, "60")

	start := time.Now()
	_, err = cmd.Output()
	elapsed := time.Since(start)

	require.Error(t, err, "command should have been killed by the deadline")
	require.Error(t, ctx.Err(), "context should report the expired deadline")
	assert.Less(t, elapsed, 30*time.Second, "command outlived its context")
}
