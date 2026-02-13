package extended

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/longduration][Serial][Disruptive] MCO Storage", func() {
	defer g.GinkgoRecover()

	var oc = exutil.NewCLI("mco-storage", exutil.KubeConfigPath())

	g.JustBeforeEach(func() {
		PreChecks(oc)
	})

	g.It("[PolarionID:42520][OTP] retrieve mc with large size from mcs [Disruptive]", func() {
		exutil.By("create new mc to add 100+ dummy files to /var/log")
		mcName := "bz1866117-add-dummy-files"
		mcTemplate := "bz1866117-add-dummy-files.yaml"
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker).SetMCOTemplate(mcTemplate)
		defer mc.DeleteWithWait()
		mc.create()

		exutil.By("get one master node to do mc query")
		masterNode := NewNodeList(oc.AsAdmin()).GetAllMasterNodesOrFail()[0]
		stdout, err := masterNode.DebugNode("curl", "-w", "'Total: %{time_total}'", "-k", "-s", "-o", "/dev/null", "https://localhost:22623/config/worker")
		o.Expect(err).NotTo(o.HaveOccurred())

		var timecost float64
		for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
			if strings.Contains(line, "Total") {
				substr := line[8 : len(line)-1]
				timecost, _ = strconv.ParseFloat(substr, 64)
				break
			}
		}
		logger.Infof("api time cost is: %f", timecost)
		o.Expect(timecost).Should(o.BeNumerically("<", 10.0))
	})

	g.It("[PolarionID:46314][OTP] Incorrect file contents if compression field is specified [Serial]", func() {
		exutil.By("Create a new MachineConfig to provision a config file in zipped format")

		fileContent := `Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do
 eiusmod tempor incididunt ut labore et dolore magna aliqua.  Ut
 enim ad minim veniam, quis nostrud exercitation ullamco laboris
nisi ut aliquip ex ea commodo consequat.  Duis aute irure dolor in
reprehenderit in voluptate velit esse cillum dolore eu fugiat
nulla pariatur.  Excepteur sint occaecat cupidatat non proident,
sunt in culpa qui officia deserunt mollit anim id est laborum.


nulla pariatur.`

		mcName := "99-gzip-test"
		destPath := "/etc/test-file"
		fileConfig := getGzipFileJSONConfig(destPath, fileContent)

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		defer mc.DeleteWithWait()

		err := mc.Create("-p", "NAME="+mcName, "-p", "POOL=worker", "-p", fmt.Sprintf("FILES=[%s]", fileConfig))
		o.Expect(err).NotTo(o.HaveOccurred())

		exutil.By("Wait until worker MachineConfigPool has finished the configuration")
		mcp := NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mcp.waitForComplete()

		exutil.By("Verfiy that the file has been properly provisioned")
		node := NewNodeList(oc.AsAdmin()).GetAllLinuxWorkerNodesOrFail()[0]
		rf := NewRemoteFile(node, destPath)
		err = rf.Fetch()
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(rf.GetTextContent()).To(o.Equal(fileContent))
		o.Expect(rf.GetNpermissions()).To(o.Equal("0644"))
		o.Expect(rf.GetUIDName()).To(o.Equal("root"))
		o.Expect(rf.GetGIDName()).To(o.Equal("root"))
	})

	g.It("[PolarionID:72129][OTP] Don't allow creating the force file via MachineConfig [Disruptive]", func() {
		var (
			filePath    = "/run/machine-config-daemon-force"
			fileContent = ""
			fileMode    = "0420" // decimal 272
			fileConfig  = getURLEncodedFileConfig(filePath, fileContent, fileMode)
			mcName      = "mco-tc-55879-create-force-file"
			mcp         = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)

			expectedRDMessage = regexp.QuoteMeta(fmt.Sprintf("cannot create %s via Ignition", filePath)) // quotemeta to scape regex characters in the file path
			expectedRDReason  = ""
		)

		exutil.By("Create the force file using a MC")

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		mc.skipWaitForMcp = true

		validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)

	})

	g.It("[PolarionID:59837][OTP] Use wrong user when creating a file [Disruptive]", func() {
		var (
			mcName              = "mco-tc-59837-create-file-with-wrong-user"
			wrongUserFileConfig = `{"contents": {"source": "data:text/plain;charset=utf-8;base64,dGVzdA=="},"mode": 420,"path": "/etc/wronguser-test-file.test","user": {"name": "wronguser"}}`
			mcp                 = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			// quotemeta to scape regex characters in the file path
			expectedNDMessage = regexp.QuoteMeta(`failed to retrieve file ownership for file \"/etc/wronguser-test-file.test\": failed to retrieve UserID for username: wronguser`)
			expectedNDReason  = "1 nodes are reporting degraded status on sync"
		)

		exutil.By("Create the force file using a MC")

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", wrongUserFileConfig)}
		mc.skipWaitForMcp = true

		validateMcpNodeDegraded(mc, mcp, expectedNDMessage, expectedNDReason, true)

	})

	g.It("[PolarionID:59867][OTP] Create files specifying user and group [Disruptive]", func() {
		var (
			filesContent = "test"
			coreUserID   = 1000
			coreGroupID  = 1000
			rootUserID   = 0
			admGroupID   = 4

			allFiles = []ign32File{
				{
					Path: "/etc/core-core-name-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(420), // decimal 0644
					User: &ign32FileUser{
						Name: "core",
					},
					Group: &ign32FileGroup{
						Name: "core",
					},
				},
				{
					Path: "/etc/core-core-id-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(416), // decimal 0640
					User: &ign32FileUser{
						ID: PtrInt(coreUserID), // core user ID number
					},
					Group: &ign32FileGroup{
						ID: PtrInt(coreGroupID), // core group ID number
					},
				},
				{
					Path: "/etc/root-adm-id-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(384), // decimal 0600
					User: &ign32FileUser{
						ID: PtrInt(rootUserID),
					},
					Group: &ign32FileGroup{
						ID: PtrInt(admGroupID),
					},
				},
				{
					Path: "/etc/nouser-test-file.test",
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(filesContent),
					},
					Mode: PtrInt(420), // decimal 0644
					User: &ign32FileUser{
						ID: PtrInt(12343), // this user does not exist
					},
					Group: &ign32FileGroup{
						ID: PtrInt(34321), // this group does not exist
					},
				},
			}
			wMcp       = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			workerNode = wMcp.GetNodesOrFail()[0] // we don't want to get "windows" nodes
		)

		exutil.By("Create new machine config to create files with different users and groups")
		fileConfig := MarshalOrFail(allFiles)
		mcName := "tc-59867-create-files-with-users"

		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf("FILES=%s", fileConfig)}
		defer mc.DeleteWithWait()

		mc.create()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that all files have been created with the right user, group, permissions and data")
		for _, file := range allFiles {
			logger.Infof("")
			logger.Infof("CHecking file: %s", file.Path)
			rf := NewRemoteFile(workerNode, file.Path)
			o.Expect(rf.Fetch()).NotTo(o.HaveOccurred(), "Error getting the file %s in node %s", file.Path, workerNode.GetName())

			logger.Infof("Checking content: %s", rf.GetTextContent())
			o.Expect(rf.GetTextContent()).To(o.Equal(filesContent),
				"The content of file %s is wrong!", file.Path)

			// Verify that the defined user name or user id (only one can be defined in the config) is the expected one
			if file.User.Name != "" {
				logger.Infof("Checking user name: %s", rf.GetUIDName())
				o.Expect(rf.GetUIDName()).To(o.Equal(file.User.Name),
					"The user who owns file %s is wrong!", file.Path)
			} else {
				logger.Infof("Checking user id: %s", rf.GetUIDNumber())
				o.Expect(rf.GetUIDNumber()).To(o.Equal(fmt.Sprintf("%d", *file.User.ID)),
					"The user id what owns file %s is wrong!", file.Path)
			}

			// Verify that if the user ID is defined and its value is the core user's one. Then the name should be "core"
			if file.User.ID != nil && *file.User.ID == coreUserID {
				logger.Infof("Checking core user name for ID: %s", rf.GetUIDNumber())
				o.Expect(rf.GetUIDName()).To(o.Equal("core"),
					"The user name who owns file %s is wrong! User name for Id %s should be 'core'",
					file.Path, rf.GetUIDNumber())
			}

			// Verify that if the user ID is defined and its value is the root user's one. Then the name should be "root"
			if file.User.ID != nil && *file.User.ID == rootUserID {
				logger.Infof("Checking root user name: %s", rf.GetUIDName())
				o.Expect(rf.GetUIDName()).To(o.Equal("root"),
					"The user name who owns file %s is wrong! User name for Id %s should be 'root'",
					file.Path, rf.GetUIDNumber())
			}

			// Verify that the defined group name or group id (only one can be defined in the config) is the expected one
			if file.Group.Name != "" {
				logger.Infof("Checking group name: %s", rf.GetGIDName())
				o.Expect(rf.GetGIDName()).To(o.Equal(file.Group.Name),
					"The group that owns file %s is wrong!", file.Path)
			} else {
				logger.Infof("Checking group id: %s", rf.GetGIDNumber())
				o.Expect(rf.GetGIDNumber()).To(o.Equal(fmt.Sprintf("%d", *file.Group.ID)),
					"The group id what owns file %s is wrong!", file.Path)
			}

			// Verify that if the group ID is defined and its value is the core group's one. Then the name should be "core"
			if file.Group.ID != nil && *file.Group.ID == coreGroupID {
				logger.Infof("Checking core group name for ID: %s", rf.GetUIDNumber())
				o.Expect(rf.GetGIDName()).To(o.Equal("core"),
					"The group name who owns file %s is wrong! Group name for Id %s should be 'core'",
					file.Path, rf.GetGIDNumber())
			}

			// Verify that if the group ID is defined and its value is the adm group's one. Then the name should be "adm"
			if file.Group.ID != nil && *file.Group.ID == admGroupID {
				logger.Infof("Checking adm group name: %s", rf.GetUIDNumber())
				o.Expect(rf.GetGIDName()).To(o.Equal("adm"),
					"The group name who owns file %s is wrong! Group name for Id %s should be 'adm'",
					file.Path, rf.GetGIDNumber())
			}

			logger.Infof("Checking file permissions: %s", rf.GetNpermissions())
			decimalPerm := ConvertOctalPermissionsToDecimalOrFail(rf.GetNpermissions())
			o.Expect(decimalPerm).To(o.Equal(*file.Mode),
				"The permssions of file %s are wrong", file.Path)

			logger.Infof("OK!\n")
		}

	})

	g.It("[PolarionID:63477][OTP] Deploy files using all available ignition configs. Default 3.5.0[Disruptive]", func() {
		var (
			wMcp                   = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			mcNames                = "mc-tc-63477"
			allVersions            = []string{"2.2.0", "3.0.0", "3.1.0", "3.2.0", "3.3.0", "3.4.0", "3.5.0"}
			defaultIgnitionVersion = "3.5.0" // default version is 3.5.0 for OCP > 4.19
		)
		defer wMcp.waitForComplete()

		exutil.By("Create MCs with all available ignition versions")
		for _, version := range allVersions {
			vID := strings.ReplaceAll(version, ".", "-")
			fileConfig := ""
			filePath := "/etc/" + vID + ".test"
			mcName := mcNames + "-" + vID

			mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
			mc.skipWaitForMcp = true
			defer mc.Delete()

			logger.Infof("Create MC %s", mc.name)
			// 2.2.0 ignition config defines a different config for files
			if version == "2.2.0" {
				logger.Infof("Generating 2.2.0 file config!")
				file := ign22File{
					Path: filePath,
					Contents: ign22Contents{
						Source: GetBase64EncodedFileSourceContent(version + " test file"),
					},
					Mode:       PtrInt(420), // decimal 0644
					Filesystem: "root",
				}

				fileConfig = string(MarshalOrFail(file))
			} else {
				logger.Debugf("Generating 3.x file config!")
				file := ign32File{
					Path: filePath,
					Contents: ign32Contents{
						Source: GetBase64EncodedFileSourceContent(version + " test file"),
					},
					Mode: PtrInt(420), // decimal 0644
				}

				fileConfig = string(MarshalOrFail(file))
			}

			mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig), "IGNITION_VERSION=" + version}
			mc.create()
		}
		logger.Infof("OK!\n")

		exutil.By("Wait for MCP to be updated")
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify default rendered ignition version")
		renderedMC, err := wMcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "Cannot get the rendered config for pool %s", wMcp.GetName())
		o.Expect(renderedMC.GetIgnitionVersion()).To(o.Equal(defaultIgnitionVersion),
			"Rendered MC should use %s default ignition version", defaultIgnitionVersion)
		logger.Infof("OK!\n")

		exutil.By("Verify that all files were created")
		node := wMcp.GetNodesOrFail()[0]
		for _, version := range allVersions {
			vID := strings.ReplaceAll(version, ".", "-")
			filePath := "/etc/" + vID + ".test"

			logger.Infof("Checking file %s", filePath)
			rf := NewRemoteFile(node, filePath)
			o.Expect(rf.Fetch()).NotTo(o.HaveOccurred(),
				"Cannot get information about file %s in node %s", filePath, node.GetName())

			o.Expect(rf.GetTextContent()).To(o.Equal(version+" test file"),
				"File %s in node %s ha not the right content")

		}
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:64833][OTP] Do not make an 'orig' copy for config.json file [Serial]", func() {

		var (
			mMcp                = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
			wMcp                = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
			configJSONFile      = "/var/lib/kubelet/config.json"
			configJSONOringFile = "/etc/machine-config-daemon/orig/var/lib/kubelet/config.json.mcdorig"
		)

		for _, node := range append(wMcp.GetNodesOrFail(), mMcp.GetNodesOrFail()...) {
			exutil.By(fmt.Sprintf("Check that the /var/lib/kubelet/config.json is preset in node %s", node.GetName()))

			configJSONRemoteFile := NewRemoteFile(node, configJSONFile)
			configJSONOringRemoteFile := NewRemoteFile(node, configJSONOringFile)

			o.Eventually(configJSONRemoteFile.Exists, "20s", "2s").Should(o.BeTrue(),
				"The file %s does not exist in node %s", configJSONRemoteFile.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")

			exutil.By(fmt.Sprintf("Check that the /etc/machine-config-daemon/orig/var/lib/kubelet/config.json.mcdorig is NOT preset in node %s",
				node.GetName()))
			o.Eventually(configJSONOringRemoteFile.Exists, "20s", "2s").Should(o.BeFalse(),
				"The file %s exists in node %s, but it should NOT", configJSONOringRemoteFile.GetFullPath(), node.GetName())
			logger.Infof("OK!\n")
		}
	})

	g.It("[PolarionID:70090][OTP] apiserver-url.env file can be created on all cluster nodes [Serial]", func() {
		exutil.By("Check file apiserver-url.env on all linux nodes")
		apiserverURLEnvFile := "/etc/kubernetes/apiserver-url.env"
		allNodes, err := NewNodeList(oc.AsAdmin()).GetAllLinux()
		o.Expect(err).NotTo(o.HaveOccurred(), "Get all linux nodes failed")
		for _, node := range allNodes {
			if node.HasTaintEffectOrFail("NoExecute") {
				logger.Infof("Node %s is tainted with 'NoExecute'. Validation skipped.", node.GetName())
				continue
			}
			logger.Infof("Check apiserver-url.env file on node %s", node.GetName())
			rf := NewRemoteFile(node, apiserverURLEnvFile)
			o.Expect(rf.Exists()).Should(o.BeTrue(), "file %s not found on node %s", apiserverURLEnvFile, node.GetName())
			logger.Infof("OK\n")
		}
	})

	g.It("[PolarionID:72008][OTP] recreate currentconfig missing on the filesystem [Disruptive]", func() {
		var (
			mcp               = GetCompactCompatiblePool(oc.AsAdmin())
			mcName            = "mco-tc-72008"
			node              = mcp.GetSortedNodesOrFail()[0]
			currentConfigFile = "/etc/machine-config-daemon/currentconfig"
			filePath          = "/etc/mco-test-case-72008"
			fileMode          = "420"
			fileContent       = "test"
			fileConfig        = getURLEncodedFileConfig(filePath, fileContent, fileMode)
		)

		exutil.By("Remove the file /etc/machine-config-daemon/currentconfig") // remove the currentconfig file
		rmCurrentConfig := NewRemoteFile(node, currentConfigFile)
		o.Expect(rmCurrentConfig.Rm()).To(o.Succeed(), "Not able to remove %s", rmCurrentConfig.GetFullPath())
		o.Expect(rmCurrentConfig).NotTo(Exist(), "%s removed but still exist", rmCurrentConfig)
		logger.Infof("OK \n")

		exutil.By("Create new Machine config") // new Machineconfig file
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf("FILES=[%s]", fileConfig)}
		defer mc.DeleteWithWait() // clean
		mc.create()
		logger.Infof("OK \n")

		exutil.By("Check that the file /etc/machine-config-daemon/currentconfig is recreated") // After update currentconfig exist
		o.Expect(rmCurrentConfig).To(Exist(), "%s Not exist", rmCurrentConfig)
		o.Expect(rmCurrentConfig.Read()).NotTo(HaveContent(o.BeEmpty()), "%s should not be empty file", rmCurrentConfig)
		logger.Infof("OK \n")

		exutil.By("Check Machine-config is applied") // machine-config is applied
		newFile := NewRemoteFile(node, filePath)
		o.Expect(newFile.Read()).To(o.And(HaveContent(fileContent), HaveOctalPermissions("0644")), "%s Does not have expected content or permissions", newFile)
		logger.Infof("OK\n")
	})

	g.It("[PolarionID:74608][OTP] Env file /etc/kubernetes/node.env should not be overwritten after a node restart [Disruptive]", g.Label("Platform:aws"), func() {
		// /etc/kubernetes/node.env only exists in AWS
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform)

		var (
			node      = GetCompactCompatiblePool(oc.AsAdmin()).GetSortedNodesOrFail()[0]
			rEnvFile  = NewRemoteFile(node, "/etc/kubernetes/node.env")
			extraLine = "\nDUMMYKEY=DUMMYVAL"
		)
		exutil.By("Get current node.env content")
		o.Expect(rEnvFile.Fetch()).To(o.Succeed(),
			"Error getting information about %s", rEnvFile)
		initialContent := rEnvFile.GetTextContent()
		initialUser := rEnvFile.GetUIDName()
		initialGroup := rEnvFile.GetGIDName()
		initialPermissions := rEnvFile.GetOctalPermissions()
		logger.Infof("Initial content: %s", initialContent)
		logger.Infof("OK!\n")

		exutil.By("Modify the content of the node.env file")
		defer rEnvFile.PushNewTextContent(initialContent)
		newContent := initialContent + extraLine
		logger.Infof("New content: %s", newContent)
		o.Expect(rEnvFile.PushNewTextContent(newContent)).To(o.Succeed(),
			"Error writing new content in %s", rEnvFile)
		logger.Infof("OK!\n")

		exutil.By("Reboot node")
		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting %s", node)
		logger.Infof("OK!\n")

		exutil.By("Check that the content was not changed after the node reboot")
		o.Eventually(rEnvFile.Read, "15m", "20s").Should(o.And(
			HaveContent(newContent),
			HaveOwner(initialUser),
			HaveGroup(initialGroup),
			HaveOctalPermissions(initialPermissions)),
			"The information in %s is not the expected one", rEnvFile)
		logger.Infof("OK!\n")

		exutil.By("Restore initial content")
		o.Eventually(rEnvFile.PushNewTextContent, "5m", "20s").WithArguments(initialContent).Should(o.Succeed(),
			"Error writing the initial content in %s", rEnvFile)
		o.Expect(node.Reboot()).To(o.Succeed(),
			"Error rebooting %s", node)
		o.Eventually(rEnvFile.Read, "15m", "20s").Should(o.And(
			HaveContent(initialContent),
			HaveOwner(initialUser),
			HaveGroup(initialGroup),
			HaveOctalPermissions(initialPermissions)),
			"The inforamtion of %s is not the expected one after restoring the initial content", rEnvFile)
		logger.Infof("OK!\n")
	})

	g.It("[PolarionID:82976][OTP] MachineConfigOperator creates incorrect file contents if compression field is specified [Disruptive]", func() {
		var (
			mcContent1  = `test-content-1`
			mcContent2  = `test-content-2`
			mcName1     = fmt.Sprintf("test-gzip-test-%s-1", GetCurrentTestPolarionIDNumber())
			mcName2     = fmt.Sprintf("test-gzip-test-%s-2", GetCurrentTestPolarionIDNumber())
			path        = "/etc/test-file"
			fileConfig1 = getGzipFileJSONConfig(path, mcContent1)
			fileConfig2 = getBase64EncodedFileConfig(path, mcContent2, "")
			mcp         = GetCompactCompatiblePool(oc.AsAdmin())
			node        = mcp.GetNodesOrFail()[0]
			rf          = NewRemoteFile(node, path)
		)

		exutil.By("Create a first MachineConfig with gzip compression.")
		mc1 := NewMachineConfig(oc.AsAdmin(), mcName1, MachineConfigPoolWorker)
		defer mc1.DeleteWithWait()
		err := mc1.Create("-p", "NAME="+mcName1, "-p", "POOL="+mcp.name, "-p", fmt.Sprintf("FILES=[%s]", fileConfig1))
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("OK\n")

		exutil.By("Create a second MachineConfig with gzip compression.")
		mc2 := NewMachineConfig(oc.AsAdmin(), mcName2, MachineConfigPoolWorker)
		defer mc2.Delete()
		err = mc2.Create("-p", "NAME="+mcName2, "-p", "POOL="+mcp.name, "-p", fmt.Sprintf("FILES=[%s]", fileConfig2))
		o.Expect(err).NotTo(o.HaveOccurred())
		logger.Infof("OK\n")

		exutil.By("Check MachineConfigPool is updated")
		mcp.waitForComplete()
		logger.Infof("OK\n")

		exutil.By("Verfiy the second MachineConfiguration content is applied")
		o.Eventually(rf.Read, "2m", "30s").Should(HaveContent(mcContent2))
		logger.Infof("OK\n")
	})

	g.It("[PolarionID:85162][OTP] Ensure MCP is degraded when we apply the extra-disks config [Disruptive]", g.Label("Platform:aws", "Platform:gce"), func() {
		skipIfNoTechPreview(oc)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)
		var (
			mcp               = GetCompactCompatiblePool(oc.AsAdmin())
			expectedRDMessage = "Failed to render configuration for pool " + mcp.GetName() + ".*reconciliation failed.*ignition disks section contains changes"
			expectedRDReason  = ""
			platform          = exutil.CheckPlatform(oc)
			mcName            = "extra-disks-" + GetCurrentTestPolarionIDNumber()
			mc                = NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate("extra-disks.yaml")
		)

		exutil.By("Step 1: Apply extra-disks MachineConfig with platform-specific device parameters")
		device1, device2 := "/dev/sdb", "/dev/sdc"
		if platform == AWSPlatform {
			device1, device2 = "/dev/nvme1n1", "/dev/nvme2n1"
		}

		mc.SetParams("-p", "DEVICE1="+device1, "-p", "DEVICE2="+device2)
		mc.skipWaitForMcp = true

		exutil.By("Step 2: Validate that MCP becomes degraded with RenderDegraded condition")
		validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)
		logger.Infof("OK! MCP is degraded with RenderDegraded condition as expected\n")
	})

	g.It("[PolarionID:84219][OTP] Ensure new nodes adopt irreconcilable config while in old nodes surf [Disruptive]", g.Label("Platform:aws", "Platform:gce"), func() {
		skipIfNoTechPreview(oc)
		skipTestIfSupportedPlatformNotMatched(oc, AWSPlatform, GCPPlatform)
		var (
			mcp                  = GetCompactCompatiblePool(oc.AsAdmin())
			machineconfiguration = GetMachineConfiguration(oc.AsAdmin())
			mcName               = "extra-disks-" + GetCurrentTestPolarionIDNumber()
			mc                   = NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName()).SetMCOTemplate("extra-disks.yaml")
			nodes                = mcp.GetSortedNodesOrFail()
		)

		exutil.By("Save initial MachineConfiguration spec")
		initialMachineConfigSpec := machineconfiguration.GetSpecOrFail()
		logger.Infof("Initial MachineConfiguration spec: %s", initialMachineConfigSpec)

		defer func() {
			logger.Infof("Restore initial MachineConfiguration spec")
			err := machineconfiguration.SetSpec(initialMachineConfigSpec)
			o.Expect(err).NotTo(o.HaveOccurred())
		}()

		exutil.By("Step 1: Enable irreconcilableValidationOverrides")
		err := machineconfiguration.EnableIrreconcilableValidationOverrides()
		o.Expect(err).NotTo(o.HaveOccurred())
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Step 2: Apply extra-disks MachineConfig")
		platform := exutil.CheckPlatform(oc)
		device1, device2 := "/dev/sdb", "/dev/sdc"
		if platform == AWSPlatform {
			device1, device2 = "/dev/nvme1n1", "/dev/nvme2n1"
		}

		mc.SetParams("-p", "DEVICE1="+device1, "-p", "DEVICE2="+device2)
		defer mc.DeleteWithWait()
		mc.create()
		logger.Infof("OK!\n")

		exutil.By("Step 3: Check irreconcilable differences for existing worker nodes")
		for _, node := range nodes {
			irreconcilableChanges := OrFail[string](node.GetIrreconcilableChanges())
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.disks"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.raid"))
			o.Expect(irreconcilableChanges).To(o.ContainSubstring("spec.config.storage.filesystems"))
			logger.Infof("Node %s has irreconcilable changes as expected", node.GetName())
		}
		logger.Infof("All worker nodes have irreconcilable changes as expected!\n")

		exutil.By("Step 4: Create duplicate machineset with custom disks")
		machineset := OrFail[*MachineSet](GetScalableMachineSet(oc.AsAdmin()))
		newMSName := machineset.GetName() + "-custom-ms"
		newMS, err := machineset.Duplicate(newMSName)
		o.Expect(err).NotTo(o.HaveOccurred())

		defer func() {
			if newMS.Exists() {
				newMS.ScaleTo(0)
				newMS.WaitUntilReady("10m")
				newMS.Delete()
			}
		}()

		if platform == AWSPlatform {
			err = newMS.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/blockDevices/-","value":{"ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"},"deviceName":"/dev/sdb"}},{"op":"add","path":"/spec/template/spec/providerSpec/value/blockDevices/-","value":{"ebs":{"encrypted":false,"iops":0,"volumeSize":120,"volumeType":"gp3"},"deviceName":"/dev/sdc"}}]`)
			o.Expect(err).NotTo(o.HaveOccurred())
		} else {
			err = newMS.Patch("json", `[{"op":"add","path":"/spec/template/spec/providerSpec/value/disks/-","value":{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}},{"op":"add","path":"/spec/template/spec/providerSpec/value/disks/-","value":{"autoDelete":true,"boot":false,"sizeGb":16,"type":"pd-standard"}}]`)
			o.Expect(err).NotTo(o.HaveOccurred())
		}

		o.Expect(newMS.ScaleTo(1)).To(o.Succeed())
		o.Expect(newMS.WaitUntilReady("15m")).To(o.Succeed())

		exutil.By("Step 5: Verify new node has no irreconcilable differences")
		newNodes := newMS.GetNodesOrFail()
		o.Expect(newNodes).To(o.HaveLen(1))
		newNode := newNodes[0]
		logger.Infof("New node is: %s", newNode.GetName())

		newNodeMCN := NewMachineConfigNode(oc.AsAdmin(), newNode.GetName())
		o.Eventually(func() string {
			return newNodeMCN.GetOrFail(`{.status.irreconcilableChanges}`)
		}, "5m", "10s").Should(o.Or(o.BeEmpty(), o.Equal("[]")), "New node should have no irreconcilable changes")

		logger.Infof("OK!\n")

		exutil.By("Step 6: Verify storage configuration on new node")

		// Mount the RAID device
		_, err = newNode.DebugNodeWithChroot("mount", "/dev/md/data", "/var/lib/data")
		o.Expect(err).NotTo(o.HaveOccurred())

		// Check mdadm details
		stdout, err := newNode.DebugNodeWithChroot("mdadm", "--detail", "--scan")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(stdout).Should(o.ContainSubstring("/dev/md/data"))

		logger.Infof("OK!\n")
	})
})
