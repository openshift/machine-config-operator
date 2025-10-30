package extended

import (
	"context"
	"fmt"
	"strings"
	"time"

	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"

	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// MachineConfigList handles list of nodes
type MachineConfigList struct {
	ResourceList
}

// MachineConfig struct is used to handle MachineConfig resources in OCP
type MachineConfig struct {
	Resource
	Template
	pool           string
	parameters     []string
	skipWaitForMcp bool
}

// NewMachineConfig create a NewMachineConfig struct
func NewMachineConfig(oc *exutil.CLI, name, pool string) *MachineConfig {
	mc := &MachineConfig{Resource: *NewResource(oc, "mc", name), pool: pool}
	return mc.SetTemplate(*NewMCOTemplate(oc, GenericMCTemplate))
}

// SetTemplate sets the template that will be used by the "create" method in order to create the MC
func (mc *MachineConfig) SetTemplate(template Template) *MachineConfig {
	mc.Template = template
	return mc
}

func (mc *MachineConfig) create() {
	mc.name = mc.name + "-" + exutil.GetRandomString()
	params := []string{"-p", "NAME=" + mc.name, "POOL=" + mc.pool}
	params = append(params, mc.parameters...)
	mc.Create(params...)

	pollerr := wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 1*time.Minute, false, func(_ context.Context) (bool, error) {
		stdout, err := mc.Get(`{.metadata.name}`)
		if err != nil {
			logger.Errorf("the err:%v, and try next round", err)
			return false, nil
		}
		if strings.Contains(stdout, mc.name) {
			logger.Infof("mc %s is created successfully", mc.name)
			return true, nil
		}
		return false, nil
	})
	exutil.AssertWaitPollNoErr(pollerr, fmt.Sprintf("create machine config %v failed", mc.name))

	if !mc.skipWaitForMcp {
		mcp := NewMachineConfigPool(mc.oc, mc.pool)
		if mc.GetKernelTypeSafe() != "" {
			mcp.SetWaitingTimeForKernelChange() // Since we configure a different kernel we wait longer for completion
		}

		if mc.HasExtensionsSafe() {
			mcp.SetWaitingTimeForExtensionsChange() // Since we configure extra extension we need to wait longer for completion
		}
		mcp.waitForComplete()
	}
}

// we need this method to be able to delete the MC without waiting for success.
// TODO: This method should be deleted when we refactor the MC struct to embed the Resource struct. But right now we have no other choice.
func (mc *MachineConfig) deleteNoWait() error {
	return mc.Delete()
}

// GetKernelTypeSafe Get the kernelType configured in this MC. If any arror happens it returns an empty string
func (mc *MachineConfig) GetKernelTypeSafe() string {
	return mc.GetSafe(`{.spec.kernelType}`, "")
}

// HasExtensionsSafe  returns true if the MC has any extension configured
func (mc *MachineConfig) HasExtensionsSafe() bool {
	ext := mc.GetSafe(`{.spec.extensions}`, "[]")
	return ext != "[]" && ext != ""
}

// `ApplyMachineConfigFixtureOriginPort` applies a MachineConfig fixture
// TODO (MCO-1960): Replace this function ported from o/origin with a standardized helper.
func ApplyMachineConfigFixtureOriginPort(oc *exutil.CLI, fixture string) error {
	err := NewMCOTemplate(oc, fixture).Apply()
	return err
}

// `DeleteMCByNameOriginPort` deletes the MC with the provided name if it exists in the cluster
// TODO (MCO-1960): Replace this function ported from o/origin with a standardized helper.
func DeleteMCByNameOriginPort(oc *exutil.CLI, machineConfigClient *machineconfigclient.Clientset, mcName string) (mcDeleted bool, err error) {
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
	deleteMCErr := oc.Run("delete").Args("machineconfig", mcName).Execute()
	return true, deleteMCErr
}
