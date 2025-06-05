package extended

import (
	"context"
	"fmt"
	"strings"
	"time"

	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"

	o "github.com/onsi/gomega"
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

// SetMCOTemplate set a template defined in the MCO testdata folder
func (mc *MachineConfig) SetMCOTemplate(templateName string) *MachineConfig {
	mc.Template = *NewMCOTemplate(mc.oc, templateName)
	return mc
}

// SetParams set parameters defined in template
func (mc *MachineConfig) SetParams(params ...string) *MachineConfig {
	if len(params) > 0 {
		mc.parameters = append(mc.parameters, params...)
	}
	return mc
}

func (mc *MachineConfig) create() {
	mc.name = mc.name + "-" + exutil.GetRandomString()
	params := []string{"-p", "NAME=" + mc.name, "POOL=" + mc.pool}
	params = append(params, mc.parameters...)
	mc.Create(params...)

	immediate := false
	pollerr := wait.PollUntilContextTimeout(context.TODO(), 5*time.Second, 1*time.Minute, immediate, func(_ context.Context) (bool, error) {
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

func (mc *MachineConfig) delete() {
	// This method waits a minimum of 1 minute for the MCP to be updated after the MC has been deleted.
	// It is very expensive, since this method is deferred very often and in those cases the MC has been already deleted.
	// In order to improve the performance we do nothing if the MC does not exist.
	if !mc.Exists() {
		logger.Infof("MachineConfig %s does not exist. We will not try to delete it.", mc.GetName())
		return
	}

	mcp := NewMachineConfigPool(mc.oc, mc.pool)
	if mc.GetKernelTypeSafe() != "" {
		mcp.SetWaitingTimeForKernelChange() // If the MC is configuring a different kernel, we increase the waiting period
	}

	err := mc.oc.AsAdmin().WithoutNamespace().Run("delete").Args("mc", mc.name, "--ignore-not-found=true").Execute()
	o.Expect(err).NotTo(o.HaveOccurred())

	mcp.waitForComplete()
}

// GetExtensions returns all the extensions configured in this MC
func (mc *MachineConfig) GetExtensions() (string, error) {
	return mc.Get(`{.spec.extensions}`)
}

// GetAuthorizedKeysByUser returns the authorizedkeys that this MC defines for the given user in a json list format
func (mc *MachineConfig) GetAuthorizedKeysByUser(user string) (string, error) {
	return mc.Get(fmt.Sprintf(`{.spec.config.passwd.users[?(@.name=="%s")].sshAuthorizedKeys}`, user))
}

// Get the kernelType configured in this MC. If any arror happens it returns an empty string
func (mc *MachineConfig) GetKernelTypeSafe() string {
	return mc.GetSafe(`{.spec.kernelType}`, "")
}

// HasExtensionsSafe  returns true if the MC has any extension configured
func (mc *MachineConfig) HasExtensionsSafe() bool {
	ext := mc.GetSafe(`{.spec.extensions}`, "[]")
	return ext != "[]" && ext != ""
}

// GetAuthorizedKeysByUserAsList returns the authorizedkeys that this MC defines for the given user as a list of strings
func (mc *MachineConfig) GetAuthorizedKeysByUserAsList(user string) ([]string, error) {
	listKeys := []string{}

	keys, err := mc.Get(fmt.Sprintf(`{.spec.config.passwd.users[?(@.name=="%s")].sshAuthorizedKeys}`, user))
	if err != nil {
		return nil, err
	}

	if keys == "" {
		return listKeys, nil
	}

	jKeys := JSON(keys)
	for _, key := range jKeys.Items() {
		listKeys = append(listKeys, key.ToString())
	}

	return listKeys, err
}

// GetIgnitionVersion returns the ignition version used in the MC
func (mc *MachineConfig) GetIgnitionVersion() (string, error) {
	return mc.Get(`{.spec.config.ignition.version}`)
}

// GetAll returns a []MachineConfig list with all existing MCs
func (mcl *MachineConfigList) GetAll() ([]MachineConfig, error) {
	allMCResources, err := mcl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMCs := make([]MachineConfig, 0, len(allMCResources))

	for _, item := range allMCResources {
		mcRes := item
		// disable the log spam while getting the MCs' "pool"
		mcRes.oc.NotShowInfo()
		defer mcRes.oc.SetShowInfo()

		allMCs = append(allMCs,
			*NewMachineConfig(mcl.oc,
				mcRes.name,
				// TODO: why do we have to provide the pool in when constructing a MC.
				// the pool is actually a label and there are machineconfigs without pool, it should not be mandatory
				mcRes.GetOrFail(`{.metadata.labels.machineconfiguration\.openshift\.io/role}`)))
	}

	return allMCs, nil
}

// GetMachineConfigCreatedByMCPs returns a list of the machineconfigs that were created by a MCP
func (mcl *MachineConfigList) GetMCPRenderedMachineConfigs() ([]MachineConfig, error) {
	mcl.SetItemsFilter(`?(@.metadata.ownerReferences[0].kind=="MachineConfigPool")`)
	return mcl.GetAll()
}

// GetMachineConfigsWithNameStartingWithRender returns a list with all the MCs  whose name starts with "render-"
func (mcl *MachineConfigList) GetMachineConfigsWithNameStartingWithRender() ([]MachineConfig, error) {
	allMCs, err := mcl.GetAll()
	if err != nil {
		return nil, err
	}

	returnMCs := []MachineConfig{}

	for _, mc := range allMCs {
		if strings.HasPrefix(mc.GetName(), "rendered-") {
			returnMCs = append(returnMCs, mc)
		}
	}

	return returnMCs, nil
}

// GetRenderedMachineConfigForMaster returns a list with all the MCs  whose name starts with "render-master"
func (mcl *MachineConfigList) GetRenderedMachineConfigForMaster() ([]MachineConfig, error) {
	mcl.SetItemsFilter(`?(@.metadata.ownerReferences[0].name=="master")`)
	allMCs, err := mcl.GetAll()
	if err != nil {
		return nil, err
	}

	returnMCs := []MachineConfig{}

	for _, mc := range allMCs {
		if strings.HasPrefix(mc.GetName(), "rendered-master") {
			returnMCs = append(returnMCs, mc)
		}
	}

	return returnMCs, nil
}
func (mcl *MachineConfigList) GetRenderedMachineConfigForMasterOrFail() []MachineConfig {
	renderedMcMasterList, err := mcl.GetRenderedMachineConfigForMaster()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the list of the machineconfigs that were created by a MCP ")
	return renderedMcMasterList

}

// GetMachineConfigCreatedByMCPs returns a list of the machineconfigs that were created by a MCP
func (mcl *MachineConfigList) GetMCPRenderedMachineConfigsOrFail() []MachineConfig {
	renderedMcList, err := mcl.GetRenderedMachineConfigForMaster()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the list of the machineconfigs that were created by a MCP ")
	return renderedMcList
}

// GetRenderedMachineConfigForWorker returns a list with all the MCs  whose name starts with "render-worker"
func (mcl *MachineConfigList) GetRenderedMachineConfigForWorker() ([]MachineConfig, error) {
	mcl.SetItemsFilter(`?(@.metadata.ownerReferences[0].name=="worker")`)
	allMCs, err := mcl.GetAll()
	if err != nil {
		return nil, err
	}

	returnMCs := []MachineConfig{}

	for _, mc := range allMCs {
		if strings.HasPrefix(mc.GetName(), "rendered-worker") {
			returnMCs = append(returnMCs, mc)
		}
	}

	return returnMCs, nil
}
func (mcl *MachineConfigList) GetRenderedMachineConfigForWorkerOrFail() []MachineConfig {
	renderedMcWorkerList, err := mcl.GetRenderedMachineConfigForWorker()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the list of the machineconfigs that were created by a MCP ")
	return renderedMcWorkerList

}
