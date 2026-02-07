package common

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

const (
	// DefaultBindAddress is the port for the metrics listener
	DefaultBindAddress = ":8797"
)

// MCC Metrics
var (
	// OSImageURLOverride tells whether cluster is using default OS image or has been overridden by user
	OSImageURLOverride = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "os_image_url_override",
			Help: "state of OS image override",
		}, []string{"pool"})

	// MCCBootImageSkewEnforcementNone indicates when boot image skew enforcement is disabled.
	// Set to 1 when mode is "None", 0 otherwise. A value of 1 indicates scaling operations may
	// not be successful
	MCCBootImageSkewEnforcementNone = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcc_boot_image_skew_enforcement_none",
			Help: "Set to 1 when boot image skew enforcement mode is None, indicating scaling may not be successful as bootimages are out of date",
		})

	// MCCDrainErr logs failed drain
	MCCDrainErr = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcc_drain_err",
			Help: "logs failed drain",
		}, []string{"node"})

	// MCCPoolAlert logs when the pool configuration changes in a way the user should know
	MCCPoolAlert = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcc_pool_alert",
			Help: "pool status alert",
		}, []string{"node"})

	// MCCSubControllerState logs the state of the subcontrollers of the MCC
	MCCSubControllerState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcc_sub_controller_state",
			Help: "state of sub-controllers in the MCC",
		}, []string{"subcontroller", "state", "object"})

	// MCCState is the state of the machine config controller
	// pause, updated, updating, degraded
	MCCState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mco_state",
			Help: "state of a specified pool",
		}, []string{"node", "pool", "state", "reason"})

	// MCCMachineCount is the total number of nodes in the pool
	MCCMachineCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mco_machine_count",
			Help: "total number of machines in a specified pool",
		}, []string{"pool"})

	// MCCUpdatedMachineCount is the updated machines in the pool
	MCCUpdatedMachineCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mco_updated_machine_count",
			Help: "total number of updated machines in specified pool",
		}, []string{"pool"})

	// MCCDegradedMachineCount is the degraded machines in the pool
	MCCDegradedMachineCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mco_degraded_machine_count",
			Help: "total number of degraded machines in specified pool",
		}, []string{"pool"})

	// MCCUnavailableMachineCount is the unavailable machines in the pool
	MCCUnavailableMachineCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mco_unavailable_machine_count",
			Help: "total number of unavailable machines in specified pool",
		}, []string{"pool"})
)

func RegisterMCCMetrics() error {
	err := RegisterMetrics([]prometheus.Collector{
		OSImageURLOverride,
		MCCDrainErr,
		MCCPoolAlert,
		MCCSubControllerState,
		MCCState,
		MCCMachineCount,
		MCCUpdatedMachineCount,
		MCCDegradedMachineCount,
		MCCUnavailableMachineCount,
		MCCBootImageSkewEnforcementNone,
	})

	if err != nil {
		return fmt.Errorf("could not register machine-config-controller metrics: %w", err)
	}

	// Initialize GaugeVecs to ensure that metrics of type GaugeVec are accessible from the dashboard even if without a logged value
	// Solution to OCPBUGS-20427: https://issues.redhat.com/browse/OCPBUGS-20427
	OSImageURLOverride.WithLabelValues("initialize").Set(0)
	MCCDrainErr.WithLabelValues("initialize").Set(0)
	MCCPoolAlert.WithLabelValues("initialize").Set(0)
	MCCSubControllerState.WithLabelValues("initialize", "initialize", "initialize").Set(0)

	return nil
}

func UpdateStateMetric(metric *prometheus.GaugeVec, labels ...string) {
	metric.WithLabelValues(labels...).SetToCurrentTime()
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
func StartMetricsListener(addr string, stopCh <-chan struct{}, registerFunc func() error, tlsMinVersion string, tlsCipherSuites []string) {
	if addr == "" {
		addr = DefaultBindAddress
	}

	klog.Info("Registering Prometheus metrics")
	if err := registerFunc(); err != nil {
		klog.Errorf("unable to register metrics: %v", err)
		// No sense in continuing starting the listener if this fails
		return
	}

	// Get TLS config from provided settings, or use defaults
	tlsConfig := GetGoTLSConfig(tlsMinVersion, tlsCipherSuites)

	klog.Infof("Starting metrics listener on %s with TLS min version: %s", addr, tlsMinVersion)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	s := http.Server{
		TLSConfig:    tlsConfig,
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		Addr:         addr,
		Handler:      mux}

	go func() {
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			klog.Errorf("metrics listener exited with error: %v", err)
		}
	}()
	<-stopCh
	if err := s.Shutdown(context.Background()); err != nil {
		if err != http.ErrServerClosed {
			klog.Errorf("error stopping metrics listener: %v", err)
		}
	} else {
		klog.Infof("Metrics listener successfully stopped")
	}
}
