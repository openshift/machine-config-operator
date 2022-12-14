package common

import (
	"context"
	"fmt"
	"net/http"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// DefaultBindAddress is the port for the metrics listener
	DefaultBindAddress = ":8797"
)

// MCC Metrics
var (
	// MachineConfigControllerPausedPoolKubeletCA logs when a certificate rotation is being held up by pause
	MachineConfigControllerPausedPoolKubeletCA = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "machine_config_controller_paused_pool_kubelet_ca",
			Help: "Set to the unix timestamp in utc of the current certificate expiry date if a certificate rotation is pending in specified paused pool",
		}, []string{"pool"})

	// OSImageURLOverride tells whether cluster is using default OS image or has been overridden by user
	OSImageURLOverride = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "os_image_url_override",
			Help: "state of OS image override",
		}, []string{"pool"})

	// MCCDrainErr logs failed drain
	MCCDrainErr = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcc_drain_err",
			Help: "logs failed drain",
		})
)

func RegisterMCCMetrics() error {
	err := RegisterMetrics([]prometheus.Collector{
		MachineConfigControllerPausedPoolKubeletCA,
		OSImageURLOverride,
		MCCDrainErr,
	})

	if err != nil {
		return fmt.Errorf("could not register machine-config-controller metrics: %w", err)
	}

	MCCDrainErr.Set(0)

	return nil
}

func RegisterMetrics(metrics []prometheus.Collector) error {
	for _, metric := range metrics {
		err := prometheus.Register(metric)
		if err != nil {
			return err
		}
	}

	return nil
}

// StartMetricsListener is metrics listener via http on localhost
func StartMetricsListener(addr string, stopCh <-chan struct{}, registerFunc func() error) {
	if addr == "" {
		addr = DefaultBindAddress
	}

	glog.Info("Registering Prometheus metrics")
	if err := registerFunc(); err != nil {
		glog.Errorf("unable to register metrics: %v", err)
		// No sense in continuing starting the listener if this fails
		return
	}

	glog.Infof("Starting metrics listener on %s", addr)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	s := http.Server{Addr: addr, Handler: mux}

	go func() {
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			glog.Errorf("metrics listener exited with error: %v", err)
		}
	}()
	<-stopCh
	if err := s.Shutdown(context.Background()); err != nil {
		if err != http.ErrServerClosed {
			glog.Errorf("error stopping metrics listener: %v", err)
		}
	} else {
		glog.Infof("Metrics listener successfully stopped")
	}
}
