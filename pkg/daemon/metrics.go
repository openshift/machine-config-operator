package daemon

import (
	"context"
	"net/http"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// DefaultBindAddress is the port for the metrics listener
	DefaultBindAddress = ":8797"

	// HostOS shows os that MCD is running on and version if RHCOS
	HostOS = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_host_os_and_version",
			Help: "os that MCD is running on and version if RHCOS",
		}, []string{"os", "version"})

	// MCDSSHAccessed shows ssh access count for a node
	MCDSSHAccessed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "ssh_accessed",
			Help: "indicates whether ssh access attempt",
		})

	// MCDDrainErr logs errors received during failed drain
	MCDDrainErr = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_drain",
			Help: "errors from failed drain",
		}, []string{"drain_time", "err"})

	// MCDPivotErr shows errors encountered during pivot
	MCDPivotErr = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_pivot_err",
			Help: "errors encountered during pivot",
		}, []string{"pivot_target", "err"})

	// MCDState is state of mcd for indicated node (ex: degraded)
	MCDState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_state",
			Help: "state of daemon on specified node",
		}, []string{"state", "reason"})

	// KubeletHealthState logs kubelet health failures and tallys count
	KubeletHealthState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_kubelet_state",
			Help: "state of kubelet health monitor",
		}, []string{"err"})

	// MCDRebootErr logs success/failure of reboot and errors
	MCDRebootErr = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_reboot_err",
		}, []string{"message", "err"})

	// MCDUpdateState logs completed update or error
	MCDUpdateState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_update_state",
			Help: "completed update config or error",
		}, []string{"config", "err"})

	metricsList = []prometheus.Collector{
		HostOS,
		MCDSSHAccessed,
		MCDDrainErr,
		MCDPivotErr,
		MCDState,
		KubeletHealthState,
		MCDRebootErr,
		MCDUpdateState,
	}
)

func registerMCDMetrics() error {
	for _, metric := range metricsList {
		err := prometheus.Register(metric)
		if err != nil {
			return err
		}
	}

	MCDDrainErr.WithLabelValues("", "").Set(0)
	MCDPivotErr.WithLabelValues("", "").Set(0)
	KubeletHealthState.WithLabelValues("").Set(0)
	MCDRebootErr.WithLabelValues("", "").Set(0)
	MCDUpdateState.WithLabelValues("", "").Set(0)

	return nil
}

// StartMetricsListener is metrics listener via http on localhost
func StartMetricsListener(addr string, stopCh chan struct{}) {
	if addr == "" {
		addr = DefaultBindAddress
	}

	glog.Info("Registering Prometheus metrics")
	if err := registerMCDMetrics(); err != nil {
		glog.Errorf("unable to register metrics: %v", err)
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
	if err := s.Shutdown(context.Background()); err != http.ErrServerClosed {
		glog.Errorf("error stopping metrics listener: %v", err)
	}
}
