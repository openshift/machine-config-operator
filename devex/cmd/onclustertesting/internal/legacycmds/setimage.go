package legacycmds

import (
	"context"
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	setImageOpts struct {
		poolName  string
		imageName string
	}
)

func SetImageCommand() *cobra.Command {
	setImageCmd := &cobra.Command{
		Use:   "set-image",
		Short: "Sets an image pullspec on a MachineConfigPool",
		Long:  "",
		RunE:  runSetImageCmd,
	}

	setImageCmd.PersistentFlags().StringVar(&setImageOpts.poolName, "pool", DefaultLayeredPoolName, "Pool name to set build status on")
	setImageCmd.PersistentFlags().StringVar(&setImageOpts.imageName, "image", "", "The image pullspec to set")

	return setImageCmd
}

func runSetImageCmd(_ *cobra.Command, _ []string) error {
	utils.ParseFlags()

	if setImageOpts.poolName == "" {
		return fmt.Errorf("no pool name provided")
	}

	if setImageOpts.imageName == "" {
		return fmt.Errorf("no image name provided")
	}

	return setImageOnPool(framework.NewClientSet(""), setImageOpts.poolName, setImageOpts.imageName)
}

func setImageOnPool(cs *framework.ClientSet, targetPool, pullspec string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := optInPool(cs, targetPool); err != nil {
			return err
		}

		return addImageToLayeredPool(cs, pullspec, targetPool)
	})
}

func addImageToLayeredPool(cs *framework.ClientSet, pullspec, targetPool string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if mcp.Labels == nil {
			if err := optInPool(cs, targetPool); err != nil {
				return err
			}
		}

		if mcp.Annotations == nil {
			mcp.Annotations = map[string]string{}
		}

		mcp.Annotations[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey] = pullspec
		mcp, err = cs.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Applied image %q to MachineConfigPool %s", pullspec, mcp.Name)
		return clearThenSetStatusOnPool(cs, targetPool, mcfgv1.MachineConfigPoolBuildSuccess, corev1.ConditionTrue)
	})
}

func clearThenSetStatusOnPool(cs *framework.ClientSet, targetPool string, condType mcfgv1.MachineConfigPoolConditionType, status corev1.ConditionStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := clearBuildStatusesOnPool(cs, targetPool); err != nil {
			return err
		}

		return setStatusOnPool(cs, targetPool, condType, status)
	})
}

func optInPool(cs *framework.ClientSet, targetPool string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if mcp.Labels == nil {
			mcp.Labels = map[string]string{}
		}

		mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

		klog.Infof("Opted MachineConfigPool %q into layering", mcp.Name)
		_, err = cs.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
		return err
	})
}
