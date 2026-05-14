//go:build iri_delete

package e2e_iri_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestIRIController_IRIDelete(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Verify IRI and its master MachineConfig exist before deletion.
	_, err := cs.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err, "IRI should exist before deletion")

	_, err = cs.MachineConfigs().Get(ctx, "02-master-internalreleaseimage", v1.GetOptions{})
	require.NoError(t, err, "Master MC should exist before deletion")

	// Remove the VAP binding that prevents IRI deletion.
	vapBindingName := "internalreleaseimage-deletion-guard-binding"
	err = cs.GetKubeclient().AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Delete(ctx, vapBindingName, v1.DeleteOptions{})
	require.NoError(t, err, "Should be able to delete VAP binding")

	// Delete the IRI resource.
	err = cs.InternalReleaseImages().Delete(ctx, "cluster", v1.DeleteOptions{})
	require.NoError(t, err, "Should be able to delete IRI after VAP binding removal")

	// Wait for the IRI to be fully garbage-collected (finalizer removed).
	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := cs.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	require.NoError(t, err, "IRI should be garbage-collected after deletion")

	// Verify master MC still exists with disabled content.
	masterMC, err := cs.MachineConfigs().Get(ctx, "02-master-internalreleaseimage", v1.GetOptions{})
	require.NoError(t, err, "Master MC should still exist after IRI deletion")

	masterIgn, err := ctrlcommon.ParseAndConvertConfig(masterMC.Spec.Config.Raw)
	require.NoError(t, err)
	require.Len(t, masterIgn.Systemd.Units, 1, "Master MC should have exactly one unit")
	require.Equal(t, constants.IRIRegistryServiceName, masterIgn.Systemd.Units[0].Name)
	require.Contains(t, *masterIgn.Systemd.Units[0].Contents, "ExecStart=/bin/true")
	require.Contains(t, *masterIgn.Systemd.Units[0].Contents, "Type=oneshot")
	require.NotContains(t, *masterIgn.Systemd.Units[0].Contents, "podman")
	require.Empty(t, masterIgn.Storage.Files, "Master MC should have no files when disabled")

	// Verify the registry port is no longer listening on a master node.
	node := helpers.GetRandomNode(t, cs, "master")
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		output, cmdErr := helpers.ExecCmdOnNodeWithError(cs, node, "bash", "-c",
			fmt.Sprintf("ss -tlnp | grep ':%d ' || echo PORT_CLOSED", ctrlcommon.IRIRegistryPort))
		if cmdErr != nil {
			return false, nil
		}
		return strings.Contains(output, "PORT_CLOSED"), nil
	})
	require.NoError(t, err, "Registry port should no longer be listening after IRI deletion")
}
