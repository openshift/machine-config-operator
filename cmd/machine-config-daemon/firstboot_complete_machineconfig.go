package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/golang/glog"
	daemon "github.com/openshift/machine-config-operator/pkg/daemon"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
		// We're done; this logic is distinct from the *non-containerized* /run/bin/machine-config-daemon
		// for what I believe is historical reasons; we could shift everything to happening inside
		// podman actually.
		return nil
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
			glog.Warningf("error: %v\n", err)
			glog.Info("Sleeping 1 minute for retry")
			time.Sleep(time.Minute)
		} else {
			break
		}
	}
}
