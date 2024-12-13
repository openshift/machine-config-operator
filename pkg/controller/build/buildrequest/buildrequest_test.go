package buildrequest

import (
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	mcoImagePullspec = "registry.hostname.com/org/repo@sha256:87980e0edfc86d01182f70c53527f74b5b01df00fe6d47668763d228d4de43a9"
)

// Validates that if an invalid extension is provided that the ConfigMap
// generation fails and the error contains the names of the invalid extensions.
func TestBuildRequestInvalidExtensions(t *testing.T) {
	t.Parallel()

	opts := getBuildRequestOpts()
	opts.MachineConfig.Spec.Extensions = []string{"invalid-ext1", "invalid-ext2"}

	br := newBuildRequest(opts)

	_, err := br.ConfigMaps()
	assert.Error(t, err)

	for _, ext := range opts.MachineConfig.Spec.Extensions {
		assert.Contains(t, err.Error(), ext)
	}
}

// Tests that the BuildRequest is constructed as expected.
func TestBuildRequest(t *testing.T) {
	t.Parallel()

	osImageURLConfig := fixtures.OSImageURLConfig()

	expectedContents := func() []string {
		return []string{
			fmt.Sprintf("FROM %s AS extract", osImageURLConfig.BaseOSContainerImage),
			fmt.Sprintf("FROM %s AS configs", osImageURLConfig.BaseOSContainerImage),
			fmt.Sprintf("LABEL baseOSContainerImage=%s", osImageURLConfig.BaseOSContainerImage),
		}
	}

	testCases := []struct {
		name                            string
		optsFunc                        func() BuildRequestOpts
		expectedContainerfileContents   []string
		unexpectedContainerfileContents []string
	}{
		{
			name: "With extensions image and extensions",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.MachineConfig.Spec.Extensions = []string{"usbguard"}
				return opts
			},
			expectedContainerfileContents: append(expectedContents(), []string{
				fmt.Sprintf("RUN --mount=type=bind,from=%s", osImageURLConfig.BaseOSExtensionsContainerImage),
				`extensions="usbguard"`,
			}...),
		},
		{
			name: "With extensions image and resolved extensions packages",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.MachineConfig.Spec.Extensions = []string{"kerberos", "usbguard"}
				return opts
			},
			expectedContainerfileContents: append(expectedContents(), []string{
				fmt.Sprintf("RUN --mount=type=bind,from=%s", osImageURLConfig.BaseOSExtensionsContainerImage),
				`extensions="krb5-workstation libkadm5 usbguard"`,
			}...),
		},
		{
			name: "Missing extensions image and extensions",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.OSImageURLConfig.BaseOSExtensionsContainerImage = ""
				opts.MachineConfig.Spec.Extensions = []string{"usbguard"}
				return opts
			},
			unexpectedContainerfileContents: []string{
				fmt.Sprintf("RUN --mount=type=bind,from=%s", osImageURLConfig.BaseOSContainerImage),
				"extensions=\"usbguard\"",
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
			name: "MachineOSConfig-provided options override OSImageURLConfig defaults",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.MachineConfig.Spec.Extensions = []string{"usbguard"}
				return opts
			},
			expectedContainerfileContents: []string{
				"FROM base-os-image-from-machineosconfig AS extract",
				"FROM base-os-image-from-machineosconfig AS configs",
				"RUN --mount=type=bind,from=base-ext-image-from-machineosconfig",
				"extensions=\"usbguard\"",
				"LABEL releaseversion=release-version-from-machineosconfig",
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
					opts.MachineOSConfig.Spec.Containerfile[0].Content,
				}...)
			}

			for _, content := range testCase.expectedContainerfileContents {
				assert.Contains(t, containerfile, content)
			}

			for _, content := range testCase.unexpectedContainerfileContents {
				assert.NotContains(t, containerfile, content)
			}

			buildJob := br.Builder().GetObject().(*batchv1.Job)

			_, err = NewBuilder(buildJob)
			assert.NoError(t, err)

			assert.Equal(t, "containerfile-worker-afc35db0f874c9bfdc586e6ba39f1504", configmaps[0].Name)
			assert.Equal(t, "mc-worker-afc35db0f874c9bfdc586e6ba39f1504", configmaps[1].Name)
			assert.Equal(t, "build-worker-afc35db0f874c9bfdc586e6ba39f1504", buildJob.Name)

			secrets, err := br.Secrets()
			assert.NoError(t, err)

			objects := []metav1.Object{
				configmaps[0],
				configmaps[1],
				buildJob,
				secrets[0],
				secrets[1],
			}

			for _, object := range objects {
				assert.True(t, utils.EphemeralBuildObjectSelector().Matches(labels.Set(object.GetLabels())))
				assert.True(t, utils.OSBuildSelector().Matches(labels.Set(object.GetLabels())))
				assert.True(t, utils.IsObjectCreatedByBuildController(object))
			}

			for _, secret := range secrets {
				assertSecretInCorrectFormat(t, secret)
			}

			assert.Equal(t, secrets[0].Name, "base-worker-afc35db0f874c9bfdc586e6ba39f1504")
			assert.Equal(t, secrets[1].Name, "final-worker-afc35db0f874c9bfdc586e6ba39f1504")

			assertBuildJobIsCorrect(t, buildJob, opts)
		})
	}
}

func assertSecretInCorrectFormat(t *testing.T, secret *corev1.Secret) {
	t.Helper()

	assert.True(t, utils.CanonicalizedSecretSelector().Matches(labels.Set(secret.GetLabels())))
	assert.Equal(t, secret.Type, corev1.SecretTypeDockerConfigJson)
	assert.NotEqual(t, secret.Type, corev1.SecretTypeDockercfg)
	assert.Contains(t, secret.Data, corev1.DockerConfigJsonKey)
	assert.NotContains(t, secret.Data, corev1.DockerConfigKey)
	assert.JSONEq(t, string(secret.Data[corev1.DockerConfigJsonKey]), `{"auths":{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}}`)
}

func assertBuildJobIsCorrect(t *testing.T, buildJob *batchv1.Job, opts BuildRequestOpts) {
	etcRpmGpgKeysOpts := optsForEtcRpmGpgKeys()
	assertBuildJobMatchesExpectations(t, opts.HasEtcPkiRpmGpgKeys, buildJob,
		etcRpmGpgKeysOpts.envVar(),
		etcRpmGpgKeysOpts.volumeForSecret(constants.EtcPkiRpmGpgSecretName),
		etcRpmGpgKeysOpts.volumeMount(),
	)

	etcYumReposDOpts := optsForEtcYumReposD()
	assertBuildJobMatchesExpectations(t, opts.HasEtcYumReposDConfigs, buildJob,
		etcYumReposDOpts.envVar(),
		etcYumReposDOpts.volumeForConfigMap(),
		etcYumReposDOpts.volumeMount(),
	)

	etcPkiEntitlementKeysOpts := optsForEtcPkiEntitlements()
	assertBuildJobMatchesExpectations(t, opts.HasEtcPkiEntitlementKeys, buildJob,
		etcPkiEntitlementKeysOpts.envVar(),
		etcPkiEntitlementKeysOpts.volumeForSecret(constants.EtcPkiEntitlementSecretName+"-"+opts.MachineOSConfig.Spec.MachineConfigPool.Name),
		etcPkiEntitlementKeysOpts.volumeMount(),
	)

	assert.Equal(t, buildJob.Spec.Template.Spec.Containers[0].Image, mcoImagePullspec)
	expectedPullspecs := []string{
		"base-os-image-from-machineosconfig",
		fixtures.OSImageURLConfig().BaseOSContainerImage,
	}

	assert.Contains(t, expectedPullspecs, buildJob.Spec.Template.Spec.Containers[1].Image)

	assertPodHasVolume(t, buildJob.Spec.Template.Spec, corev1.Volume{
		Name: "final-image-push-creds",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: "final-worker-afc35db0f874c9bfdc586e6ba39f1504",
				Items: []corev1.KeyToPath{
					{
						Key:  corev1.DockerConfigJsonKey,
						Path: "config.json",
					},
				},
			},
		},
	})

	assertPodHasVolume(t, buildJob.Spec.Template.Spec, corev1.Volume{
		Name: "base-image-pull-creds",
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: "base-worker-afc35db0f874c9bfdc586e6ba39f1504",
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

func assertPodHasVolume(t *testing.T, pod corev1.PodSpec, volume corev1.Volume) {
	assert.Contains(t, pod.Volumes, volume)
}

func assertBuildJobMatchesExpectations(t *testing.T, shouldBePresent bool, buildJob *batchv1.Job, envvar corev1.EnvVar, volume corev1.Volume, volumeMount corev1.VolumeMount) {
	for _, container := range buildJob.Spec.Template.Spec.Containers {
		if shouldBePresent {
			assert.Contains(t, container.Env, envvar)
			assert.Contains(t, container.VolumeMounts, volumeMount)
			assertPodHasVolume(t, buildJob.Spec.Template.Spec, volume)
		} else {
			assert.NotContains(t, container.Env, envvar)
			assert.NotContains(t, container.VolumeMounts, volumeMount)
			assert.NotContains(t, buildJob.Spec.Template.Spec.Volumes, volume)
		}

		assert.Contains(t, container.Env, corev1.EnvVar{
			Name:  "TAG",
			Value: "registry.hostname.com/org/repo:worker-afc35db0f874c9bfdc586e6ba39f1504",
		})
	}
}

func getBuildRequestOpts() BuildRequestOpts {
	containerfileContents := `FROM configs AS final
RUN rpm-ostree install && \
	ostree container commit`

	layeredObjects := fixtures.NewObjectBuildersForTest("worker")
	layeredObjects.MachineOSConfigBuilder.
		WithContainerfile(mcfgv1.NoArch, containerfileContents)

	layeredObjects.MachineOSBuildBuilder.
		// Note: This is set statically so that the test suite is less brittle.
		WithName("worker-afc35db0f874c9bfdc586e6ba39f1504").
		WithRenderedImagePushspec("registry.hostname.com/org/repo:worker-afc35db0f874c9bfdc586e6ba39f1504")

	legacySecret := `{"registry.hostname.com": {"username": "user", "password": "s3kr1t", "auth": "s00pers3kr1t", "email": "user@hostname.com"}}`
	newSecret := `{"auths":` + legacySecret + `}`

	return BuildRequestOpts{
		MachineConfig:   &mcfgv1.MachineConfig{},
		MachineOSConfig: layeredObjects.MachineOSConfigBuilder.MachineOSConfig(),
		MachineOSBuild:  layeredObjects.MachineOSBuildBuilder.MachineOSBuild(),
		Images: &ctrlcommon.Images{
			RenderConfigImages: ctrlcommon.RenderConfigImages{
				MachineConfigOperator: mcoImagePullspec,
			},
		},
		OSImageURLConfig: fixtures.OSImageURLConfig(),
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
