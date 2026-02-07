package util

import (
	"strings"

	o "github.com/onsi/gomega"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// DebugNodeWithChroot creates a debugging session of the node with chroot
func DebugNodeWithChroot(oc *CLI, nodeName string, cmd ...string) (string, error) {
	stdOut, stdErr, err := debugNode(oc, nodeName, []string{}, true, true, cmd...)
	return strings.Join([]string{stdOut, stdErr}, "\n"), err
}

// DebugNodeWithOptions launches debug container with options e.g. --image
func DebugNodeWithOptions(oc *CLI, nodeName string, options []string, cmd ...string) (string, error) {
	stdOut, stdErr, err := debugNode(oc, nodeName, options, false, true, cmd...)
	return strings.Join([]string{stdOut, stdErr}, "\n"), err
}

// DebugNodeWithOptionsAndChroot launches debug container using chroot and with options e.g. --image
func DebugNodeWithOptionsAndChroot(oc *CLI, nodeName string, options []string, cmd ...string) (string, error) {
	stdOut, stdErr, err := debugNode(oc, nodeName, options, true, true, cmd...)
	return strings.Join([]string{stdOut, stdErr}, "\n"), err
}

// DebugNode creates a debugging session of the node
func DebugNode(oc *CLI, nodeName string, cmd ...string) (string, error) {
	stdOut, stdErr, err := debugNode(oc, nodeName, []string{}, false, true, cmd...)
	return strings.Join([]string{stdOut, stdErr}, "\n"), err
}

//nolint:unparam
func debugNode(oc *CLI, nodeName string, cmdOptions []string, needChroot, recoverNsLabels bool, cmd ...string) (stdOut, stdErr string, err error) {
	var (
		debugNodeNamespace string
		isNsPrivileged     bool
		cargs              []string
		outputError        error
	)
	cargs = []string{"node/" + nodeName}
	// Enhance for debug node namespace used logic
	// if "--to-namespace=" option is used, then uses the input options' namespace, otherwise use oc.Namespace()
	// if oc.Namespace() is empty, uses "default" namespace instead
	hasToNamespaceInCmdOptions, index := StringsSliceElementsHasPrefix(cmdOptions, "--to-namespace=", false)
	if hasToNamespaceInCmdOptions {
		debugNodeNamespace = strings.TrimPrefix(cmdOptions[index], "--to-namespace=")
	} else {
		debugNodeNamespace = oc.Namespace()
		if debugNodeNamespace == "" {
			debugNodeNamespace = "default"
		}
	}
	// Running oc debug node command in normal projects
	// (normal projects mean projects that are not clusters default projects like: "openshift-xxx" et al)
	// need extra configuration on 4.12+ ocp test clusters
	// https://github.com/openshift/oc/blob/master/pkg/helpers/cmd/errors.go#L24-L29
	if !strings.HasPrefix(debugNodeNamespace, "openshift-") {
		isNsPrivileged, outputError = IsNamespacePrivileged(oc, debugNodeNamespace)
		if outputError != nil {
			return "", "", outputError
		}
		if !isNsPrivileged {
			if recoverNsLabels {
				defer RecoverNamespaceRestricted(oc, debugNodeNamespace)
			}
			outputError = SetNamespacePrivileged(oc, debugNodeNamespace)
			if outputError != nil {
				return "", "", outputError
			}
		}
	}

	// For default nodeSelector enabled test clusters we need to add the extra annotation to avoid the debug pod's
	// nodeSelector overwritten by the scheduler
	if IsDefaultNodeSelectorEnabled(oc) && !IsWorkerNode(oc, nodeName) && !IsSpecifiedAnnotationKeyExist(oc, "ns/"+debugNodeNamespace, "", `openshift.io/node-selector`) {
		AddAnnotationsToSpecificResource(oc, "ns/"+debugNodeNamespace, "", `openshift.io/node-selector=`)
		defer RemoveAnnotationFromSpecificResource(oc, "ns/"+debugNodeNamespace, "", `openshift.io/node-selector`)
	}

	if len(cmdOptions) > 0 {
		cargs = append(cargs, cmdOptions...)
	}
	if !hasToNamespaceInCmdOptions {
		cargs = append(cargs, "--to-namespace="+debugNodeNamespace)
	}
	if needChroot {
		cargs = append(cargs, "--", "chroot", "/host")
	} else {
		cargs = append(cargs, "--")
	}
	cargs = append(cargs, cmd...)
	return oc.AsAdmin().WithoutNamespace().Run("debug").Args(cargs...).Outputs()
}

// GetNodeListByLabel gets the node list by label
func GetNodeListByLabel(oc *CLI, labelKey string) []string {
	output, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("node", "-l", labelKey, "-o=jsonpath={.items[*].metadata.name}").Output()
	o.Expect(err).NotTo(o.HaveOccurred(), "Fail to get node with label %v, got error: %v\n", labelKey, err)
	nodeNameList := strings.Fields(output)
	return nodeNameList
}

// IsDefaultNodeSelectorEnabled judges whether the test cluster enabled the defaultNodeSelector
func IsDefaultNodeSelectorEnabled(oc *CLI) bool {
	defaultNodeSelector, getNodeSelectorErr := oc.AsAdmin().WithoutNamespace().Run("get").Args("scheduler", "cluster", "-o=jsonpath={.spec.defaultNodeSelector}").Output()
	if getNodeSelectorErr != nil && strings.Contains(defaultNodeSelector, `the server doesn't have a resource type`) {
		e2e.Logf("WARNING: The scheduler API is not supported on the test cluster")
		return false
	}
	o.Expect(getNodeSelectorErr).NotTo(o.HaveOccurred(), "Fail to get cluster scheduler defaultNodeSelector got error: %v\n", getNodeSelectorErr)
	return !strings.EqualFold(defaultNodeSelector, "")
}

// IsWorkerNode judges whether the node has the worker role
func IsWorkerNode(oc *CLI, nodeName string) bool {
	isWorker, _ := StringsSliceContains(GetNodeListByLabel(oc, `node-role.kubernetes.io/worker`), nodeName)
	return isWorker
}

// DeleteLabelFromNode delete the custom label from the node
func DeleteLabelFromNode(oc *CLI, node, label string) (string, error) {
	return oc.AsAdmin().WithoutNamespace().Run("label").Args("node", node, label+"-").Output()
}

// GetNodeHostname returns the cluster node hostname
func GetNodeHostname(oc *CLI, node string) (string, error) {
	hostname, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("node", node, "-o", "jsonpath='{..kubernetes\\.io/hostname}'").Output()
	return strings.Trim(hostname, "'"), err
}
