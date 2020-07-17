package operator

import (
	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestIsCloudConfigRequired(t *testing.T) {
	testInfra := configv1.Infrastructure{}
	testInfra.Status.Platform = "None"
	required := isCloudConfigRequired(&testInfra)
	assert.False(t, required)
}
