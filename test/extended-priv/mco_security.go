package extended

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
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO security", func() {
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

		PreChecks(oc)
	})
	g.It("[PolarionID:66048][OTP] Check image registry user bundle certificate [Disruptive]", func() {

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
	// AI-assisted: Split from original test 67660 into three separate test cases for better isolation
	g.It("[PolarionID:85749][OTP] MCS generates ignition configs with kubelet CA cert [Disruptive]", func() {
		var (
			kubeCertFile   = "/etc/kubernetes/kubelet-ca.crt"
			ignitionConfig = "3.4.0"
		)

		exutil.By(fmt.Sprintf(`Check that the "%s" is in the ignition config`, kubeCertFile))
		jsonPath := fmt.Sprintf(`storage.files.#(path=="%s")`, kubeCertFile)
		o.Eventually(mcp.GetMCSIgnitionConfig,
			"1m", "20s").WithArguments(true, ignitionConfig).ShouldNot(
			HavePathWithValue(jsonPath, o.BeEmpty()),
			"The file %s is not served in the ignition config", kubeCertFile)

		logger.Infof("OK!\n")
	})

	// AI-assisted: Split from original test 67660 into three separate test cases for better isolation
	g.It("[PolarionID:85750][OTP] MCS generates ignition configs with user CA bundle cert [Disruptive]", func() {
		var (
			testID                = GetCurrentTestPolarionIDNumber()
			proxy                 = NewResource(oc.AsAdmin(), "proxy", "cluster")
			certFileKey           = "ca-bundle.crt"
			userCABundleConfigMap = NewConfigMap(oc.AsAdmin(), "openshift-config", "user-ca-bundle")
			cmName                = fmt.Sprintf("test-proxy-config-%s", testID)
			cmNamespace           = "openshift-config"
			proxyConfigMap        *ConfigMap
			userCABundleCertFile  = "/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt"
			ignitionConfig        = "3.4.0"
			node                  = mcp.GetSortedNodesOrFail()[0]
		)

		logger.Infof("Test ID: %s - Using pool %s for testing", testID, mcp.GetName())

		exutil.By("Getting initial status")
		rfUserCA := NewRemoteFile(node, userCABundleCertFile)
		o.Expect(rfUserCA.Fetch()).To(o.Succeed(), "Error getting the initial user CA bundle content")
		initialUserCAContent := rfUserCA.GetTextContent()

		defer func() {
			wMcp.waitForComplete()
			mMcp.waitForComplete()

			exutil.By("Checking that the user CA bundle file content was properly restored when the configuration was removed")
			o.Eventually(rfUserCA.Read, "5m", "20s").Should(exutil.Secure(HaveContent(initialUserCAContent)),
				"The user CA bundle file content was not restored after the configuration was removed")
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

			// TODO: remove this when the userCA bundle is handled using a controller and not a MC. It will be implemented in the near future
			mcp.waitForComplete()

			logger.Infof("OK!\n")
		} else {
			logger.Infof("The proxy is already configured to use the CA inside this configmap: %s", proxyConfigMapName)
			proxyConfigMap = NewConfigMap(oc.AsAdmin(), "openshift-config", proxyConfigMapName)
		}

		exutil.By(fmt.Sprintf(`Check that the "%s" is in the ignition config`, userCABundleCertFile))
		logger.Infof("Check that the file is served in the ignition config")
		jsonPath := fmt.Sprintf(`storage.files.#(path=="%s")`, userCABundleCertFile)
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
	})

	// AI-assisted: Split from original test 67660 into three separate test cases for better isolation
	g.It("[PolarionID:85751][OTP] MCS generates ignition configs with cloud CA cert [Disruptive]", func() {
		var (
			testID                     = GetCurrentTestPolarionIDNumber()
			cloudCertFileKey           = "ca-bundle.pem"
			kubeCloudProviderConfigMap = GetCloudProviderConfigMap(oc.AsAdmin())
			kubeCloudManagedConfigMap  = NewConfigMap(oc.AsAdmin(), "openshift-config-managed", "kube-cloud-config")
			kubeCloudCertFile          = "/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem"
			ignitionConfig             = "3.4.0"
			node                       = mcp.GetSortedNodesOrFail()[0]
		)

		logger.Infof("Test ID: %s - Using pool %s for testing", testID, mcp.GetName())

		exutil.By("Getting initial status")
		rfCloudCA := NewRemoteFile(node, kubeCloudCertFile)
		o.Expect(rfCloudCA.Fetch()).To(o.Succeed(), "Error getting the initial cloud CA bundle content")
		initialCloudCAContent := rfCloudCA.GetTextContent()

		defer func() {
			wMcp.waitForComplete()
			mMcp.waitForComplete()

			exutil.By("Checking that the cloud CA bundle file content was properly restored when the configuration was removed")
			o.Eventually(rfCloudCA.Read, "5m", "20s").Should(exutil.Secure(HaveContent(initialCloudCAContent)),
				"The cloud CA bundle file content was not restored after the configuration was removed")
			logger.Infof("OK!\n")
		}()

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
			jsonPath := fmt.Sprintf(`storage.files.#(path=="%s")`, kubeCloudCertFile)
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

	g.It("[PolarionID:71991][OTP] post action of user-ca-bundle change will skip drain,reboot and restart crio service [Disruptive]", func() {
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
		defer mc.DeleteWithWait()

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
		mc.Delete()
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
	// https://issues.redhat.com/browse/MCO-2113
	g.It("[PolarionID:70857][OTP][DEPRECATED] boostrap kubeconfig must be updated when kube-apiserver server CA is rotated [Disruptive]", g.Label("Exclude:BreaksSubsequentTests"), func() {
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

	// Issue  https://issues.redhat.com/browse/OCPBUGS-76990 breaks the cluster, it cannot be recovered and tests cannot continue executing. Hence, we don't execute this test until the issue is fixed
	g.It("[PolarionID:75222][OTP] tlSecurityProfile switch and check the expected tlsMinVersion and cipheres suite are seen in MCS,MSS and rbac-kube-proxy pod logs[Disruptive]", g.Label("Exclude: excluded until OCPBUGS-76990 is fixed"), func() {

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

		exutil.By("Patch the Modern tlsSecurityProfile")
		o.Expect(apiServer.Patch("json",
			`[{ "op": "add", "path": "/spec/tlsSecurityProfile", "value":  {"type": "Modern","modern": {}}}]`)).To(o.Succeed(), "Error patching Modern profile")
		logger.Infof("OK!\n")
		exutil.By("Check that all cluster operators are stable")
		o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
		logger.Infof("Wait for MCC to get the leader lease")
		o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "12m", "20s").Should(o.BeTrue(),
			"The controller pod didn't acquire the lease properly.")
		mMcp.waitForComplete()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")
		exutil.By("Verify for Modern TLS Profile")
		csNameList = getCipherSuitesNameforSpecificVersion(VersionTLS13)
		validateCorrectTLSProfileSecurity(oc, "Modern", "VersionTLS13", csNameList)
		logger.Infof("OK!\n")

	})

	// Issue  https://issues.redhat.com/browse/OCPBUGS-76990 breaks the cluster, it cannot be recovered and tests cannot continue executing. Hence, we don't execute this test until the issue is fixed
	g.It("[PolarionID:75543][OTP] tlsSecurity setting is also propagated on node in kubelet.conf [Disruptive]", g.Label("Exclude: excluded until OCPBUGS-76990 is fixed"), func() {

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

		exutil.By("Patch the Modern tlsSecurityProfile")
		o.Expect(apiServer.Patch("json",
			`[{ "op": "add", "path": "/spec/tlsSecurityProfile", "value":  {"type": "Modern","modern": {}}}]`)).To(o.Succeed(), "Error patching Modern profile")
		logger.Infof("OK!\n")

		exutil.By("Check that all cluster operators are stable")
		o.Expect(WaitForStableCluster(oc.AsAdmin(), "30s", "50m")).To(o.Succeed(), "Not all COs were ready after configuring the tls profile")
		logger.Infof("Wait for MCC to get the leader lease")
		o.Eventually(NewController(oc.AsAdmin()).HasAcquiredLease, "12m", "20s").Should(o.BeTrue(),
			"The controller pod didn't acquire the lease properly.")
		mMcp.waitForComplete()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify for Modern TLS Profile")
		csVersion13 := getCipherSuitesNameforSpecificVersion(VersionTLS13)
		validateCorrectTLSProfileSecurity(oc, "Modern", "VersionTLS13", csVersion13)
		exutil.By("Verify for Custom TLS Profile not changed in kubeletConfig")
		validateCorrectTLSProfileSecurityInKubeletConfig(node, "VersionTLS11", customCipherSuite)
		logger.Infof("OK!\n")

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
	g.It("[PolarionID:76587][OTP] MCS port should not expose weak ciphers to external client from master node IP [Disruptive]", func() {
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

	g.It("[PolarionID:75521][OTP] Log details for malformed certificates. No infinite loop [Disruptive]", func() {
		var (
			configCM              = NewConfigMap(oc.AsAdmin(), "openshift-config", "cloud-provider-config")
			bundleKey             = "ca-bundle.pem"
			malformedCertFilePath = generateTemplateAbsolutePath("malformedcert.pem")
			mcc                   = NewController(oc.AsAdmin())
			expectedErrorMsg      = "Malformed certificate 'CloudProviderCAData' detected and is not syncing. Error: x509: malformed certificate, Cert data: -----BEGIN CERTIFICATE---"
			restoreFunc           func() error
		)

		if !configCM.Exists() {
			g.Skip(fmt.Sprintf("%s does not exist, we cannot recofigure it", configCM))
		}

		currentBundle, hasKey, err := configCM.HasKey(bundleKey)
		o.Expect(err).NotTo(o.HaveOccurred(), "Error checking if key %s exists in %s", bundleKey, configCM)
		if hasKey {
			restoreFunc = func() error {
				logger.Infof("Restoring initial data in %s", configCM)
				configCM.oc.NotShowInfo()
				return configCM.SetData(bundleKey + "=" + currentBundle)
			}
		} else {
			restoreFunc = func() error {
				return configCM.RemoveDataKey(bundleKey)
			}
		}
		defer restoreFunc()

		exutil.By("Configure a malformed certificate")
		o.Expect(
			configCM.SetData("--from-file="+bundleKey+"="+malformedCertFilePath),
		).To(o.Succeed(), "Error configuring the %s value in %s", bundleKey, malformedCertFilePath)
		logger.Infof("OK!\n")

		exutil.By("Check that the error is correctly reported")
		o.Eventually(mcc.GetLogs, "5m", "20s").Should(o.ContainSubstring(expectedErrorMsg),
			"The malformed certificate is not correctly reported in the controller logs")
		logger.Infof("OK!\n")

		exutil.By("Restore the initial certificate values")
		o.Expect(restoreFunc()).To(o.Succeed(),
			"Error restoring the initial certificate values in %s", configCM)
		logger.Infof("OK!\n")

		exutil.By("Check that no more errors are reported")
		currentLogs, err := mcc.GetLogs()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting MCC logs")
		o.Eventually(func() (string, error) {
			// we return the new recently printed logs only
			var diffLogs string
			newLogs, err := mcc.GetLogs()
			if err != nil {
				return "", err
			}
			diffLogs = strings.ReplaceAll(newLogs, currentLogs, "")
			currentLogs = newLogs
			logger.Infof("Checking diff logs: %s", diffLogs)
			return diffLogs, nil

		}, "5m", "20s").ShouldNot(o.ContainSubstring(expectedErrorMsg),
			"The certificate was fixed but the controller is still reporting an error")

		o.Consistently(func() (string, error) {
			// we return the new recently printed logs only
			var diffLogs string
			newLogs, err := mcc.GetLogs()
			if err != nil {
				return "", err
			}
			diffLogs = strings.ReplaceAll(newLogs, currentLogs, "")
			currentLogs = newLogs
			logger.Infof("Checking diff logs: %s", diffLogs)
			return diffLogs, nil

		}, "1m", "20s").ShouldNot(o.ContainSubstring(expectedErrorMsg),
			"The certificate was fixed but the controller is still reporting an error")
		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:77784][OTP] Implement a Controller Path for Managing User-Data Secrets and Rotating MCS TLS Certificates [Disruptive]", func() {
		skipTestIfWorkersCannotBeScaled(oc.AsAdmin())

		var (
			mcsCaSecret                 = NewSecret(oc.AsAdmin(), "openshift-machine-config-operator", "machine-config-server-ca")
			mcsTLSSecret                = NewSecret(oc.AsAdmin(), "openshift-machine-config-operator", "machine-config-server-tls")
			notAfterAnnotaion           = "auth.openshift.io/certificate-not-after"
			notBeforeAnnotaion          = "auth.openshift.io/certificate-not-before"
			workerUserDataSecret        = NewSecret(oc.AsAdmin(), MachineAPINamespace, "worker-user-data")
			masterUserDataSecret        = NewSecret(oc.AsAdmin(), MachineAPINamespace, "master-user-data")
			workerUserDataManagedSecret = NewSecret(oc.AsAdmin(), MachineAPINamespace, "worker-user-data-managed")
			masterUserDataManagedSecret = NewSecret(oc.AsAdmin(), MachineAPINamespace, "worker-user-data-managed")
		)

		exutil.By("To verify the MCS-CA and MCS-TLS secret present with required annotation")
		o.Expect(mcsCaSecret).To(Exist())
		o.Expect(mcsTLSSecret).To(Exist())

		o.Expect(mcsCaSecret.GetAnnotationOrFail(notAfterAnnotaion)).NotTo(o.BeEmpty())
		o.Expect(mcsCaSecret.GetAnnotationOrFail(notBeforeAnnotaion)).NotTo(o.BeEmpty())
		o.Expect(mcsTLSSecret.GetAnnotationOrFail(notAfterAnnotaion)).NotTo(o.BeEmpty())
		o.Expect(mcsTLSSecret.GetAnnotationOrFail(notBeforeAnnotaion)).NotTo(o.BeEmpty())

		logger.Infof("OK!\n")

		exutil.By("Edit the MCS-TLS Secret and check the certificate rotated")
		newCert := rotateTLSSecretOrFail(mcsTLSSecret)
		logger.Debugf("New TLS cert:\n%s", newCert)
		logger.Infof("OK!\n")

		exutil.By("Edit the MCS-CA Secret and check config map update with new one")
		verifyMcsCASecretRotateOrFail(mcsCaSecret, workerUserDataManagedSecret, masterUserDataManagedSecret, workerUserDataSecret, masterUserDataSecret)
		logger.Infof("OK!\n")

		exutil.By("Verify if able to add new worker node.")
		msl, err := NewMachineSetList(oc.AsAdmin(), MachineAPINamespace).GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(), "Get machinesets failed")
		o.Expect(msl).ShouldNot(o.BeEmpty(), "Machineset list is empty")
		ms := msl[0]

		o.Expect(ms.AddToScale(1)).NotTo(o.HaveOccurred())

		defer func() {
			exutil.By("Scale to orignal no. of worker node")
			ms.AddToScale(-1)
			mcp.waitForComplete()
		}()

		o.Eventually(ms.GetIsReady, "20m", "2m").Should(o.BeTrue(), "MachineSet %s is not ready", ms.GetName())

		logger.Infof("OK!\n")

	})

	g.It("[PolarionID:80438][OTP] Unexpected Permissions in cluster-reader ClusterRole", func() {
		var (
			clusterReaderCR = NewResource(oc.AsAdmin(), "ClusterRole", "cluster-reader")
		)

		o.Eventually(clusterReaderCR.Get, "2m", "10s").WithArguments(`{.rules[*].verbs}`).ShouldNot(
			o.Or(
				o.ContainSubstring(`patch`),
				o.ContainSubstring(`delete`),
				o.ContainSubstring(`update`),
			), `%s Should not contain "patch", "delete", or "update" verbs`, clusterReaderCR,
		)
	})

	g.It("[PolarionID:43278][OTP] security fix for unsafe cipher [Serial]", func() {
		exutil.By("check go version >= 1.15")
		_, clusterVersion, cvErr := exutil.GetClusterVersion(oc)
		o.Expect(cvErr).NotTo(o.HaveOccurred())
		o.Expect(clusterVersion).NotTo(o.BeEmpty())
		logger.Infof("cluster version is %s", clusterVersion)
		commitID, commitErr := getCommitID(oc, "machine-config", clusterVersion)
		o.Expect(commitErr).NotTo(o.HaveOccurred())
		// there is a case that in the payload no commit id from mco
		if commitID == "" {
			g.Skip("No code change from MCO, skip this case")
		}
		logger.Infof("machine config commit id is %s", commitID)
		goVersion, verErr := getGoVersion("machine-config-operator", commitID)
		o.Expect(verErr).NotTo(o.HaveOccurred())
		logger.Infof("go version is: %f", goVersion)
		o.Expect(goVersion).Should(o.BeNumerically(">=", 1.15))

		exutil.By("verify TLS protocol version is 1.3")
		intAPIServerURI, err := GetAPIServerInternalURI(oc.AsAdmin())
		o.Expect(err).NotTo(o.HaveOccurred())
		masterNode := NewNodeList(oc.AsAdmin()).GetAllMasterNodesOrFail()[0]
		sslOutput, sslErr := masterNode.DebugNodeWithChroot("bash", "-c", fmt.Sprintf("printf 'Q\\n' | openssl s_client -connect %s:6443", intAPIServerURI))
		logger.Infof("ssl protocol version is:\n %s", sslOutput)
		o.Expect(sslErr).NotTo(o.HaveOccurred())
		o.Expect(sslOutput).Should(o.ContainSubstring("TLSv1.3"))

		exutil.By("verify whether the unsafe cipher is disabled")
		cipherOutput, cipherErr := masterNode.DebugNodeWithOptions([]string{"--image=" + TestSSLImage, "-n", MachineConfigNamespace}, "testssl.sh", "--quiet", "--sweet32", "localhost:6443")
		logger.Infof("test ssh script output:\n %s", cipherOutput)
		o.Expect(cipherErr).NotTo(o.HaveOccurred())
		o.Expect(cipherOutput).Should(o.ContainSubstring("not vulnerable (OK)"))
	})

	g.It("[PolarionID:46965][OTP] Avoid workload disruption for GPG Public Key Rotation [Serial]", func() {

		exutil.By("create new machine config with base64 encoded gpg public key")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		startTime := workerNode.GetDateOrFail()
		mcName := "add-gpg-pub-key"
		mcTemplate := "add-gpg-pub-key.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		defer mc.DeleteWithWait()
		mc.create()

		exutil.By("checkout machine config daemon logs to verify ")
		log, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(log).Should(o.ContainSubstring("/etc/machine-config-daemon/no-reboot/containers-gpg.pub"))
		o.Expect(log).Should(o.ContainSubstring("Changes do not require drain, skipping"))
		o.Expect(log).Should(o.MatchRegexp(MCDCrioReloadedRegexp))

		o.Expect(workerNode.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", workerNode.GetName())

		exutil.By("verify crio.service status")
		cmdOut, cmdErr := workerNode.DebugNodeWithChroot("systemctl", "is-active", "crio.service")
		o.Expect(cmdErr).NotTo(o.HaveOccurred())
		o.Expect(cmdOut).Should(o.ContainSubstring("active"))

	})

	g.It("[PolarionID:47062][OTP] change policy.json on worker nodes [Serial]", func() {

		exutil.By("create new machine config to change /etc/containers/policy.json")
		workerNode := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		startTime := workerNode.GetDateOrFail()
		mcName := "change-policy-json"
		mcTemplate := "change-policy-json.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		defer mc.DeleteWithWait()
		mc.create()

		exutil.By("verify file content changes")
		fileContent, fileErr := workerNode.DebugNodeWithChroot("cat", "/etc/containers/policy.json")
		o.Expect(fileErr).NotTo(o.HaveOccurred())
		logger.Infof(fileContent)
		o.Expect(fileContent).Should(o.ContainSubstring(`{"default": [{"type": "insecureAcceptAnything"}]}`))
		o.Expect(fileContent).ShouldNot(o.ContainSubstring("transports"))

		exutil.By("checkout machine config daemon logs to make sure node drain/reboot are skipped")
		log, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, workerNode.GetMachineConfigDaemon(), "")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(log).Should(o.ContainSubstring("/etc/containers/policy.json"))
		o.Expect(log).Should(o.ContainSubstring("Changes do not require drain, skipping"))
		o.Expect(log).Should(o.MatchRegexp(MCDCrioReloadedRegexp))

		o.Expect(workerNode.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", workerNode.GetName())

		exutil.By("verify crio.service status")
		cmdOut, cmdErr := workerNode.DebugNodeWithChroot("systemctl", "is-active", "crio.service")
		o.Expect(cmdErr).NotTo(o.HaveOccurred())
		o.Expect(cmdOut).Should(o.ContainSubstring("active"))

	})

	g.It("[PolarionID:62084][OTP] Certificate rotation in paused pools [Disruptive]", func() {
		var (
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			certSecret = NewSecret(oc.AsAdmin(), "openshift-kube-apiserver-operator", "kube-apiserver-to-kubelet-signer")
		)

		exutil.By("Pause MachineConfigPools")
		defer mMcp.waitForComplete()
		defer wMcp.waitForComplete()

		defer wMcp.pause(false)
		wMcp.pause(true)
		defer mMcp.pause(false)
		mMcp.pause(true)
		logger.Infof("OK!\n")

		exutil.By("Get current kube-apiserver certificate")
		initialCert := certSecret.GetDataValueOrFail("tls.crt")
		logger.Infof("Current certificate length: %d", len(initialCert))
		logger.Infof("OK!\n")

		exutil.By("Rotate certificate")
		o.Expect(
			certSecret.Patch("merge", `{"metadata": {"annotations": {"auth.openshift.io/certificate-not-after": null}}}`),
		).To(o.Succeed(),
			"The secret could not be patched in order to rotate the certificate")
		logger.Infof("OK!\n")

		exutil.By("Get current kube-apiserver certificate")
		logger.Infof("Wait for certificate rotation")
		o.Eventually(certSecret.GetDataValueOrFail, "5m", "10s").WithArguments("tls.crt").
			ShouldNot(o.Equal(initialCert),
				"The certificate was not rotated")

		newCert := certSecret.GetDataValueOrFail("tls.crt")
		logger.Infof("New certificate length: %d", len(newCert))
		logger.Infof("OK!\n")

		o.Expect(initialCert).NotTo(o.Equal(newCert),
			"The certificate was not rotated")
		logger.Infof("OK!\n")

		// We verify all nodes in the pools (be aware that windows nodes do not belong to any pool, we are skipping them)
		for _, node := range append(wMcp.GetNodesOrFail(), mMcp.GetNodesOrFail()...) {
			logger.Infof("Checking certificate in node: %s", node.GetName())

			rfCert := NewRemoteFile(node, "/etc/kubernetes/kubelet-ca.crt")

			// Eventually the certificate file in all nodes should contain the new rotated certificate
			o.Eventually(func(gm o.Gomega) string { // Passing o.Gomega as parameter we can use assertions inside the Eventually function without breaking the retries.
				gm.Expect(rfCert.Fetch()).To(o.Succeed(),
					"Cannot read the certificate file in node:%s ", node.GetName())
				return rfCert.GetTextContent()
			}, "5m", "10s").
				Should(o.ContainSubstring(newCert),
					"The certificate file %s in node %s does not contain the new rotated certificate.", rfCert.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")
		}

		exutil.By("Unpause MachineConfigPools")
		logger.Infof("Check that once we unpause the pools the pending config can be applied without problems")
		wMcp.pause(false)
		mMcp.pause(false)
		wMcp.waitForComplete()
		mMcp.waitForComplete()

		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:65208][OTP] Check the visibility of certificates [Serial]", func() {
		var (
			cc   = NewControllerConfig(oc.AsAdmin(), "machine-config-controller")
			mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		)

		exutil.By("Check that the ControllerConfig resource is storing the right kube-apiserver-client-ca information")
		kubeAPIServerClientCACM := NewNamespacedResource(oc.AsAdmin(), "ConfigMap", "openshift-config-managed", "kube-apiserver-client-ca")
		kubeAPIServerClientCA := kubeAPIServerClientCACM.GetOrFail(`{.data.ca-bundle\.crt}`)

		ccKubeAPIServerClientCA, err := cc.GetKubeAPIServerServingCAData()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the kubeAPIServerServingCAData information from the ControllerConfig")

		// We write the Expect command so that the certificates are not printed in case of failure
		o.Expect(strings.Trim(ccKubeAPIServerClientCA, "\n") == strings.Trim(kubeAPIServerClientCA, "\n")).To(o.BeTrue(),
			"The value of kubeAPIServerServingCAData in the ControllerConfig does not equal the value of configmap -n openshift-config-managed kube-apiserver-client-ca")
		logger.Infof("OK!\n")

		exutil.By("Check that the ControllerConfig resource is storing the right rootCAData  information")
		rootCADataCM := NewNamespacedResource(oc.AsAdmin(), "ConfigMap", "kube-system", "root-ca")
		rootCAData := rootCADataCM.GetOrFail(`{.data.ca\.crt}`)

		ccRootCAData, err := cc.GetRootCAData()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the rootCAData information from the ControllerConfig")

		// We write the Expect command so that the certificates are not printed in case of failure
		o.Expect(strings.Trim(ccRootCAData, "\n") == strings.Trim(rootCAData, "\n")).To(o.BeTrue(),
			"The value of rootCAData in the ControllerConfig does not equal the value of configmap -n kube-system root-ca")
		logger.Infof("OK!\n")

		exutil.By("Check the information from the KubeAPIServerServingCAData certificates")

		ccKCertsInfo, err := cc.GetCertificatesInfoByBundleFileName("KubeAPIServerServingCAData")
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the controller config information for KubeAPIServerServingCAData certificates")

		kubeAPIServerCertsInfo, err := GetCertificatesInfoFromPemBundle("KubeAPIServerServingCAData", []byte(ccKubeAPIServerClientCA))
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error extracting certificate info from KubeAPIServerServingCAData pem bundle")

		o.Expect(kubeAPIServerCertsInfo).To(o.Equal(ccKCertsInfo),
			"The ControllerConfig is not reporting the right information about the certificates in KubeAPIServerServingCAData bundle")

		logger.Infof("OK!\n")

		exutil.By("Check the information from the rootCAData certificates")

		ccRCertsInfo, err := cc.GetCertificatesInfoByBundleFileName("RootCAData")
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the controller config information for rootCAData certificates")

		rootCACertsInfo, err := GetCertificatesInfoFromPemBundle("RootCAData", []byte(ccRootCAData))
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error extracting certificate info from RootCAData pem bundle")

		o.Expect(rootCACertsInfo).To(o.Equal(ccRCertsInfo),
			"The ControllerConfig is not reporting the right information about the certificates in rootCAData bundle")
		logger.Infof("OK!\n")

		exutil.By("Check that MCPs are reporting information regarding kubeapiserverserviccadata certificates")
		certsExpiry, err := mMcp.GetCertsExpiry()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the certificates expiry information from master MCP")

		o.Expect(certsExpiry).To(o.HaveLen(len(ccKCertsInfo)),
			"The expry certs info reported in master MCP has len %d, but the list of kubeAPIServer certs has len %d.\nExpiry:%s\nKubeAPIServer:%s",
			len(certsExpiry), len(ccKCertsInfo), certsExpiry, ccKCertsInfo)

		for i, certInfo := range ccKCertsInfo {
			certExpry := certsExpiry[i]

			logger.Infof("%s", certExpry)

			o.Expect(certExpry.Bundle).To(o.Equal(certInfo.BundleFile),
				"Bundle mismatch at index %d", i)
			// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
			o.Expect(certExpry.Expiry).To(o.Equal(certInfo.NotAfter),
				"Expiry information does not match ControllerConfig at index %d", i)
			o.Expect(certExpry.Subject).To(o.Equal(certInfo.Subject),
				"Subject mismatch at index %d", i)
		}

		logger.Infof("OK!\n")

		exutil.By("Check that the description of ControllerConfig includes the certificates info")
		ccDesc, err := cc.Describe()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error describing the ControllerConfig resource")

		o.Expect(ccDesc).To(o.And(
			o.ContainSubstring("Controller Certificates:"),
			o.ContainSubstring("Bundle File"),
			// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
			o.ContainSubstring("Not After"),
			o.ContainSubstring("Not Before"),
			o.ContainSubstring("Signer"),
			o.ContainSubstring("Subject"),
		),
			"The ControllerConfig description should include information about the certificate, but it does not:\n%s", ccDesc)
		logger.Infof("OK!\n")

		exutil.By("Check that the description of MCP includes the certificates info")
		mMcpDesc, err := mMcp.Describe()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error describing the master MCP resource")

		o.Expect(mMcpDesc).To(o.And(
			o.ContainSubstring("Cert Expirys"),
			o.ContainSubstring("Bundle"),
			// Date fields have been temporarily removed by devs:  https://github.com/openshift/machine-config-operator/pull/3866
			o.ContainSubstring("Expiry"),
		),
			"The master MCP description should include information about the certificate, but it does not:\n%s", mMcpDesc)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:66436][OTP] disable weak SSH cipher suites [Serial]", func() {

		var (
			// the list of weak cipher suites can be found here:  https://issues.redhat.com/browse/OCPBUGS-15202
			weakSuites = []string{"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
				"TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA",
				"TLS_RSA_WITH_AES_128_CBC_SHA",
				"TLS_RSA_WITH_AES_256_CBC_SHA",
				"TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256"}
		)

		exutil.By("Verify that the controller pod is not using weakSuites")
		ccRbacProxyArgs, err := oc.AsAdmin().WithoutNamespace().Run("get").Args("pod", "-n", MachineConfigNamespace, "-l", ControllerLabel+"="+ControllerLabelValue,
			"-o", `jsonpath={.items[0].spec.containers[?(@.name=="kube-rbac-proxy")].args}`).Output()

		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the arguments used in kube-rbac-proxy container in the controller pod")

		o.Expect(ccRbacProxyArgs).To(o.ContainSubstring("--tls-cipher-suites"),
			"Controller's kube-rbac-proxy container is not declaring the list of allowed cipher suites")

		for _, weakSuite := range weakSuites {
			logger.Infof("Verifying that %s is not used", weakSuite)
			o.Expect(ccRbacProxyArgs).NotTo(o.ContainSubstring(weakSuite),
				"Controller's kube-rbac-proxy container is using the weak cipher suite %s, and it should not", weakSuite)
			logger.Infof("Suite ok")
		}
		logger.Infof("OK!\n")

		exutil.By("Connect to the rbac-proxy service to verify the cipher")
		mMcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		masterNode := mMcp.GetNodesOrFail()[0]
		cipherOutput, cipherErr := masterNode.DebugNodeWithOptions([]string{"--image=" + TestSSLImage, "-n", MachineConfigNamespace}, "testssl.sh", "--color", "0", "localhost:9001")
		logger.Infof("test ssh script output:\n %s", cipherOutput)
		o.Expect(cipherErr).NotTo(o.HaveOccurred())
		o.Expect(cipherOutput).Should(o.MatchRegexp(`Obsoleted CBC ciphers \(AES, ARIA etc.\) +not offered`))

		for _, weakSuite := range weakSuites {
			logger.Infof("Verifying that %s is not used", weakSuite)
			o.Expect(cipherOutput).NotTo(o.ContainSubstring(weakSuite),
				"The rbac-proxy service cipher test is reporting weak cipher suite: %s", weakSuite)
			logger.Infof("Suite ok")
		}
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:67395][OTP] rotate kubernetes certificate authority. Certificates managed via non-MC path [Disruptive]", func() {

		var (
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			mcList     = NewMachineConfigList(oc.AsAdmin())
			certSecret = NewSecret(oc.AsAdmin(), "openshift-kube-apiserver-operator", "kube-apiserver-to-kubelet-signer")
		)

		logger.Infof("Get currently rendered MCs")
		initialMCs, err := mcList.GetAll()
		o.Expect(err).NotTo(o.HaveOccurred(),
			"Error getting the list of rendered MCs")
		logger.Infof("%d rendered MCs", len(initialMCs))
		logger.Infof("OK!\n")

		exutil.By("Get start time and start collecting events.")
		// be aware that windows nodes do not belong to any pool, we are skipping them
		checkedNodes := append(mMcp.GetNodesOrFail(), wMcp.GetNodesOrFail()...)

		startTime, dErr := checkedNodes[0].GetDate()
		o.Expect(dErr).ShouldNot(o.HaveOccurred(), "Error getting date in node %s", checkedNodes[0].GetName())

		for i := range checkedNodes {
			o.Expect(checkedNodes[i].IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
				"Error getting the latest event in node %s", checkedNodes[i].GetName())
		}
		logger.Infof("OK!\n")

		exutil.By("Rotate certificate")
		newCert := rotateTLSSecretOrFail(certSecret)
		logger.Infof("OK!\n")

		exutil.By("Check that no new MC is created")
		o.Consistently(mcList.GetAll, "5m", "1m").Should(o.HaveLen(len(initialMCs)),
			"New machine configs have been created, but they should not be created")
		logger.Infof("OK!\n")

		for _, node := range checkedNodes {
			exutil.By(fmt.Sprintf("Checking that the certificate is rotated in node: %s", node.GetName()))

			rfCert := NewRemoteFile(node, "/etc/kubernetes/kubelet-ca.crt")

			// Eventually the certificate file in all nodes should contain the new rotated certificate
			o.Eventually(func(gm o.Gomega) string { // Passing o.Gomega as parameter we can use assertions inside the Eventually function without breaking the retries.
				gm.Expect(rfCert.Fetch()).To(o.Succeed(),
					"Cannot read the certificate file in node:%s ", node.GetName())
				return rfCert.GetTextContent()
			}, "5m", "10s").
				Should(o.ContainSubstring(newCert),
					"The certificate file %s in node %s does not contain the new rotated certificate.", rfCert.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Checking that node: %s was not rebooted", node.GetName()))
			o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
				"The node %s must NOT be rebooted after rotating the certificate, but it was rebooted. Uptime date happened after the start config time.", node.GetName())

			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Checking events in node: %s", node.GetName()))
			o.Expect(node.GetEvents()).NotTo(HaveEventsSequence("Drain"),
				"Error, a Drain event was triggered but it shouldn't")
			o.Expect(node.GetEvents()).NotTo(HaveEventsSequence("Reboot"),
				"Error, a Reboot event was triggered but it shouldn't")

			logger.Infof("OK!\n")

		}
	})

	// OCPBUGS-86332: Starting in 4.22 we will no longer fix any bootimage related bugs from bootimages earlier than 4.13
	// Removed test: PolarionID:80403 (4.5)
})

// EventuallyFileExistsInNode fails the test if the certificate file does not exist in the node after the time specified as parameters
func EventuallyImageRegistryCertificateExistsInNode(certFileName, certContent string, node *Node, timeout, poll string) {
	certPath := filepath.Join(ImageRegistryCertificatesDir, certFileName, ImageRegistryCertificatesFileName)
	EventuallyFileExistsInNode(certPath, certContent, node, timeout, poll)
}

// EventuallyFileExistsInNode fails the test if the file does not exist in the node after the time specified as parameters
func EventuallyFileExistsInNode(filePath, expectedContent string, node *Node, timeout, poll string) {
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
func BundleFileIsCATrusted(bundleFile string, node *Node) (bool, error) {
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
		logger.Errorf("Error fetching remote file %s", bundleRemote)
		return false, err
	}

	err = objsignCABundleRemote.Fetch()
	if err != nil {
		logger.Errorf("Error fetching remote file %s", objsignCABundleRemote)
		return false, err
	}

	bundleCerts, err := splitBundleCertificates([]byte(bundleRemote.GetTextContent()))
	if err != nil {
		logger.Errorf("Error splitting file %s", bundleRemote)
		return false, err
	}

	objsignCABundleCerts, err := splitBundleCertificates([]byte(objsignCABundleRemote.GetTextContent()))
	if err != nil {
		logger.Errorf("Error splitting file %s", objsignCABundleRemote)
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
			if strings.Contains(err.Error(), "negative serial number") {
				// Since golang 1.23 certificates with negative serial numbers are considered a failure
				// https://stackoverflow.com/questions/79061981/failed-to-parse-certificate-from-server-x509-negative-serial-number
				// We don't want to modify the compilation, so we just ignore the failing certificates
				logger.Errorf("Error parsing certificate because of negative serial number. Ignoring certificate: %s", err)
				pemBundle = rest
				continue
			}
			return nil, fmt.Errorf("Error parsing certificate: %s", err)
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
	logger.Infof("Found %d kube-proxy pods\n", len(getKubeProxyPod))

	mccPodName, err := getMachineConfigControllerPod(oc.AsAdmin())
	o.Expect(err).NotTo(o.HaveOccurred())
	mcc := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, mccPodName)
	logger.Infof("%s\n", mcc.GetOrFail(containerArgsPath))
	logger.Infof("OK!\n")

	mcspod, err := GetMCSPodNames(oc.AsAdmin())
	o.Expect(err).NotTo(o.HaveOccurred())
	logger.Infof("Found %d MCS pods\n", len(mcspod))

	logger.Infof("%s\n", apiServer.GetOrFail(tlsProfileTypePath))
	logger.Infof("OK!\n")

	o.Expect(apiServer.GetOrFail(tlsProfileTypePath)).To(o.ContainSubstring(tlsSecurityProfile), "The %s tlsSecuirtyProfile is not applied properly", tlsSecurityProfile)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-min-version for %s in all kube-proxy pods", tlsSecurityProfile))
	for _, kubeProxyPodName := range getKubeProxyPod {
		kubeproxy := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, kubeProxyPodName)
		logger.Infof("Checking kube-proxy pod: %s\n", kubeProxyPodName)
		o.Expect(kubeproxy.GetOrFail(containerArgsPath)).To(o.ContainSubstring("--tls-min-version=%s", tlsMinVersionStr), "Error getting required tls-min-version for given tlsSecuirtyProfile in %s pod", kubeProxyPodName)
	}
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-min-version for %s in MCC pod", tlsSecurityProfile))
	o.Expect(mcc.GetOrFail(containerArgsPath)).To(o.ContainSubstring("--tls-min-version=%s", tlsMinVersionStr), "Error getting required tls-min-version for tlsSecuirtyProfile in %s pod", mccPodName)
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-min-version for %s in all MCS pods", tlsSecurityProfile))
	for _, mcsPodName := range mcspod {
		logger.Infof("Checking MCS pod: %s\n", mcsPodName)
		o.Eventually(exutil.GetSpecificPodLogs, "3m", "10s").WithArguments(oc, MachineConfigNamespace, MachineConfigServer, mcsPodName, "").
			Should(o.ContainSubstring(tlsMinVersionStr), "Error getting required tls-min-version for %s pod", mcsPodName)

	}
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-cipher-suite for %s in all kube-proxy pods", tlsSecurityProfile))
	for _, kubeProxyPodName := range getKubeProxyPod {
		kubeproxy := NewNamespacedResource(oc.AsAdmin(), "pod", MachineConfigNamespace, kubeProxyPodName)
		logger.Infof("Checking kube-proxy pod: %s\n", kubeProxyPodName)
		for i := range cipherSuite {
			o.Expect(kubeproxy.GetOrFail(containerArgsPath)).To(o.ContainSubstring(cipherSuite[i]), "Error getting %s cipher suite for given tlsSecuirtyProfile of %s pod", cipherSuite[i], kubeProxyPodName)
		}
	}
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-cipher-suite for %s in MCC pod", tlsSecurityProfile))
	for i := range cipherSuite {
		o.Expect(mcc.GetOrFail(containerArgsPath)).To(o.ContainSubstring(cipherSuite[i]), "Error getting %s cipher suite for given tlsSecuirtyProfile of  %s pod", cipherSuite[i], mccPodName)
	}
	logger.Infof("OK!\n")

	exutil.By(fmt.Sprintf("To check the valid tls-cipher-suite for %s in all MCS pods", tlsSecurityProfile))
	for _, mcsPodName := range mcspod {
		logger.Infof("Checking MCS pod: %s\n", mcsPodName)
		var mcsLogs string
		o.Eventually(func() (string, error) {
			var err error
			mcsLogs, err = exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigServer, mcsPodName, "")
			return mcsLogs, err
		}, "3m", "10s").ShouldNot(o.BeEmpty(), "Cannot get MCS logs")

		o.Expect(err).NotTo(o.HaveOccurred())
		for i := range cipherSuite {
			o.Expect(mcsLogs).To(o.ContainSubstring(cipherSuite[i]), "Error getting %s cipher suite for given tlsSecuirtyProfile of %s pod", cipherSuite[i], mcsPodName)
		}
	}
	logger.Infof("OK!\n")
}

func validateCorrectTLSProfileSecurityInKubeletConfig(node *Node, tlsMinVersion string, cipherSuite []string) {
	stdout, err := node.DebugNodeWithChroot("cat", "/etc/kubernetes/kubelet.conf")
	o.Expect(err).NotTo(o.HaveOccurred())
	exutil.By("To check the kubeletConfig to have same tls setting as of API server")
	o.Expect(stdout).To(o.ContainSubstring("tlsMinVersion: %s", tlsMinVersion), "Error %s tlsMinVersion is not updated in kubelet config", tlsMinVersion)
	for _, csname := range cipherSuite {
		o.Expect(stdout).To(o.ContainSubstring(csname), "Error %s cipher suite is not updated in kubelet config", csname)
	}
	logger.Infof("OK!\n")
}
