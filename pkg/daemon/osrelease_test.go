package daemon

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestIsLikeTraditionalRHEL7(t *testing.T) {
	var testOS OperatingSystem
	testOS.VersionID = "7"
	assert.True(t, testOS.IsLikeTraditionalRHEL7())
	testOS.VersionID = "7.5"
	assert.True(t, testOS.IsLikeTraditionalRHEL7())
	testOS.VersionID = "8"
	assert.False(t, testOS.IsLikeTraditionalRHEL7())
	testOS.VersionID = "6.8"
	assert.False(t, testOS.IsLikeTraditionalRHEL7())
}

func TestIsLikeTraditionalRHEL8(t *testing.T) {
	var testOS OperatingSystem
	testOS.VersionID = "8"
	assert.True(t, testOS.IsLikeTraditionalRHEL8())
	testOS.VersionID = "8.5"
	assert.True(t, testOS.IsLikeTraditionalRHEL8())
	testOS.VersionID = "7"
	assert.False(t, testOS.IsLikeTraditionalRHEL8())
	testOS.VersionID = "6.8"
	assert.False(t, testOS.IsLikeTraditionalRHEL8())
}
