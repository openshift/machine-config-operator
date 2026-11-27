package e2e_techpreview

import (
	"context"
	"fmt"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/osimagestream"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Tests that a given clusters' OSImageStream is configured correctly.
func TestOSImageStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cs := framework.NewClientSet("")

	testCases := []struct {
		name     string
		testFunc func(*testing.T, *mcfgv1alpha1.OSImageStream)
	}{
		{
			name: "OSImageStream has default stream",
			testFunc: func(t *testing.T, osis *mcfgv1alpha1.OSImageStream) {
				assert.NotEqual(t, osis.Status.DefaultStream, "", "Expected status.defaultStream not to be empty")
				defaultImageStream, err := osimagestream.GetOSImageStreamSetByName(osis, osis.Status.DefaultStream)
				assert.NoError(t, err)
				assert.NotNil(t, defaultImageStream)
			},
		},
		{
			name: "OSImageStream has available streams",
			testFunc: func(t *testing.T, osis *mcfgv1alpha1.OSImageStream) {
				expectedCount, targetVariant, err := getExpectedOSImageStreamCount(cs)
				require.NoError(t, err)

				assertOSImageStreamCount(t, osis, expectedCount, targetVariant)
			},
		},
		{
			name: "Default OSImageStream matches machine-config-osimageurl ConfigMap",
			testFunc: func(t *testing.T, osis *mcfgv1alpha1.OSImageStream) {
				cm, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "machine-config-osimageurl", metav1.GetOptions{})
				require.NoError(t, err)

				defaultImageStream, err := osimagestream.GetOSImageStreamSetByName(osis, osis.Status.DefaultStream)
				require.NoError(t, err)

				assert.Equal(t, cm.Data["baseOSContainerImage"], string(defaultImageStream.OSImage), "Default OS image should match configmap")
				assert.Equal(t, cm.Data["baseOSExtensionsContainerImage"], string(defaultImageStream.OSExtensionsImage), "Default extensions image should match configmap")
			},
		},
		{
			name: "Default OSImageStream matches rendered MachineConfigs and MachineConfigPools",
			testFunc: func(t *testing.T, osis *mcfgv1alpha1.OSImageStream) {
				mcps, err := cs.GetMcfgclient().MachineconfigurationV1().MachineConfigPools().List(ctx, metav1.ListOptions{})
				require.NoError(t, err)

				defaultImageStream, err := osimagestream.GetOSImageStreamSetByName(osis, osis.Status.DefaultStream)
				require.NoError(t, err)

				for _, mcp := range mcps.Items {
					assert.Equal(t, mcp.Spec.OSImageStream.Name, "", "MachineConfigPool %s osImageStream.name should be empty when default OSImageStream is in use")

					mc, err := cs.GetMcfgclient().MachineconfigurationV1().MachineConfigs().Get(ctx, mcp.Spec.Configuration.Name, metav1.GetOptions{})
					require.NoError(t, err)

					assert.Equal(t, mc.Spec.OSImageURL, string(defaultImageStream.OSImage), "MachineConfig %s osImageURL should match default OSImageStream", mc.Name)
					assert.Equal(t, mc.Spec.BaseOSExtensionsContainerImage, string(defaultImageStream.OSExtensionsImage), "MachineConfig %s baseOSExtensionsContainerImage image should match default OSImageStream", mc.Name)
				}
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			osisList, err := cs.GetMcfgclient().MachineconfigurationV1alpha1().OSImageStreams().List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, osisList.Items, 1)

			testCase.testFunc(t, &osisList.Items[0])
		})
	}
}

// This is a separate function because it could be reused in a lightweight
// nightly job that checks the release payload images without needing an active
// cluster.
func assertOSImageStreamCount(t *testing.T, osis *mcfgv1alpha1.OSImageStream, expectedCount int, targetVariant string) {
	t.Helper()

	msg := fmt.Sprintf("%s should have %d available stream(s), found %d with name(s): %v", targetVariant, expectedCount, len(osis.Status.AvailableStreams), osimagestream.GetStreamSetsNames(osis.Status.AvailableStreams))

	assert.Len(t, osis.Status.AvailableStreams, expectedCount, msg)
}

// This will not work correctly for the OKD case until
// https://issues.redhat.com/browse/MCO-2114 is resolved.
func getExpectedOSImageStreamCount(cs *framework.ClientSet) (int, string, error) {
	isOKD, err := helpers.IsOKDCluster(cs)
	if err != nil {
		return -1, "", fmt.Errorf("could not determine if target is an OKD cluster: %w", err)
	}

	if isOKD {
		return 1, "OKD", nil
	}

	return 2, "OCP", nil
}
