package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Tests that a given image pullspec with a tag and SHA is correctly substituted.
func TestParseImagePullspec(t *testing.T) {
	t.Parallel()

	out, err := ParseImagePullspec("registry.hostname.com/org/repo:latest", "sha256:628e4e8f0a78d91015c6cebeee95931ae2e8defe5dfb4ced4a82830e08937573")
	assert.NoError(t, err)
	assert.Equal(t, "registry.hostname.com/org/repo@sha256:628e4e8f0a78d91015c6cebeee95931ae2e8defe5dfb4ced4a82830e08937573", out)
}
