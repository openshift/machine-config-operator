package main

import (
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

var (
	setImageCmd = &cobra.Command{
		Use:   "set-image",
		Short: "Sets an image pullspec on a MachineConfigPool",
		Long:  "",
		Run:   runSetImageCmd,
	}

	setImageOpts struct {
		poolName  string
		imageName string
	}
)

func init() {
	rootCmd.AddCommand(setImageCmd)
	setImageCmd.PersistentFlags().StringVar(&setImageOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to set build status on")
	setImageCmd.PersistentFlags().StringVar(&setImageOpts.imageName, "image", "", "The image pullspec to set")
}

func runSetImageCmd(_ *cobra.Command, _ []string) {
	common(setImageOpts)

	if setImageOpts.poolName == "" {
		klog.Fatalln("No pool name provided!")
	}

	if setImageOpts.imageName == "" {
		klog.Fatalln("No image name provided!")
	}

	cs := framework.NewClientSet("")
	failOnError(setImageOnPool(cs, setImageOpts.poolName, setImageOpts.imageName))
}

func setImageOnPool(cs *framework.ClientSet, targetPool, pullspec string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := optInPool(cs, targetPool); err != nil {
			return err
		}

		return addImageToLayeredPool(cs, pullspec, targetPool)
	})
}
