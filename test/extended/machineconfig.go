// TODO (MCO-1960): Deduplicate these functions with the helpers defined in /extended-priv/machineconfig.go.
package extended

import (
	"context"

	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	extpriv "github.com/openshift/machine-config-operator/test/extended-priv"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"

	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// `ApplyMachineConfigFixture` applies a MachineConfig fixture
func ApplyMachineConfigFixture(oc *exutil.CLI, fixture string) error {
	err := extpriv.NewMCOTemplate(oc, fixture).Apply()
	return err
}

// `DeleteMCByName` deletes the MC with the provided name if it exists in the cluster
func DeleteMCByName(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, mcName string) (mcDeleted bool, err error) {
	// Check if the MC still exists
	_, getMCErr := machineConfigClient.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), mcName, metav1.GetOptions{})
	if getMCErr != nil {
		// If the MC does not exist in the cluster, we have nothing to delete
		if apierrs.IsNotFound(getMCErr) {
			logger.Infof("MC `%s` does not exist so no deletion is necessary.", mcName)
			return false, nil
		}
		return false, getMCErr
	}

	// Delete the desired MC
	deleteMCErr := oc.AsAdmin().Run("delete").Args("machineconfig", mcName).Execute()
	return true, deleteMCErr
}
