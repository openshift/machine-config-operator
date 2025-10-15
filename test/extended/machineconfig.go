package extended

import (
	"context"
	"fmt"
	"strings"
	"time"

	exutil "github.com/openshift/origin/test/extended/util"
	compat_otp "github.com/openshift/origin/test/extended/util/compat_otp"
	logger "github.com/openshift/origin/test/extended/util/compat_otp/logext"

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
	mc.name = mc.name + "-" + compat_otp.GetRandomString()
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
	compat_otp.AssertWaitPollNoErr(pollerr, fmt.Sprintf("create machine config %v failed", mc.name))

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
