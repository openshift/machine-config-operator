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

	// MCDDrain shows drain duration in seconds if successful or error
	MCDDrain = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_drain",
		}, []string{"message", "err"})
)

func registerMCDMetrics() {

	prometheus.MustRegister(HostOS)
	prometheus.MustRegister(MCDSSHAccessed)
	prometheus.MustRegister(MCDDrain)

	MCDDrain.WithLabelValues("", "").Set(0)

}

// StartMetricsListener is metrics listener via http on localhost
func StartMetricsListener(addr string, stopCh chan struct{}) {
	if addr == "" {
		addr = DefaultBindAddress
	}

	glog.Info("Registering Prometheus metrics")
	registerMCDMetrics()

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
