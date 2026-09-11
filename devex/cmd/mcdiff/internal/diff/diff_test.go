package diff

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareMatch(t *testing.T) {
	t.Parallel()

	got := Compare([]byte("PermitRootLogin no\n"), []byte("PermitRootLogin no\n"), "expected", "actual")
	assert.True(t, got.Match)
	assert.Equal(t, 19, got.ExpectedSize)
	assert.Equal(t, 19, got.ActualSize)
	assert.Empty(t, got.UnifiedDiff)
}

func TestCompareMismatchUnifiedDiff(t *testing.T) {
	t.Parallel()

	got := Compare([]byte("PermitRootLogin no\n"), []byte("PermitRootLogin yes\n"), "/etc/ssh/sshd_config", "./sshd_config")
	require.False(t, got.Match)
	assert.Equal(t, 19, got.ExpectedSize)
	assert.Equal(t, 20, got.ActualSize)
	assert.Contains(t, got.UnifiedDiff, "--- /etc/ssh/sshd_config")
	assert.Contains(t, got.UnifiedDiff, "+++ ./sshd_config")
	assert.Contains(t, got.UnifiedDiff, "-PermitRootLogin no")
	assert.Contains(t, got.UnifiedDiff, "+PermitRootLogin yes")
}

func TestCompareSizeMismatch(t *testing.T) {
	t.Parallel()

	got := Compare([]byte("abc"), []byte("abcdef"), "", "")
	require.False(t, got.Match)
	assert.Equal(t, 3, got.ExpectedSize)
	assert.Equal(t, 6, got.ActualSize)
	assert.NotEmpty(t, got.UnifiedDiff)
}

func TestCompareLineEndingsOnly(t *testing.T) {
	t.Parallel()

	got := Compare([]byte("a\nb\n"), []byte("a\r\nb\r\n"), "expected", "actual")
	require.False(t, got.Match, "raw bytes differ when CRLF vs LF")
	assert.Equal(t, 4, got.ExpectedSize)
	assert.Equal(t, 6, got.ActualSize)
	assert.Equal(t, lineEndingOnly, got.UnifiedDiff)
	assert.NotContains(t, got.UnifiedDiff, "-a")
}

func TestCompareTrailingNewlineOnly(t *testing.T) {
	t.Parallel()

	got := Compare([]byte("server clock.redhat.com iburst\n"), []byte("server clock.redhat.com iburst"), "/etc/chrony.conf", "node:worker-0")
	require.False(t, got.Match)
	assert.Equal(t, 31, got.ExpectedSize)
	assert.Equal(t, 30, got.ActualSize)
	assert.Equal(t, trailingNewlineOnly, got.UnifiedDiff)
	assert.NotContains(t, got.UnifiedDiff, "-server")
	assert.NotContains(t, got.UnifiedDiff, "+server")
}

func TestCompareResolvConfContentStillDiffs(t *testing.T) {
	t.Parallel()

	got := Compare([]byte("nameserver 1.1.1.1\n"), []byte("nameserver 8.8.8.8\n"), "/etc/resolv.conf", "node:worker-0")
	require.False(t, got.Match)
	assert.Contains(t, got.UnifiedDiff, "-nameserver 1.1.1.1")
	assert.Contains(t, got.UnifiedDiff, "+nameserver 8.8.8.8")
}

func TestWithModesMismatch(t *testing.T) {
	t.Parallel()

	expected := 0o644
	actual := 0o755
	got := WithModes(Compare([]byte("same\n"), []byte("same\n"), "expected", "actual"), &expected, &actual)
	assert.True(t, got.Match)
	assert.False(t, got.ModeMatch)
	assert.Equal(t, 0o644, *got.ExpectedMode)
	assert.Equal(t, 0o755, *got.ActualMode)
}

func TestModesMatchDefaultWhenExpectedNil(t *testing.T) {
	t.Parallel()

	actual644 := 0o644
	actual755 := 0o755
	assert.True(t, ModesMatch(nil, &actual644))
	assert.False(t, ModesMatch(nil, &actual755))
	assert.True(t, ModesMatch(&actual644, nil), "unknown actual mode is not a mismatch")
}

func TestCompareEmptyMatch(t *testing.T) {
	t.Parallel()

	got := Compare(nil, []byte{}, "", "")
	assert.True(t, got.Match)
	assert.Equal(t, 0, got.ExpectedSize)
	assert.Equal(t, 0, got.ActualSize)
	assert.Empty(t, got.UnifiedDiff)
}
