package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	// Enable sha256 in container image references
	_ "crypto/sha256"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/openshift/machine-config-operator/pkg/daemon/nodenet"
)

const (
	kubeletSvcOverridePath = "/etc/systemd/system/kubelet.service.d/20-nodenet.conf"
	crioSvcOverridePath    = "/etc/systemd/system/crio.service.d/20-nodenet.conf"
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

	var chosenAddress net.IP
	if len(nodeAddrs) > 0 {
		chosenAddress = nodeAddrs[0]
	} else {
		return fmt.Errorf("Failed to find node IP")
	}
	glog.Infof("Chosen Node IP %s", chosenAddress)

	// Kubelet
	glog.V(2).Infof("Opening Kubelet service override path %s", kubeletSvcOverridePath)
	kOverride, err := os.Create(kubeletSvcOverridePath)
	if err != nil {
		return err
	}
	defer kOverride.Close()

	kOverrideContent := fmt.Sprintf("[Service]\nEnvironment=\"KUBELET_NODE_IP=%s\"\n", chosenAddress)
	glog.V(3).Infof("Writing Kubelet service override with content %s", kOverrideContent)
	_, err = kOverride.WriteString(kOverrideContent)
	if err != nil {
		return err
	}

	// CRI-O
	crioOverrideDir := filepath.Dir(crioSvcOverridePath)
	err = os.MkdirAll(crioOverrideDir, 0755)
	if err != nil {
		return err
	}
	glog.V(2).Infof("Opening CRI-O service override path %s", crioSvcOverridePath)
	cOverride, err := os.Create(crioSvcOverridePath)
	if err != nil {
		return err
	}
	defer cOverride.Close()

	cOverrideContent := fmt.Sprintf("[Service]\nEnvironment=\"CONTAINER_STREAM_ADDRESS=%s\"\n", chosenAddress)
	glog.V(3).Infof("Writing CRI-O service override with content %s", cOverrideContent)
	_, err = cOverride.WriteString(cOverrideContent)
	if err != nil {
		return err
	}
	return nil
}
