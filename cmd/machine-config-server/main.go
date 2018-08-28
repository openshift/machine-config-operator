package main

import (
	"flag"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
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
		port  int
		cert  string
		key   string
		debug bool
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	rootCmd.PersistentFlags().IntVar(&rootOpts.port, "port", 49500, "port to serve ignition configs")
	rootCmd.PersistentFlags().StringVar(&rootOpts.cert, "cert", "/etc/ssl/mcs/tls.crt", "cert file for TLS")
	rootCmd.PersistentFlags().StringVar(&rootOpts.key, "key", "/etc/ssl/mcs/tls.key", "key file for TLS")
	rootCmd.PersistentFlags().BoolVar(&rootOpts.debug, "debug", false, "turns off TLS")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		glog.Exitf("Error executing mcs: %v", err)
	}
}
