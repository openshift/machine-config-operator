package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	health "github.com/InVisionApp/go-health"
	"github.com/InVisionApp/go-health/checkers"
	"github.com/golang/glog"
	"github.com/spf13/cobra"

	utilnet "k8s.io/utils/net"

	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "Runs the apiserver-watcher",
		Long:  "",
		RunE:  runRunCmd,
	}

	runOpts struct {
		rootMount      string
		healthCheckURL string
		platform       string
		vip            string
		ipv6           bool
	}
)

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.PersistentFlags().StringVar(&runOpts.healthCheckURL, "health-check-url", "", "HTTP(s) URL for the health check")
	runCmd.PersistentFlags().StringVar(&runOpts.platform, "platform", "", "platform defines the behavior of the apiserver-watcher, currently supported gcp and azure")
	runCmd.PersistentFlags().StringVar(&runOpts.vip, "vip", "", "The VIP to remove if the health check fails. Determined from URL if not provided")
	runCmd.PersistentFlags().BoolVar(&runOpts.ipv6, "ipv6", false, "The VIP IP family (default to IPv4)")
}

func runRunCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	uri, err := url.Parse(runOpts.healthCheckURL)
	if err != nil {
		return fmt.Errorf("failed to parse health-check-url: %v", err)
	}
	if !uri.IsAbs() {
		return fmt.Errorf("invalid URI %q (no scheme)", uri)
	}

	// Get the VIP
	var vip string
	if runOpts.vip != "" {
		vip = runOpts.vip
		if net.ParseIP(vip) == nil {
			return fmt.Errorf("vip %s is an invalid IP address", vip)
		}
		if utilnet.IsIPv6String(vip) != runOpts.ipv6 {
			return fmt.Errorf("vip %s IP family mismatch, ipv6 option %v", vip, runOpts.ipv6)
		}
	} else {
		addrsList, err := net.LookupHost(uri.Hostname())
		if err != nil {
			return fmt.Errorf("failed to lookup host %s: %v", uri.Hostname(), err)
		}
		addrs := filterAddrList(addrsList, runOpts.ipv6)
		if len(addrs) == 0 || len(addrs) > 1 {
			return fmt.Errorf("hostname %s unexpected number of addresses %d, expected one - aborting", uri.Hostname(), len(addrs))
		}
		vip = addrs[0]
	}
	glog.Infof("Using VIP %s", vip)

	// The health check should always connect to localhost, not be load-balanced
	uri.Host = net.JoinHostPort("localhost", uri.Port())

	httpCheck, err := checkers.NewHTTP(&checkers.HTTPConfig{
		URL: uri,
		Client: &http.Client{Transport: &http.Transport{
			// #nosec G402
			// health checks to https endpoints can use InsecureSkipVerify.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}},
	})
	if err != nil {
		return fmt.Errorf("failed to create httpCheck: %v", err)
	}

	// careful: the timing here needs to correspond to the load balancer's
	// parameters. We need to remove routes just after we've been removed
	// as a backend in the load-balancer, and add routes before we've been
	// re-added.
	// see openshift/installer/data/data/gcp/network/lb-private.tf
	// see openshift/installer/data/data/azure/vnet/internal-lb.tf
	var handler handler
	switch runOpts.platform {
	case "gcp":
		handler, err = newGCPHandler(vip)
	case "azure":
		handler, err = newAzureHandler(vip, runOpts.ipv6)
	default:
		return fmt.Errorf("invalid platform %s", runOpts.platform)
	}
	if err != nil {
		return err
	}

	errCh := make(chan error)
	tracker := &healthTracker{
		state:            unknownTrackerState,
		ErrCh:            errCh,
		SuccessThreshold: handler.successThreshold(),
		FailureThreshold: handler.failureThreshold(),
		OnFailure:        handler.onFailure,
		OnSuccess:        handler.onSuccess,
	}

	h := health.New()
	h.AddChecks([]*health.Config{{
		Name:       "dependency-check",
		Checker:    httpCheck,
		Interval:   time.Duration(2) * time.Second,
		Fatal:      true,
		OnComplete: tracker.OnComplete,
	}})

	if err := h.Start(); err != nil {
		return fmt.Errorf("failed to start heath checker: %v", err)
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for sig := range c {
			glog.Infof("Signal %s received: treating service as down", sig)
			if err := handler.onFailure(); err != nil {
				glog.Infof("Failed to mark service down on signal: %s", err)
			}
			os.Exit(0)
		}
	}()

	for {
		select {
		case err := <-errCh:
			if err != nil {
				return fmt.Errorf("error running health checker: %v", err)
			}
		}
	}
}

type handler interface {
	successThreshold() int
	failureThreshold() int
	onFailure() error
	onSuccess() error
}

type trackerState int

const (
	unknownTrackerState trackerState = iota
	failedTrackerState
	succeededTrackerState
)

type healthTracker struct {
	sync.Mutex

	state trackerState

	succeeded int
	failed    int

	// ErrCh is used to collect errors
	ErrCh chan<- error

	// SuccessThreshold is the number of consecutive success
	SuccessThreshold int

	// FailureThreshold is the number of consecutive failure that trigger OnFailure func
	FailureThreshold int

	// OnFailure is the function that is triggered when the health check is in FAILED state
	// Non nil error are sent over the ErrCh
	// Only one OnFailure function will be active at a time.
	OnFailure func() error

	// OnSuccess is the function that is triggered when the health check is in SUCCEEDED state
	// Non nil error are sent over the ErrCh
	// Only one OnFailure function will be active at a time.
	OnSuccess func() error
}

func (sl *healthTracker) OnComplete(state *health.State) {
	sl.Lock()
	defer sl.Unlock()

	switch state.Status {
	case "ok":
		sl.failed = 0
		sl.succeeded++
		if sl.succeeded >= sl.SuccessThreshold {
			if sl.state != succeededTrackerState {
				glog.Info("Running OnSuccess trigger")
				if err := sl.OnSuccess(); err != nil {
					sl.ErrCh <- err
				}
			}
			sl.state = succeededTrackerState
		}
	case "failed":
		sl.succeeded = 0
		sl.failed++
		if sl.failed >= sl.FailureThreshold {
			if sl.state != failedTrackerState {
				glog.Info("Running OnFailure trigger")
				if err := sl.OnFailure(); err != nil {
					sl.ErrCh <- err
				}
			}
			sl.state = failedTrackerState
		}
	}
}

// filterAddrList filter a list of IP addresses by IP family
func filterAddrList(ips []string, isIPv6 bool) []string {
	addrList := []string{}
	for _, ip := range ips {
		if utilnet.IsIPv6String(ip) == isIPv6 {
			addrList = append(addrList, ip)
		}
	}
	return addrList
}
