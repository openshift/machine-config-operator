package e2e_ocl_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/types"
	"github.com/davecgh/go-spew/spew"
	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"

	"github.com/openshift/machine-config-operator/pkg/controller/build/imagepruner"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	"github.com/openshift/machine-config-operator/test/framework"
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
	t.Parallel()

	skipIfUnableToRun(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, createAndPushScratchImage(ctx, t, realImagePullspec, realImageRegistrySecretPath, ""))

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
	assert.True(t, imagepruner.IsImageNotFoundErr(err))
	assert.False(t, imagepruner.IsAccessDeniedErr(err))
	assert.True(t, imagepruner.IsTolerableDeleteErr(err))
}

// This test sets up an ImageStream and exposes the internal image registry
// which backs it. It then creates a scratch image and pushes it to the
// internal image registry and performs a series of tests against it in order
// to validate the returned errors.
func TestImagePrunerOnCluster(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	canContinue, err := canTestOnInClusterRegistry(ctx, "")
	if err != nil {
		t.Skip("")
	}

	if !canContinue {
		t.Skip("")
	}

	cs := framework.NewClientSet("")

	// Allow the internal image regsistry to be accessed from outside of the cluster.
	externalRegistryHostname, err := helpers.ExposeClusterImageRegistry(ctx, cs)
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, helpers.UnexposeClusterImageRegistry(ctx, cs))
	})

	// Set up the imagestream for this test.
	pushSecretName, pullspec, cleanupFunc := setupImageStream(t, cs, metav1.ObjectMeta{Name: "imagepruner", Namespace: ctrlcommon.MCONamespace})
	t.Cleanup(cleanupFunc)

	// Parse the internal registry hostname from the image pullspec.
	parsed, err := reference.ParseNamed(pullspec)
	require.NoError(t, err)
	internalRegistryHostname := reference.Domain(parsed)

	// Retrieve the long-lived image pull secret and add the external registry hostname to it.
	secret, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Get(ctx, pushSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	secretPath := filepath.Join(t.TempDir(), "config.json")
	secret, err = addExternalRegistryHostnameToSecret(internalRegistryHostname, externalRegistryHostname, secret, secretPath)
	require.NoError(t, err)

	// Replace the internal registry hostname with the external image registry hostname.
	pullspec = strings.ReplaceAll(pullspec, internalRegistryHostname, externalRegistryHostname)

	// Retrieve the ingress cert so that we do not have to disable SSL verification.
	ingressCert, err := cs.CoreV1Interface.Secrets("openshift-ingress").Get(ctx, "router-certs-default", metav1.GetOptions{})
	require.NoError(t, err)
	certsDir := filepath.Join(t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(certsDir, externalRegistryHostname+".crt"), ingressCert.Data["tls.crt"], 0o644))

	// Wait for the route to finish setting up. We can determine that the route
	// setup is complete when we get an image not found error when inspecting a
	// nonexistent image.
	err = wait.PollImmediate(time.Second, time.Minute, func() (bool, error) {
		imgPruner := imagepruner.NewImageInspectorDeleter()
		sysCtx := &types.SystemContext{DockerCertPath: certsDir, AuthFilePath: secretPath}

		_, _, err = imgPruner.ImageInspect(ctx, sysCtx, pullspec)

		// If we get an image not found error, that means the route is set up
		// because we were able to authenticate to the image registry and make a
		// query for a nonexistent image.
		if imagepruner.IsImageNotFoundErr(err) {
			return true, nil
		}

		// If this is an HTTP 503 error, that means the route has not finished
		// being set up, so we need to try again.
		var unexpectedHTTPError docker.UnexpectedHTTPStatusError
		if errors.As(err, &unexpectedHTTPError) && unexpectedHTTPError.StatusCode == http.StatusServiceUnavailable {
			return false, nil
		}

		// We were unable to identify this error, so return it.
		return false, fmt.Errorf("unknown registry error when polling: %w", err)
	})

	require.NoError(t, err, unwrapAll(err))

	// Now we can run our test cases. All test cases use the
	// ImageInspectorDeleter directly since we need to have a bit more control
	// over the SystemContext given that we're running out-of-cluster.
	t.Run("Inspect without creds", func(t *testing.T) {
		t.Parallel()

		imgPruner := imagepruner.NewImageInspectorDeleter()
		sysCtx := &types.SystemContext{DockerCertPath: certsDir}

		_, _, err = imgPruner.ImageInspect(ctx, sysCtx, pullspec)
		assert.Error(t, err)
		assert.True(t, imagepruner.IsAccessDeniedErr(err), "expected access denied err: %s", unwrapAll(err))
	})

	t.Run("Inspect nonexistent image digest with creds", func(t *testing.T) {
		t.Parallel()

		imgPruner := imagepruner.NewImageInspectorDeleter()
		sysCtx := &types.SystemContext{DockerCertPath: certsDir, AuthFilePath: secretPath}

		// Use a fake digest in the image pullspec.
		fakeDigestedPullspec, err := replaceTagWithDigestOnPullspec(pullspec, "fake-hash")
		require.NoError(t, err)

		_, _, err = imgPruner.ImageInspect(ctx, sysCtx, fakeDigestedPullspec)
		assert.Error(t, err)
		assert.True(t, imagepruner.IsImageNotFoundErr(err), "expected image not found err: %s", unwrapAll(err))
	})

	t.Run("Inspect nonexistent image tag with creds", func(t *testing.T) {
		t.Parallel()

		imgPruner := imagepruner.NewImageInspectorDeleter()
		sysCtx := &types.SystemContext{DockerCertPath: certsDir, AuthFilePath: secretPath}

		fakeRepoPullspec, err := replaceRepoOnPullspec(pullspec, "fake-repo")
		require.NoError(t, err)

		_, _, err = imgPruner.ImageInspect(ctx, sysCtx, fakeRepoPullspec)
		assert.Error(t, err)
		assert.True(t, imagepruner.IsImageNotFoundErr(err), "expected image not found err: %s", unwrapAll(err))
	})

	t.Run("Inspect nonexistent image repo with creds", func(t *testing.T) {
		t.Parallel()

		imgPruner := imagepruner.NewImageInspectorDeleter()
		sysCtx := &types.SystemContext{DockerCertPath: certsDir, AuthFilePath: secretPath}

		fakeTagPullspec, err := replaceTagOnPullspec(pullspec, "fake-tag")
		require.NoError(t, err)

		_, _, err = imgPruner.ImageInspect(ctx, sysCtx, fakeTagPullspec)
		assert.Error(t, err)
		assert.True(t, imagepruner.IsImageNotFoundErr(err), "expected image not found err: %s", unwrapAll(err))
	})

	t.Run("Push image and inspect", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, createAndPushScratchImage(ctx, t, pullspec, secretPath, certsDir))

		imgPruner := imagepruner.NewImageInspectorDeleter()
		sysCtx := &types.SystemContext{DockerCertPath: certsDir, AuthFilePath: secretPath}

		_, _, err := imgPruner.ImageInspect(ctx, sysCtx, pullspec)
		assert.NoError(t, err)

		// The long-lived pull secret does not confer the ability to delete images
		// from the registry, so this is expected to be access denied.
		err = imgPruner.DeleteImage(ctx, sysCtx, pullspec)
		assert.Error(t, err)
		assert.True(t, imagepruner.IsAccessDeniedErr(err), "expected access denied err: %s", unwrapAll(err))
	})
}

// This test attempts to make real requests to image registries that one may
// not have the appropriate credentials to run. The general idea here is to
// ensure that our error detection code functions as it should.
//
// Each test case represents a given image registry as well as a commonly-used
// image that is readily available there. For each test case, we do the
// following:
// 1. Attempt to inspect the image; this should succeed in all cases.
// 2. Get the image digest from the inspected image, mutate it so that it does
// not exist, then attempt to inspect that pullspec.
// 3. Change the tag on the image pullspec to a known nonexistent tag, then
// attempt to insect that pullspec.
// 4. Change the repo on the image pullspect o a known nonexistent repo, then
// attempt to inspect that pullspec.
// 5. Perform the same operations described above for deletion.
// 6. Ensure that the errors returned (if any) match what we expect.
func TestImagePrunerErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)

	type expectedErr struct {
		accessDenied  bool
		imageNotFound bool
	}

	type operation struct {
		existingImage     expectedErr
		nonexistentRepo   expectedErr
		nonexistentTag    expectedErr
		nonexistentDigest expectedErr
	}

	type testCase struct {
		name     string
		pullspec string
		deletion operation
		inspect  operation
	}

	testCases := []testCase{
		{
			name:     "Quay.io",
			pullspec: "quay.io/skopeo/stable:latest",
			deletion: operation{
				existingImage: expectedErr{
					accessDenied: true,
				},
				nonexistentRepo: expectedErr{
					accessDenied: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
			inspect: operation{
				nonexistentRepo: expectedErr{
					accessDenied: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
		},
		{
			name:     "Docker.io",
			pullspec: "docker.io/library/python:latest",
			deletion: operation{
				existingImage: expectedErr{
					accessDenied: true,
				},
				nonexistentRepo: expectedErr{
					accessDenied: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
			inspect: operation{
				nonexistentRepo: expectedErr{
					accessDenied: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
		},
		{
			name:     "Fedora Registry",
			pullspec: "registry.fedoraproject.org/fedora:latest",
			deletion: operation{
				existingImage: expectedErr{
					accessDenied: true,
				},
				nonexistentRepo: expectedErr{
					imageNotFound: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
			inspect: operation{
				nonexistentRepo: expectedErr{
					imageNotFound: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
		},
		{
			name:     "GitHub image registry",
			pullspec: "ghcr.io/open-webui/open-webui:latest",
			deletion: operation{
				existingImage: expectedErr{
					accessDenied: true,
				},
				nonexistentRepo: expectedErr{
					accessDenied: true,
				},
				nonexistentTag: expectedErr{
					accessDenied: true,
				},
				nonexistentDigest: expectedErr{
					accessDenied: true,
				},
			},
			inspect: operation{
				nonexistentRepo: expectedErr{
					accessDenied: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
		},
		{
			name:     "Google image registry",
			pullspec: "gcr.io/google.com/cloudsdktool/google-cloud-cli:stable",
			deletion: operation{
				existingImage: expectedErr{
					accessDenied: true,
				},
				nonexistentRepo: expectedErr{
					imageNotFound: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
			inspect: operation{
				nonexistentRepo: expectedErr{
					imageNotFound: true,
				},
				nonexistentTag: expectedErr{
					imageNotFound: true,
				},
				nonexistentDigest: expectedErr{
					imageNotFound: true,
				},
			},
		},
	}

	// Runs the imagepruner inspect function against the given pullspec and
	// checks that errors are what we expect them to be. The error and digest are
	// returned for additional assertions and use elsewhere.
	inspectTestFunc := func(t *testing.T, pullspec string, expected expectedErr) (*digest.Digest, error) {
		t.Helper()

		ip, k8sSecret, err := setupImagePrunerForTestWithEmptyCreds(t)
		require.NoError(t, err)

		_, imgDigest, err := ip.InspectImage(ctx, pullspec, k8sSecret, &mcfgv1.ControllerConfig{})
		if !expected.imageNotFound && !expected.accessDenied {
			require.NoError(t, err)
		} else {
			assert.Equal(t, expected.imageNotFound, imagepruner.IsImageNotFoundErr(err), "image not found error should be %v", !expected.imageNotFound)
			assert.Equal(t, expected.accessDenied, imagepruner.IsAccessDeniedErr(err), "access denied error should be %v", !expected.accessDenied)
		}

		return imgDigest, err
	}

	// Runs the imagepruner delete function against the given pullspec and checks
	// that errors are what we expect them to be. The error is returned for
	// additional assertions.
	deleteTestFunc := func(t *testing.T, pullspec string, expected expectedErr) error {
		t.Helper()

		ip, k8sSecret, err := setupImagePrunerForTestWithEmptyCreds(t)
		require.NoError(t, err)

		err = ip.DeleteImage(ctx, pullspec, k8sSecret, &mcfgv1.ControllerConfig{})
		// We should always get an error back for this test because we do not have
		// permissions to delete images.
		assert.Error(t, err)
		assert.Equal(t, expected.imageNotFound, imagepruner.IsImageNotFoundErr(err), "image not found error should be %v", !expected.imageNotFound)
		assert.Equal(t, expected.accessDenied, imagepruner.IsAccessDeniedErr(err), "access denied error should be %v", !expected.accessDenied)
		assert.True(t, imagepruner.IsTolerableDeleteErr(err))

		return err
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			// Because each test case targets a different image registry, we can run
			// them in parallel. However, each subtest must be run sequentially in
			// order to ensure that we don't run into any rate limiting.
			t.Parallel()

			// This should always be successful, hence why the expected outcome is
			// not wired up to the individual test cases.
			_, inspectExistingErr := inspectTestFunc(t, testCase.pullspec, expectedErr{})
			require.NoError(t, inspectExistingErr)

			// Use a fake digest in the image pullspec.
			fakeDigestedPullspec, err := replaceTagWithDigestOnPullspec(testCase.pullspec, "fake-hash")
			require.NoError(t, err)

			// Replace the tag on the tagged pullspec with a tag we know will not be there.
			fakeTagPullspec, err := replaceTagOnPullspec(testCase.pullspec, "fake-tag")
			require.NoError(t, err)

			// Replace the repo on the pullspec with a repo we know will not be there.
			fakeRepoPullspec, err := replaceRepoOnPullspec(testCase.pullspec, "fake-repo")
			require.NoError(t, err)

			t.Run("Inspect", func(t *testing.T) {
				t.Run("Existing image", func(t *testing.T) {
					assert.NoError(t, inspectExistingErr)
				})

				t.Run("Nonexistent image digest", func(t *testing.T) {
					_, err = inspectTestFunc(t, fakeDigestedPullspec, testCase.inspect.nonexistentDigest)
					assert.Error(t, err)
				})

				t.Run("Nonexistent image tag", func(t *testing.T) {
					_, err = inspectTestFunc(t, fakeTagPullspec, testCase.inspect.nonexistentTag)
					assert.Error(t, err)
				})

				t.Run("Nonexistent image repo", func(t *testing.T) {
					_, err = inspectTestFunc(t, fakeRepoPullspec, testCase.inspect.nonexistentRepo)
					assert.Error(t, err)
				})
			})

			t.Run("Delete", func(t *testing.T) {
				// This should always return an error because we're not authenticated
				// against any of the image registries used here.
				t.Run("Existing image", func(t *testing.T) {
					assert.Error(t, deleteTestFunc(t, testCase.pullspec, testCase.deletion.existingImage))
				})

				t.Run("Nonexistent image digest", func(t *testing.T) {
					assert.Error(t, deleteTestFunc(t, fakeDigestedPullspec, testCase.deletion.nonexistentDigest))
				})

				t.Run("Nonexistent image tag", func(t *testing.T) {
					assert.Error(t, deleteTestFunc(t, fakeTagPullspec, testCase.deletion.nonexistentTag))
				})

				t.Run("Nonexistent image repo", func(t *testing.T) {
					assert.Error(t, deleteTestFunc(t, fakeRepoPullspec, testCase.deletion.nonexistentRepo))
				})
			})
		})
	}
}

// Tests that the image pullspec mutation functions work as they should.
func TestImagePrunerHelpers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name                      string
		pullspec                  string
		expectedRepoReplacement   string
		expectedTagReplacement    string
		expectedDigestReplacement string
	}{
		{
			name:                      "Simple tagged",
			pullspec:                  "registry.hostname.com/org/repo:tag",
			expectedRepoReplacement:   "registry.hostname.com/org/notrealgoaway:tag",
			expectedTagReplacement:    "registry.hostname.com/org/repo:notrealgoaway",
			expectedDigestReplacement: "registry.hostname.com/org/repo@sha256:86dec32bc7f325bef814f17689b40c8c04017f3ead2bb600dbda09a26da27a7a",
		},
		{
			name:                      "Simple digested",
			pullspec:                  "registry.hostname.com/org/repo@sha256:4bc453b53cb3d914b45f4b250294236adba2c0e09ff6f03793949e7e39fd4cc1",
			expectedRepoReplacement:   "registry.hostname.com/org/notrealgoaway@sha256:4bc453b53cb3d914b45f4b250294236adba2c0e09ff6f03793949e7e39fd4cc1",
			expectedTagReplacement:    "registry.hostname.com/org/repo@sha256:86dec32bc7f325bef814f17689b40c8c04017f3ead2bb600dbda09a26da27a7a",
			expectedDigestReplacement: "registry.hostname.com/org/repo@sha256:86dec32bc7f325bef814f17689b40c8c04017f3ead2bb600dbda09a26da27a7a",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Run("Repo", func(t *testing.T) {
				replacedRepo, err := replaceRepoOnPullspec(testCase.pullspec, "notrealgoaway")
				assert.NoError(t, err)
				assert.Equal(t, testCase.expectedRepoReplacement, replacedRepo)
			})

			t.Run("Tag", func(t *testing.T) {
				replacedTag, err := replaceTagOnPullspec(testCase.pullspec, "notrealgoaway")
				assert.NoError(t, err)
				assert.Equal(t, testCase.expectedTagReplacement, replacedTag)
			})

			t.Run("Digest", func(t *testing.T) {
				replacedDigest, err := replaceTagWithDigestOnPullspec(testCase.pullspec, "notrealgoaway")
				assert.NoError(t, err)
				assert.Equal(t, testCase.expectedDigestReplacement, replacedDigest)
			})
		})
	}
}

// Replaces the repo portion of the given pullspec with 'notrealgoaway'.
// Handles both tagged and digested pullspecs.
func replaceRepoOnPullspec(pullspec, fakeRepoName string) (string, error) {
	named, err := reference.ParseNamed(pullspec)
	if err != nil {
		return "", err
	}

	splitChar := "/"

	path := reference.Path(named)
	if strings.Contains(path, splitChar) {
		split := strings.Split(path, splitChar)
		split[len(split)-1] = fakeRepoName
		path = strings.Join(split, splitChar)
	} else {
		path = fakeRepoName
	}

	// Validate our final pullspec before returning to catch any errors.
	getParsedPullspec := func(p string) (string, error) {
		parsed, err := reference.ParseNamed(p)
		if err != nil {
			return "", fmt.Errorf("could not parse generated pullspec %s: %w", p, err)
		}

		return parsed.String(), nil
	}

	if tagged, ok := named.(reference.NamedTagged); ok {
		return getParsedPullspec(fmt.Sprintf("%s/%s:%s", reference.Domain(named), path, tagged.Tag()))
	}

	digested, digestedOK := named.(reference.Digested)
	canonical, canonicalOK := named.(reference.Canonical)

	digest := ""
	if digestedOK {
		digest = digested.Digest().String()
	}

	if canonicalOK {
		digest = canonical.Digest().String()
	}

	if digest != "" {
		return getParsedPullspec(fmt.Sprintf("%s/%s@%s", reference.Domain(named), path, digest))
	}

	return pullspec, fmt.Errorf("don't know what to do with this pullspec")
}

// Replaces the tag portion of the given pullspec with 'notrealgoaway'. If a
// digested pullspec is given, we use the fake tag to create a SHA256 sum instead.
func replaceTagOnPullspec(pullspec, fakeTag string) (string, error) {
	named, err := reference.ParseNamed(pullspec)
	if err != nil {
		return "", err
	}

	if _, ok := named.(reference.NamedTagged); ok {
		taggedRef, err := reference.WithTag(named, fakeTag)
		if err != nil {
			return "", err
		}

		return taggedRef.String(), nil
	}

	_, digestedOK := named.(reference.Digested)
	_, canonicalOK := named.(reference.Canonical)

	if (digestedOK || canonicalOK) || (digestedOK && canonicalOK) {
		return replaceTagWithDigestOnPullspec(pullspec, fakeTag)
	}

	return "", fmt.Errorf("don't know what to do with this pullspec")
}

// Replaces the tag on a given pullspec with a sha256 representation of the
// provided string.
func replaceTagWithDigestOnPullspec(pullspec, fakeDigestContent string) (string, error) {
	hasher := sha256.New()
	hasher.Write([]byte(fakeDigestContent))
	fakeDigest := fmt.Sprintf("sha256:%x", hasher.Sum(nil))

	return utils.ParseImagePullspec(pullspec, fakeDigest)
}

// Determines if a given test which depends on real creds can be run.
func skipIfUnableToRun(t *testing.T) {
	if realImageRegistrySecretPath != "" && realImagePullspec != "" {
		t.Logf("Test suite invoked with -image-registry-secret %q and -image-pullspec %q, will perform full image registry test", realImageRegistrySecretPath, realImagePullspec)
	} else {
		t.Skip("-image-registry-secret and -image-pullspec flags unset")
	}
}

// Creates a new imagepruner instance with empty creds.
func setupImagePrunerForTestWithEmptyCreds(t *testing.T) (imagepruner.ImagePruner, *corev1.Secret, error) {
	// Write an "empty" creds file since we don't actually need creds for this test.
	authfilePath := filepath.Join(t.TempDir(), "authfile.json")
	if err := os.WriteFile(authfilePath, []byte(`{"auths":{}}`), 0o755); err != nil {
		return nil, nil, err
	}

	return setupImagePrunerForTest(authfilePath)
}

// Creates a new imagepruner with populated creds from the given path.
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

// Unwraps all of the errors in a given error chain and calls spew.Sdump() on
// each one to get rich type information for debugging purporses.
func unwrapAll(err error) string {
	// The function should handle a nil error gracefully.
	if err == nil {
		return ""
	}

	out := []string{}

	// Loop indefinitely until an error can no longer be unwrapped.
	for {
		// Attempt to unwrap the current error.
		unwrapped := errors.Unwrap(err)

		out = append(out, spew.Sdump(err))

		// If unwrapped is nil, we've reached the innermost error.
		if unwrapped == nil {
			return strings.Join(out, "\n")
		}

		// If unwrapped is not nil, continue the loop with the newly unwrapped error.
		err = unwrapped
	}
}

// Adds the external image registry hostname to the given secret and writes it
// to the given path as a DockerConfigJSON secret.
func addExternalRegistryHostnameToSecret(internalRegistryHostname, externalRegistryHostname string, secret *corev1.Secret, secretPath string) (*corev1.Secret, error) {
	is, err := secrets.NewImageRegistrySecret(secret)
	if err != nil {
		return nil, err
	}

	dockerconfigJSON := is.DockerConfigJSON()
	if _, ok := dockerconfigJSON.Auths[internalRegistryHostname]; !ok {
		return nil, fmt.Errorf("secret %s missing internal registry hostname %s", secret.Name, internalRegistryHostname)
	}

	dockerconfigJSON.Auths[externalRegistryHostname] = dockerconfigJSON.Auths[internalRegistryHostname]

	is, err = secrets.NewImageRegistrySecret(dockerconfigJSON)
	if err != nil {
		return nil, err
	}

	k8sSecret, err := is.K8sSecret(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return nil, err
	}

	out := secret.DeepCopy()
	out.Data = k8sSecret.Data

	secretBytes, err := is.JSONBytes(corev1.SecretTypeDockerConfigJson)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(secretPath, secretBytes, 0o644); err != nil {
		return nil, err
	}

	return out, nil
}

// Determines if a test can target the internal cluster image registry.
func canTestOnInClusterRegistry(ctx context.Context, kubeconfig string) (bool, error) {
	cs, err := framework.NewClientSetOrError(kubeconfig)
	if err != nil {
		return false, fmt.Errorf("could not get clientset: %w", err)
	}

	cv, err := cs.ConfigV1Interface.ClusterVersions().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("could not list clusterversions: %w", err)
	}

	for _, cv := range cv.Items {
		for _, capability := range cv.Status.Capabilities.EnabledCapabilities {
			if capability == "ImageRegistry" {
				return true, nil
			}
		}
	}

	return false, nil
}

// Skopeo requires that a policy.json file be present. Usually, this file is
// placed in /etc/containers/policy.json when Skopeo is installed. Because we
// must install skopeo from source in CI, this file is missing. So what we do
// in this scenario is write our own policy.json file to a temp directory
// instead. The temp directory is managed by the Go test suite and will be
// removed after the test is finished.
func writePolicyFile(t *testing.T) (string, error) {
	policyPath := filepath.Join(t.TempDir(), "policy.json")

	// Compacted contents of https://github.com/containers/skopeo/blob/main/default-policy.json
	policyJSONBytes := []byte(`{"default":[{"type":"insecureAcceptAnything"}],"transports":{"docker-daemon":{"":[{"type":"insecureAcceptAnything"}]}}}`)

	return policyPath, os.WriteFile(policyPath, policyJSONBytes, 0o755)
}

// Creates an empty scratch image and pushes it to the given pullspec using the
// provided secret path. Accepts an optional certsDir parameter which is
// particularly useful for pushing internal image registries which have
// self-signed certificates.
func createAndPushScratchImage(ctx context.Context, t *testing.T, pullspec, secretPath, certsDir string) error {
	tmpDir := t.TempDir()

	srcImage := filepath.Join(tmpDir, helpers.ImageTarballFilename)

	if err := helpers.CreateScratchImageTarball(tmpDir); err != nil {
		return err
	}

	policyPath, err := writePolicyFile(t)
	if err != nil {
		return fmt.Errorf("could not write policy.json file: %w", err)
	}

	cmd := exec.Command("skopeo", "--policy", policyPath, "copy", "--dest-authfile", secretPath, "tarball://"+srcImage, "docker://"+pullspec)
	if certsDir != "" {
		cmd = exec.Command("skopeo", "--policy", policyPath, "copy", "--dest-cert-dir", certsDir, "--dest-authfile", secretPath, "tarball://"+srcImage, "docker://"+pullspec)
	}

	t.Logf("Copying %s to %s using skopeo", srcImage, pullspec)
	t.Logf("%v", cmd.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
