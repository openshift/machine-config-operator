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
	osImageURLConfigMap := getOSImageURLConfigMap()
	onClusterBuildConfigMap := getOnClusterBuildConfigMap()

	ibr := newImageBuildRequestFromBuildInputs(&BuildInputs{
		pool:                 mcp,
		osImageURL:           osImageURLConfigMap,
		onClusterBuildConfig: onClusterBuildConfigMap,
	})

	dockerfile, err := ibr.renderDockerfile()
	assert.NoError(t, err)

	expectedDockerfileContents := []string{
		osImageURLConfigMap.Data[releaseVersionConfigKey],
		osImageURLConfigMap.Data[baseOSExtensionsContainerImageConfigKey],
		mcp.Name,
		mcp.Spec.Configuration.Name,
		machineConfigJSONFilename,
	}

	for _, content := range expectedDockerfileContents {
		assert.Contains(t, dockerfile, content)
	}

	assert.NotNil(t, ibr.toBuild())
	assert.NotNil(t, ibr.toBuildPod())

	dockerfileConfigmap, err := ibr.dockerfileToConfigMap()
	assert.NoError(t, err)
	assert.NotNil(t, dockerfileConfigmap)
	assert.Equal(t, dockerfileConfigmap.Data["Dockerfile"], dockerfile)

	assert.Equal(t, osImageURLConfigMap.Data[baseOSContainerImageConfigKey], ibr.BaseImage.Pullspec)
	assert.Equal(t, osImageURLConfigMap.Data[baseOSExtensionsContainerImageConfigKey], ibr.ExtensionsImage.Pullspec)

	assert.Equal(t, onClusterBuildConfigMap.Data[BaseImagePullSecretNameConfigKey], ibr.BaseImage.PullSecret.Name)
	assert.Equal(t, onClusterBuildConfigMap.Data[BaseImagePullSecretNameConfigKey], ibr.ExtensionsImage.PullSecret.Name)

	assert.Equal(t, onClusterBuildConfigMap.Data[FinalImagePullspecConfigKey], ibr.FinalImage.Pullspec)

	assert.Equal(t, onClusterBuildConfigMap.Data[FinalImagePushSecretNameConfigKey], ibr.FinalImage.PullSecret.Name)

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
	osImageURLConfigMap := getOSImageURLConfigMap()
	onClusterBuildConfigMap := getOnClusterBuildConfigMap()

	delete(osImageURLConfigMap.Data, baseOSExtensionsContainerImageConfigKey)

	ibr := newImageBuildRequestFromBuildInputs(&BuildInputs{
		pool:                 mcp,
		osImageURL:           osImageURLConfigMap,
		onClusterBuildConfig: onClusterBuildConfigMap,
	})

	dockerfile, err := ibr.renderDockerfile()
	assert.NoError(t, err)

	assert.NotContains(t, dockerfile, "AS extensions")
}

func TestImageBuildRequestWithCustomDockerfile(t *testing.T) {
	t.Parallel()

	mcp := newMachineConfigPool("worker", "rendered-worker-1")
	osImageURLConfigMap := getOSImageURLConfigMap()
	onClusterBuildConfigMap := getOnClusterBuildConfigMap()
	customDockerfileConfigMap := getCustomDockerfileConfigMap(map[string]string{
		"worker": "FROM configs AS final\nRUN dnf install -y python3",
	})

	ibr := newImageBuildRequestFromBuildInputs(&BuildInputs{
		pool:                 mcp,
		osImageURL:           osImageURLConfigMap,
		onClusterBuildConfig: onClusterBuildConfigMap,
		customDockerfiles:    customDockerfileConfigMap,
	})

	dockerfile, err := ibr.renderDockerfile()
	assert.NoError(t, err)

	t.Logf(dockerfile)

	expectedDockerfileContents := []string{
		"FROM configs AS final",
		"RUN dnf install -y python3",
		osImageURLConfigMap.Data[baseOSContainerImageConfigKey],
		osImageURLConfigMap.Data[releaseVersionConfigKey],
		osImageURLConfigMap.Data[baseOSExtensionsContainerImageConfigKey],
		mcp.Name,
		mcp.Spec.Configuration.Name,
		machineConfigJSONFilename,
	}

	for _, content := range expectedDockerfileContents {
		assert.Contains(t, dockerfile, content)
	}
}
