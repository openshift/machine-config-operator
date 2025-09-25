package daemon

import (
	"fmt"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/prometheus/client_golang/prometheus"
)

// MCD Metrics
var (
	// hostOS shows os that MCD is running on and version if RHCOS
	hostOS = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_host_os_and_version",
			Help: "os that MCD is running on and version if RHCOS",
		}, []string{"os", "version"})

	// mcdPivotErr flags error encountered during pivot
	mcdPivotErr = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcd_pivot_errors_total",
			Help: "Total number of errors encountered during pivot.",
		})

	// mcdState is state of mcd for indicated node (ex: degraded)
	mcdState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_state",
			Help: "state of daemon on specified node",
		}, []string{"state", "reason"})

	// kubeletHealthState logs kubelet health failures and tallys count
	kubeletHealthState = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcd_kubelet_state",
			Help: "state of kubelet health monitor",
		})

	// mcdRebootErr tallys failed reboot attempts
	mcdRebootErr = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "mcd_reboots_failed_total",
			Help: "Total number of reboots that failed.",
		})

	// mcdUpdateState logs completed update or error
	mcdUpdateState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_update_state",
			Help: "completed update config or error",
		}, []string{"config", "err"})

	// mcdConfigDrift logs a config drift
	mcdConfigDrift = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcd_config_drift",
			Help: "timestamp for config drift",
		})

	// unsupportedPackages counts the number of unsupported packages installed on the node, categorized by vendor
	unsupportedPackages = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mcd_local_unsupported_packages",
			Help: "Total number of locally layered unsupported packages installed on the node",
		},
		[]string{"node"})
)

// Updates metric with new labels & timestamp, deletes any existing
// gauges stored in the metric prior to doing so.
// More context: https://issues.redhat.com/browse/OCPBUGS-1662
// We are using these metrics as a node state logger, so it is undesirable
// to have multiple metrics of the same kind when the state changes.
func UpdateStateMetric(metric *prometheus.GaugeVec, labels ...string) {
	metric.Reset()
	metric.WithLabelValues(labels...).SetToCurrentTime()
}

func RegisterMCDMetrics() error {
	err := ctrlcommon.RegisterMetrics([]prometheus.Collector{
		hostOS,
		mcdPivotErr,
		mcdState,
		kubeletHealthState,
		mcdRebootErr,
		mcdUpdateState,
		mcdConfigDrift,
		unsupportedPackages,
	})

	if err != nil {
		return fmt.Errorf("could not register machine-config-daemon metrics: %w", err)
	}

	kubeletHealthState.Set(0)

	return nil
}
