package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectHistoricalCommit(t *testing.T) {
	date := func(s string) time.Time {
		t.Helper()
		d, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return d
	}

	t.Run("double bump within grace window returns the value in effect at cutoff, not the latest bump", func(t *testing.T) {
		// Bump B lands just before the cutoff; bump C lands just after it, inside the window. If
		// the grace period were naively anchored to "time since the last bump" (bump C, ~3 months
		// ago), it would look like there's still time left on the clock. Reconstructing the actual
		// value as of the cutoff sidesteps that: it should return bump B, what was genuinely in
		// effect 4 months ago, regardless of the later bump C. Commits are newest-first, matching
		// the GitHub API's default order.
		commits := []ghCommit{
			{SHA: "bumpC", Date: date("2026-03-01T00:00:00Z")}, // after cutoff, within the window
			{SHA: "bumpB", Date: date("2025-12-15T00:00:00Z")}, // before cutoff
			{SHA: "bumpA", Date: date("2025-01-01T00:00:00Z")}, // long-standing baseline
		}
		cutoff := date("2026-02-01T00:00:00Z")

		sha, usedFallback, err := selectHistoricalCommit(commits, cutoff)
		require.NoError(t, err)
		assert.Equal(t, "bumpB", sha)
		assert.False(t, usedFallback)
	})

	t.Run("no commit predates the window falls back to the oldest available", func(t *testing.T) {
		commits := []ghCommit{
			{SHA: "newer", Date: date("2026-05-01T00:00:00Z")},
			{SHA: "oldest", Date: date("2026-04-15T00:00:00Z")},
		}
		cutoff := date("2026-01-01T00:00:00Z")

		sha, usedFallback, err := selectHistoricalCommit(commits, cutoff)
		require.NoError(t, err)
		assert.Equal(t, "oldest", sha)
		assert.True(t, usedFallback)
	})

	t.Run("no commit history is an error", func(t *testing.T) {
		_, _, err := selectHistoricalCommit(nil, date("2026-01-01T00:00:00Z"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no commit history found")
	})
}

func TestParseSkewLimitConstants(t *testing.T) {
	t.Run("extracts both constants", func(t *testing.T) {
		src := "package common\n\nconst (\n\tRHCOSVersionBootImageSkewLimit = \"9.2\"\n\tOCPVersionBootImageSkewLimit   = \"4.13.0\"\n)\n"
		limits, err := parseSkewLimitConstants(src)
		require.NoError(t, err)
		assert.Equal(t, "9.2", limits.RHCOS)
		assert.Equal(t, "4.13.0", limits.OCP)
	})

	t.Run("missing constants is an error", func(t *testing.T) {
		_, err := parseSkewLimitConstants("package common\n")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not find both")
	})
}
