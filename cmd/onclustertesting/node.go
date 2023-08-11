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

	optOutCmd = &cobra.Command{
		Use:   "optout",
		Short: "Opts a node out of on-cluster builds",
		Long:  "",
		Run:   runOptOutCmd,
	}

	nodeOpts struct {
		poolName string
		nodeName string
		force    bool
	}
)

func init() {
	rootCmd.AddCommand(optInCmd)
	optInCmd.PersistentFlags().StringVar(&nodeOpts.poolName, "pool", defaultLayeredPoolName, "Pool name")
	optInCmd.PersistentFlags().StringVar(&nodeOpts.nodeName, "node", "", "MachineConfig name")

	rootCmd.AddCommand(optOutCmd)
	optOutCmd.PersistentFlags().StringVar(&nodeOpts.poolName, "pool", defaultLayeredPoolName, "Pool name")
	optOutCmd.PersistentFlags().StringVar(&nodeOpts.nodeName, "node", "", "MachineConfig name")
	optOutCmd.PersistentFlags().BoolVar(&nodeOpts.force, "force", false, "Forcefully opt node out")
}

func runOptInCmd(_ *cobra.Command, _ []string) {
	common(nodeOpts)

	if isEmpty(nodeOpts.poolName) {
		klog.Fatalln("No pool name provided!")
	}

	if isEmpty(nodeOpts.nodeName) {
		klog.Fatalln("No node name provided!")
	}

	failOnError(optInNode(framework.NewClientSet(""), nodeOpts.nodeName, nodeOpts.poolName))
}

func runOptOutCmd(_ *cobra.Command, _ []string) {
	common(nodeOpts)

	if !nodeOpts.force && isEmpty(nodeOpts.poolName) {
		klog.Fatalln("No pool name provided!")
	}

	if isEmpty(nodeOpts.nodeName) {
		klog.Fatalln("No node name provided!")
	}

	failOnError(optOutNode(framework.NewClientSet(""), nodeOpts.nodeName, nodeOpts.poolName, nodeOpts.force))
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

func optOutNode(cs *framework.ClientSet, nodeName, poolName string, force bool) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		workerMCP, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
		if err != nil {
			return err
		}

		if force {
			klog.Infof("Forcefully opting node %q out of layering", node.Name)
			return resetNodeAnnotationsAndLabels(cs, workerMCP, node)
		}

		role := helpers.MCPNameToRole(poolName)

		if _, ok := node.Labels[role]; !ok {
			return fmt.Errorf("node %q does not have a label matching %q", node.Name, role)
		}

		delete(node.Labels, role)

		_, err = cs.CoreV1Interface.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
		if err == nil {
			klog.Infof("Opted node %q out of on-cluster builds", node.Name)
		}

		return err
	})
}
