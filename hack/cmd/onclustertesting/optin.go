package main

import (
	"context"
	"fmt"

	"github.com/openshift/machine-config-operator/hack/internal/pkg/utils"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

type optInAndOutOpts struct {
	poolName string
	nodeName string
	force    bool
}

func init() {
	optInOpts := optInAndOutOpts{}

	optInCmd := &cobra.Command{
		Use:   "optin",
		Short: "Opts a node into on-cluster builds",
		Long:  "",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runOptInCmd(optInOpts)
		},
	}

	optInCmd.PersistentFlags().StringVar(&optInOpts.poolName, "pool", defaultLayeredPoolName, "Pool name")
	optInCmd.PersistentFlags().StringVar(&optInOpts.nodeName, "node", "", "Node name")

	rootCmd.AddCommand(optInCmd)
}

func runOptInCmd(opts optInAndOutOpts) error {
	utils.ParseFlags()

	if isEmpty(opts.poolName) {
		return fmt.Errorf("no pool name provided")
	}

	if isEmpty(opts.nodeName) {
		return fmt.Errorf("no node name provided")
	}

	return optInNode(framework.NewClientSet(""), opts.nodeName, opts.poolName)
}

func optInNode(cs *framework.ClientSet, nodeName, targetPool string) error {
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil {
		return err
	}

	klog.Infof("Found pool %q", targetPool)

	mosc, err := getMachineOSConfigForPool(cs, mcp)
	if err != nil {
		return err
	}

	klog.Infof("Found MachineOSConfig %s for pool", mosc.Name)

	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Found node %q", nodeName)

		invalidNodeRoles := []string{
			helpers.MCPNameToRole("master"),
			helpers.MCPNameToRole("control-plane"),
		}

		for _, invalidNodeRole := range invalidNodeRoles {
			if _, ok := node.Labels[invalidNodeRole]; ok {
				return fmt.Errorf("cannot opt node with role %q into layering", invalidNodeRole)
			}
		}

		if _, ok := node.Labels[helpers.MCPNameToRole(targetPool)]; ok {
			return fmt.Errorf("node %q already has label %s", node.Name, helpers.MCPNameToRole(targetPool))
		}

		node.Labels[helpers.MCPNameToRole(targetPool)] = ""

		_, err = cs.CoreV1Interface.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
		if err == nil {
			klog.Infof("Node %q opted into layering via pool %q", node.Name, mcp.Name)
		}
		return err
	})
}
