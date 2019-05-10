package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/wait"
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "Runs the setup-etcd-environment",
		Long:  "",
		RunE:  runRunCmd,
	}

	runOpts struct {
		discoverySRV string
		ifName       string
		outputFile   string
	}
)

func init() {
	rootCmd.AddCommand(runCmd)
	rootCmd.PersistentFlags().StringVar(&runOpts.discoverySRV, "discovery-srv", "", "DNS domain used to bootstrap initial etcd cluster.")
	rootCmd.PersistentFlags().StringVar(&runOpts.outputFile, "output-file", "", "file where the envs are written. If empty, prints to Stdout.")
}

func runRunCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Version, version.Hash)

	if runOpts.discoverySRV == "" {
		return errors.New("--discovery-srv cannot be empty")
	}

	ips, err := ipAddrs()
	if err != nil {
		return err
	}

	var dns string
	var ip string
	if err := wait.PollImmediate(30*time.Second, 5*time.Minute, func() (bool, error) {
		for _, cand := range ips {
			found, err := reverseLookupSelf("etcd-server-ssl", "tcp", runOpts.discoverySRV, cand)
			if err != nil {
				glog.Errorf("error looking up self: %v", err)
				continue
			}
			if found != "" {
				dns = found
				ip = cand
				return true, nil
			}
			glog.V(4).Infof("no matching dns for %s", cand)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("could not find self: %v", err)
	}
	glog.Infof("dns name is %s", dns)

	out := os.Stdout
	if runOpts.outputFile != "" {
		f, err := os.Create(runOpts.outputFile)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}

	return writeEnvironmentFile(map[string]string{
		"IPV4_ADDRESS":      ip,
		"DNS_NAME":          dns,
		"WILDCARD_DNS_NAME": fmt.Sprintf("*.%s", runOpts.discoverySRV),
	}, out)
}

func ipAddrs() ([]string, error) {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips, err
	}
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		ip = ip.To4()
		if ip == nil {
			continue // not an ipv4 address
		}
		if !ip.IsGlobalUnicast() {
			continue // we only want global unicast address
		}
		ips = append(ips, ip.String())
	}

	return ips, nil
}

// returns the target from the SRV record that resolves to self.
func reverseLookupSelf(service, proto, name, self string) (string, error) {
	_, srvs, err := net.LookupSRV(service, proto, name)
	if err != nil {
		return "", err
	}
	selfTarget := ""
	for _, srv := range srvs {
		glog.V(4).Infof("checking against %s", srv.Target)
		addrs, err := net.LookupHost(srv.Target)
		if err != nil {
			return "", fmt.Errorf("could not resolve member %q", srv.Target)
		}

		for _, addr := range addrs {
			if addr == self {
				selfTarget = strings.Trim(srv.Target, ".")
				break
			}
		}
	}
	if selfTarget == "" {
		return "", fmt.Errorf("could not find self")
	}
	return selfTarget, nil
}

func writeEnvironmentFile(m map[string]string, w io.Writer) error {
	var buffer bytes.Buffer
	for k, v := range m {
		buffer.WriteString(fmt.Sprintf("ETCD_%s=%s\n", k, v))
	}
	if _, err := buffer.WriteTo(w); err != nil {
		return err
	}
	return nil
}
