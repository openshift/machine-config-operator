package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"sync"
	"syscall"
	"time"

	health "github.com/InVisionApp/go-health"
	"github.com/InVisionApp/go-health/checkers"
	"github.com/golang/glog"
	"github.com/spf13/cobra"

	"github.com/openshift/machine-config-operator/pkg/version"
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "Runs the gcp-routes-controller",
		Long:  "",
		RunE:  runRunCmd,
	}

	runOpts struct {
		gcpRoutesService string
		rootMount        string
		healthCheckURL   string
		vip              string
	}
)

// downFileDir is the directory in which gcp-routes will look for a flag-file that
// indicates the route to the VIP should be withdrawn.
const downFileDir = "/run/gcp-routes"

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.PersistentFlags().StringVar(&runOpts.gcpRoutesService, "gcp-routes-service", "openshift-gcp-routes.service", "The name for the service controlling gcp routes on host")
	runCmd.PersistentFlags().StringVar(&runOpts.rootMount, "root-mount", "/rootfs", "where the nodes root filesystem is mounted for writing down files or chrooting.")
	runCmd.PersistentFlags().StringVar(&runOpts.healthCheckURL, "health-check-url", "", "HTTP(s) URL for the health check")
	runCmd.PersistentFlags().StringVar(&runOpts.vip, "vip", "", "The VIP to remove if the health check fails. Determined from URL if not provided")
}

type downMode int

const (
	modeStopService = iota
	modeDownFile
)

type handler struct {
	mode downMode
	vip  string
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
		return fmt.Errorf("failed to create httpCheck: %v", err)
	}
	errCh := make(chan error)

	// careful: the timing here needs to correspond to the load balancer's
	// parameters. We need to remove routes just after we've been removed
	// as a backend in the load-balancer, and add routes before we've been
	// re-added.
	// see openshift/installer/data/data/gcp/network/lb-private.tf
	tracker := &healthTracker{
		state:            unknownTrackerState,
		ErrCh:            errCh,
		SuccessThreshold: 1,
		FailureThreshold: 8, // LB = 6 seconds, plus 10 seconds for propagation
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
			glog.Infof("Signal %s received: shutting down gcp routes service", sig)
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

func newHandler(uri *url.URL) (*handler, error) {
	h := handler{}

	// determine mode: if /run/gcp-routes exists, we can us the downfile mode
	realPath := path.Join(runOpts.rootMount, downFileDir)
	fi, err := os.Stat(realPath)
	if err == nil && fi.IsDir() {
		glog.Infof("%s exists, starting in downfile mode", realPath)
		h.mode = modeDownFile
	} else {
		glog.Infof("%s not accessible, will stop gcp-routes.service on health failure", realPath)
		h.mode = modeStopService
	}

	// if StopService mode and rootfs specified, chroot
	if h.mode == modeStopService && runOpts.rootMount != "" {
		glog.Infof(`Calling chroot("%s")`, runOpts.rootMount)
		if err := syscall.Chroot(runOpts.rootMount); err != nil {
			return nil, fmt.Errorf("unable to chroot to %s: %s", runOpts.rootMount, err)
		}

		glog.V(2).Infof("Moving to / inside the chroot")
		if err := os.Chdir("/"); err != nil {
			return nil, fmt.Errorf("unable to change directory to /: %s", err)
		}
	}

	// otherwise, resolve vip
	if h.mode == modeDownFile {
		if runOpts.vip != "" {
			h.vip = runOpts.vip
		} else {
			addrs, err := net.LookupHost(uri.Hostname())
			if err != nil {
				return nil, fmt.Errorf("failed to lookup host %s: %v", uri.Hostname(), err)
			}
			if len(addrs) != 1 {
				return nil, fmt.Errorf("hostname %s has %d addresses, expected 1 - aborting", uri.Hostname(), len(addrs))
			}
			h.vip = addrs[0]
			glog.Infof("Using VIP %s", h.vip)
		}
	}

	return &h, nil
}

// onFailure: either stop the routes service, or write downfile
func (h *handler) onFailure() error {
	if h.mode == modeDownFile {
		downFile := path.Join(runOpts.rootMount, downFileDir, fmt.Sprintf("%s.down", h.vip))
		fp, err := os.OpenFile(downFile, os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("failed to create downfile (%s): %v", downFile, err)
		}
		_ = fp.Close()
		glog.Infof("healthcheck failed, created downfile %s", downFile)
	} else {
		if err := exec.Command("systemctl", "stop", runOpts.gcpRoutesService).Run(); err != nil {
			return fmt.Errorf("Failed to terminate gcp routes service %v", err)
		}
		glog.Infof("healthcheck failed, stopped %s", runOpts.gcpRoutesService)
	}
	return nil
}

// onSuccess: either start routes service, or remove down file
func (h *handler) onSuccess() error {
	if h.mode == modeDownFile {
		downFile := path.Join(runOpts.rootMount, downFileDir, fmt.Sprintf("%s.down", h.vip))
		err := os.Remove(downFile)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove downfile (%s): %v", downFile, err)
		}
		glog.Infof("healthcheck succeeded, removed downfile %s", downFile)
	} else {
		if err := exec.Command("systemctl", "start", runOpts.gcpRoutesService).Run(); err != nil {
			return fmt.Errorf("Failed to terminate gcp routes service %v", err)
		}
		glog.Infof("healthcheck succeeded, started %s", runOpts.gcpRoutesService)
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
