package util

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// Pod Parameters can be used to set the template parameters except PodName as PodName can be provided using pod.Name
type Pod struct {
	Name       string
	Namespace  string
	Template   string
	Parameters []string
}

// Create creates a pod on the basis of Pod struct
// For Ex: pod := Pod{Name: "PodName", Namespace: "NSName", Template: "PodTemplateLocation", Parameters: []string{"HOSTNAME=NODE_IP"}}
// pod.Create(oc)
// The pod name parameter must be NAME in the template file
func (pod *Pod) Create(oc *CLI) {
	e2e.Logf("Creating pod: %s", pod.Name)
	params := []string{"--ignore-unknown-parameters=true", "-f", pod.Template, "-p", "NAME=" + pod.Name}
	CreateNsResourceFromTemplate(oc, pod.Namespace, append(params, pod.Parameters...)...)
	AssertPodToBeReady(oc, pod.Name, pod.Namespace)
}

// Delete pod
func (pod *Pod) Delete(oc *CLI) error {
	e2e.Logf("Deleting pod: %s", pod.Name)
	return oc.AsAdmin().WithoutNamespace().Run("delete").Args("pod", pod.Name, "-n", pod.Namespace, "--ignore-not-found=true").Execute()

}

// AssertPodToBeReady poll pod status to determine it is ready
func AssertPodToBeReady(oc *CLI, podName, namespace string) {
	err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, 3*time.Minute, false, func(_ context.Context) (bool, error) {
		stdout, err := oc.AsAdmin().Run("get").Args("pod", podName, "-n", namespace, "-o", "jsonpath='{.status.conditions[?(@.type==\"Ready\")].status}'").Output()
		if err != nil {
			e2e.Logf("the err:%v, and try next round", err)
			return false, nil
		}
		if strings.Contains(stdout, "True") {
			e2e.Logf("Pod %s is ready!", podName)
			return true, nil
		}
		return false, nil
	})
	AssertWaitPollNoErr(err, fmt.Sprintf("Pod %s status is not ready!", podName))
}

// GetSpecificPodLogs returns the pod logs by the specific filter
func GetSpecificPodLogs(oc *CLI, namespace, container, podName, filter string) (string, error) {
	return GetSpecificPodLogsCombinedOrNot(oc, namespace, container, podName, filter, false)
}

// GetSpecificPodLogsCombinedOrNot returns the pod logs by the specific filter with combining stderr or not
func GetSpecificPodLogsCombinedOrNot(oc *CLI, namespace, container, podName, filter string, combined bool) (string, error) {
	var cargs []string
	if container != "" {
		cargs = []string{"-n", namespace, "-c", container, podName}
	} else {
		cargs = []string{"-n", namespace, podName}
	}
	podLogs, err := oc.AsAdmin().WithoutNamespace().Run("logs").Args(cargs...).OutputToFile("podLogs.txt")
	if err != nil {
		e2e.Logf("unable to get the pod (%s) logs", podName)
		return podLogs, err
	}
	var filterCmd = ""
	if filter != "" {
		filterCmd = " | grep -i " + filter
	}
	var filteredLogs []byte
	var errCmd error
	if combined {
		filteredLogs, errCmd = exec.Command("bash", "-c", "cat "+podLogs+filterCmd).CombinedOutput()
	} else {
		filteredLogs, errCmd = exec.Command("bash", "-c", "cat "+podLogs+filterCmd).Output()
	}
	return string(filteredLogs), errCmd
}

// GetPodName returns the pod name
func GetPodName(oc *CLI, namespace, podLabel, node string) (string, error) {
	args := []string{"pods", "-n", namespace, "-l", podLabel,
		"--field-selector", "spec.nodeName=" + node, "-o", "jsonpath='{..metadata.name}'"}
	daemonPod, err := oc.AsAdmin().WithoutNamespace().Run("get").Args(args...).Output()
	return strings.ReplaceAll(daemonPod, "'", ""), err
}

// AssertAllPodsToBeReadyWithPollerParams assert all pods in NS are in ready state until timeout in a given namespace
// Pros: allow user to customize poller parameters
func AssertAllPodsToBeReadyWithPollerParams(oc *CLI, namespace string, interval, timeout time.Duration, selector string) {
	err := wait.PollUntilContextTimeout(context.Background(), interval, timeout, false, func(_ context.Context) (bool, error) {

		// get the status flag for all pods
		// except the ones which are in Complete Status.
		// it use 'ne' operator which is only compatible with 4.10+ oc versions
		template := "'{{- range .items -}}{{- range .status.conditions -}}{{- if ne .reason \"PodCompleted\" -}}{{- if eq .type \"Ready\" -}}{{- .status}} {{\" \"}}{{- end -}}{{- end -}}{{- end -}}{{- end -}}'"
		baseArgs := []string{"pods", "-n", namespace}
		if selector != "" {
			baseArgs = append(baseArgs, "-l", selector)
		}
		stdout, err := oc.AsAdmin().Run("get").Args(baseArgs...).Template(template).Output()
		if err != nil {
			e2e.Logf("the err:%v, and try next round", err)
			return false, nil
		}
		if strings.Contains(stdout, "False") {
			return false, nil
		}
		return true, nil
	})
	AssertWaitPollNoErr(err, fmt.Sprintf("Some Pods are not ready in NS %s!", namespace))
}

// AssertAllPodsToBeReady assert all pods in NS are in ready state until timeout in a given namespace
func AssertAllPodsToBeReady(oc *CLI, namespace string) {
	AssertAllPodsToBeReadyWithPollerParams(oc, namespace, 10*time.Second, 4*time.Minute, "")
}

// AssertAllPodsToBeReadyWithSelector assert all pods in NS are in ready state until timeout in a given namespace
// The selector parameter follows the regular oc/kubectl format for the --selector option.
func AssertAllPodsToBeReadyWithSelector(oc *CLI, namespace, selector string) {
	AssertAllPodsToBeReadyWithPollerParams(oc, namespace, 10*time.Second, 4*time.Minute, selector)
}
