package e2e_2of2

import (
	"archive/tar"
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/containers/common/pkg/retry"
	"github.com/containers/image/v5/types"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/machine-config-operator/pkg/imageutils"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testRetryOpts = retry.Options{MaxRetry: 2}

func releaseManifestsMatcher(header *tar.Header) bool {
	return strings.TrimLeft(header.Name, "./") == "release-manifests/image-references"
}

// setupSysContext creates and returns a SysContext from the cluster's controller config.
// The caller is responsible for calling Cleanup() on the returned SysContext.
func setupSysContext(t *testing.T, cs *framework.ClientSet) *imageutils.SysContext {
	t.Helper()

	controllerConfig, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, controllerConfig.Spec.PullSecret)

	secret, err := cs.Secrets(controllerConfig.Spec.PullSecret.Namespace).Get(context.TODO(), controllerConfig.Spec.PullSecret.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, secret)

	sysContext, err := imageutils.NewSysContextFromControllerConfig(secret, controllerConfig)
	require.NoError(t, err)
	require.NotNil(t, sysContext)

	return sysContext
}

// getMCOImageReference retrieves the machine-config-operator container image from the MCO deployment.
func getMCOImageReference(t *testing.T, cs *framework.ClientSet) string {
	t.Helper()

	mcoDeployment, err := cs.Deployments("openshift-machine-config-operator").Get(context.TODO(), "machine-config-operator", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, mcoDeployment)

	for _, container := range mcoDeployment.Spec.Template.Spec.Containers {
		if container.Name == "machine-config-operator" {
			return container.Image
		}
	}

	require.FailNow(t, "machine-config-operator container not found in deployment")
	return "" // unreachable, but keeps compiler happy
}

// getMCOImage retrieves the MCO container image with retry logic and performs standard assertions.
// The caller is responsible for closing the returned ImageSource.
func getMCOImage(t *testing.T, ctx context.Context, sysCtx *types.SystemContext, cs *framework.ClientSet) (types.Image, types.ImageSource) {
	t.Helper()

	image, source, err := imageutils.GetImage(ctx, sysCtx, getMCOImageReference(t, cs), &testRetryOpts)
	require.NoError(t, err)
	require.NotNil(t, source)
	require.NotNil(t, image)

	return image, source
}

// getControllerConfigImages retrieves the deduplicated list of container images from the
// machine-config-controller ControllerConfig, returning them in sorted order.
func getControllerConfigImages(t *testing.T, cs *framework.ClientSet) []string {
	t.Helper()

	controllerConfig, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	require.NoError(t, err)
	// Remove duplicate images
	ccImages := make(map[string]struct{})
	for _, image := range controllerConfig.Spec.Images {
		ccImages[image] = struct{}{}
	}
	return slices.Sorted(maps.Keys(ccImages))
}

// TestReadImageFileContent validates the ability to extract a specific file from a container image.
// It tests the ReadImageFileContent function by retrieving the image-references manifest from
// the cluster's payload image.
func TestReadImageFileContent(t *testing.T) {
	cs := framework.NewClientSet("")
	sysContext := setupSysContext(t, cs)
	defer func() {
		require.NoError(t, sysContext.Cleanup())
	}()

	clusterVersion, err := cs.ClusterVersions().Get(context.TODO(), "version", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, clusterVersion)

	timedCtx, timedCtxCancelFn := context.WithTimeout(context.Background(), time.Minute)
	defer timedCtxCancelFn()
	content, err := imageutils.ReadImageFileContent(timedCtx, sysContext.SysContext, clusterVersion.Status.Desired.Image, releaseManifestsMatcher, nil)
	require.NoError(t, err)

	// Note: The test file is a file used in the MCO to fetch OSImageStreams, and it's
	// used here only as an example. This test should limit itself to the image
	// manipulation logic to grab a file from an image reference.
	// The file points to the Payload ImageStream
	// Note: If at some point in the future the file doesn't exist pick
	// any other file in that image (or change the image too). The specific file
	// to test against is not important.
	require.NotNil(t, content, "the Payload Image must contain the image-references file")
	imageStream := resourceread.ReadImageStreamV1OrDie(content)
	require.NotNil(t, imageStream)
}

// TestGetImage validates the ability to fetch and inspect a container image.
// It tests the GetImage function by retrieving the MCO container image from the cluster,
// ensuring both the image and its source are properly initialized.
func TestGetImage(t *testing.T) {
	cs := framework.NewClientSet("")
	sysContext := setupSysContext(t, cs)
	defer func() {
		require.NoError(t, sysContext.Cleanup())
	}()

	timedCtx, timedCtxCancelFn := context.WithTimeout(context.Background(), time.Minute)
	defer timedCtxCancelFn()

	image, source := getMCOImage(t, timedCtx, sysContext.SysContext, cs)
	defer func() {
		require.NoError(t, source.Close())
	}()
	require.NotNil(t, image)
	require.NotNil(t, source)
}

// TestGetImageHelpers validates the image inspection helper functions.
// It tests GetDigestFromImage and GetInspectInfoFromImage by retrieving the MCO image's
// digest and metadata, ensuring the returned information is valid and complete.
func TestGetImageHelpers(t *testing.T) {
	cs := framework.NewClientSet("")
	sysContext := setupSysContext(t, cs)
	defer func() {
		require.NoError(t, sysContext.Cleanup())
	}()

	timedCtx, timedCtxCancelFn := context.WithTimeout(context.Background(), time.Minute)
	defer timedCtxCancelFn()

	image, source := getMCOImage(t, timedCtx, sysContext.SysContext, cs)
	defer func() {
		require.NoError(t, source.Close())
	}()

	digest, err := imageutils.GetDigestFromImage(timedCtx, image, &testRetryOpts)
	require.NoError(t, err)
	require.NotEmpty(t, digest)

	inspectInfo, err := imageutils.GetInspectInfoFromImage(timedCtx, image, &testRetryOpts)
	require.NoError(t, err)
	require.NotNil(t, inspectInfo)

	// Perform a simple assertion to ensure the inspect info is not empty
	require.NotEmpty(t, inspectInfo.Labels)

	// Test known label
	label, ok := inspectInfo.Labels["io.openshift.release.operator"]
	require.True(t, ok)
	require.Equal(t, "true", strings.ToLower(label))
}

// TestBulkInspector validates the BulkInspector functionality for concurrent image inspection.
// It tests the NewBulkInspector and Inspect methods across multiple scenarios including
// default behavior with unlimited concurrency, rate-limited inspection, error handling with
// FailOnErr=false (soft errors), and FailOnErr=true (hard errors with early termination).
func TestBulkInspector(t *testing.T) {
	// Common setup for all subtests
	cs := framework.NewClientSet("")
	sysContext := setupSysContext(t, cs)
	defer func() {
		require.NoError(t, sysContext.Cleanup())
	}()

	testImages := getControllerConfigImages(t, cs)
	require.NotEmpty(t, testImages, "ControllerConfig requires to have some images to test the BulkInspector")

	t.Run("Default", func(t *testing.T) {
		timedCtx, timedCtxCancelFn := context.WithTimeout(context.Background(), time.Minute)
		defer timedCtxCancelFn()

		bulkInspector := imageutils.NewBulkInspector(nil)
		results, err := bulkInspector.Inspect(timedCtx, sysContext.SysContext, testImages...)
		require.NoError(t, err)
		require.Equal(t, len(testImages), len(results))

		resultsByImage := make(map[string]imageutils.BulkInspectResult)
		for _, result := range results {
			resultsByImage[result.Image] = result
		}

		// Test that all images were inspected
		for _, image := range testImages {
			result, found := resultsByImage[image]
			require.True(t, found, "image %s not found in results", image)
			require.NoError(t, result.Error)
			require.NotNil(t, result.InspectInfo)
		}
	})

	t.Run("RateLimited", func(t *testing.T) {
		timedCtx, timedCtxCancelFn := context.WithTimeout(context.Background(), time.Minute)
		defer timedCtxCancelFn()

		// This inspection is slower, test with fewer images
		caseImages := testImages[:min(1, len(testImages))]
		bulkInspector := imageutils.NewBulkInspector(&imageutils.BulkInspectorOptions{Count: 1, RetryOpts: &testRetryOpts})
		results, err := bulkInspector.Inspect(timedCtx, sysContext.SysContext, caseImages...)
		require.NoError(t, err)
		require.Equal(t, len(caseImages), len(results))

		resultsByImage := make(map[string]imageutils.BulkInspectResult)
		for _, result := range results {
			resultsByImage[result.Image] = result
		}

		// Test that all images were inspected
		for _, image := range caseImages {
			result, found := resultsByImage[image]
			require.True(t, found, "image %s not found in results", image)
			require.NoError(t, result.Error)
			require.NotNil(t, result.InspectInfo)
		}
	})

	t.Run("DefaultSoftError", func(t *testing.T) {
		timedCtx, timedCtxCancelFn := context.WithTimeout(context.Background(), time.Minute)
		defer timedCtxCancelFn()

		// Append an image that will make the BulkInspector report and error
		invalidImage := testImages[0] + "0"
		caseImages := append(slices.Clone(testImages), invalidImage)

		bulkInspector := imageutils.NewBulkInspector(nil)
		results, err := bulkInspector.Inspect(timedCtx, sysContext.SysContext, caseImages...)
		require.NoError(t, err)
		require.Equal(t, len(caseImages), len(results))

		resultsByImage := make(map[string]imageutils.BulkInspectResult)
		for _, result := range results {
			resultsByImage[result.Image] = result
		}

		// Test that all images were inspected
		for _, image := range caseImages {
			result, found := resultsByImage[image]
			require.True(t, found, "image %s not found in results", image)
			if image == invalidImage {
				require.Error(t, result.Error)
			} else {
				require.NoError(t, result.Error)
				require.NotNil(t, result.InspectInfo)
			}

		}
	})

	t.Run("FailOnErr", func(t *testing.T) {
		timedCtx, timedCtxCancelFn := context.WithTimeout(context.Background(), time.Minute)
		defer timedCtxCancelFn()

		// Append an image that will make it fail
		failImages := append(slices.Clone(testImages), testImages[0]+"0")

		bulkInspector := imageutils.NewBulkInspector(&imageutils.BulkInspectorOptions{FailOnErr: true, Count: 0})
		results, err := bulkInspector.Inspect(timedCtx, sysContext.SysContext, failImages...)
		require.Nil(t, results)
		require.Error(t, err)
	})
}
