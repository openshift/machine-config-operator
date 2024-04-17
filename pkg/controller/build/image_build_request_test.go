package build

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Tests that Image Build Requests is constructed as expected and does a
// (mostly) smoke test of its methods.
func TestImageBuildRequest(t *testing.T) {
	t.Parallel()

	mcp := newMachineConfigPool("worker", "rendered-worker-1")
	machineOSConfig := getMachineOSConfig()
	machineOSBuild := getMachineOSBuild(mcp.Spec.Configuration.Name)

	ibr := newImageBuildRequestFromBuildInputs(machineOSBuild, machineOSConfig)

	dockerfile, err := ibr.renderDockerfile()
	assert.NoError(t, err)

	expectedDockerfileContents := []string{
		mcp.Name,
		mcp.Spec.Configuration.Name,
		machineConfigJSONFilename,
	}

	for _, content := range expectedDockerfileContents {
		assert.Contains(t, dockerfile, content)
	}

	assert.NotNil(t, ibr.toBuildPod())

	dockerfileConfigmap, err := ibr.dockerfileToConfigMap()
	assert.NoError(t, err)
	assert.NotNil(t, dockerfileConfigmap)
	assert.Equal(t, dockerfileConfigmap.Data["Dockerfile"], dockerfile)

	assert.Equal(t, "dockerfile-rendered-worker-1", ibr.getDockerfileConfigMapName())
	assert.Equal(t, "build-rendered-worker-1", ibr.getBuildName())
	assert.Equal(t, "mc-rendered-worker-1", ibr.getMCConfigMapName())
}

// Tests that the Dockerfile is correctly rendered in the absence of the
// extensions image. For now, we just check whether the extensions image is
// imported. Once we wire up the extensions container, we'll need to modify
// this to ensure that the remainder of the Dockerfile gets rendered correctly.
func TestImageBuildRequestMissingExtensionsImage(t *testing.T) {
	t.Parallel()

	mcp := newMachineConfigPool("worker", "rendered-worker-1")
	machineOSConfig := getMachineOSConfig()
	machineOSBuild := getMachineOSBuild(mcp.Spec.Configuration.Name)

	ibr := newImageBuildRequestFromBuildInputs(machineOSBuild, machineOSConfig)

	dockerfile, err := ibr.renderDockerfile()
	assert.NoError(t, err)

	assert.NotContains(t, dockerfile, "AS extensions")
}
