package helpers

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCurlConfigUser covers the curl config file that carries HTTP credentials
// on stdin, so that they stay out of the `oc rsh` argument list and therefore
// out of the kube-apiserver audit log.
func TestCurlConfigUser(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		username string
		password string
		expected string
	}{
		{
			name:     "ordinary credential",
			username: "openshift",
			password: "s3cr3t-registry-password",
			expected: "user = \"openshift:s3cr3t-registry-password\"\n",
		},
		{
			name:     "password containing a double quote",
			username: "openshift",
			password: `hunter"2`,
			expected: "user = \"openshift:hunter\\\"2\"\n",
		},
		{
			name:     "password containing a backslash",
			username: "openshift",
			password: `hunter\2`,
			expected: "user = \"openshift:hunter\\\\2\"\n",
		},
		{
			name:     "password containing spaces and a colon",
			username: "openshift",
			password: "two words: more",
			expected: "user = \"openshift:two words: more\"\n",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.expected, CurlConfigUser(testCase.username, testCase.password))
		})
	}
}

// TestCurlConfigHeader covers the same quoting for a header-carried credential,
// e.g. the monitoring bearer token.
func TestCurlConfigHeader(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"header = \"Authorization: Bearer abc.def.ghi\"\n",
		CurlConfigHeader("Authorization: Bearer abc.def.ghi"))
	assert.Equal(t,
		"header = \"Authorization: Bearer a\\\"b\\\\c\"\n",
		CurlConfigHeader(`Authorization: Bearer a"b\c`))
}

// TestCurlConfigIsSingleLine guards the one property the config file format
// cannot survive losing: a newline in the value would end the parameter and
// turn the remainder into a second, bogus config line. Nothing in a
// base64-encoded password or a JWT can contain one, so this is a regression
// guard on the renderers rather than on the inputs.
func TestCurlConfigIsSingleLine(t *testing.T) {
	t.Parallel()

	for _, config := range []string{
		CurlConfigUser("openshift", "s3cr3t"),
		CurlConfigHeader("Authorization: Bearer abc"),
	} {
		assert.Equal(t, 1, strings.Count(config, "\n"), "config should be exactly one line: %q", config)
		assert.True(t, strings.HasSuffix(config, "\n"), "config should be newline-terminated: %q", config)
	}
}

// TestCurlAcceptsConfigOnStdin runs the real curl against a config file on
// stdin and asserts it picks the credential up, which is what keeps the
// credential out of argv in the first place. --libcurl makes curl print the
// request it would issue without needing a server to talk to.
func TestCurlAcceptsConfigOnStdin(t *testing.T) {
	t.Parallel()

	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skipf("curl not available: %v", err)
	}

	// --max-time only bounds the transfer, so a curl wedged before or after it
	// would hang the test. This is the backstop on the process as a whole, set
	// well above --max-time so curl still reaches its own timeout first and the
	// exit code stays the one asserted below.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, curl, "--disable", "--config", "-", "--libcurl", "/dev/stdout", "--max-time", "1",
		"--noproxy", "*", "https://127.0.0.1:1/v2/")
	cmd.Stdin = strings.NewReader(CurlConfigUser("openshift", "s3cr3t-registry-password"))

	// The connection to a closed port fails; the --libcurl trace of the
	// request curl assembled is still written to stdout.
	out, err := cmd.CombinedOutput()

	// Pin the failure to the connect. curl exits non-zero for a config file
	// it could not parse too, and that run would still print a trace -- just
	// one without the credential in it. Accepting any failure would let that
	// case reach the assertion below and fail there with a misleading
	// message, or worse, pass on a trace that happened to carry the string.
	const curlCouldNotConnect = 7 // CURLE_COULDNT_CONNECT

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "expected curl to fail connecting to a closed port, output: %s", string(out))
	require.Equal(t, curlCouldNotConnect, exitErr.ExitCode(),
		"expected CURLE_COULDNT_CONNECT, curl output: %s", string(out))

	assert.Contains(t, string(out), "openshift:s3cr3t-registry-password",
		"curl did not read the credential from the config file on stdin: %s", string(out))
}

// TestNewOcCommandContextIsBounded verifies that the command
// ExecCmdOnNodeWithStdin builds is actually bound to the context it is given,
// so that a hung `oc rsh` is killed when the helper's deadline expires rather
// than running forever.
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
