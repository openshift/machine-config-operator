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

	// mcdSSHAccessed shows ssh access count for a node
	mcdSSHAccessed = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "ssh_accesses_total",
			Help: "Total number of SSH access occurred.",
		})

	// mcdDrainErr logs failed drain
	mcdDrainErr = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "mcd_drain_err",
			Help: "logs failed drain",
		})

	// mcdPivotErr flags error encountered during pivot
	mcdPivotErr = prometheus.NewCounter(
		prometheus.CounterOpts{
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
)

func RegisterMCDMetrics() error {
	err := ctrlcommon.RegisterMetrics([]prometheus.Collector{
		hostOS,
		mcdSSHAccessed,
		mcdDrainErr,
		mcdPivotErr,
		mcdState,
		kubeletHealthState,
		mcdRebootErr,
		mcdUpdateState,
	})

	if err != nil {
		return fmt.Errorf("could not register MCC metrics: %w", err)
	}

	mcdDrainErr.Set(0)
	kubeletHealthState.Set(0)
	mcdUpdateState.WithLabelValues("", "").Set(0)

	return nil
}
