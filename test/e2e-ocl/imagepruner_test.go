package e2e_ocl

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	"github.com/openshift/machine-config-operator/test/helpers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Used by TestImagePruner only when flags are passed.
var realImageRegistrySecretPath string
var realImagePullspec string

func init() {
	flag.StringVar(&realImageRegistrySecretPath, "image-registry-secret", "", "Path to image registry creds for real test")
	flag.StringVar(&realImagePullspec, "image-pullspec", "", "Path to image for real test")
}

// This test does the following:
// - Creates an empty (scratch) image and uploads it to the specified registry using skopeo.
// - Tests that the ImagePruner can inspect the image.
// - Tests that the ImagePruner cna delete the image.
// - Tests that the image has been deleted.
//
// To run this test, one needs the following:
// - Admin-level creds to an image repository such as Quay.io.
// - A pull secret on disk with the creds for that image repository.
// - The image repository must exist.
//
// The test can be run with the following incantation.
// $ go test -tags='containers_image_openpgp exclude_graphdriver_devicemapper exclude_graphdriver_btrfs containers_image_ostree_stub' -v -count=1 -image-registry-secret /path/to/image/creds/on/disk -image-pullspec quay.io/org/repo:tag
func TestImagePruner(t *testing.T) {
	skipIfUnableToRun(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	tmpDir := t.TempDir()

	srcImage := filepath.Join(tmpDir, helpers.ImageTarballFilename)

	require.NoError(t, helpers.CreateScratchImageTarball(tmpDir))

	t.Logf("Copying %s to %s using skopeo", srcImage, realImagePullspec)
	cmd := exec.Command("skopeo", "copy", "--dest-authfile", realImageRegistrySecretPath, "tarball://"+srcImage, "docker://"+realImagePullspec)
	t.Logf(cmd.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run())

	ip, k8sSecret, err := setupImagePrunerForTest(realImageRegistrySecretPath)
	require.NoError(t, err)

	t.Logf("Inspecting %s using ImagePruner", realImagePullspec)

	inspect, digest, err := ip.InspectImage(ctx, realImagePullspec, k8sSecret, &mcfgv1.ControllerConfig{})
	assert.NoError(t, err)
	assert.NotNil(t, inspect)
	assert.NotNil(t, digest)

	t.Logf("Deleting image %s using ImagePruner", realImagePullspec)

	assert.NoError(t, ip.DeleteImage(ctx, realImagePullspec, k8sSecret, &mcfgv1.ControllerConfig{}))

	t.Logf("Inspecting %s again using ImagePruner; expecting an error this time", realImagePullspec)
	_, _, err = ip.InspectImage(ctx, realImagePullspec, k8sSecret, &mcfgv1.ControllerConfig{})
	assert.Error(t, err)
	assert.True(t, imagepruner.IsTolerableDeleteErr(err))
}

// This test attempts to make real requests to image registries that one may
// not have the appropriate credentials to run. The general idea here is to
// ensure that our deletion error toleration code continues to correctly detect
// whether deletion failed due to inadequate permissions.
func TestImagePrunerErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	testCases := []struct {
		name             string
		pullspec         string
		expectInspectErr bool
		expectDeleteErr  bool
	}{
		{
			name:             "Quay.io",
			pullspec:         "quay.io/skopeo/stable:latest",
			expectInspectErr: false,
			expectDeleteErr:  true,
		},
		{
			name:             "Docker.io existing image",
			pullspec:         "docker.io/library/python:latest",
			expectInspectErr: false,
			expectDeleteErr:  true,
		},
		{
			name:             "Docker.io nonexistent image",
			pullspec:         "docker.io/library/notrealgoaway:latest",
			expectInspectErr: true,
			expectDeleteErr:  true,
		},
		{
			name:             "Fedora Registry",
			pullspec:         "registry.fedoraproject.org/fedora:latest",
			expectInspectErr: false,
			expectDeleteErr:  true,
		},
		{
			name:             "GitHub image registry - existing image",
			pullspec:         "ghcr.io/open-webui/open-webui:latest",
			expectInspectErr: false,
			expectDeleteErr:  true,
		},
		{
			name:             "GitHub image registry - nonexistent image",
			pullspec:         "ghcr.io/cheesesashimi/zacks-openshift-helpers:latest",
			expectInspectErr: true,
			expectDeleteErr:  true,
		},
		{
			name:             "Google image registry - existing image",
			pullspec:         "gcr.io/google.com/cloudsdktool/google-cloud-cli:stable",
			expectInspectErr: false,
			expectDeleteErr:  true,
		},
		{
			name:             "Google image registry - nonexistant image",
			pullspec:         "gcr.io/google.com/cloudsdktool/notrealgoaway:latest",
			expectInspectErr: true,
			expectDeleteErr:  true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Run("Inspect", func(t *testing.T) {
				ip, k8sSecret, err := setupImagePrunerForTestWithEmptyCreds(t)
				require.NoError(t, err)

				_, _, err = ip.InspectImage(ctx, testCase.pullspec, k8sSecret, &mcfgv1.ControllerConfig{})
				if testCase.expectInspectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			})

			t.Run("Delete", func(t *testing.T) {
				ip, k8sSecret, err := setupImagePrunerForTestWithEmptyCreds(t)
				require.NoError(t, err)

				err = ip.DeleteImage(ctx, testCase.pullspec, k8sSecret, &mcfgv1.ControllerConfig{})
				if testCase.expectDeleteErr {
					assert.Error(t, err)
					assert.True(t, imagepruner.IsTolerableDeleteErr(err))
				} else {
					assert.NoError(t, err)
				}
			})
		})
	}
}

func skipIfUnableToRun(t *testing.T) {
	if realImageRegistrySecretPath != "" && realImagePullspec != "" {
		t.Logf("Test suite invoked with -image-registry-secret %q and -image-pullspec %q, will perform full image registry test", realImageRegistrySecretPath, realImagePullspec)
	} else {
		t.Skip("-image-registry-secret and -image-pullspec flags unset")
	}
}

func setupImagePrunerForTestWithEmptyCreds(t *testing.T) (imagepruner.ImagePruner, *corev1.Secret, error) {
	// Write an "empty" creds file since we don't actually need creds for this test.
	authfilePath := filepath.Join(t.TempDir(), "authfile.json")
	if err := os.WriteFile(authfilePath, []byte(`{"auths":{}}`), 0o755); err != nil {
		return nil, nil, err
	}

	return setupImagePrunerForTest(authfilePath)
}

func setupImagePrunerForTest(credPath string) (imagepruner.ImagePruner, *corev1.Secret, error) {
	secretBytes, err := os.ReadFile(credPath)
	if err != nil {
		return nil, nil, err
	}

	is, err := secrets.NewImageRegistrySecret(secretBytes)
	if err != nil {
		return nil, nil, err
	}

	k8sSecret, err := is.K8sSecret(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return nil, nil, err
	}

	return imagepruner.NewImagePruner(), k8sSecret, nil
}
