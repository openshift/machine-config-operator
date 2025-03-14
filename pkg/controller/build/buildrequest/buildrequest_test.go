package buildrequest

import (
	"flag"
	"fmt"
	"strings"
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
	mcoImagePullspec  = "registry.hostname.com/org/repo@sha256:87980e0edfc86d01182f70c53527f74b5b01df00fe6d47668763d228d4de43a9"
	baseImagePullspec = "registry.hostname.com/org/repo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	extImagePullspec  = "registry.hostname.com/org/repo@sha256:e0fcb915febfed55c9161502bc48c887811deb4dfc5d5c7d015f367c73b9756e"
)

// Optional flag that will print the rendered Containerfile for each test case.
// To use it, run the test suite like this:
// $ go test -v . -print-containerfile
var printContainerfile = flag.Bool("print-containerfile", false, "Print the rendered Containerfile")

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

	expectedContents := func() []string {
		return []string{
			fmt.Sprintf("FROM %s AS extract", baseImagePullspec),
			fmt.Sprintf("FROM %s AS configs", baseImagePullspec),
			fmt.Sprintf("LABEL baseOSContainerImage=%s", baseImagePullspec),
		}
	}

	expectedExtensionsContents := func(extPackages []string) []string {
		return []string{
			fmt.Sprintf("RUN --mount=type=bind,from=%s", extImagePullspec),
			"--mount=type=bind,from=extract,source=/etc/yum.repos.d/coreos-extensions.repo,target=/etc/yum.repos.d/coreos-extensions.repo,bind-propagation=rshared,rw,z",
			"--mount=type=bind,from=extract,source=/tmp/install-extensions.sh,target=/tmp/install-extensions.sh,bind-propagation=rshared,rw,z",
			"COPY ./coreos-extensions.repo /etc/yum.repos.d/coreos-extensions.repo",
			"COPY ./install-extensions.sh /tmp/install-extensions.sh",
			"RUN chmod 0644 /etc/yum.repos.d/coreos-extensions.repo",
			"chmod +x /tmp/install-extensions.sh",
			fmt.Sprintf("LABEL extensionsImage=%s", extImagePullspec),
			fmt.Sprintf("/tmp/install-extensions.sh %s", strings.Join(extPackages, " ")),
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
			expectedContainerfileContents: append(expectedContents(),
				expectedExtensionsContents([]string{"usbguard"})...),
		},
		{
			name: "With extensions image and resolved extensions packages",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.MachineConfig.Spec.Extensions = []string{"kerberos", "usbguard"}
				return opts
			},
			expectedContainerfileContents: append(expectedContents(),
				expectedExtensionsContents([]string{"krb5-workstation", "libkadm5", "usbguard"})...),
		},
		{
			name: "Missing extensions image and extensions",
			optsFunc: func() BuildRequestOpts {
				opts := getBuildRequestOpts()
				opts.OSImageURLConfig.BaseOSExtensionsContainerImage = ""
				opts.MachineConfig.Spec.Extensions = []string{"usbguard"}
				return opts
			},
			unexpectedContainerfileContents: expectedExtensionsContents([]string{}),
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

			assertContainerfileConfigMap(t, configmaps[0], opts)

			containerfile := configmaps[0].Data["Containerfile"]

			if printContainerfile != nil && *printContainerfile == true {
				t.Log(containerfile)
			}

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

func assertContainerfileConfigMap(t *testing.T, configmap *corev1.ConfigMap, opts BuildRequestOpts) {
	t.Helper()

	assert.Equal(t, "containerfile-worker-afc35db0f874c9bfdc586e6ba39f1504", configmap.Name)
	assert.Equal(t, configmap.Data["inner-build-script.sh"], innerBuildScript)

	if opts.MachineConfig.Spec.Extensions != nil {
		// If we have extensions, then we should expect the following keys / values to be present.
		assert.Contains(t, configmap.Data, "install-extensions.sh")
		assert.Contains(t, configmap.Data, "coreos-extensions.repo")
		assert.Equal(t, configmap.Data["install-extensions.sh"], installExtensionsScript)
		assert.Equal(t, configmap.Data["coreos-extensions.repo"], coreosExtensionsRepo)
	} else {
		// Otherwise, we should expect them not to be present.
		assert.NotContains(t, configmap.Data, "install-extensions.sh")
		assert.NotContains(t, configmap.Data, "coreos-extensions.repo")
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
		baseImagePullspec,
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

	// Inject our own pullspecs for easier assertions.
	osImageURLConfig := fixtures.OSImageURLConfig()
	osImageURLConfig.BaseOSContainerImage = baseImagePullspec
	osImageURLConfig.BaseOSExtensionsContainerImage = extImagePullspec

	return BuildRequestOpts{
		MachineConfig:   &mcfgv1.MachineConfig{},
		MachineOSConfig: layeredObjects.MachineOSConfigBuilder.MachineOSConfig(),
		MachineOSBuild:  layeredObjects.MachineOSBuildBuilder.MachineOSBuild(),
		Images: &ctrlcommon.Images{
			RenderConfigImages: ctrlcommon.RenderConfigImages{
				MachineConfigOperator: mcoImagePullspec,
			},
		},
		OSImageURLConfig: osImageURLConfig,
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
