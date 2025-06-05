package extended

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	logger "github.com/openshift/machine-config-operator/test/extended/util/logext"

	o "github.com/onsi/gomega"
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

// IgnoreLogsBeforeNow when it is called all logs generated before calling it will be ignored by "GetLogs"
func (mcc *Controller) IgnoreLogsBeforeNow() error {
	mcc.logsCheckPoint = ""
	logsUptoNow, err := mcc.GetLogs()
	if err != nil {
		return err
	}
	mcc.logsCheckPoint = logsUptoNow

	return nil
}

// IgnoreLogsBeofreNowOrFail  when it is called all logs generated before calling it will be ignored by "GetLogs", if this method fails, the test is failed
func (mcc *Controller) IgnoreLogsBeforeNowOrFail() *Controller {
	err := mcc.IgnoreLogsBeforeNow()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error trying to ignore old logs in the MachineConfigController pod")

	return mcc
}

// StopIgnoringLogs when it is called "IgnoreLogsBeforeNow" effect will not be taken into account anymore, and "GetLogs" will return full logs in MCO controller
func (mcc *Controller) StopIgnoringLogs() {
	mcc.logsCheckPoint = ""
}

// GetIgnoredLogs returns the logs that will be ignored after calling "IgnoreLogsBeforeNow"
func (mcc Controller) GetIgnoredLogs() string {
	return mcc.logsCheckPoint
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

// GetLogsAsList returns the MCO controller logs as a list strings. One string per line
func (mcc Controller) GetLogsAsList() ([]string, error) {
	logs, err := mcc.GetLogs()
	if err != nil {
		return nil, err
	}

	return strings.Split(logs, "\n"), nil
}

// GetFilteredLogsAsList returns the filtered logs as a lit of strings, one string per line.
func (mcc Controller) GetFilteredLogsAsList(regex string) ([]string, error) {
	logs, err := mcc.GetLogsAsList()
	if err != nil {
		return nil, err
	}

	filteredLogs := []string{}
	for _, line := range logs {
		match, err := regexp.MatchString(regex, line)
		if err != nil {
			logger.Errorf("Error filtering log lines. Error: %s", err)
			return nil, err
		}

		if match {
			filteredLogs = append(filteredLogs, line)
		}
	}

	return filteredLogs, nil
}

// GetFilteredLogs returns the logs filtered by a regexp applied to every line. If the match is ok the log line is accepted.
// This function can return big log so, please, try not to print the returned value in your tests
func (mcc Controller) GetFilteredLogs(regex string) (string, error) {
	logs, err := mcc.GetFilteredLogsAsList(regex)
	if err != nil {
		return "", err
	}

	return strings.Join(logs, "\n"), nil
}

// RemovePod removes the  controller pod forcing the creation of a new one
func (mcc Controller) RemovePod() error {
	cachedPodName, err := mcc.GetCachedPodName()
	if err != nil {
		return err
	}
	if cachedPodName == "" {
		err := fmt.Errorf("Cannot get controller pod name. Failed getting MCO controller logs")
		logger.Errorf("Error getting controller pod name. Error: %s", err)
		return err
	}

	// remove the cached podname, since it will not be valid anymore
	mcc.podName = ""

	return mcc.oc.WithoutNamespace().Run("delete").Args("pod", "-n", MachineConfigNamespace, cachedPodName).Execute()
}

// GetNode return the node where the machine controller is running
func (mcc Controller) GetNode() (*Node, error) {
	controllerPodName, err := mcc.GetCachedPodName()
	if err != nil {
		return nil, err
	}

	controllerPod := NewNamespacedResource(mcc.oc, "pod", MachineConfigNamespace, controllerPodName)
	nodeName, err := controllerPod.Get(`{.spec.nodeName}`)
	if err != nil {
		return nil, err
	}

	return NewNode(mcc.oc, nodeName), nil
}

// GetPreviousLogs returns previous logs of mcc pod
func (mcc Controller) GetPreviousLogs() (string, error) {
	cachedPodName, err := mcc.GetCachedPodName()
	if err != nil {
		return "", err
	}
	if cachedPodName == "" {
		err := fmt.Errorf("Cannot get controller pod name. Failed getting MCO controller logs")
		logger.Errorf("Error getting controller pod name. Error: %s", err)
		return "", err
	}

	prevLogs, err := NewNamespacedResource(mcc.oc, "pod", MachineConfigNamespace, cachedPodName).Logs("-p")
	if err != nil {
		exitError, ok := err.(*exutil.ExitError)
		if ok {
			if strings.Contains(exitError.StdErr, "not found") {
				logger.Infof("There was no previous pod for %s", cachedPodName)
				return "", nil
			}
			logger.Infof("An execution error happened, but was not expected: %s", exitError.StdErr)
		}
		logger.Infof("Unexpected error while getting MCC previous logs: %s", err)
		return prevLogs, err
	}
	return prevLogs, nil
}

// checkMCCPanic fails the test case if a panic happened in the MCC
func checkMCCPanic(oc *exutil.CLI) {
	var (
		mcc = NewController(oc.AsAdmin())
	)

	exutil.By("Check MCC Logs for Panic is not produced")
	mccPrevLogs, err := mcc.GetPreviousLogs()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting previous MCC logs")

	o.Expect(mccPrevLogs).NotTo(o.Or(o.ContainSubstring("panic"), o.ContainSubstring("Panic")), "Panic is seen in MCC previous logs after deleting OCB resources:\n%s", mccPrevLogs)
	mccLogs, err := mcc.GetLogs()

	o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MCC logs")
	o.Expect(mccLogs).NotTo(o.Or(o.ContainSubstring("panic"), o.ContainSubstring("Panic")), "Panic is seen in MCC logs after deleting OCB resources:\n%s", mccLogs)

	logger.Infof("OK!\n")
}
