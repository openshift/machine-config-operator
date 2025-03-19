package legacycmds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ghodss/yaml"
	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
)

var (
	machineConfigOpts struct {
		poolName      string
		machineConfig string
		sshMC         bool
		dryRun        bool
	}
)

func MachineConfigCommand() *cobra.Command {
	machineConfigCmd := &cobra.Command{
		Use:   "machineconfig",
		Short: "Creates a MachineConfig in a layered MachineConfigPool to cause a build",
		Long:  "",
		RunE:  runMachineConfigCmd,
	}

	machineConfigCmd.PersistentFlags().StringVar(&machineConfigOpts.poolName, "pool", DefaultLayeredPoolName, "Pool name to target")
	machineConfigCmd.PersistentFlags().StringVar(&machineConfigOpts.machineConfig, "machineconfig", "", "MachineConfig name to create")
	machineConfigCmd.PersistentFlags().BoolVar(&machineConfigOpts.sshMC, "ssh-config", false, "Creates a MachineConfig that adds an SSH key to avoid reboots")
	machineConfigCmd.PersistentFlags().BoolVar(&machineConfigOpts.dryRun, "dry-run", false, "Dump the MachineConfig to stdout instead of applying it")

	return machineConfigCmd

}

func runMachineConfigCmd(_ *cobra.Command, _ []string) error {
	utils.ParseFlags()

	if extractOpts.poolName == "" {
		return fmt.Errorf("no pool name provided")
	}

	cs := framework.NewClientSet("")

	return createMachineConfig(cs, machineConfigOpts.poolName, machineConfigOpts.machineConfig)
}

func createMachineConfig(cs *framework.ClientSet, targetPool, name string) error {
	mc := getMachineConfig(machineConfigOpts.machineConfig, machineConfigOpts.poolName, machineConfigOpts.sshMC)
	mc.Labels = map[string]string{
		CreatedByOnClusterBuildsHelper: "",
	}

	if machineConfigOpts.dryRun {
		return dumpYAMLToStdout(mc)
	}

	_, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil {
		return err
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	klog.Infof("Created MachineConfig %q targeting pool %q", name, targetPool)

	renderedConfig, err := WaitForRenderedConfigs(cs, targetPool, name)
	if err != nil {
		return err
	}

	klog.Infof("MachineConfigPool %s got rendered config %q", targetPool, renderedConfig)

	return nil
}

func getMachineConfig(name, targetPool string, sshMC bool) *mcfgv1.MachineConfig {
	if name == "" {
		name = fmt.Sprintf("%s-%s", targetPool, uuid.NewUUID())
	}

	if !sshMC {
		return helpers.NewMachineConfig(name, helpers.MCLabelForRole(targetPool), "", []ign3types.File{
			helpers.CreateEncodedIgn3File(filepath.Join("/etc", name), name, 420),
		})
	}

	return getSSHMachineConfig(name, targetPool, string(uuid.NewUUID()))
}

func getSSHMachineConfig(mcName, mcpName, sshKeyContent string) *mcfgv1.MachineConfig {
	// Adding authorized key for user core
	testIgnConfig := ctrlcommon.NewIgnConfig()

	testIgnConfig.Passwd.Users = []ign3types.PasswdUser{
		{
			Name:              "core",
			SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{ign3types.SSHAuthorizedKey(sshKeyContent)},
		},
	}

	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   mcName,
			Labels: helpers.MCLabelForRole(mcpName),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(testIgnConfig),
			},
		},
	}
}

func dumpYAMLToStdout(in interface{}) error {
	out, err := yaml.Marshal(in)
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(out)
	return err
}
