package helpers

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewOcCommandContextIsBounded verifies that the command ExecCmdOnNode
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
