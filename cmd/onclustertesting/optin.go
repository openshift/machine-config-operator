package main

import (
	"context"
	"fmt"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	optInCmd = &cobra.Command{
		Use:   "optin",
		Short: "Opts a node into on-cluster builds",
		Long:  "",
		Run:   runOptInCmd,
	}

	optInOpts struct {
		poolName string
		nodeName string
		force    bool
	}
)

func init() {
	rootCmd.AddCommand(optInCmd)
	optInCmd.PersistentFlags().StringVar(&optInOpts.poolName, "pool", defaultLayeredPoolName, "Pool name")
	optInCmd.PersistentFlags().StringVar(&optInOpts.nodeName, "node", "", "MachineConfig name")
}

func runOptInCmd(_ *cobra.Command, _ []string) {
	common(optInOpts)

	if isEmpty(optInOpts.poolName) {
		klog.Fatalln("No pool name provided!")
	}

	if isEmpty(optInOpts.nodeName) {
		klog.Fatalln("No node name provided!")
	}

	failOnError(optInNode(framework.NewClientSet(""), optInOpts.nodeName, optInOpts.poolName))
}

func optInNode(cs *framework.ClientSet, nodeName, targetPool string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Found pool %q", targetPool)

		if _, ok := mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel]; !ok {
			return fmt.Errorf("Pool %q is not opted into layering", mcp.Name)
		}

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
