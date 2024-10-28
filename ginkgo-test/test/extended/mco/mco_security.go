package mco

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
)

var _ = g.Describe("[sig-mco] MCO security", func() {
	defer g.GinkgoRecover()

	var (
		oc   = exutil.NewCLI("mco-security", exutil.KubeConfigPath())
		wMcp *MachineConfigPool
		mMcp *MachineConfigPool
		// Compact compatible MCP. If the node is compact/SNO this variable will be the master pool, else it will be the worker pool
		mcp *MachineConfigPool
		cc  *ControllerConfig
	)

	g.JustBeforeEach(func() {
		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mcp = GetCompactCompatiblePool(oc.AsAdmin())
		cc = NewControllerConfig(oc.AsAdmin(), "machine-config-controller")
		logger.Infof("%s %s %s", wMcp, mMcp, mcp)

		preChecks(oc)
	})
	g.It("Author:sregidor-NonHyperShiftHOST-Medium-66048-Check image registry user bundle certificate [Disruptive]", func() {

		if !IsCapabilityEnabled(oc.AsAdmin(), "ImageRegistry") {
			g.Skip("ImageRegistry is not installed, skip this test")
		}

		var (
			mergedTrustedImageRegistryCACM = NewConfigMap(oc.AsAdmin(), "openshift-config-managed", "merged-trusted-image-registry-ca")
			imageConfig                    = NewResource(oc.AsAdmin(), "image.config.openshift.io", "cluster")
			certFileName                   = "caKey.pem"
			cmName                         = "cm-test-ca"
		)

		exutil.By("Get current image.config spec")
		initImageConfigSpec := imageConfig.GetOrFail(`{.spec}`)

		defer func() {
			logger.Infof("Restore original image.config spec: %s", initImageConfigSpec)
			_ = imageConfig.Patch("json", `[{ "op": "add", "path": "/spec", "value": `+initImageConfigSpec+`}]`)
		}()

		initialCMCreationTime := mergedTrustedImageRegistryCACM.GetOrFail(`{.metadata.creationTimestamp}`)
		logger.Infof("OK!\n")

		exutil.By("Add new  additionalTrustedCA to the image.config resource")
		logger.Infof("Creating new config map with a new CA")
		additionalTrustedCM, err := CreateConfigMapWithRandomCert(oc.AsAdmin(), "openshift-config", cmName, certFileName)
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error creating a configmap with a CA")

		defer additionalTrustedCM.Delete()

		newCertificate := additionalTrustedCM.GetDataValueOrFail(certFileName)

		logger.Infof("Configure the image.config resource to use the new configmap")
		o.Expect(imageConfig.Patch("merge", fmt.Sprintf(`{"spec": {"additionalTrustedCA": {"name": "%s"}}}`, cmName))).To(
			o.Succeed(),
			"Error setting the new image.config spec")

		logger.Infof("OK!\n")

		exutil.By("Check that the ControllerConfig has been properly synced")
		o.Eventually(cc.GetImageRegistryBundleUserDataByFileName,
			"3m", "20s").WithArguments(certFileName).Should(
			exutil.Secure(o.Equal(newCertificate)),
			"The new certificate was not properly added to the controller config imageRegistryBundleUserData")

		usrDataInfo, err := GetCertificatesInfoFromPemBundle(certFileName, []byte(newCertificate))
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error extracting certificate info from the new additional trusted CA")

		o.Expect(cc.GetCertificatesInfoByBundleFileName(certFileName)).To(
			o.Equal(usrDataInfo),
			"The information reported in the ControllerConfig for bundle file %s is wrong", certFileName)

		logger.Infof("OK!\n")

		exutil.By("Check that the merged-trusted-image-registry-ca configmap has been properly synced")
		o.Expect(mergedTrustedImageRegistryCACM.GetDataValueOrFail(certFileName)).To(
			exutil.Secure(o.Equal(newCertificate)),
			"The configmap -n  openshift-config-managed merged-trusted-image-registry-ca was not properly synced")

		o.Expect(mergedTrustedImageRegistryCACM.Get(`{.metadata.creationTimestamp}`)).To(
			o.Equal(initialCMCreationTime),
			"The %s resource was not patched! it was recreated! The configmap should be patched since https://issues.redhat.com/browse/OCPBUGS-18800")

		logger.Infof("OK!\n")

		// We verify that all nodes in the pools have the new certificate (be aware that windows nodes do not belong to any pool, we are skipping them)
		for _, node := range append(wMcp.GetNodesOrFail(), mMcp.GetNodesOrFail()...) {
			exutil.By(fmt.Sprintf("Check that the certificate was correctly deployed in node %s", node.GetName()))

			EventuallyImageRegistryCertificateExistsInNode(certFileName, newCertificate, node, "5m", "30s")
			logger.Infof("OK!\n")
		}

		exutil.By("Configure an empty image.config spec")
		o.Expect(imageConfig.Patch("json", `[{ "op": "add", "path": "/spec", "value": {}}]`)).To(
			o.Succeed(),
			"Error configuring an empty image.config spec")
		logger.Infof("OK!\n")

		exutil.By("Check that the ControllerConfig was properly synced")

		o.Eventually(cc.GetImageRegistryBundleUserData, "45s", "20s").ShouldNot(
			exutil.Secure(o.HaveKey(certFileName)),
			"The new certificate was not properly removed from the ControllerConfig imageRegistryBundleUserData")

		o.Expect(cc.GetCertificatesInfoByBundleFileName(certFileName)).To(
			exutil.Secure(o.BeEmpty()),
			"The information reported in the ControllerConfig for bundle file %s was not removed", certFileName)

		logger.Infof("OK!\n")

		exutil.By("Check that the merged-trusted-image-registry-ca configmap has been properly synced")
		o.Expect(mergedTrustedImageRegistryCACM.GetDataMap()).NotTo(
			exutil.Secure(o.HaveKey(newCertificate)),
			"The certificate was not removed from the configmap -n  openshift-config-managed merged-trusted-image-registry-ca")

		o.Expect(mergedTrustedImageRegistryCACM.Get(`{.metadata.creationTimestamp}`)).To(
			o.Equal(initialCMCreationTime),
			"The %s resource was not patched! it was recreated! The configmap should be patched since https://issues.redhat.com/browse/OCPBUGS-18800")

		logger.Infof("OK!\n")

		// We verify that the certificate was removed from all nodes in the pools (be aware that windows nodes do not belong to any pool, we are skipping them)
		for _, node := range append(wMcp.GetNodesOrFail(), mMcp.GetNodesOrFail()...) {
			exutil.By(fmt.Sprintf("Check that the certificate was correctly removed from node %s", node.GetName()))

			certPath := filepath.Join(ImageRegistryCertificatesDir, certFileName, ImageRegistryCertificatesFileName)
			rfCert := NewRemoteFile(node, certPath)

			logger.Infof("Checking certificate file %s", certPath)

			o.Eventually(rfCert.Exists, "5m", "20s").Should(
				o.BeFalse(),
				"The certificate %s was not removed from the node %s. But it should have been removed after the image.config reconfiguration",
				certPath, node.GetName())

			logger.Infof("OK!\n")
		}
	})
	g.It("Author:sregidor-NonHyperShiftHOST-High-67660-MCS generates ignition configs with certs [Disruptive]", func() {
		var (
			proxy                      = NewResource(oc.AsAdmin(), "proxy", "cluster")
			certFileKey                = "ca-bundle.crt"
			cloudCertFileKey           = "ca-bundle.pem"
			userCABundleConfigMap      = NewConfigMap(oc.AsAdmin(), "openshift-config", "user-ca-bundle")
			cmName                     = "test-proxy-config"
			cmNamespace                = "openshift-config"
			proxyConfigMap             *ConfigMap
			kubeCloudProviderConfigMap = GetCloudProviderConfigMap(oc.AsAdmin())
			kubeCloudManagedConfigMap  = NewConfigMap(oc.AsAdmin(), "openshift-config-managed", "kube-cloud-config")
			kubeCertFile               = "/etc/kubernetes/kubelet-ca.crt"
			userCABundleCertFile       = "/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt"
			kubeCloudCertFile          = "/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem"
			ignitionConfig             = "3.4.0"
			node                       = mcp.GetSortedNodesOrFail()[0]
		)

		logger.Infof("Using pool %s for testing", mcp.GetName())

		exutil.By("Getting initial status")
		rfUserCA := NewRemoteFile(node, userCABundleCertFile)
		o.Expect(rfUserCA.Fetch()).To(o.Succeed(), "Error getting the initial user CA bundle content")
		initialUserCAContent := rfUserCA.GetTextContent()

		rfCloudCA := NewRemoteFile(node, kubeCloudCertFile)
		o.Expect(rfCloudCA.Fetch()).To(o.Succeed(), "Error getting the initial cloud CA bundle content")
		initialCloudCAContent := rfCloudCA.GetTextContent()

		defer func() {
			exutil.By("Checking that the user CA bundle file content was properly restored when the configuration was removed")
			o.Eventually(rfUserCA, "5m", "20s").Should(exutil.Secure(HaveContent(initialUserCAContent)),
				"The user CA bundle file content was not restored after the configuration was removed")
			logger.Infof("OK!\n")

			exutil.By("Checking that the cloud CA bundle file content was properly restored when the configuration was removed")
			o.Eventually(rfCloudCA, "5m", "20s").Should(exutil.Secure(HaveContent(initialCloudCAContent)),
				"The cloud CA bundle file content was not restored after the configuration was removed")
			logger.Infof("OK!\n")
		}()

		logger.Infof("OK!\n")

		// Create a new config map and configure the proxy additional trusted CA if necessary
		proxyConfigMapName := proxy.GetOrFail(`{.spec.trustedCA.name}`)
		if proxyConfigMapName == "" {
			var err error
			exutil.By("Configure the proxy with an additional trusted CA")
			logger.Infof("Create a configmap with the CA")
			proxyConfigMap, err = CreateConfigMapWithRandomCert(oc.AsAdmin(), cmNamespace, cmName, certFileKey)
			o.Expect(err).NotTo(o.HaveOccurred(),
				"Error creating a configmap with a CA")
			defer proxyConfigMap.Delete()

			logger.Infof("Patch the proxy resource to use the new configmap")
			initProxySpec := proxy.GetOrFail(`{.spec}`)
			defer func() {
				logger.Infof("Restore original proxy spec: %s", initProxySpec)
				_ = proxy.Patch("json", `[{ "op": "add", "path": "/spec", "value": `+initProxySpec+`}]`)
			}()
			proxy.Patch("merge", fmt.Sprintf(`{"spec": {"trustedCA": {"name": "%s"}}}`, cmName))
			logger.Infof("OK!\n")
		} else {
			logger.Infof("The proxy is already configured to use the CA inside this configmap: %s", proxyConfigMapName)
			proxyConfigMap = NewConfigMap(oc.AsAdmin(), "openshift-config", proxyConfigMapName)
		}

		exutil.By(fmt.Sprintf(`Check that the "%s" is in the ignition config`, kubeCertFile))
		jsonPath := fmt.Sprintf(`storage.files.#(path=="%s")`, kubeCertFile)
		o.Eventually(mcp.GetMCSIgnitionConfig,
			"1m", "20s").WithArguments(true, ignitionConfig).ShouldNot(
			HavePathWithValue(jsonPath, o.BeEmpty()),
			"The file %s is not served in the ignition config", kubeCertFile)

		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf(`Check that the "%s" is in the ignition config`, userCABundleCertFile))
		logger.Infof("Check that the file is served in the ignition config")
		jsonPath = fmt.Sprintf(`storage.files.#(path=="%s")`, userCABundleCertFile)
		o.Eventually(mcp.GetMCSIgnitionConfig,
			"1m", "20s").WithArguments(true, ignitionConfig).ShouldNot(
			HavePathWithValue(jsonPath, o.BeEmpty()),
			"The file %s is not served in the ignition config", userCABundleCertFile)

		logger.Infof("Check that the file has the right content in the nodes")

		certContent := ""
		if userCABundleConfigMap.Exists() {
			userCABundleCert, exists, err := userCABundleConfigMap.HasKey(certFileKey)
			o.Expect(err).NotTo(o.HaveOccurred(), "Error checking if %s contains key '%s'", userCABundleConfigMap, certFileKey)
			if exists {
				certContent = userCABundleCert
			}
		} else {
			logger.Infof("%s does not exist. We don't take it into account", userCABundleConfigMap)
		}

		// OCPQE-17800 only merge the cert contents when trusted CA in proxy/cluster is not cm/user-ca-bundle
		if proxyConfigMap.GetName() != userCABundleConfigMap.GetName() {
			certContent += proxyConfigMap.GetDataValueOrFail(certFileKey)
		}

		EventuallyFileExistsInNode(userCABundleCertFile, certContent, node, "3m", "20s")

		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf(`Check that the "%s" is CA trusted. Command update-ca-trust was executed when the file was added`, userCABundleCertFile))
		o.Eventually(BundleFileIsCATrusted, "5m", "20s").WithArguments(userCABundleCertFile, node).Should(
			o.BeTrue(), "The %s file was not ca-trusted. It seems that the update-ca-trust command was not executed after updating the file", userCABundleCertFile)
		logger.Infof("OK!\n")

		exutil.By(fmt.Sprintf(`Check that the "%s" is in the ignition config`, kubeCloudCertFile))
		kubeCloudCertContent, err := kubeCloudManagedConfigMap.GetDataValue("ca-bundle.pem")
		if err != nil {
			logger.Infof("No KubeCloud cert configured, configuring a new value")
			if kubeCloudProviderConfigMap != nil && kubeCloudProviderConfigMap.Exists() {
				_, caPath, err := createCA(createTmpDir(), cloudCertFileKey)
				o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new random certificate")
				defer kubeCloudProviderConfigMap.RemoveDataKey(cloudCertFileKey)
				kubeCloudProviderConfigMap.SetData("--from-file=" + cloudCertFileKey + "=" + caPath)
				o.Eventually(kubeCloudManagedConfigMap.GetDataValueOrFail, "5m", "20s").WithArguments(cloudCertFileKey).ShouldNot(o.BeEmpty(),
					"A new CA was added to %s but the managed resource %s was not populated", kubeCloudProviderConfigMap, kubeCloudManagedConfigMap)
				kubeCloudCertContent = kubeCloudManagedConfigMap.GetDataValueOrFail(cloudCertFileKey)

			} else {
				logger.Infof("It is not possible to configure a new CloudCA. CloudProviderConfig configmap is not defined in the infrastructure resource or it does not exist")
				kubeCloudCertContent = ""
			}

		}

		if kubeCloudCertContent != "" {
			logger.Infof("Check that the file is served in the ignition config")
			jsonPath = fmt.Sprintf(`storage.files.#(path=="%s")`, kubeCloudCertFile)
			o.Eventually(mcp.GetMCSIgnitionConfig,
				"6m", "20s").WithArguments(true, ignitionConfig).ShouldNot(
				HavePathWithValue(jsonPath, o.BeEmpty()),
				"The file %s is not served in the ignition config", kubeCloudCertFile)

			logger.Infof("Check that the file has the right content in the nodes")
			EventuallyFileExistsInNode(kubeCloudCertFile, kubeCloudCertContent, node, "3m", "20s")

		} else {
			logger.Infof("No KubeCloud cert was configured and it was not possible to define a new one, we skip the cloudCA validation")
		}

		logger.Infof("OK!\n")
	})

	g.It("Author:rioliu-NonHyperShiftHOST-NonPreRelease-Longduration-High-71991-post action of user-ca-bundle change will skip drain,reboot and restart crio service [Disruptive]", func() {
		var (
			mcName                 = "mco-tc-71991"
			filePath               = "/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt"
			mode                   = 420 // decimal 0644
			objsignCABundlePemPath = "/etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem"
			node                   = mcp.GetSortedNodesOrFail()[0]
			behaviourValidator     = UpdateBehaviourValidator{
				RebootNodesShouldBeSkipped: true,
				DrainNodesShoulBeSkipped:   true,
				Checkers: []Checker{
					NodeEventsChecker{
						EventsSequence:        []string{"Reboot", "Drain"},
						EventsAreNotTriggered: true,
					},
				},
			}
		)

		behaviourValidator.Initialize(mcp, nil)

		exutil.By("Removing all MCD pods to clean the logs")
		o.Expect(RemoveAllMCDPods(oc)).To(o.Succeed(), "Error removing all MCD pods in %s namespace", MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Create a new certificate")
		_, caPath, err := createCA(createTmpDir(), "newcert.pem")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new random certificate")

		bcert, err := os.ReadFile(caPath)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error reading the new random certificate")
		cert := string(bcert)
		logger.Infof("OK!\n")

		exutil.By("Create the MachineConfig with the new certificate")
		file := ign32File{
			Path: filePath,
			Contents: ign32Contents{
				Source: GetBase64EncodedFileSourceContent(cert),
			},
			Mode: PtrTo(mode),
		}

		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", string(MarshalOrFail(file)))}
		mc.skipWaitForMcp = true
		defer mc.delete()

		mc.create()
		logger.Infof("OK!\n")

		// Check that the MC is applied according to the expected behaviour
		behaviourValidator.Validate()

		exutil.By("Check that the certificate was created and updated in the cluster by using update-ca-trust command")
		certRemote := NewRemoteFile(node, filePath)
		objsignCABundleRemote := NewRemoteFile(node, objsignCABundlePemPath)

		o.Eventually(certRemote, "5m", "20s").Should(Exist(),
			"The file %s does not exist in the node %s after applying the configuration", certRemote.fullPath, node.GetName())

		o.Eventually(certRemote, "5m", "20s").Should(exutil.Secure(HaveContent(o.ContainSubstring(cert))),
			"%s doesn't have the expected content. It doesn't include the configured certificate", certRemote)

		o.Eventually(objsignCABundleRemote, "5m", "20s").Should(Exist(),
			"The file %s does not exist in the node %s after applying the configuration", certRemote.fullPath, node.GetName())

		o.Expect(certRemote.Fetch()).To(o.Succeed(),
			"There was an error trying to the the content of file %s in node %s", certRemote.fullPath, node.GetName())

		// diff /etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt | less
		// The new certificate should be included in the /etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem file when we execute the update-ca-trust command
		o.Expect(objsignCABundleRemote.Read()).To(exutil.Secure(HaveContent(o.ContainSubstring(certRemote.GetTextContent()))),
			"In node %s: The the content of the file %s should have been added to the file %s. Command 'update-ca-trust' was not executed by MCD",
			node.GetName(), certRemote.fullPath, objsignCABundleRemote.fullPath)
		logger.Infof("OK!\n")

		exutil.By("Removing all MCD pods to clean the logs before the MC deletion")
		o.Expect(RemoveAllMCDPods(oc)).To(o.Succeed(), "Error removing all MCD pods in %s namespace", MachineConfigNamespace)
		logger.Infof("OK!\n")

		exutil.By("Delete the MachineConfig")
		behaviourValidator.Initialize(mcp, nil) // re-initialize the validator to ignore previous events
		mc.deleteNoWait()
		logger.Infof("OK!\n")

		// Check that the MC is removed according to the expected behaviour
		behaviourValidator.Validate()

		exutil.By("Check that the openshift-config-user-ca-bundle.crt file does not include the certificate anymore and the nodes were updated with update-ca-trust")
		// The file is not removed, it is always present but with empty content
		o.Eventually(certRemote.Read, "5m", "20s").ShouldNot(exutil.Secure(HaveContent(o.ContainSubstring(cert))),
			"The certificate has been removed, but %s still contains the certificate", certRemote.fullPath, node.GetName())
		o.Eventually(objsignCABundleRemote, "5m", "20s").Should(Exist(),
			"The file %s does not exist in the node %s but it should exist after removing the configuration", certRemote.fullPath, node.GetName())

		o.Expect(objsignCABundleRemote.Read()).NotTo(exutil.Secure(HaveContent(o.ContainSubstring(cert))),
			"In node %s: The the certificate should have been removed from the file %s. Command 'update-ca-trust' was not executed by MCD after removing the MC",
			node.GetName(), certRemote.fullPath, objsignCABundleRemote.fullPath)
		logger.Infof("OK!\n")
	})

	// In the latest branches times are artificially reduced and after about 6 or 7 hours all kube-apiserver certificates are rotated
	// If we execute this test case, when this rotation happens the kubeconfig file needs to be updated to use new certificates and all test cases start failing because of this
	// If we don't execute this test case, when this rotation happens the kubeconfig needs no update
	// We will skip this test case in prow jobs and we will execute it only out of CI
	g.It("Author:sregido-DEPRECATED-NonHyperShiftHOST-NonPreRelease-Critical-70857-boostrap-kubeconfig must be updated when kube-apiserver server CA is rotated [Disruptive]", func() {
		var (
			mco                      = NewResource(oc.AsAdmin(), "co", "machine-config")
			kubernetesKubeconfigPath = "/etc/kubernetes/kubeconfig"
			kubeletKubeconfigPath    = "/var/lib/kubelet/kubeconfig"
			lbServingSignerSecret    = NewSecret(oc.AsAdmin(), "openshift-kube-apiserver-operator", "loadbalancer-serving-signer")
			kubeAPIServerCM          = NewConfigMap(oc.AsAdmin(), "openshift-config-managed", "kube-apiserver-server-ca")
			node                     = mcp.GetSortedNodesOrFail()[0]
			startTime                = node.GetDateOrFail()
		)

		// we are going to fail the test if there is any CO degraded, so we want to know the initial status of the COs
		NewResourceList(oc.AsAdmin(), "co").PrintDebugCommand()

		exutil.By("Rotate certificate in loadbalancer-serving-signer secret")
		newCert := rotateTLSSecretOrFail(lbServingSignerSecret)
		logger.Debugf("New TLS cert:\n%s", newCert)
		logger.Infof("OK!\n")

		exutil.By("Check that the kube-apiserver-serve-ca configmap contains the new TLS secret")
		o.Eventually(kubeAPIServerCM.GetDataValue, "5m", "20s").WithArguments("ca-bundle.crt").Should(
			exutil.Secure(o.ContainSubstring(newCert)),
			"The new TLS certificate was not added to configmap %s", kubeAPIServerCM)

		caBundle := strings.TrimSpace(kubeAPIServerCM.GetDataValueOrFail("ca-bundle.crt"))
		logger.Debugf("New CA bundle:\n%s", caBundle)
		logger.Infof("OK!\n")

		exutil.By("Check kubernetes kubconfig file was correctly updated")
		// Eventually the kubeconfig file should be updated with the new certificates stored in kube-apiserver-serve-ca
		rfKubernetesKubecon := NewRemoteFile(node, kubernetesKubeconfigPath)
		o.Eventually(func() (string, error) {
			err := rfKubernetesKubecon.Fetch()
			if err != nil {
				return "", err
			}
			cert, err := getCertsFromKubeconfig(rfKubernetesKubecon.GetTextContent())
			if err != nil {
				return "", err
			}

			logger.Debugf("Kube cert:\n%s", cert)

			return strings.TrimSpace(cert), nil
		}, "5m", "10s").
			Should(exutil.Secure(o.Equal(caBundle)),
				"%s does not contain the certificates stored in %s.", kubernetesKubeconfigPath, kubeAPIServerCM)

		o.Expect(rfKubernetesKubecon).To(o.And(
			HaveOctalPermissions("0600"),
			HaveOwner("root"),
			HaveGroup("root")),
			"Wrong security attributes in %s", rfKubernetesKubecon)
		logger.Infof("OK!\n")

		exutil.By("Check kubelet kubconfig file was correctly updated")
		// Eventually the kubeconfig file should be updated with the new certificates stored in kube-apiserver-serve-ca
		o.Eventually(func() (string, error) {
			rfKubeletKubecon := NewRemoteFile(node, kubeletKubeconfigPath)
			err := rfKubeletKubecon.Fetch()
			if err != nil {
				return "", err
			}
			cert, err := getCertsFromKubeconfig(rfKubeletKubecon.GetTextContent())
			if err != nil {
				return "", err
			}
			return cert, nil
		}, "5m", "10s").
			Should(exutil.Secure(o.Equal(caBundle)),
				"%s does not contain the certificates stored in %s.", kubernetesKubeconfigPath, kubeAPIServerCM)

		o.Expect(rfKubernetesKubecon).To(o.And(
			HaveOctalPermissions("0600"),
			HaveOwner("root"),
			HaveGroup("root")),
			"Wrong security attributes in %s", rfKubernetesKubecon)
		logger.Infof("OK!\n")

		exutil.By("Check that kubelet was restarted")
		o.Eventually(node.GetUnitActiveEnterTime, "6m", "20s").WithArguments("kubelet.service").Should(o.BeTemporally(">", startTime),
			"Kubelet service was NOT restarted, but it should be")
		logger.Infof("OK!\n")

		exutil.By("Check that MCO pods are healthy")
		o.Expect(waitForAllMCOPodsReady(oc.AsAdmin(), 10*time.Minute)).To(o.Succeed(),
			"MCO pods are not Ready after cert rotation")

		o.Eventually(mco, "5m", "20s").ShouldNot(BeDegraded(), "Error! %s is degraded:\n%s", mco, mco.PrettyString())
		logger.Infof("OK!\n")

		exutil.By("Check that all cluster operators are healthy")
		checkAllOperatorsHealthy(oc.AsAdmin(), "20m", "30s")
		logger.Infof("OK!\n")
	})

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Longduration-Critical-75222-tlSecurityProfile switch and check the expected tlsMinVersion and cipheres suite are seen in MCS,MSS and rbac-kube-proxy pod logs[Disruptive]", func() {

		var (
			apiServer = NewResource(oc.AsAdmin(), "apiserver", "cluster")
		)

		exutil.By("Verify for Intermediate TLS Profile")
		csNameList := getCipherSuitesNameforSpecificVersion(VersionTLS12)
		var csVersion12 []string
		for i := range csNameList {
			if !strings.Contains(csNameList[i], "_CBC_") {
				csVersion12 = append(csVersion12, csNameList[i])
			}
		}
		validateCorrectTLSProfileSecurity(oc, "", "VersionTLS12", csVersion12)
		logger.Infof("OK!\n")

		defer func(initialConfig string) {
			exutil.By("Restore with previous apiserver value")
			apiServer.SetSpec(initialConfig)
			exutil.By("Check that all cluster operators are stable")
			o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
			logger.Infof("Wait for MCC to get the leader lease")
			o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "6m", "20s").Should(o.BeTrue(),
				"The controller pod didn't acquire the lease properly.")
			mMcp.waitForComplete()
			wMcp.waitForComplete()
			logger.Infof("OK!\n")
		}(apiServer.GetSpecOrFail())

		exutil.By("Patch the Custom tlsSecurityProfile")
		o.Expect(apiServer.Patch("json",
			`[{ "op": "add", "path": "/spec/tlsSecurityProfile", "value": {"type": "Custom","custom": {"ciphers": ["ECDHE-ECDSA-CHACHA20-POLY1305","ECDHE-RSA-CHACHA20-POLY1305", "ECDHE-RSA-AES128-GCM-SHA256",  "ECDHE-ECDSA-AES128-GCM-SHA256" ],"minTLSVersion": "VersionTLS11"}}}]`)).To(o.Succeed(), "Error patching tlsSecurityProfile")

		logger.Infof("OK!\n")
		exutil.By("Check that all cluster operators are stable")
		o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
		logger.Infof("Wait for MCC to get the leader lease")
		o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "12m", "20s").Should(o.BeTrue(),
			"The controller pod didn't acquire the lease properly.")
		mMcp.waitForComplete()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify for Custom TLS Profile")
		customCipherSuite := []string{"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256", "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"}
		validateCorrectTLSProfileSecurity(oc, "Custom", "VersionTLS11", customCipherSuite)
		logger.Infof("OK!\n")

		exutil.By("Patch the Old tlsSecurityProfile")
		o.Expect(apiServer.Patch("json",
			`[{ "op": "add", "path": "/spec/tlsSecurityProfile", "value":  {"type": "Old","old": {}}}]`)).To(o.Succeed(), "Error patching http proxy")

		logger.Infof("OK!\n")
		exutil.By("Check that all cluster operators are stable")
		o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
		logger.Infof("Wait for MCC to get the leader lease")
		o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "12m", "20s").Should(o.BeTrue(),
			"The controller pod didn't acquire the lease properly.")
		mMcp.waitForComplete()
		wMcp.waitForComplete()

		exutil.By("Verify for Old TLS Profile")
		csNameList = getCipherSuitesNameforSpecificVersion(VersionTLS10)
		validateCorrectTLSProfileSecurity(oc, "Old", "VersionTLS10", csNameList)
		logger.Infof("OK!\n")

		// For now Modern Profile is not supported
		match := `Unsupported value: "Modern"`
		exutil.By("Patch the Modern tlsSecurityProfile")
		tlsPatch := apiServer.Patch("json",
			`[{ "op": "add", "path": "/spec/tlsSecurityProfile", "value":  {"type": "Modern"}}]`)
		o.Expect(tlsPatch.(*exutil.ExitError).StdErr).To(o.ContainSubstring(match))

	})

	g.It("Author:ptalgulk-NonHyperShiftHOST-NonPreRelease-Longduration-Medium-75543-tlsSecurity setting is also propagated on node in kubelet.conf [Disruptive]", func() {

		var (
			node              = wMcp.GetSortedNodesOrFail()[0]
			apiServer         = NewResource(oc.AsAdmin(), "apiserver", "cluster")
			kcName            = "tc-75543-set-kubelet-custom-tls-profile"
			kcTemplate        = generateTemplateAbsolutePath("custom-tls-profile-kubelet-config.yaml")
			customCipherSuite = []string{"TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256", "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"}
		)
		csNameList := getCipherSuitesNameforSpecificVersion(VersionTLS12)
		var csVersion12 []string
		for _, name := range csNameList {
			if !strings.Contains(name, "_CBC_") {
				csVersion12 = append(csVersion12, name)
			}
		}
		exutil.By("Verify for Intermediate TLS Profile in kubeletConfig")
		validateCorrectTLSProfileSecurityInKubeletConfig(node, "VersionTLS12", csVersion12)
		exutil.By("Verify for Intermediate TLS Profile pod logs")
		validateCorrectTLSProfileSecurity(oc, "", "VersionTLS12", csVersion12)

		defer func(initialConfig string) {
			exutil.By("Restore with previous apiserver value")
			apiServer.SetSpec(initialConfig)
			exutil.By("Check that all cluster operators are stable")
			o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
			logger.Infof("Wait for MCC to get the leader lease")
			o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "6m", "20s").Should(o.BeTrue(),
				"The controller pod didn't acquire the lease properly.")
			mMcp.waitForComplete()
			wMcp.waitForComplete()
			logger.Infof("OK!\n")
		}(apiServer.GetSpecOrFail())

		exutil.By("Patch the Old tlsSecurityProfile")
		o.Expect(apiServer.Patch("json",
			`[{ "op": "add", "path": "/spec/tlsSecurityProfile", "value":  {"type": "Old","old": {}}}]`)).To(o.Succeed(), "Error patching http proxy")
		logger.Infof("OK!\n")
		exutil.By("Check that all cluster operators are stable")
		o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
		logger.Infof("Wait for MCC to get the leader lease")
		o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "12m", "20s").Should(o.BeTrue(),
			"The controller pod didn't acquire the lease properly.")
		mMcp.waitForComplete()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify for Old TLS Profile in kubeletConfig")
		csVersion10 := getCipherSuitesNameforSpecificVersion(VersionTLS10)
		validateCorrectTLSProfileSecurityInKubeletConfig(node, "VersionTLS10", csVersion10)
		exutil.By("Verify for Old TLS Profile in pod logs")
		validateCorrectTLSProfileSecurity(oc, "Old", "VersionTLS10", csVersion10)

		exutil.By("Create Kubeletconfig to configure a custom tlsSecurityProfile")
		kc := NewKubeletConfig(oc.AsAdmin(), kcName, kcTemplate)
		defer kc.Delete()
		kc.create()
		logger.Infof("KubeletConfig was created. Waiting for success.")
		kc.waitUntilSuccess("5m")
		logger.Infof("OK!\n")

		exutil.By("Wait for Worker MachineConfigPool to be updated")
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify for Custom TLS Profile in kubeletConfig")
		validateCorrectTLSProfileSecurityInKubeletConfig(node, "VersionTLS11", customCipherSuite)

		exutil.By("Patch the Intermediate tlsSecurityProfile to check kubeletconfig settings are not changed")
		o.Expect(apiServer.Patch("json",
			`[{ "op": "add", "path": "/spec/tlsSecurityProfile", "value":  {"type": "Intermediate","intermediate": {}}}]`)).To(o.Succeed(), "Error patching http proxy")
		logger.Infof("OK!\n")
		exutil.By("Check that all cluster operators are stable")
		o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
		logger.Infof("Wait for MCC to get the leader lease")
		o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "12m", "20s").Should(o.BeTrue(),
			"The controller pod didn't acquire the lease properly.")
		mMcp.waitForComplete()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify for Intermediate TLS Profile pod logs")
		validateCorrectTLSProfileSecurity(oc, "Intermediate", "VersionTLS12", csVersion12)
		exutil.By("Verify for Custom TLS Profile not changed in kubeletConfig")
		validateCorrectTLSProfileSecurityInKubeletConfig(node, "VersionTLS11", customCipherSuite)

		exutil.By("Delete create kubeletConfig template")
		kc.DeleteOrFail()
		o.Expect(kc).NotTo(Exist())
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("To check the kubeletConfig to have same tls setting as of API server")
		validateCorrectTLSProfileSecurityInKubeletConfig(node, "VersionTLS12", csVersion12)
		logger.Infof("OK!\n")

	})
	g.It("Author:sregidor-NonHyperShiftHOST-NonPreRelease-Critical-76587-MCS port should not expose weak ciphers to external client from master node IP [Disruptive]", func() {
		var (
			node            = mcp.GetSortedNodesOrFail()[0]
			port            = 22623
			insecureCiphers = []string{"TLS_RSA_WITH_AES_128_GCM_SHA256", "TLS_RSA_WITH_AES_256_GCM_SHA384"}
		)

		exutil.By("Remove iptable rules")
		logger.Infof("Remove the IPV4 iptables rules that block the ignition config")
		removedRules, err := node.RemoveIPTablesRulesByRegexp(fmt.Sprintf("%d", port))
		defer node.ExecIPTables(removedRules)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error removing the IPv4 iptables rules for port %s in node %s", port, node.GetName())

		logger.Infof("Remove the IPV6 ip6tables rules that block the ignition config")
		removed6Rules, err := node.RemoveIP6TablesRulesByRegexp(fmt.Sprintf("%d", port))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error removing the IPv6 iptables rules for port %s in node %s", port, node.GetName())
		defer node.ExecIP6Tables(removed6Rules)
		logger.Infof("OK!\n")

		internalAPIServerURI, err := GetAPIServerInternalURI(mcp.oc)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the internal apiserver URL")

		exutil.By("Check that no weak cipher is exposed")
		url := fmt.Sprintf("%s:%d", internalAPIServerURI, port)
		cipherOutput, cipherErr := node.DebugNodeWithOptions([]string{"--image=" + TestSSLImage, "-n", MachineConfigNamespace}, "testssl.sh", "--color", "0", url)
		logger.Infof("test ssh script output:\n %s", cipherOutput)
		o.Expect(cipherErr).NotTo(o.HaveOccurred())
		for _, insecureCipher := range insecureCiphers {
			logger.Infof("Verify %s", insecureCipher)
			o.Expect(cipherOutput).NotTo(o.ContainSubstring(insecureCipher),
				"MCO is exposing weak ciphers in %s", internalAPIServerURI)
			logger.Infof("OK")
		}
		logger.Infof("Verify SWEET32")
		o.Expect(cipherOutput).To(o.MatchRegexp("SWEET32 .*"+regexp.QuoteMeta("not vulnerable (OK)")),
			"%s is vulnerable to SWEET32", internalAPIServerURI)
		logger.Infof("OK!\n")
	})

})

// EventuallyFileExistsInNode fails the test if the certificate file does not exist in the node after the time specified as parameters
func EventuallyImageRegistryCertificateExistsInNode(certFileName, certContent string, node Node, timeout, poll string) {
	certPath := filepath.Join(ImageRegistryCertificatesDir, certFileName, ImageRegistryCertificatesFileName)
	EventuallyFileExistsInNode(certPath, certContent, node, timeout, poll)
}

// EventuallyFileExistsInNode fails the test if the file does not exist in the node after the time specified as parameters
func EventuallyFileExistsInNode(filePath, expectedContent string, node Node, timeout, poll string) {
	logger.Infof("Checking file %s in node %s", filePath, node.GetName())
	rfCert := NewRemoteFile(node, filePath)
	o.Eventually(func(gm o.Gomega) { // Passing o.Gomega as parameter we can use assertions inside the Eventually function without breaking the retries.
		gm.Expect(rfCert.Fetch()).To(o.Succeed(),
			"Cannot read the certificate file %s in node:%s ", rfCert.fullPath, node.GetName())

		gm.Expect(rfCert.GetTextContent()).To(exutil.Secure(o.Equal(expectedContent)),
			"the certificate stored in file %s does not match the expected value", rfCert.fullPath)
	}, timeout, poll).
		Should(o.Succeed(),
			"The file %s in node %s does not contain the expected certificate.", rfCert.GetFullPath(), node.GetName())
}

// BundleFileIsCATrusted check that the provided bundle file is included in file /etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem which means that it is ca-trusted
func BundleFileIsCATrusted(bundleFile string, node Node) (bool, error) {
	var (
		objsignCABundlePemPath = "/etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem"
		objsignCABundleRemote  = NewRemoteFile(node, objsignCABundlePemPath)

		bundleRemote = NewRemoteFile(node, bundleFile)
	)

	if !bundleRemote.Exists() {
		return false, fmt.Errorf("File %s does not exist", bundleRemote.GetFullPath())
	}

	if !objsignCABundleRemote.Exists() {
		return false, fmt.Errorf("File %s does not exist", objsignCABundleRemote.GetFullPath())
	}

	err := bundleRemote.Fetch()
	if err != nil {
		return false, err
	}

	err = objsignCABundleRemote.Fetch()
	if err != nil {
		return false, err
	}

	bundleCerts, err := splitBundleCertificates([]byte(bundleRemote.GetTextContent()))
	if err != nil {
		return false, err
	}

	objsignCABundleCerts, err := splitBundleCertificates([]byte(objsignCABundleRemote.GetTextContent()))
	if err != nil {
		return false, err
	}

	// The new certificates should be included in the /etc/pki/ca-trust/extracted/pem/objsign-ca-bundle.pem file when we execute the update-ca-trust command
	for _, bundleCert := range bundleCerts {
		found := false
		for _, objsignCert := range objsignCABundleCerts {
			if bundleCert.Equal(objsignCert) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}

	return true, nil
}

// splitBundleCertificates reads a pem bundle and returns a slice with all the certificates contained in this pem bundle
func splitBundleCertificates(pemBundle []byte) ([]*x509.Certificate, error) {

	certsList := []*x509.Certificate{}
	for {
		block, rest := pem.Decode(pemBundle)
		if block == nil {
			return nil, fmt.Errorf("failed to parse certificate PEM:\n%s", string(pemBundle))
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}

		certsList = append(certsList, cert)
		pemBundle = rest
		if len(rest) == 0 {
			break
		}

	}

	return certsList, nil
}

// getCipherSuitesNameforSpecificVersion returns the names of cipher suite for the provided version
func getCipherSuitesNameforSpecificVersion(version uint16) []string {
	cipherSuites := getCipherSuitesForVersion(version)
	cipherSuiteNames := []string{}

	for _, cipherSuite := range cipherSuites {
		cipherSuiteNames = append(cipherSuiteNames, cipherSuite.Name)
	}

	return cipherSuiteNames
}

// getCipherSuitesForVersion returns the cipher suite list along with name,ID, security issues for provided version
func getCipherSuitesForVersion(version uint16) []*tls.CipherSuite {
	var suites []*tls.CipherSuite
	for _, cs := range tls.CipherSuites() {
		for _, v := range cs.SupportedVersions {
			if v == version {
				suites = append(suites, cs)
				break
			}
		}
	}
	return suites
}

// validateCorrectTLSProfileSecurity helps to check the valid tls-min-version and tls-cipher-suite
func validateCorrectTLSProfileSecurity(oc *exutil.CLI, tlsSecurityProfile, tlsMinVersionStr string, cipherSuite []string) {

	var (
		containerArgsPath  = `{.spec.containers[*].args[*]}`
		tlsProfileTypePath = `{.spec.tlsSecurityProfile.type}`
		apiServer          = NewResource(oc.AsAdmin(), "apiserver", "cluster")
	)

	exutil.By("Get the kube-rbac-proxy, MCC, MCS pods")
	getKubeProxyPod, err := getAllKubeProxyPod(oc.AsAdmin(), MachineConfigNamespace)
	o.Expect(err).NotTo(o.HaveOccurred())
	kubeproxy := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, getKubeProxyPod[0])
	logger.Infof("%s\n", kubeproxy.GetOrFail(containerArgsPath))
	logger.Infof("OK!\n")

	mccPodName, err := getMachineConfigControllerPod(oc.AsAdmin())
	o.Expect(err).NotTo(o.HaveOccurred())
	mcc := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, mccPodName)
	logger.Infof("%s\n", mcc.GetOrFail(containerArgsPath))
	logger.Infof("OK!\n")

	mcspod, err := GetMCSPodNames(oc.AsAdmin())
	o.Expect(err).NotTo(o.HaveOccurred())
	mcsLogs, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigServer, mcspod[0], "")
	o.Expect(err).NotTo(o.HaveOccurred())
	logger.Infof("%s\n", mcsLogs)
	logger.Infof("OK!\n")

	logger.Infof("%s\n", apiServer.GetOrFail(tlsProfileTypePath))
	logger.Infof("OK!\n")

	o.Expect(apiServer.GetOrFail(tlsProfileTypePath)).To(o.ContainSubstring(tlsSecurityProfile), "The %s tlsSecuirtyProfile is not applied properly", tlsSecurityProfile)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-min-version for %s", tlsSecurityProfile))
	o.Expect(kubeproxy.GetOrFail(containerArgsPath)).To(o.ContainSubstring("--tls-min-version=%s", tlsMinVersionStr), "Error getting required tls-min-version for given tlsSecuirtyProfile in %s pod", getKubeProxyPod[0])
	o.Expect(mcc.GetOrFail(containerArgsPath)).To(o.ContainSubstring("--tls-min-version=%s", tlsMinVersionStr), "Error getting required tls-min-version for tlsSecuirtyProfile in %s pod", mccPodName)
	o.Expect(exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigServer, mcspod[0], "")).To(o.ContainSubstring(tlsMinVersionStr), "Error getting required tls-min-version for %s pod", mcspod[0])
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-cipher-suite for %s", tlsSecurityProfile))
	for i := range cipherSuite {
		o.Expect(kubeproxy.GetOrFail(containerArgsPath)).To(o.ContainSubstring(cipherSuite[i]), "Error getting %s cipher suite for given tlsSecuirtyProfile of %s pod", cipherSuite[i], getKubeProxyPod[0])
		o.Expect(mcc.GetOrFail(containerArgsPath)).To(o.ContainSubstring(cipherSuite[i]), "Error getting %s cipher suite for given tlsSecuirtyProfile of  %s pod", cipherSuite[i], mccPodName)
		o.Expect(exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigServer, mcspod[0], "")).To(o.ContainSubstring(cipherSuite[i]), "Error getting %s cipher suite for given tlsSecuirtyProfile of %s pod", cipherSuite[i], mcspod[0])
	}
	logger.Infof("OK!\n")
}

func validateCorrectTLSProfileSecurityInKubeletConfig(node Node, tlsMinVersion string, cipherSuite []string) {
	stdout, err := node.DebugNodeWithChroot("cat", "/etc/kubernetes/kubelet.conf")
	o.Expect(err).NotTo(o.HaveOccurred())
	exutil.By("To check the kubeletConfig to have same tls setting as of API server")
	o.Expect(stdout).To(o.ContainSubstring("tlsMinVersion: %s", tlsMinVersion), "Error %s tlsMinVersion is not updated in kubelet config", tlsMinVersion)
	for _, csname := range cipherSuite {
		o.Expect(stdout).To(o.ContainSubstring(csname), "Error %s cipher suite is not updated in kubelet config", csname)
	}
	logger.Infof("OK!\n")
}
