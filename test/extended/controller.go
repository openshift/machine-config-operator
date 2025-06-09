package extended

import (
	"fmt"
	"strings"
	"time"

	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"
)

// Controller handles the functinalities related to the MCO controller pod
type Controller struct {
	oc             *exutil.CLI
	logsCheckPoint string
	podName        string
}

// NewController creates a new Controller struct
func NewController(oc *exutil.CLI) *Controller {
	return &Controller{oc: oc, logsCheckPoint: "", podName: ""}
}

// GetCachedPodName returns the cached value of the MCO controller pod name. If there is no value available it tries to execute a command to get the pod name from the cluster
func (mcc *Controller) GetCachedPodName() (string, error) {
	if mcc.podName == "" {
		podName, err := mcc.GetPodName()
		if err != nil {
			logger.Infof("Error trying to get the machine-config-controller pod name. Error: %s", err)
			return "", err
		}

		mcc.podName = podName
	}

	return mcc.podName, nil
}

// GetPodName executed a command to get the current pod name of the MCO controller pod. Updateds the cached value of the pod name
// This function refreshes the pod name cache
func (mcc *Controller) GetPodName() (string, error) {
	podName, err := mcc.oc.WithoutNamespace().Run("get").Args("pod", "-n", MachineConfigNamespace, "-l", ControllerLabel+"="+ControllerLabelValue, "-o", "jsonpath={.items[0].metadata.name}").Output()
	if err != nil {
		return "", err
	}
	mcc.podName = podName
	return podName, nil
}

// GetLogs returns the MCO controller logs. Logs generated before calling the function "IgnoreLogsBeforeNow" will not be returned
// This function can return big log so, please, try not to print the returned value in your tests
func (mcc *Controller) GetLogs() (string, error) {
	var (
		podAllLogs = ""
		err        error
	)

	err = Retry(5, 5*time.Second, func() error {
		podAllLogs, err = mcc.GetRawLogs()
		if err != nil {
			mcc.podName = ""
		}
		return err
	})

	if err != nil {
		return "", err
	}
	// Remove the logs before the check point
	return strings.Replace(podAllLogs, mcc.logsCheckPoint, "", 1), nil
}

// GetRawLogs return the controller pod's logs without removing the ignored logs part
func (mcc Controller) GetRawLogs() (string, error) {
	cachedPodName, err := mcc.GetCachedPodName()
	if err != nil {
		return "", err
	}
	if cachedPodName == "" {
		err := fmt.Errorf("Cannot get controller pod name. Failed getting MCO controller logs")
		logger.Errorf("Error getting controller pod name. Error: %s", err)
		return "", err
	}
	podAllLogs, err := exutil.GetSpecificPodLogs(mcc.oc, MachineConfigNamespace, ControllerContainer, cachedPodName, "")
	if err != nil {
		logger.Errorf("Error getting log lines. Error: %s", err)
		return "", err
	}

	return podAllLogs, nil
}

// HasAcquiredLease returns true if the controller acquired the lease properly
func (mcc Controller) HasAcquiredLease() (bool, error) {
	podAllLogs, err := mcc.GetRawLogs()
	if err != nil {
		return false, err
	}

	return strings.Contains(podAllLogs, "successfully acquired lease"), nil
}
