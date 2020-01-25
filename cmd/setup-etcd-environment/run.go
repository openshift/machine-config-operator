package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/joho/godotenv"
	ceoapi "github.com/openshift/cluster-etcd-operator/pkg/operator/api"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	etcdBootstrapName        = "etcd-bootstrap"
	etcdInitialExisting      = "existing"
	EtcdScalingAnnotationKey = "etcd.operator.openshift.io/scale"
	retryDuration            = 10 * time.Second
	saTokenPath              = "/var/run/secrets/kubernetes.io/serviceaccount/token"

	// testing only
	testHost      = "etcd-test.testcluster.openshift.com"
	testIPAddress = "127.0.0.256"
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "Runs the setup-etcd-environment",
		Long:  "",
		RunE:  runRunCmd,
	}
	runOpts opts
)

type opts struct {
	discoverySRV string
	ifName       string
	outputFile   string
	bootstrapSRV bool
}

type SetupEnv struct {
	opts            *opts
	etcdDNS         string
	etcdName        string
	etcdDataDir     string
	etcdIP          string
	Client          *kubernetes.Clientset
	unsupportedArch string
}

func init() {
	rootCmd.AddCommand(runCmd)
	rootCmd.PersistentFlags().StringVar(&runOpts.discoverySRV, "discovery-srv", "", "DNS domain used to populate envs from SRV query.")
	rootCmd.PersistentFlags().StringVar(&runOpts.outputFile, "output-file", "", "file where the envs are written. If empty, prints to Stdout.")
	rootCmd.PersistentFlags().BoolVar(&runOpts.bootstrapSRV, "bootstrap-srv", true, "use SRV discovery for bootstraping etcd cluster.")
}

func newSetupEnv(runOpts *opts, etcdName, etcdDataDir string, ips []string) (*SetupEnv, error) {
	dns, ip, err := validateMember(runOpts.discoverySRV, etcdName, ips)
	if err != nil {
		return nil, err
	}

	// enable etcd to run using s390 and s390x. Because these are not officially supported upstream
	// etcd requires population of environment variable ETCD_UNSUPPORTED_ARCH at runtime.
	// https://github.com/etcd-io/etcd/blob/master/Documentation/op-guide/supported-platform.md
	var unsupportedArch string
	arch := runtime.GOARCH
	if strings.HasPrefix(arch, "s390") {
		unsupportedArch = arch
	}

	s := &SetupEnv{
		opts:            runOpts,
		etcdName:        etcdName,
		etcdDataDir:     etcdDataDir,
		etcdDNS:         dns,
		etcdIP:          ip,
		unsupportedArch: unsupportedArch,
	}
	if err := s.setClient(); err != nil {
		return nil, err
	}

	return s, nil
}

func runRunCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if runOpts.discoverySRV == "" {
		return errors.New("--discovery-srv cannot be empty")
	}

	etcdName := os.Getenv("ETCD_NAME")
	if etcdName == "" {
		return fmt.Errorf("environment variable ETCD_NAME has no value")
	}
	etcdDataDir := os.Getenv("ETCD_DATA_DIR")
	if etcdDataDir == "" {
		return fmt.Errorf("environment variable ETCD_DATA_DIR has no value")
	}
	if !inCluster() {
		glog.V(4).Infof("KUBERNETES_SERVICE_HOST or KUBERNETES_SERVICE_PORT contain no value, running in standalone mode.")
	}

	// preferredIP allows for a specific IP to be used as long as it is found on the interface. If found
	// use it to populate `ETCD_IPV4_ADDRESS` on disk otherwise we fallback to default lookup list.
	preferredIP := os.Getenv("ETCD_IPV4_ADDRESS")

	ips, err := ipAddrs(preferredIP)
	if err != nil {
		return err
	}

	setupEnv, err := newSetupEnv(&runOpts, etcdName, etcdDataDir, ips)
	if err != nil {
		return err
	}

	exportEnv, err := setupEnv.getExportEnv()
	if err != nil {
		return err
	}

	out := os.Stdout
	if runOpts.outputFile != "" {
		f, err := os.Create(runOpts.outputFile)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}

	if err := writeEnvironmentFile(exportEnv, out, true); err != nil {
		return err
	}

	parsedIP := net.ParseIP(setupEnv.etcdIP)
	if parsedIP == nil {
		return fmt.Errorf("Failed to parse IP '%s'", setupEnv.etcdIP)
	}

	escapedIP := setupEnv.etcdIP
	escapedAllIPs := "0.0.0.0"
	localhostIP := "127.0.0.1"
	escapedLocalhostIP := "127.0.0.1"
	if parsedIP.To4() == nil {
		// This is an IPv6 address, not IPv4.

		// When using an IPv6 address in a URL, we must wrap the address portion in
		// [::] so that a ":port" suffix can still be added and parsed correctly.
		escapedIP = fmt.Sprintf("[%s]", setupEnv.etcdIP)
		escapedAllIPs = "[::]"
		localhostIP = "::1"
		escapedLocalhostIP = "[::1]"
	}

	unexportedEnv := map[string]string{
		// TODO This can actually be IPv6, so we should rename this ...
		"IPV4_ADDRESS":         setupEnv.etcdIP,
		"ESCAPED_IP_ADDRESS":   escapedIP,
		"ESCAPED_ALL_IPS":      escapedAllIPs,
		"LOCALHOST_IP":         localhostIP,
		"ESCAPED_LOCALHOST_IP": escapedLocalhostIP,
		"WILDCARD_DNS_NAME":    fmt.Sprintf("*.%s", setupEnv.opts.discoverySRV),
	}
	if setupEnv.etcdDNS != "" {
		unexportedEnv["DNS_NAME"] = setupEnv.etcdDNS
	}
	return writeEnvironmentFile(unexportedEnv, out, false)
}

func (s *SetupEnv) getExportEnv() (map[string]string, error) {
	// if etcd is managed by the operator populate ENV from configmap
	// TODO: alaypatel07/hexfusion figure out a better way of implementing isScaling
	// if s.isEtcdScaling() {
	if inCluster() && !s.opts.bootstrapSRV {
		return s.getOperatorEnv()
	}
	// initialize envs used to bootstrap etcd using SRV discovery or disaster recovery.
	return s.getBootstrapEnv()
}

// getOperatorEnv returns a maping of ENV variables required to scale etcd via operator
func (s *SetupEnv) getOperatorEnv() (map[string]string, error) {
	env := make(map[string]string)
	env["NAME"] = s.etcdName

	memberList := s.getEtcdMemberList()
	env["INITIAL_CLUSTER"] = strings.Join(memberList, ",")
	env["INITIAL_CLUSTER_STATE"] = etcdInitialExisting

	endpoints, err := s.getEtcdEndpoints()
	if err != nil {
		return nil, err
	}
	env["ENDPOINTS"] = strings.Join(endpoints, ",")
	return env, nil
}

func (s *SetupEnv) getEtcdEndpoints() ([]string, error) {
	ep, err := s.Client.CoreV1().Endpoints("openshift-etcd").Get("etcd", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	hostEtcdEndpoint, err := s.Client.CoreV1().Endpoints("openshift-etcd").Get("host-etcd", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if len(hostEtcdEndpoint.Subsets) != 1 {
		return nil, fmt.Errorf("openshift-etcd/host-etcd endpoint subset length should be %d, found %d", 1, len(hostEtcdEndpoint.Subsets))
	}

	endpoints := make([]string, 0)
	for _, member := range hostEtcdEndpoint.Subsets[0].Addresses {
		// TODO validate IP?
		if member.Hostname == etcdBootstrapName {
			if member.IP != "" {
				endpoints = append(endpoints, fmt.Sprintf("https://%s:2379", member.IP))
				break
			}
			return nil, fmt.Errorf("openshift-etcd/host-etcd hostname %s has no IP", etcdBootstrapName)
		}
	}
	if len(ep.Subsets) != 1 {
		return nil, fmt.Errorf("openshift-etcd/etcd endpoint subset length should be %d, found %d", 1, len(ep.Subsets))
	}
	for _, s := range ep.Subsets[0].Addresses {
		endpoints = append(endpoints, "https://"+s.IP+":2379")
	}
	return endpoints, nil
}

func (s *SetupEnv) getEtcdMemberList() []string {
	var memberList []string

	// wait forever for success and retry every duration interval
	wait.PollInfinite(retryDuration, func() (bool, error) {
		var e ceoapi.EtcdScaling
		result, err := s.Client.CoreV1().ConfigMaps("openshift-etcd").Get("member-config", metav1.GetOptions{})
		if err != nil {
			glog.Errorf("error creating client %v", err)
			return false, nil
		}
		if err := json.Unmarshal([]byte(result.Annotations[EtcdScalingAnnotationKey]), &e); err != nil {
			glog.Errorf("error decoding result %v", err)
			return false, nil
		}
		if e.Metadata.Name != s.etcdName {
			glog.Errorf("could not find self in member-config")
			return false, nil
		}
		members := e.Members
		if len(members) == 0 {
			glog.Errorf("no members found in member-config")
			return false, nil
		}
		for _, m := range members {
			memberList = append(memberList, fmt.Sprintf("%s=%s", m.Name, m.PeerURLS[0]))
		}
		memberList = append(memberList, fmt.Sprintf("%s=https://%s:2380", s.etcdName, s.etcdDNS))
		return true, nil
	})
	return memberList
}

// getBootstrapEnv populates ENV variables used for bootstrapping etcd with SRV and recovery. We take care
// to persist important ENV during reboot. We also make sure to not mix ENV that might cause etcd to fail to start.
func (s *SetupEnv) getBootstrapEnv() (map[string]string, error) {
	bootstrapEnv := make(map[string]string)
	if _, err := os.Stat(s.opts.outputFile); !os.IsNotExist(err) {
		// read existing ENV file
		existingEnv, err := godotenv.Read(s.opts.outputFile)
		if err != nil {
			return nil, err
		}
		// persist any observed envs used for recovery. We want to make sure that ETCD_INITIAL_CLUSTER and
		// ETCD_INITIAL_CLUSTER_STATE survive reboot.
		for _, val := range []string{"INITIAL_CLUSTER", "INITIAL_CLUSTER_STATE"} {
			rkey := fmt.Sprintf("ETCD_%s", val)
			if existingEnv[rkey] != "" {
				bootstrapEnv[val] = existingEnv[rkey]
			}
		}
	}

	// Define SRV discovery variables if enabeled and not in recovery mode. This is important because we can
	// not mix `ETCD_DISCOVERY_SRV` with ETCD_INITIAL_CLUSTER` or etcd will fail to start.
	if len(bootstrapEnv) == 0 && s.opts.bootstrapSRV {
		bootstrapEnv["DISCOVERY_SRV"] = s.opts.discoverySRV
	}
	return bootstrapEnv, nil
}

// isEtcdScaling performs a series of checks to see if etcd is currently attempting to scale from the
// `cluster-etcd-operator` and returns true if yes. If false we can conclude that the member is already
// part of the cluster or we have/are bootstrapping etcd using SRV discovery.
func (s *SetupEnv) isEtcdScaling() bool {
	// TODO: reimplement me!
	if _, err := os.Stat(fmt.Sprintf("%s/member", s.etcdDataDir)); os.IsNotExist(err) && !s.opts.bootstrapSRV && inCluster() {
		return true
	}
	return false
}

func (s *SetupEnv) setClient() error {
	if inCluster() && !s.opts.bootstrapSRV {
		wait.PollInfinite(retryDuration, func() (bool, error) {
			if _, err := os.Stat(saTokenPath); os.IsNotExist(err) {
				glog.Errorf("serviceaccount failed: %v", err)
				return false, nil
			}
			return true, nil
		})
		clientConfig, err := rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
		client, err := kubernetes.NewForConfig(clientConfig)
		if err != nil {
			return fmt.Errorf("error creating client: %v", err)
		}
		s.Client = client
		return nil
	}
	glog.Infof("no client set")
	return nil
}

// ipAddrs checks the local interfaces and returns a populated list of addresses as a slice of string.
// This function also accepts an optional preferredIP address which if found will be explicitly used.
func ipAddrs(preferredIP string) ([]string, error) {
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
		if !ip.IsGlobalUnicast() {
			continue // we only want global unicast address
		}
		ips = append(ips, ip.String())
	}

	// validate preferredIP is valid for this system or fall back to default list.
	if preferredIP != "" {
		for _, ip := range ips {
			if ip == preferredIP {
				glog.Infof("ipAddrs: preferredIP %s found", preferredIP)
				return []string{preferredIP}, nil
			}
		}
		glog.Infof("ipAddrs: preferredIP %s not found on system interfaces", preferredIP)
	}
	return ips, nil
}

// reverseLookupSelf returns the target from the SRV record that resolves to self.
func reverseLookupSelf(service, proto, name, self string) (string, error) {
	_, srvs, err := net.LookupSRV(service, proto, name)
	if err != nil {
		return "", err
	}
	for _, srv := range srvs {
		glog.V(4).Infof("checking against %s", srv.Target)
		selfTarget, err := lookupTargetMatchSelf(srv.Target, self)
		if err != nil {
			return "", err
		}
		if selfTarget != "" {
			return selfTarget, nil
		}
	}
	return "", fmt.Errorf("could not find self")
}

// lookupTargetMatchSelf takes a target FQDN and performs a DNS lookup. If the result of the
// lookup addresses matches self we return that address.
func lookupTargetMatchSelf(target, self string) (string, error) {
	var selfTarget string
	addrs, err := net.LookupHost(target)
	if err != nil {
		return selfTarget, fmt.Errorf("could not resolve member %q", target)
	}
	for _, addr := range addrs {
		if addr == self {
			selfTarget = strings.Trim(target, ".")
			break
		}
	}
	return selfTarget, nil
}

func writeEnvironmentFile(m map[string]string, w io.Writer, export bool) error {
	var buffer bytes.Buffer
	for k, v := range m {
		env := fmt.Sprintf("ETCD_%s=%s\n", k, v)
		if export == true {
			env = fmt.Sprintf("export %s", env)
		}
		buffer.WriteString(env)
	}
	if _, err := buffer.WriteTo(w); err != nil {
		return err
	}
	return nil
}

func inCluster() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" || os.Getenv("KUBERNETES_SERVICE_PORT") == "" {
		return false
	}
	return true
}

// validateMember performs an SRV lookup for a service based on the cluster domain. If an IP address
// found in the list of IPs for the system we return that IP address and the matching FQDN.
func validateMember(discoverySRV, etcdName string, ips []string) (string, string, error) {
	var dns string
	var ip string
	// for testing only
	if len(ips) > 0 && ips[0] == testIPAddress {
		return testHost, testIPAddress, nil
	}

	// bootstrap is a special case we do not use FQDN
	if etcdName == etcdBootstrapName {
		return dns, ips[0], nil
	}

	if err := wait.PollImmediate(30*time.Second, 5*time.Minute, func() (bool, error) {
		for _, cand := range ips {
			found, err := reverseLookupSelf("etcd-server-ssl", "tcp", discoverySRV, cand)
			if err != nil {
				glog.Errorf("error looking up self for candidate IP %s: %v", cand, err)
				continue
			}
			if found != "" {
				dns = found
				ip = cand
				return true, nil
			}
			glog.V(4).Infof("no matching dns for %s in %s: %v", cand, discoverySRV, err)
		}
		return false, nil
	}); err != nil {
		return dns, ip, fmt.Errorf("could not find self: %v", err)
	}
	glog.Infof("dns name is %s", dns)
	return dns, ip, nil
}
