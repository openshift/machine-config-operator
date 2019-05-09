package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"

	utilnet "k8s.io/apimachinery/pkg/util/net"
)

const (
	componentName = "machine-config-server"
)

var (
	rootCmd = &cobra.Command{
		Use:   componentName,
		Short: "Run Machine Config Server",
		Long:  "",
	}

	rootOpts struct {
		address string
		sport   int
		isport  int
		cert    string
		key     string
	}
)

// findDefaultAddress uses the k8s ChooseHostInterface to provide a default
// address to bind to. This function panic's on ChooseHostInterface error.
func findDefaultAddress() string {
	ip, err := utilnet.ChooseHostInterface()
	if err != nil {
		panic(err)
	}

	return ip.String()
}

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	rootCmd.PersistentFlags().IntVar(&rootOpts.sport, "secure-port", 22623, "secure port to serve ignition configs")
	rootCmd.PersistentFlags().StringVar(&rootOpts.cert, "cert", "/etc/ssl/mcs/tls.crt", "cert file for TLS")
	rootCmd.PersistentFlags().StringVar(&rootOpts.key, "key", "/etc/ssl/mcs/tls.key", "key file for TLS")
	rootCmd.PersistentFlags().IntVar(&rootOpts.isport, "insecure-port", 22624, "insecure port to serve ignition configs")
	rootCmd.PersistentFlags().StringVar(&rootOpts.address, "address", findDefaultAddress(), "address to bind ports on")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Exitf("Error executing mcs: %v", err)
	}
}
