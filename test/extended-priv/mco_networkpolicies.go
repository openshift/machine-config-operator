package extended

import (
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Network Policies", func() {
	defer g.GinkgoRecover()

	var (
		oc = exutil.NewCLI("mco-network-policies", exutil.KubeConfigPath())
	)

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:89664] Verify MCO network policies enforce connectivity restrictions", func() {
		testNum := GetCurrentTestPolarionIDNumber()

		testPodName := "nettest"
		testPodImage := AlpineImage
		testPodResource := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, testPodName)

		defer func() {
			_ = testPodResource.Delete("--ignore-not-found=true")
		}()

		exutil.By("Create test pod with HTTP listener for network policy verification")
		err := oc.AsAdmin().WithoutNamespace().Run("run").Args(
			testPodName,
			fmt.Sprintf("--image=%s", testPodImage),
			"-n", MachineConfigNamespace,
			"--restart=Never",
			"--command", "--", "sh", "-c",
			"while true; do echo -e 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok' | nc -l -p 8080; done",
		).Execute()
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to create test pod")

		exutil.AssertPodToBeReady(oc, testPodName, MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Verify test pod listener is reachable from within the pod (positive control)")
		output, err := exutil.RemoteShPod(oc.AsAdmin(), MachineConfigNamespace, testPodName, "wget", "-q", "-O-", "--timeout=5", "http://127.0.0.1:8080")
		o.Expect(err).NotTo(o.HaveOccurred(), "Test pod HTTP listener is not working")
		o.Expect(output).To(o.Equal("ok"), "Unexpected response from test pod listener")
		logger.Infof("OK!\n")

		if !IsDisconnectedCluster(oc.AsAdmin()) {
			exutil.By("Verify test pod can reach external internet (egress allowed)")
			output, err = exutil.RemoteShPod(oc.AsAdmin(), MachineConfigNamespace, testPodName, "wget", "-q", "-O-", "--timeout=5", "https://google.com")
			o.Expect(err).NotTo(o.HaveOccurred(), "Test pod cannot reach external internet")
			o.Expect(output).NotTo(o.BeEmpty(), "Expected response from google.com")
			logger.Infof("OK!\n")
		} else {
			logger.Infof("Skipping external internet connectivity test in disconnected cluster\n")
		}

		exutil.By("Verify test pod can reach Kubernetes API server (egress allowed)")
		output, err = exutil.RemoteShPod(oc.AsAdmin(), MachineConfigNamespace, testPodName, "wget", "-q", "-O-", "--timeout=5", "--no-check-certificate", "https://kubernetes.default.svc:443/healthz")
		o.Expect(err).NotTo(o.HaveOccurred(), "Test pod cannot reach Kubernetes API")
		o.Expect(output).To(o.Equal("ok"), "Unexpected response from Kubernetes API")
		logger.Infof("OK!\n")

		exutil.By("Verify MCC pod cannot reach test pod (ingress blocked by default-deny)")
		mccPodName, err := getMachineConfigControllerPod(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to get MCC pod")
		o.Expect(mccPodName).NotTo(o.BeEmpty(), "MCC pod not found")

		testPodIP := testPodResource.GetOrFail("{.status.podIP}")

		// Listener is proven reachable (positive control above), so any failure here proves the NetworkPolicy blocks ingress
		_, err = exutil.RemoteShContainer(oc.AsAdmin(), MachineConfigNamespace, mccPodName, "machine-config-controller", "curl", "-sk", "--connect-timeout", "5", fmt.Sprintf("http://%s:8080", testPodIP))
		o.Expect(err).To(o.HaveOccurred(), "MCC pod should not be able to reach test pod due to default-deny")
		logger.Infof("OK!\n")

		exutil.By("Verify only port 9001 is allowed on MCC pod (other ports blocked)")
		mccPod := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, mccPodName)
		mccPodIP := mccPod.GetOrFail("{.status.podIP}")

		// Port 9001 is explicitly allowed by network policy — expect 401 Unauthorized proving connection succeeded
		_, err = exutil.RemoteShPod(oc.AsAdmin(), MachineConfigNamespace, testPodName, "wget", "-q", "-O-", "--timeout=5", "--no-check-certificate", fmt.Sprintf("https://%s:9001/metrics", mccPodIP))
		o.Expect(err).To(o.HaveOccurred(), "Expected HTTP error on port 9001 (authentication required)")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Expected ExitError with stderr")
		o.Expect(err.(*exutil.ExitError).StdErr).To(o.ContainSubstring("Unauthorized"), "Expected 401 Unauthorized response on port 9001 (proves connection succeeded)")
		logger.Infof("OK!\n")

		// Port 8443 is not allowed by network policy — expect timeout
		_, err = exutil.RemoteShPod(oc.AsAdmin(), MachineConfigNamespace, testPodName, "wget", "-q", "-O-", "--timeout=5", "--no-check-certificate", fmt.Sprintf("https://%s:8443/metrics", mccPodIP))
		o.Expect(err).To(o.HaveOccurred(), "Port 8443 should be blocked by network policy")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Expected ExitError with stderr")
		o.Expect(err.(*exutil.ExitError).StdErr).To(o.ContainSubstring("download timed out"), "Expected timeout on port 8443 — NetworkPolicy must drop the traffic")
		logger.Infof("OK!\n")

		// Port 443 is not allowed by network policy — expect timeout
		_, err = exutil.RemoteShPod(oc.AsAdmin(), MachineConfigNamespace, testPodName, "wget", "-q", "-O-", "--timeout=5", "--no-check-certificate", fmt.Sprintf("https://%s:443", mccPodIP))
		o.Expect(err).To(o.HaveOccurred(), "Port 443 should be blocked by network policy")
		o.Expect(err).To(o.BeAssignableToTypeOf(&exutil.ExitError{}), "Expected ExitError with stderr")
		o.Expect(err.(*exutil.ExitError).StdErr).To(o.ContainSubstring("download timed out"), "Expected timeout on port 443 — NetworkPolicy must drop the traffic")
		logger.Infof("OK!\n")

		exutil.By("Verify MCO pod can reach Kubernetes API (egress allowed)")
		mcoPodName, err := getMachineConfigOperatorPod(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred(), "Failed to get MCO pod")
		o.Expect(mcoPodName).NotTo(o.BeEmpty(), "MCO pod not found")

		output, err = exutil.RemoteShContainer(oc.AsAdmin(), MachineConfigNamespace, mcoPodName, "machine-config-operator", "curl", "-sk", "--connect-timeout", "5", "https://kubernetes.default.svc:443/healthz")
		o.Expect(err).NotTo(o.HaveOccurred(), "MCO pod cannot reach Kubernetes API")
		o.Expect(output).To(o.Equal("ok"), "Unexpected response from Kubernetes API")
		logger.Infof("OK!\n")

		logger.Infof("Test %s completed successfully!\n", testNum)
	})
})
