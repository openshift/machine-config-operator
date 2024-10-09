package buildrequest

import (
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	baseOSImagePullspec           string = "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:12e89d631c0ca1700262583acfb856b6e7dbe94800cb38035d68ee5cc912411c"
	baseOSExtensionsImagePullspec string = "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:5b6d901069e640fc53d2e971fa1f4802bf9dea1a4ffba67b8a17eaa7d8dfa336"
	mcoImagePullspec              string = "registry.ci.openshift.org/ocp/4.14-2023-05-29-125629@sha256:87980e0edfc86d01182f70c53527f74b5b01df00fe6d47668763d228d4de43a9"
	releaseVersion                string = "release-version"
)

func expectedContents() []string {
	return []string{
		fmt.Sprintf("FROM %s AS extract", baseOSImagePullspec),
		fmt.Sprintf("FROM %s AS configs", baseOSImagePullspec),
		fmt.Sprintf("LABEL baseOSContainerImage=%s", baseOSImagePullspec),
	}
}

// Tests that Image Build Requests is constructed as expected.
func TestImageBuildRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                            string
		optsFunc                        func() BuildRequestOpts
		expectedContainerfileContents   []string
		unexpectedContainerfileContents []string
	}{
		{
			name:     "With extensions image",
			optsFunc: getBuildRequestOpts,
			expectedContainerfileContents: append(expectedContents(), []string{
				fmt.Sprintf("FROM %s AS extensions", baseOSExtensionsImagePullspec),
			}...),
		},
		{
			name: "Missing extensions image",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.MachineOSConfig.Spec.BuildInputs.BaseOSExtensionsImagePullspec = ""
				return opts
			},
			unexpectedContainerfileContents: []string{
				fmt.Sprintf("FROM %s AS extensions", baseOSExtensionsImagePullspec),
			},
		},
		{
			name: "Has EtcPkiRpmGpgKeys",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.HasEtcPkiRpmGpgKeys = true
				return opts
			},
		},
		{
			name: "Has EtcPkiEntitlementKeys",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.HasEtcPkiEntitlementKeys = true
				return opts
			},
		},
		{
			name: "Has EtcYumReposD",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.HasEtcYumReposDConfigs = true
				return opts
			},
		},
		{
			name: "Has All Keys",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.HasEtcPkiRpmGpgKeys = true
				opts.HasEtcPkiEntitlementKeys = true
				opts.HasEtcYumReposDConfigs = true
				return opts
			},
		},
		{
			name: "OSImageURLConfig options are used instead",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.MachineOSConfig.Spec.BuildInputs.BaseOSImagePullspec = ""
				opts.OSImageURLConfig.BaseOSContainerImage = "base-os-image-from-osimageurlconfig"
				opts.MachineOSConfig.Spec.BuildInputs.BaseOSExtensionsImagePullspec = ""
				opts.OSImageURLConfig.BaseOSExtensionsContainerImage = "base-os-image-from-osimageurlconfig"
				opts.MachineOSConfig.Spec.BuildInputs.ReleaseVersion = ""
				opts.OSImageURLConfig.ReleaseVersion = "custom-release-version"
				return opts
			},
			expectedContainerfileContents: []string{
				"FROM base-os-image-from-osimageurlconfig AS extract",
				"FROM base-os-image-from-osimageurlconfig AS configs",
				"LABEL releaseversion=custom-release-version",
			},
			unexpectedContainerfileContents: expectedContents(),
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			opts := testCase.optsFunc()

			br := newBuildRequest(opts)
			configmaps, err := br.ConfigMaps()
			assert.NoError(t, err)

			assert.Equal(t, opts, br.Opts())

			containerfile := configmaps[0].Data["Containerfile"]

			if len(testCase.expectedContainerfileContents) == 0 {
				testCase.expectedContainerfileContents = append(expectedContents(), []string{
					machineConfigJSONFilename,
					opts.MachineOSConfig.Spec.BuildInputs.Containerfile[0].Content,
				}...)
			}

			for _, content := range testCase.expectedContainerfileContents {
				assert.Contains(t, containerfile, content)
			}

			for _, content := range testCase.unexpectedContainerfileContents {
				assert.NotContains(t, containerfile, content)
			}

			buildPod := br.BuildPod()

			assert.Equal(t, "containerfile-rendered-worker-1", configmaps[0].Name)
			assert.Equal(t, "mc-rendered-worker-1", configmaps[1].Name)
			assert.Equal(t, "build-rendered-worker-1", buildPod.Name)

			secrets, err := br.Secrets()
			assert.NoError(t, err)

			objects := []metav1.Object{
				configmaps[0],
				configmaps[1],
				buildPod,
				secrets[0],
				secrets[1],
			}

			for _, object := range objects {
				assert.True(t, constants.EphemeralBuildObjectSelector().Matches(labels.Set(object.GetLabels())))
				assert.True(t, constants.OSBuildSelector().Matches(labels.Set(object.GetLabels())))
				assert.True(t, constants.IsObjectCreatedByBuildController(object))
			}

			for _, secret := range secrets {
				assertSecretInCorrectFormat(t, secret)
			}

			assert.Equal(t, secrets[0].Name, "base-rendered-worker-1")
			assert.Equal(t, secrets[1].Name, "final-rendered-worker-1")

			assertBuildPodIsCorrect(t, buildPod, opts)
		})
	}
}

func assertSecretInCorrectFormat(t *testing.T, secret *corev1.Secret) {
	t.Helper()

	assert.True(t, constants.CanonicalizedSecretSelector().Matches(labels.Set(secret.GetLabels())))
	assert.Equal(t, secret.Type, corev1.SecretTypeDockerConfigJson)
	assert.NotEqual(t, secret.Type, corev1.SecretTypeDockercfg)
	assert.Contains(t, secret.Data, corev1.DockerConfigJsonKey)
	assert.NotContains(t, secret.Data, corev1.DockerConfigKey)
	assert.JSONEq(t, string(secret.Data[corev1.DockerConfigJsonKey]), `{"auths":{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}}`)
}

func assertBuildPodIsCorrect(t *testing.T, buildPod *corev1.Pod, opts BuildRequestOpts) {
	etcRpmGpgKeysOpts := optsForEtcRpmGpgKeys()
	assertBuildPodMatchesExpectations(t, opts.HasEtcPkiRpmGpgKeys, buildPod,
		etcRpmGpgKeysOpts.envVar(),
		etcRpmGpgKeysOpts.volumeForSecret(),
		etcRpmGpgKeysOpts.volumeMount(),
	)

	etcYumReposDOpts := optsForEtcYumReposD()
	assertBuildPodMatchesExpectations(t, opts.HasEtcYumReposDConfigs, buildPod,
		etcYumReposDOpts.envVar(),
		etcYumReposDOpts.volumeForConfigMap(),
		etcYumReposDOpts.volumeMount(),
	)

	etcPkiEntitlementKeysOpts := optsForEtcPkiEntitlements()
	assertBuildPodMatchesExpectations(t, opts.HasEtcPkiEntitlementKeys, buildPod,
		etcPkiEntitlementKeysOpts.envVar(),
		etcPkiEntitlementKeysOpts.volumeForSecret(),
		etcPkiEntitlementKeysOpts.volumeMount(),
	)

	assert.Equal(t, buildPod.Spec.Containers[0].Image, mcoImagePullspec)
	expectedPullspecs := []string{
		"base-os-image-from-osimageurlconfig",
		baseOSImagePullspec,
	}

	assert.Contains(t, expectedPullspecs, buildPod.Spec.Containers[1].Image)

	assertPodHasVolume(t, buildPod, corev1.Volume{
		Name: "final-image-push-creds",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: "final-rendered-worker-1",
				Items: []corev1.KeyToPath{
					{
						Key:  corev1.DockerConfigJsonKey,
						Path: "config.json",
					},
				},
			},
		},
	})

	assertPodHasVolume(t, buildPod, corev1.Volume{
		Name: "base-image-pull-creds",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: "base-rendered-worker-1",
				Items: []corev1.KeyToPath{
					{
						Key:  corev1.DockerConfigJsonKey,
						Path: "config.json",
					},
				},
			},
		},
	})
}

func assertPodHasVolume(t *testing.T, pod *corev1.Pod, volume corev1.Volume) {
	assert.Contains(t, pod.Spec.Volumes, volume)
}

func assertBuildPodMatchesExpectations(t *testing.T, shouldBePresent bool, buildPod *corev1.Pod, envvar corev1.EnvVar, volume corev1.Volume, volumeMount corev1.VolumeMount) {
	for _, container := range buildPod.Spec.Containers {
		if shouldBePresent {
			assert.Contains(t, container.Env, envvar)
			assert.Contains(t, container.VolumeMounts, volumeMount)
			assertPodHasVolume(t, buildPod, volume)
		} else {
			assert.NotContains(t, container.Env, envvar)
			assert.NotContains(t, container.VolumeMounts, volumeMount)
			assert.NotContains(t, buildPod.Spec.Volumes, volume)
		}

		assert.Contains(t, container.Env, corev1.EnvVar{
			Name:  "TAG",
			Value: "registry.hostname.com/org/repo:rendered-worker-1",
		})
	}
}

func getBuildRequestOpts() BuildRequestOpts {
	containerfileContents := `FROM configs AS final
RUN rpm-ostree install && \
	ostree container commit`

	layeredBuilder := helpers.NewLayeredBuilder("worker")

	layeredBuilder.MachineConfigPoolBuilder().WithMachineConfig("rendered-worker-1")

	layeredBuilder.MachineOSConfigBuilder().
		WithBaseOSImagePullspec(baseOSImagePullspec).
		WithBaseImagePullSecret("base-image-pull-secret").
		WithRenderedImagePushSecret("final-image-push-secret").
		WithCurrentImagePullSecret("current-image-pull-secret").
		WithContainerfile(mcfgv1alpha1.NoArch, containerfileContents).
		WithExtensionsImagePullspec(baseOSExtensionsImagePullspec)

	mosc := layeredBuilder.MachineOSConfig()
	mosc.Spec.BuildInputs.ReleaseVersion = releaseVersion
	mosc.Status.CurrentImagePullspec = "registry.hostname.com/org/repo:rendered-worker-1"

	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`
	newSecret := `{"auths":` + legacySecret + `}`

	return BuildRequestOpts{
		MachineConfig:   &mcfgv1.MachineConfig{},
		MachineOSConfig: mosc,
		MachineOSBuild:  layeredBuilder.MachineOSBuild(),
		Images: &ctrlcommon.Images{
			RenderConfigImages: ctrlcommon.RenderConfigImages{
				MachineConfigOperator: mcoImagePullspec,
			},
		},
		OSImageURLConfig: &ctrlcommon.OSImageURLConfig{},
		BaseImagePullSecret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "base-image-pull-secret",
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(newSecret),
			},
		},
		FinalImagePushSecret: &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "final-image-pull-secret",
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(newSecret),
			},
		},
	}
}
