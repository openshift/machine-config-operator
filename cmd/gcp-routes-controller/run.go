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
	"os/exec"
	"os/signal"
	"strings"
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
		rootMount      string
		healthCheckURL string
	}
)

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.PersistentFlags().StringVar(&runOpts.rootMount, "root-mount", "/rootfs", "where the nodes root filesystem is mounted for chroot and file manipulation.")
	runCmd.PersistentFlags().StringVar(&runOpts.healthCheckURL, "health-check-url", "", "HTTP(s) URL for the health check")
}

func runRunCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	// To help debugging, immediately log version
	glog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	if runOpts.rootMount != "" {
		glog.Infof(`Calling chroot("%s")`, runOpts.rootMount)
		if err := syscall.Chroot(runOpts.rootMount); err != nil {
			return fmt.Errorf("Unable to chroot to %s: %s", runOpts.rootMount, err)
		}

		glog.V(2).Infof("Moving to / inside the chroot")
		if err := os.Chdir("/"); err != nil {
			return fmt.Errorf("Unable to change directory to /: %s", err)
		}
	}

	uri, err := url.Parse(runOpts.healthCheckURL)
	if err != nil {
		return fmt.Errorf("failed to parse health-check-url: %v", err)
	}
	if !uri.IsAbs() {
		return fmt.Errorf("invalid URI %q (no scheme)", uri)
	}

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
	tracker := &healthTracker{
		state:            unknownTrackerState,
		ErrCh:            errCh,
		SuccessThreshold: 2,
		FailureThreshold: 10,
		OnFailure:        func() error { _ = exec.Command("systemctl", "stop", "gcp-routes.service"); return RunDelRoutes() },
		OnSuccess:        runSetRoutes,
	}

	h := health.New()
	h.AddChecks([]*health.Config{{
		Name:       "dependency-check",
		Checker:    httpCheck,
		Interval:   time.Duration(5) * time.Second,
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
			_ = exec.Command("systemctl", "stop", "gcp-routes.service").Run()
			if err := RunDelRoutes(); err != nil {
				glog.Infof("Failed to terminate gcp routes service on signal: %s", err)
			} else {
				break
			}
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

func getRoutes() (map[string][]string, error) {
	routes := make(map[string][]string)
	const netPath = "network-interfaces/"
	ifs, err := getInstances(netPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get instances at %s: %v", netPath, err)
	}
	for _, vif := range ifs {
		macs, err := getInstances(netPath + vif + "mac")
		if err != nil {
			return nil, fmt.Errorf("failed to get instances at %s: %v", netPath+vif+"mac", err)
		}
		hwAddr := macs[0]
		fwipPath := netPath + vif + "forwarded-ips/"
		devName, err := getIfname(hwAddr)
		if err != nil {
			return nil, fmt.Errorf("failed to get interface name for %s: %v", hwAddr, err)
		}
		levels, err := getInstances(fwipPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get instances at %s: %v", fwipPath, err)
		}
		for _, level := range levels {
			fwips, err := getInstances(fwipPath + level)
			if err != nil {
				return nil, fmt.Errorf("failed to get instances at %s: %v", fwipPath+level, err)
			}
			glog.Info("Processing route for NIC " + vif + hwAddr + " as " + devName + " for " + strings.Join(fwips, ", "))
			routes[devName] = append(routes[devName], fwips...)
		}
	}

	return routes, nil
}

func getIfname(hwAddr string) (string, error) {
	devs, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("failed to list interfaces: %v", err)
	}
	for _, dev := range devs {
		if dev.HardwareAddr.String() == hwAddr {
			return dev.Name, nil
		}
	}

	return "", fmt.Errorf("interface not found")
}

func getInstances(instanceType string) ([]string, error) {
	const baseURL = "http://metadata.google.internal/computeMetadata/v1/instance/"

	uri, err := url.ParseRequestURI(baseURL + instanceType)
	if err != nil {
		return nil, fmt.Errorf("failed to parse request URI %s: %v", baseURL+instanceType, err)
	}

	req, err := http.NewRequest(http.MethodGet, uri.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %v", err)
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get HTTP response: %v", err)
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTTP response: %v", err)
	}

	return strings.Fields(string(body)), nil
}

func runSetRoutes() error {
	for true {
		routes, err := getRoutes()
		if err != nil {
			return fmt.Errorf("failed to get routes: %v", err)
		}
		for devName, fwips := range routes {
			cmd := fmt.Sprintf("ip route show dev %s table local proto 66 | awk '{print$2}'", devName)
			ipOutput, err := exec.Command("bash", "-c", cmd).Output()
			glog.Info("RunSetRoutes CMD: " + cmd + "; Output: " + string(ipOutput))
			if err != nil {
				return fmt.Errorf("failed to show routes with command '%s': %v", cmd, err)
			}
			ips := strings.Fields(string(ipOutput))
			for _, currentRoute := range ips {

				if !sliceContainsString(fwips, currentRoute) {
					glog.Info("Removing stale forwarded IP " + currentRoute + "/32")
					op, err := exec.Command("ip", "route", "del", currentRoute+"/32", "dev", devName, "table", "local", "proto", "66").Output()
					glog.Info("RunSetRoutes route del Output: " + string(op))
					if err != nil {
						return fmt.Errorf("failed to remove stale forwarded IP %s for device %s: %v", currentRoute, devName, err)
					}
				}
			}
			for _, fwip := range fwips {
				op, err := exec.Command("ip", "route", "replace", "to", "local", fwip, "dev", devName, "proto", "66").Output()
				glog.Info("RunSetRoutes route replace Output: " + string(op))
				if err != nil {
					return fmt.Errorf("failed to replace route to IP %s for device %s: %v", fwip, devName, err)
				}
			}
		}
		time.Sleep(30 * time.Second)
	}

	return nil
}

// RunDelRoutes deletes all GCP routes. This function is also used by the delete-routes subcommand of gcp-routes-controller
func RunDelRoutes() error {
	routes, err := getRoutes()
	if err != nil {
		return fmt.Errorf("failed to get routes: %v", err)
	}
	for devName, fwips := range routes {
		cmd := fmt.Sprintf("ip route show dev %s table local proto 66 | awk '{print$2}'", devName)
		ipOutput, err := exec.Command("bash", "-c", cmd).Output()
		glog.Info("RunDelRoutes CMD: " + cmd + "; Output: " + string(ipOutput))
		if err != nil {
			return fmt.Errorf("failed to show routes with command '%s': %v", cmd, err)
		}
		ips := strings.Fields(string(ipOutput))
		for _, currentRoute := range ips {
			if sliceContainsString(fwips, currentRoute) {
				glog.Info("Removing forwarded IP " + currentRoute + "/32")
				op, err := exec.Command("ip", "route", "del", currentRoute+"/32", "dev", devName, "table", "local", "proto", "66").Output()
				glog.Info("RunDelRoutes route del Output: " + string(op))
				if err != nil {
					return fmt.Errorf("failed to remove forwarded IP %s for device %s: %v", currentRoute, devName, err)
				}
			}
		}

	}

	return nil
}

func sliceContainsString(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}
