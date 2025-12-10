package extended

import (
	"context"
	"fmt"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/test/e2e/framework"
	admissionapi "k8s.io/pod-security-admission/api"
	"k8s.io/utils/ptr"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	machineconfigclient "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

const (
	nodeSizingTestMCPName       = "node-sizing-test"
	nodeSizingTestKubeletName   = "auto-sizing-enabled"
	nodeSizingTestPodName       = "node-sizing-test"
	nodeSizingAutoSizingPodName = "node-sizing-autosizing-test"
	nodeSizingEnvFile           = "/etc/node-sizing-enabled.env"
	nodeSizingEnvHostFile       = "/host/etc/node-sizing-enabled.env"
	nodeSizingEnvKey            = "NODE_SIZING_ENABLED"
	mcpPoolLabel                = "machineconfiguration.openshift.io/pool"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial] Node sizing", func() {
	defer g.GinkgoRecover()

	f := framework.NewDefaultFramework("node-sizing")
	f.NamespacePodSecurityLevel = admissionapi.LevelPrivileged

	oc := exutil.NewCLI("mco-node-sizing", exutil.KubeConfigPath()).AsAdmin()

	g.It("should have NODE_SIZING_ENABLED=true by default and NODE_SIZING_ENABLED=false when KubeletConfig with autoSizingReserved=false is applied [Slow][Disruptive][apigroup:machineconfiguration.openshift.io]", func(ctx context.Context) {
		mcClient, err := machineconfigclient.NewForConfig(oc.KubeFramework().ClientConfig())
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating machine config client")

		testMCPLabel := fmt.Sprintf("%s%s", NodeRoleLabelPrefix, nodeSizingTestMCPName)

		// Select a worker node without custom role labels
		g.By("Getting a worker node to test")
		selectedNode := selectWorkerNodeWithoutCustomRoles(ctx, oc)
		nodeName := selectedNode.Name
		framework.Logf("Testing on node: %s", nodeName)

		// Label node for custom MCP
		g.By(fmt.Sprintf("Labeling node %s with %s", nodeName, testMCPLabel))
		labelNodeForTest(ctx, oc, nodeName, testMCPLabel)

		// Create custom MachineConfigPool
		g.By(fmt.Sprintf("Creating custom MachineConfigPool %s", nodeSizingTestMCPName))
		createCustomMachineConfigPool(ctx, mcClient, nodeSizingTestMCPName, testMCPLabel)

		// Add cleanup handlers
		registerMCPCleanup(ctx, mcClient, nodeSizingTestMCPName)
		registerNodeLabelCleanup(ctx, oc, mcClient, nodeName, testMCPLabel, nodeSizingTestMCPName)

		// Wait for custom MCP to be ready
		g.By("Waiting for custom MachineConfigPool to be ready")
		err = waitForMCPToBeReadyNodeSizing(ctx, mcClient, nodeSizingTestMCPName, 5*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), "Custom MachineConfigPool should become ready")

		// Setup namespace with privileged pod security
		namespace := oc.Namespace()
		g.By("Setting privileged pod security labels on namespace")
		err = oc.Run("label").Args("namespace", namespace, "pod-security.kubernetes.io/enforce=privileged", "pod-security.kubernetes.io/audit=privileged", "--overwrite").Execute()
		o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to label namespace with privileged pod security")

		// Verify default state (NODE_SIZING_ENABLED=true)
		g.By("Creating a privileged pod with /etc mounted to verify default state")
		pod := createPrivilegedPodWithHostEtcNodeSizing(nodeSizingTestPodName, namespace, nodeName)
		_, err = oc.KubeClient().CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to create privileged pod")

		g.By("Waiting for pod to be running")
		waitForPodRunningNodeSizing(ctx, oc, nodeSizingTestPodName, namespace)

		verifyNodeSizingEnabledFileContent(oc, nodeSizingTestPodName, namespace, nodeName, TrueString)

		g.By("Deleting the test pod before applying KubeletConfig")
		err = oc.KubeClient().CoreV1().Pods(namespace).Delete(ctx, nodeSizingTestPodName, metav1.DeleteOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to delete test pod")

		// Create KubeletConfig with autoSizingReserved=false
		g.By("Creating KubeletConfig with autoSizingReserved=false")
		createKubeletConfigForTest(ctx, mcClient, nodeSizingTestKubeletName, nodeSizingTestMCPName)

		// Add cleanup for KubeletConfig
		registerKubeletConfigCleanup(ctx, mcClient, nodeSizingTestKubeletName, nodeSizingTestMCPName)

		// Verify KubeletConfig was created
		g.By("Waiting for KubeletConfig to be created")
		verifyKubeletConfigCreated(ctx, mcClient, nodeSizingTestKubeletName)

		// Wait for MCP to start updating
		g.By(fmt.Sprintf("Waiting for %s MCP to start updating", nodeSizingTestMCPName))
		waitForMCPToStartUpdating(ctx, mcClient, nodeSizingTestMCPName)

		// Wait for MCP to be ready with new configuration
		g.By(fmt.Sprintf("Waiting for %s MCP to be ready with new configuration", nodeSizingTestMCPName))
		err = waitForMCPToBeReadyNodeSizing(ctx, mcClient, nodeSizingTestMCPName, 15*time.Minute)
		o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("%s MCP should become ready with new configuration", nodeSizingTestMCPName))

		// Verify NODE_SIZING_ENABLED=false after KubeletConfig is applied
		g.By("Creating a second privileged pod with /etc mounted to verify KubeletConfig was applied")
		pod = createPrivilegedPodWithHostEtcNodeSizing(nodeSizingAutoSizingPodName, namespace, nodeName)
		_, err = oc.KubeClient().CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to create privileged pod")

		// Add cleanup for test pod
		registerPodCleanup(ctx, oc, nodeSizingAutoSizingPodName, namespace)

		g.By("Waiting for pod to be running")
		waitForPodRunningNodeSizing(ctx, oc, nodeSizingAutoSizingPodName, namespace)

		verifyNodeSizingEnabledFileContent(oc, nodeSizingAutoSizingPodName, namespace, nodeName, FalseString)

		// Execute cleanup synchronously before test completes to ensure cluster is in clean state
		// DeferCleanup will still run as a safety net in case this cleanup fails
		cleanupSynchronously(ctx, oc, mcClient, nodeSizingAutoSizingPodName, namespace, nodeSizingTestKubeletName, nodeSizingTestMCPName, nodeName, testMCPLabel)
	})
})

// selectWorkerNodeWithoutCustomRoles selects a worker node that doesn't have custom role labels
func selectWorkerNodeWithoutCustomRoles(ctx context.Context, oc *exutil.CLI) *corev1.Node {
	workerLabelSelector := fmt.Sprintf("%s%s", NodeRoleLabelPrefix, common.MachineConfigPoolWorker)
	nodes, err := oc.KubeClient().CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: workerLabelSelector,
	})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to list worker nodes")
	o.Expect(len(nodes.Items)).To(o.BeNumerically(">", 0), "Should have at least one worker node")

	g.By("Selecting a worker node without custom role labels")
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if !hasCustomRoleLabels(node) {
			return node
		}
	}

	o.Expect(false).To(o.BeTrue(), "Should find at least one worker node without custom role labels")
	return nil // This line will never be reached due to the Expect above
}

// hasCustomRoleLabels checks if a node has multiple role labels, which indicates custom roles
func hasCustomRoleLabels(node *corev1.Node) bool {
	roleCount := 0
	for labelKey := range node.Labels {
		if strings.HasPrefix(labelKey, NodeRoleLabelPrefix) {
			roleCount++
			if roleCount > 1 {
				framework.Logf("Skipping node %s: has multiple role labels", node.Name)
				return true
			}
		}
	}
	return false
}

// labelNodeForTest labels a node with the test MCP label
func labelNodeForTest(ctx context.Context, oc *exutil.CLI, nodeName, label string) {
	node, err := oc.KubeClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to get node")

	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels[label] = ""
	_, err = oc.KubeClient().CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to label node")
}

// createCustomMachineConfigPool creates a custom MachineConfigPool for testing
func createCustomMachineConfigPool(ctx context.Context, mcClient *machineconfigclient.Clientset, mcpName, nodeLabel string) {
	testMCP := &mcfgv1.MachineConfigPool{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "MachineConfigPool",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: mcpName,
			Labels: map[string]string{
				mcpPoolLabel: mcpName,
			},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			MachineConfigSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      mcfgv1.MachineConfigRoleLabelKey,
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{common.MachineConfigPoolWorker, mcpName},
					},
				},
			},
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					nodeLabel: "",
				},
			},
		},
	}

	_, err := mcClient.MachineconfigurationV1().MachineConfigPools().Create(ctx, testMCP, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to create custom MachineConfigPool")
}

// createKubeletConfigForTest creates a KubeletConfig with autoSizingReserved=false
func createKubeletConfigForTest(ctx context.Context, mcClient *machineconfigclient.Clientset, kubeletConfigName, mcpName string) {
	autoSizingReserved := false
	kubeletConfig := &mcfgv1.KubeletConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "KubeletConfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: kubeletConfigName,
		},
		Spec: mcfgv1.KubeletConfigSpec{
			AutoSizingReserved: &autoSizingReserved,
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					mcpPoolLabel: mcpName,
				},
			},
		},
	}

	_, err := mcClient.MachineconfigurationV1().KubeletConfigs().Create(ctx, kubeletConfig, metav1.CreateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to create KubeletConfig")
}

// verifyKubeletConfigCreated verifies that the KubeletConfig was created with correct settings
func verifyKubeletConfigCreated(ctx context.Context, mcClient *machineconfigclient.Clientset, kubeletConfigName string) {
	var createdKC *mcfgv1.KubeletConfig
	var err error
	o.Eventually(func() error {
		createdKC, err = mcClient.MachineconfigurationV1().KubeletConfigs().Get(ctx, kubeletConfigName, metav1.GetOptions{})
		return err
	}, 30*time.Second, 5*time.Second).Should(o.Succeed(), "KubeletConfig should be created")

	o.Expect(createdKC.Spec.AutoSizingReserved).NotTo(o.BeNil(), "AutoSizingReserved should not be nil")
	o.Expect(*createdKC.Spec.AutoSizingReserved).To(o.BeFalse(), "AutoSizingReserved should be false")
}

// waitForMCPToStartUpdating waits for the MCP to start updating
func waitForMCPToStartUpdating(ctx context.Context, mcClient *machineconfigclient.Clientset, mcpName string) {
	o.Eventually(func() bool {
		mcp, err := mcClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, mcpName, metav1.GetOptions{})
		if err != nil {
			framework.Logf("Error getting %s MCP: %v", mcpName, err)
			return false
		}
		// Check if MCP is updating (has conditions indicating update in progress)
		for _, condition := range mcp.Status.Conditions {
			if condition.Type == mcfgv1.MachineConfigPoolUpdating && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, 2*time.Minute, 10*time.Second).Should(o.BeTrue(), fmt.Sprintf("%s MCP should start updating", mcpName))
}

// registerMCPCleanup registers a DeferCleanup handler for MachineConfigPool deletion
func registerMCPCleanup(ctx context.Context, mcClient *machineconfigclient.Clientset, mcpName string) {
	g.DeferCleanup(func() {
		g.By("Cleaning up custom MachineConfigPool")
		deleteErr := mcClient.MachineconfigurationV1().MachineConfigPools().Delete(ctx, mcpName, metav1.DeleteOptions{})
		if deleteErr != nil {
			framework.Logf("Failed to delete MachineConfigPool %s: %v", mcpName, deleteErr)
		}
	})
}

// registerNodeLabelCleanup registers a DeferCleanup handler for node label removal and transition back to worker pool
func registerNodeLabelCleanup(ctx context.Context, oc *exutil.CLI, mcClient *machineconfigclient.Clientset, nodeName, label, mcpName string) {
	g.DeferCleanup(func() {
		g.By(fmt.Sprintf("Removing node label %s from node %s", label, nodeName))
		node, getErr := oc.KubeClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if getErr != nil {
			framework.Logf("Failed to get node for cleanup: %v", getErr)
			return
		}

		delete(node.Labels, label)
		_, updateErr := oc.KubeClient().CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		if updateErr != nil {
			framework.Logf("Failed to remove label from node %s: %v", nodeName, updateErr)
			return
		}

		// Wait for the node to transition back to the worker pool configuration
		g.By(fmt.Sprintf("Waiting for node %s to transition back to worker pool", nodeName))
		waitForNodeToTransitionToWorkerPool(ctx, oc, nodeName, mcpName)

		// Additionally wait for the worker MCP to be ready and updated
		g.By("Waiting for worker MachineConfigPool to be ready after node transition")
		waitErr := waitForMCPToBeReadyNodeSizing(ctx, mcClient, common.MachineConfigPoolWorker, 5*time.Minute)
		if waitErr != nil {
			framework.Logf("Warning: worker MCP may not be fully ready after node transition: %v", waitErr)
		}
	})
}

// registerKubeletConfigCleanup registers a DeferCleanup handler for KubeletConfig deletion
func registerKubeletConfigCleanup(ctx context.Context, mcClient *machineconfigclient.Clientset, kubeletConfigName, mcpName string) {
	g.DeferCleanup(func() {
		g.By("Cleaning up KubeletConfig")
		deleteErr := mcClient.MachineconfigurationV1().KubeletConfigs().Delete(ctx, kubeletConfigName, metav1.DeleteOptions{})
		if deleteErr != nil {
			framework.Logf("Failed to delete KubeletConfig %s: %v", kubeletConfigName, deleteErr)
		}

		// Wait for custom MCP to be ready after cleanup
		g.By("Waiting for custom MCP to be ready after KubeletConfig deletion")
		waitErr := waitForMCPToBeReadyNodeSizing(ctx, mcClient, mcpName, 10*time.Minute)
		if waitErr != nil {
			framework.Logf("Failed to wait for custom MCP to be ready: %v", waitErr)
		}
	})
}

// registerPodCleanup registers a DeferCleanup handler for pod deletion
func registerPodCleanup(ctx context.Context, oc *exutil.CLI, podName, namespace string) {
	g.DeferCleanup(func() {
		g.By("Cleaning up test pod")
		deleteErr := oc.KubeClient().CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
		if deleteErr != nil {
			framework.Logf("Failed to delete pod %s: %v", podName, deleteErr)
		}
	})
}

// waitForNodeToTransitionToWorkerPool waits for a node to transition back to worker pool configuration
func waitForNodeToTransitionToWorkerPool(ctx context.Context, oc *exutil.CLI, nodeName, mcpName string) {
	o.Eventually(func() bool {
		currentNode, err := oc.KubeClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			framework.Logf("Error getting node: %v", err)
			return false
		}
		currentConfig := currentNode.Annotations[CurrentMachineConfigAnnotationKey]
		desiredConfig := currentNode.Annotations[DesiredMachineConfigAnnotationKey]
		state := currentNode.Annotations[MachineConfigDaemonStateAnnotationKey]

		// Check if the node is using a worker config (not node-sizing-test config)
		// and is in Done state (not Updating, Degraded, etc.)
		isWorkerConfig := currentConfig != "" &&
			!strings.Contains(currentConfig, mcpName) &&
			currentConfig == desiredConfig &&
			state == MachineConfigDaemonStateDone

		if isWorkerConfig {
			framework.Logf("Node %s successfully transitioned to worker config: %s (state: %s)", nodeName, currentConfig, state)
		} else {
			framework.Logf("Node %s still transitioning: current=%s, desired=%s, state=%s", nodeName, currentConfig, desiredConfig, state)
		}
		return isWorkerConfig
	}, 10*time.Minute, 10*time.Second).Should(o.BeTrue(), fmt.Sprintf("Node %s should transition back to worker pool", nodeName))
}

// cleanupSynchronously performs synchronous cleanup to ensure cluster is in clean state before test completes
func cleanupSynchronously(ctx context.Context, oc *exutil.CLI, mcClient *machineconfigclient.Clientset, podName, namespace, kubeletConfigName, mcpName, nodeName, nodeLabel string) {
	g.By("Cleaning up test pod")
	err := oc.KubeClient().CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to delete test pod")

	g.By("Cleaning up KubeletConfig")
	err = mcClient.MachineconfigurationV1().KubeletConfigs().Delete(ctx, kubeletConfigName, metav1.DeleteOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to delete KubeletConfig")

	g.By("Waiting for custom MCP to be ready after KubeletConfig deletion")
	err = waitForMCPToBeReadyNodeSizing(ctx, mcClient, mcpName, 10*time.Minute)
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("%s MCP should become ready after KubeletConfig deletion", mcpName))

	g.By(fmt.Sprintf("Removing node label %s from node %s", nodeLabel, nodeName))
	node, err := oc.KubeClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to get node for cleanup")

	delete(node.Labels, nodeLabel)
	_, err = oc.KubeClient().CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("Should be able to remove label from node %s", nodeName))

	g.By(fmt.Sprintf("Waiting for node %s to transition back to worker pool", nodeName))
	waitForNodeToTransitionToWorkerPool(ctx, oc, nodeName, mcpName)

	g.By("Waiting for worker MachineConfigPool to be ready after node transition")
	err = waitForMCPToBeReadyNodeSizing(ctx, mcClient, common.MachineConfigPoolWorker, 5*time.Minute)
	o.Expect(err).NotTo(o.HaveOccurred(), "Worker MCP should be ready after node transition")

	g.By("Cleaning up custom MachineConfigPool")
	err = mcClient.MachineconfigurationV1().MachineConfigPools().Delete(ctx, mcpName, metav1.DeleteOptions{})
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to delete custom MachineConfigPool")

	framework.Logf("Successfully completed cleanup for node sizing test")
}

// createPrivilegedPodWithHostEtcNodeSizing creates a privileged pod with /etc mounted
func createPrivilegedPodWithHostEtcNodeSizing(podName, namespace, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "test-container",
					Image:   "image-registry.openshift-image-registry.svc:5000/openshift/tools:latest",
					Command: []string{"/bin/sh", "-c", "sleep 300"},
					SecurityContext: &corev1.SecurityContext{
						Privileged: ptr.To(true),
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "host-etc",
							MountPath: "/host/etc",
							ReadOnly:  true,
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "host-etc",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/etc",
						},
					},
				},
			},
		},
	}
}

// waitForPodRunningNodeSizing waits for a pod to reach running state
func waitForPodRunningNodeSizing(ctx context.Context, oc *exutil.CLI, podName, namespace string) {
	o.Eventually(func() bool {
		p, err := oc.KubeClient().CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return p.Status.Phase == corev1.PodRunning
	}, "2m", "5s").Should(o.BeTrue(), "Pod should be running")
}

// verifyNodeSizingEnabledFileContent verifies the NODE_SIZING_ENABLED value in the env file
func verifyNodeSizingEnabledFileContent(oc *exutil.CLI, podName, namespace, nodeName, expectedValue string) {
	g.By("Verifying /etc/node-sizing-enabled.env file exists")
	output, err := oc.Run("exec").Args(podName, "-n", namespace, "--", "test", "-f", nodeSizingEnvHostFile).Output()
	o.Expect(err).NotTo(o.HaveOccurred(), fmt.Sprintf("File %s should exist on node %s. Output: %s", nodeSizingEnvFile, nodeName, output))

	g.By("Reading /etc/node-sizing-enabled.env file contents")
	output, err = oc.Run("exec").Args(podName, "-n", namespace, "--", "cat", nodeSizingEnvHostFile).Output()
	o.Expect(err).NotTo(o.HaveOccurred(), "Should be able to read /etc/node-sizing-enabled.env")

	framework.Logf("Contents of %s:\n%s", nodeSizingEnvFile, output)

	g.By(fmt.Sprintf("Verifying %s=%s is set in the file", nodeSizingEnvKey, expectedValue))
	o.Expect(strings.TrimSpace(output)).To(o.ContainSubstring(fmt.Sprintf("%s=%s", nodeSizingEnvKey, expectedValue)),
		fmt.Sprintf("File should contain %s=%s", nodeSizingEnvKey, expectedValue))

	framework.Logf("Successfully verified %s=%s on node %s", nodeSizingEnvKey, expectedValue, nodeName)
}

// waitForMCPToBeReadyNodeSizing waits for a MachineConfigPool to be ready
func waitForMCPToBeReadyNodeSizing(ctx context.Context, mcClient *machineconfigclient.Clientset, poolName string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 10*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		mcp, err := mcClient.MachineconfigurationV1().MachineConfigPools().Get(ctx, poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		// Check if all conditions are met for a ready state
		updating := false
		degraded := false
		ready := false

		for _, condition := range mcp.Status.Conditions {
			switch condition.Type {
			case mcfgv1.MachineConfigPoolUpdating:
				if condition.Status == corev1.ConditionTrue {
					updating = true
				}
			case mcfgv1.MachineConfigPoolDegraded:
				if condition.Status == corev1.ConditionTrue {
					degraded = true
				}
			case mcfgv1.MachineConfigPoolUpdated:
				if condition.Status == corev1.ConditionTrue {
					ready = true
				}
			}
		}

		if degraded {
			return false, fmt.Errorf("MachineConfigPool %s is degraded", poolName)
		}

		// Ready when not updating and updated condition is true
		isReady := !updating && ready && mcp.Status.ReadyMachineCount == mcp.Status.MachineCount

		if isReady {
			framework.Logf("MachineConfigPool %s is ready: %d/%d machines ready",
				poolName, mcp.Status.ReadyMachineCount, mcp.Status.MachineCount)
		} else {
			framework.Logf("MachineConfigPool %s not ready yet: updating=%v, ready=%v, machines=%d/%d",
				poolName, updating, ready, mcp.Status.ReadyMachineCount, mcp.Status.MachineCount)
		}

		return isReady, nil
	})
}
