package main

import (
	"flag"
	"fmt"
	"net"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/openshift/machine-config-operator/pkg/daemon/nodenet"
)

var nodeIPCmd = &cobra.Command{
	Use:                   "node-ip",
	DisableFlagsInUseLine: true,
	Short:                 "Node IP tools",
	Long:                  "Node IP has tools that aid in the configuration of nodes in platforms that use Virtual IPs",
}

var nodeIPShowCmd = &cobra.Command{
	Use:                   "show",
	DisableFlagsInUseLine: true,
	Short:                 "Show a configured IP address that directly routes to the given Virtual IPs",
	Args:                  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		err := show(cmd, args)
		if err != nil {
			glog.Exitf("error in node-ip show: %v\n", err)
		}
	},
}

var nodeIPSetCmd = &cobra.Command{
	Use:                   "set",
	DisableFlagsInUseLine: true,
	Short:                 "Sets container runtime services to bind to a configured IP address that directly routes to the given virtual IPs",
	Args:                  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		err := set(cmd, args)
		if err != nil {
			glog.Exitf("error in node-ip set: %v\n", err)
		}
	},
}

// init executes upon import
func init() {
	rootCmd.AddCommand(nodeIPCmd)
	nodeIPCmd.AddCommand(nodeIPShowCmd)
	nodeIPCmd.AddCommand(nodeIPSetCmd)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	flag.Set("logtostderr", "true")
	flag.Parse()
}

func show(_ *cobra.Command, args []string) error {
	vips := make([]net.IP, len(args))
	for i, arg := range args {
		vips[i] = net.ParseIP(arg)
		if vips[i] == nil {
			return fmt.Errorf("Failed to parse IP address %s", arg)
		}
		glog.V(3).Infof("Parsed Virtual IP %s", vips[i])
	}

	nodeAddrs, err := nodenet.AddressesRouting(vips, nodenet.NonDeprecatedAddress, nodenet.NonDefaultRoute)
	if err != nil {
		return err
	}

	if len(nodeAddrs) > 0 {
		fmt.Println(nodeAddrs[0])
	} else {
		return fmt.Errorf("Failed to find node IP")
	}

	return nil
}

func set(_ *cobra.Command, args []string) error {
	return nil
}
