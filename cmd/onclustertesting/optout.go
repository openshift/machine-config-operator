package main

import (
	"context"
	"fmt"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	optOutCmd = &cobra.Command{
		Use:   "optout",
		Short: "Opts a node out of on-cluster builds",
		Long:  "",
		Run:   runOptOutCmd,
	}

	optOutOpts struct {
		poolName string
		nodeName string
		force    bool
	}
)

func init() {
	rootCmd.AddCommand(optOutCmd)
	optOutCmd.PersistentFlags().StringVar(&optOutOpts.poolName, "pool", defaultLayeredPoolName, "Pool name")
	optOutCmd.PersistentFlags().StringVar(&optOutOpts.nodeName, "node", "", "MachineConfig name")
	optOutCmd.PersistentFlags().BoolVar(&optOutOpts.force, "force", false, "Forcefully opt node out")
}

func runOptOutCmd(_ *cobra.Command, _ []string) {
	common(optOutOpts)

	if !optOutOpts.force && isEmpty(optOutOpts.poolName) {
		klog.Fatalln("No pool name provided!")
	}

	if isEmpty(optOutOpts.nodeName) {
		klog.Fatalln("No node name provided!")
	}

	failOnError(optOutNode(framework.NewClientSet(""), optOutOpts.nodeName, optOutOpts.poolName, optOutOpts.force))
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
