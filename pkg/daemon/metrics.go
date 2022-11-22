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
			Name: "ssh_accessed_total",
			Help: "Total number of SSH access occurred.",
		})

	// MCDDrainErr logs failed drain
	MCDDrainErr = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcd_drain_err",
			Help: "logs failed drain",
		})

	// MCDPivotErr flags error encountered during pivot
	MCDPivotErr = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "mcd_pivot_errors_total",
			Help: "Total number of errors encountered during pivot.",
		})

	// MCDState is state of mcd for indicated node (ex: degraded)
	MCDState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_state",
			Help: "state of daemon on specified node",
		}, []string{"state", "reason"})

	// KubeletHealthState logs kubelet health failures and tallys count
	KubeletHealthState = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcd_kubelet_state",
			Help: "state of kubelet health monitor",
		})

	// MCDRebootErr tallys failed reboot attempts
	MCDRebootErr = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "mcd_reboots_failed_total",
			Help: "Total number of reboots that failed.",
		})

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

	MCDDrainErr.Set(0)
	KubeletHealthState.Set(0)
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
