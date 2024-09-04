package build

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Tests that Image Build Requests is constructed as expected and does a
// (mostly) smoke test of its methods.
func TestImageBuildRequest(t *testing.T) {
	t.Parallel()

	mcp := newMachineConfigPool("worker", "rendered-worker-1")
	machineOSConfig := newMachineOSConfig(mcp)
	machineOSBuild := newMachineOSBuild(machineOSConfig, mcp)

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

	buildPod := ibr.toBuildPod()

	mcConfigMap, err := ibr.toConfigMap(&mcfgv1.MachineConfig{})
	assert.NoError(t, err)

	objects := []metav1.Object{
		mcConfigMap,
		dockerfileConfigmap,
		buildPod,
	}

	for _, object := range objects {
		assert.True(t, isEphemeralBuildObject(object))
		assert.True(t, hasAllRequiredOSBuildLabels(object.GetLabels()))
		assert.True(t, IsObjectCreatedByBuildController(object))
	}
}

// Tests that the Dockerfile is correctly rendered in the absence of the
// extensions image. For now, we just check whether the extensions image is
// imported. Once we wire up the extensions container, we'll need to modify
// this to ensure that the remainder of the Dockerfile gets rendered correctly.
func TestImageBuildRequestMissingExtensionsImage(t *testing.T) {
	t.Parallel()

	mcp := newMachineConfigPool("worker", "rendered-worker-1")
	machineOSConfig := newMachineOSConfig(mcp)
	machineOSBuild := newMachineOSBuild(machineOSConfig, mcp)

	ibr := newImageBuildRequestFromBuildInputs(machineOSBuild, machineOSConfig)

	dockerfile, err := ibr.renderDockerfile()
	assert.NoError(t, err)

	assert.NotContains(t, dockerfile, "AS extensions")
}
