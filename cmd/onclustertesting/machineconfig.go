package main

import (
	"context"
	"fmt"
	"path/filepath"

	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
)

var (
	machineConfigCmd = &cobra.Command{
		Use:   "machineconfig",
		Short: "Creates a MachineConfig in a layered MachineConfigPool to cause a build",
		Long:  "",
		Run:   runMachineConfigCmd,
	}

	machineConfigOpts struct {
		poolName      string
		machineConfig string
	}
)

func init() {
	rootCmd.AddCommand(machineConfigCmd)
	machineConfigCmd.PersistentFlags().StringVar(&machineConfigOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to target")
	machineConfigCmd.PersistentFlags().StringVar(&machineConfigOpts.machineConfig, "machineconfig", "", "MachineConfig name to create")
}

func runMachineConfigCmd(_ *cobra.Command, _ []string) {
	common(machineConfigOpts)

	if extractOpts.poolName == "" {
		klog.Fatalln("No pool name provided!")
	}

	cs := framework.NewClientSet("")

	failOnError(createMachineConfig(cs, machineConfigOpts.poolName, machineConfigOpts.machineConfig))
}

func createMachineConfig(cs *framework.ClientSet, targetPool, name string) error {
	_, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if name == "" {
		name = fmt.Sprintf("%s-%s", targetPool, uuid.NewUUID())
	}

	mc := helpers.NewMachineConfig(name, helpers.MCLabelForRole(targetPool), "", []ign3types.File{
		helpers.CreateEncodedIgn3File(filepath.Join("/etc", name), name, 420),
	})

	_, err = cs.MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	klog.Infof("Created MachineConfig %q targeting pool %q", name, targetPool)

	renderedConfig, err := waitForRenderedConfigs(cs, targetPool, name)
	if err != nil {
		return err
	}

	klog.Infof("MachineConfigPool %s got rendered config %q", targetPool, renderedConfig)

	return nil
}
