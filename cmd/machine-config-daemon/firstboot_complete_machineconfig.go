package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"
	"time"

	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

var firstbootCompleteMachineconfig = &cobra.Command{
	Use:                   "firstboot-complete-machineconfig",
	DisableFlagsInUseLine: true,
	Short:                 "Complete the host's initial boot into a MachineConfig",
	Args:                  cobra.MaximumNArgs(0),
	Run:                   executeFirstbootCompleteMachineConfig,
}

var persistNics bool

// init executes upon import
func init() {
	rootCmd.AddCommand(firstbootCompleteMachineconfig)
	firstbootCompleteMachineconfig.PersistentFlags().BoolVar(&persistNics, "persist-nics", false, "Run nmstatectl persist-nic-names")
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
}

func runFirstBootCompleteMachineConfig(_ *cobra.Command, _ []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	exitCh := make(chan error)
	defer close(exitCh)

	if persistNics {
		// If asked, before we try an OS update, persist NIC names so that
		// we handle the reprovision case with old disk images and Ignition configs
		// that provide static IP addresses.
		if err := daemon.PersistNetworkInterfaces("/rootfs"); err != nil {
			return fmt.Errorf("failed to persist network interfaces: %w", err)
		}
	}

	klog.Infof(`Calling chroot("%s")`, startOpts.rootMount)
	if err := syscall.Chroot(startOpts.rootMount); err != nil {
		return fmt.Errorf("failed to chroot to %s: %w", startOpts.rootMount, err)
	}

	klog.V(2).Infof("Moving to / inside the chroot")
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("failed to change directory to /: %w", err)
	}

	dn, err := daemon.New(exitCh)
	if err != nil {
		return err
	}

	return dn.RunFirstbootCompleteMachineconfig()
}

func executeFirstbootCompleteMachineConfig(cmd *cobra.Command, args []string) {
	for {
		err := runFirstBootCompleteMachineConfig(cmd, args)
		if err != nil {
			klog.Warningf("error: %v\n", err)
			klog.Info("Sleeping 1 minute for retry")
			time.Sleep(time.Minute)
		} else {
			break
		}
	}
}
