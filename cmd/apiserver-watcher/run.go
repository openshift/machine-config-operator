package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"sync"
	"time"

	health "github.com/InVisionApp/go-health"
	"github.com/InVisionApp/go-health/checkers"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

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
	}
)

// downFileDir is the directory in which gcp-routes will look for a flag-file that
// indicates the route to the VIP should be withdrawn.
const downFileDir = "/run/cloud-routes"

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.PersistentFlags().StringVar(&runOpts.rootMount, "root-mount", "/rootfs", "where the nodes root filesystem is mounted for writing down files or chrooting.")
	runCmd.PersistentFlags().StringVar(&runOpts.healthCheckURL, "health-check-url", "", "HTTP(s) URL for the health check. The hostname is also used to determine the virtual IPs")
}

type handler struct {
	vips []string
}

func runRunCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	uri, err := url.Parse(runOpts.healthCheckURL)
	if err != nil {
		return fmt.Errorf("failed to parse health-check-url: %w", err)
	}
	if !uri.IsAbs() {
		return fmt.Errorf("invalid URI %q (no scheme)", uri)
	}

	handler, err := newHandler(uri)
	if err != nil {
		return err
	}

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
		return fmt.Errorf("failed to create httpCheck: %w", err)
	}
	errCh := make(chan error)

	// careful: the timing here needs to correspond to the load balancer's
	// parameters. We need to remove routes just after we've been removed
	// as a backend in the load-balancer, and add routes before we've been
	// re-added.
	// see openshift/installer/data/data/gcp/network/lb-private.tf
	// see openshift/installer/data/data/azure/vnet/internal-lb.tf
	tracker := &healthTracker{
		state:            unknownTrackerState,
		ErrCh:            errCh,
		SuccessThreshold: 1,
		FailureThreshold: 8, // LB = 6 seconds, plus 10 seconds for propagation
		OnFailure:        handler.onFailure,
		OnSuccess:        handler.onSuccess,
	}

	h := health.New()
	h.Logger = &logger{}
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
			klog.Infof("Signal %s received: treating service as down", sig)
			if err := handler.onFailure(); err != nil {
				klog.Infof("Failed to mark service down on signal: %s", err)
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

func newHandler(uri *url.URL) (*handler, error) {
	addrs, err := net.LookupHost(uri.Hostname())
	if err != nil {
		return nil, fmt.Errorf("failed to lookup host %s: %v", uri.Hostname(), err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("hostname %s has no addresses, expected at least 1 - aborting", uri.Hostname())
	}

	h := handler{
		vips: addrs,
	}
	klog.Infof("Using VIPs %v", h.vips)
	return &h, nil
}

// onFailure: either stop the routes service, or write downfile
func (h *handler) onFailure() error {
	for _, vip := range h.vips {
		if err := writeVipStateFile(vip, "down"); err != nil {
			return err
		}
		klog.Infof("healthcheck failed, created downfile %s.down", vip)
		if err := removeVipStateFile(vip, "up"); err != nil {
			return err
		}
	}
	return nil
}

// onSuccess: either start routes service, or remove down file
func (h *handler) onSuccess() error {
	for _, vip := range h.vips {
		if err := removeVipStateFile(vip, "down"); err != nil {
			return err
		}
		klog.Infof("healthcheck succeeded, removed downfile %s.down", vip)
		if err := writeVipStateFile(vip, "up"); err != nil {
			return err
		}
	}
	return nil
}

func writeVipStateFile(vip, state string) error {
	file := path.Join(runOpts.rootMount, downFileDir, fmt.Sprintf("%s.%s", vip, state))
	// Disable gosec here to avoid throwing
	// G306: Expect WriteFile permissions to be 0600 or less
	// #nosec
	err := ioutil.WriteFile(file, nil, 0644)
	if err != nil {
		return fmt.Errorf("failed to create file (%s): %v", file, err)
	}
	return nil
}

func removeVipStateFile(vip, state string) error {
	file := path.Join(runOpts.rootMount, downFileDir, fmt.Sprintf("%s.%s", vip, state))
	err := os.Remove(file)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove file (%s): %v", file, err)
	}
	return nil
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
				klog.Info("Running OnSuccess trigger")
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
				klog.Info("Running OnFailure trigger")
				if err := sl.OnFailure(); err != nil {
					sl.ErrCh <- err
				}
			}
			sl.state = failedTrackerState
		}
	}
}
