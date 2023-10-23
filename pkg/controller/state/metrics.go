package state

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

const (
	// DefaultBindAddress is the port for the metrics listener
	DefaultBindAddress = ":8797"
)

func DefineMetrics() map[string]prometheus.Collector {
	metrics := make(map[string]prometheus.Collector)

	// test metric
	metrics["msc_test"] = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "msc_test",
			Help: "Test whether msc can talk to prometheus",
		})

	metrics["msc_test_2"] = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "msc_test_2",
			Help: "Test whether msc can talk to prometheus",
		})

	metrics["msc_test_3"] = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "msc_test_3",
			Help: "Test whether msc can talk to prometheus",
		})

	metrics["msc_test_4"] = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "msc_test_4",
			Help: "Test whether msc can talk to prometheus",
		})

	// // MCC Metrics
	// // OSImageURLOverride tells whether cluster is using default OS image or has been overridden by user
	// metrics["os_image_url_override"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "os_image_url_override",
	// 		Help: "state of OS image override",
	// 	}, []string{"pool"})
	// // MCCDrainErr logs failed drain
	// metrics["mcc_drain_err"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mcc_drain_err",
	// 		Help: "logs failed drain",
	// 	}, []string{"node"})
	// // MCCPoolAlert logs when the pool configuration changes in a way the user should know.
	// metrics["mcc_pool_alert"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mcc_pool_alert",
	// 		Help: "pool status alert",
	// 	}, []string{"node"})

	// //MCD Metrics
	// // hostOS shows os that MCD is running on and version if RHCOS
	// metrics["mcd_host_os_and_version"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mcd_host_os_and_version",
	// 		Help: "os that MCD is running on and version if RHCOS",
	// 	}, []string{"os", "version"})
	// // mcdPivotErr flags error encountered during pivot
	// metrics["mcd_pivot_errors_total"] = prometheus.NewGauge(
	// 	prometheus.GaugeOpts{
	// 		Name: "mcd_pivot_errors_total",
	// 		Help: "Total number of errors encountered during pivot.",
	// 	})
	// // mcdState is state of mcd for indicated node (ex: degraded)
	// metrics["mcd_state"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mcd_state",
	// 		Help: "state of daemon on specified node",
	// 	}, []string{"state", "reason"})
	// // kubeletHealthState logs kubelet health failures and tallys count
	// metrics["mcd_kubelet_state"] = prometheus.NewGauge(
	// 	prometheus.GaugeOpts{
	// 		Name: "mcd_kubelet_state",
	// 		Help: "state of kubelet health monitor",
	// 	})
	// // mcdRebootErr tallys failed reboot attempts
	// metrics["mcd_reboots_failed_total"] = prometheus.NewCounter(
	// 	prometheus.CounterOpts{
	// 		Name: "mcd_reboots_failed_total",
	// 		Help: "Total number of reboots that failed.",
	// 	})
	// // mcdUpdateState logs completed update or error
	// metrics["mcd_update_state"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mcd_update_state",
	// 		Help: "completed update config or error",
	// 	}, []string{"config", "err"})

	// //MCO metrics
	// // mcoState is the state of the machine config operator
	// // pause, updated, updating, degraded
	// metrics["mco_state"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mco_state",
	// 		Help: "state of a specified pool",
	// 	}, []string{"pool", "state", "reason"})
	// // mcoMachineCount is the total number of nodes in the pool
	// metrics["mco_machine_count"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mco_machine_count",
	// 		Help: "total number of machines in a specified pool",
	// 	}, []string{"pool"})
	// // mcoUpdatedMachineCount is the updated machines in the pool
	// metrics["mco_updated_machine_count"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mco_updated_machine_count",
	// 		Help: "total number of updated machines in specified pool",
	// 	}, []string{"pool"})
	// // mcoDegradedMachineCount is the degraded machines in the pool
	// metrics["mco_degraded_machine_count"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mco_degraded_machine_count",
	// 		Help: "total number of degraded machines in specified pool",
	// 	}, []string{"pool"})
	// // mcoUnavailableMachineCount is the degraded machines in the pool
	// metrics["mco_unavailable_machine_count"] = prometheus.NewGaugeVec(
	// 	prometheus.GaugeOpts{
	// 		Name: "mco_unavailable_machine_count",
	// 		Help: "total number of unavailable machines in specified pool",
	// 	}, []string{"pool"})

	return metrics
}

func RegisterMetrics(userInput []string) error {
	metrics := DefineMetrics()
	for _, input := range userInput {
		err := prometheus.Register(metrics[input])
		if err != nil {
			return err
		}
	}
	return nil
}

// StartMetricsListener is metrics listener via http on localhost
func StartMetricsListener(addr string, stopCh <-chan struct{}, registerFunc func([]string) error, userInput []string) {
	if addr == "" {
		addr = DefaultBindAddress
	}

	klog.Info("Registering Prometheus metrics")
	if err := registerFunc(userInput); err != nil {
		klog.Errorf("unable to register metrics: %v", err)
		// No sense in continuing starting the listener if this fails
		return
	}

	klog.Infof("Starting metrics listener on %s", addr)
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	s := http.Server{Addr: addr, Handler: mux}

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
