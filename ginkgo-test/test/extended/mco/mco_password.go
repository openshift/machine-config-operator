package mco

import (
	"fmt"
	"path/filepath"
	"regexp"

	expect "github.com/google/goexpect"
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

var _ = g.Describe("[sig-mco] MCO password", func() {
	defer g.GinkgoRecover()

	var (
		oc                = exutil.NewCLI("mco-password", exutil.KubeConfigPath())
		passwordHash      string
		updatedPasswdHash string
		user              string
		password          string
		updatedPassword   string
		wMcp              *MachineConfigPool
		mMcp              *MachineConfigPool
		// Compact compatible MCP. If the node is compact/SNO this variable will be the master pool, else it will be the worker pool
		mcp *MachineConfigPool
	)

	g.JustBeforeEach(func() {
		passwordHash = "$6$uim4LuKWqiko1l5K$QJUwg.4lAyU4egsM7FNaNlSbuI6JfQCRufb99QuF082BpbqFoHP3WsWdZ5jCypS0veXWN1HDqO.bxUpE9aWYI1"      // sha-512 "coretest"
		updatedPasswdHash = "$6$sGXk8kzDPwf165.v$9Oc0fXJpFmUy8cSZzzjrW7pDQwaYbPojAR7CHAKRl81KDYrk2RQrcFI9gLfhfrPMHI2WuX4Us6ZBkO1KfF48/." // sha-512 "coretest2"
		user = "core"
		password = "coretest"
		updatedPassword = "coretest2"
		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mcp = GetCompactCompatiblePool(oc.AsAdmin())

		preChecks(oc)
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-59417-MCD create/update password with MachineConfig in CoreOS nodes[Disruptive]", func() {
		var (
			mcName = "tc-59417-test-core-passwd"
		)

		allCoreos := mcp.GetCoreOsNodesOrFail()
		if len(allCoreos) == 0 {
			logger.Infof("No CoreOs nodes are configured in the pool %s. We use master pool for testing", mcp.GetName())
			mcp = mMcp
			allCoreos = mcp.GetCoreOsNodesOrFail()
		}

		node := allCoreos[0]
		startTime := node.GetDateOrFail()

		exutil.By("Configure a password for 'core' user")
		_, _ = node.GetDate() // for debugging purposes, it prints the node's current time in the logs
		o.Expect(node.IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
			"Error getting the latest event in node %s", node.GetName())

		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, user, passwordHash)}
		mc.skipWaitForMcp = true

		defer mc.delete()
		mc.create()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check MCD logs to make sure drain and reboot are skipped")
		podLogs, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, node.GetMachineConfigDaemon(), `"drain\|reboot"`)
		o.Expect(err).NotTo(o.HaveOccurred(), "Errot getting the drain and reboot logs: %s", err)
		logger.Infof("Pod logs to skip node drain and reboot:\n %v", podLogs)
		o.Expect(podLogs).Should(o.ContainSubstring("Changes do not require drain, skipping"))

		o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted, but it was rebooted. Uptime date happened after the start config time.", node.GetName())

		logger.Infof("OK!\n")

		exutil.By("Check events to make sure that drain and reboot events were not triggered")
		nodeEvents, eErr := node.GetEvents()
		o.Expect(eErr).ShouldNot(o.HaveOccurred(), "Error getting drain events for node %s", node.GetName())
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Drain"), "Error, a Drain event was triggered but it shouldn't")
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Reboot"), "Error, a Reboot event was triggered but it shouldn't")
		logger.Infof("OK!\n")

		exutil.By("Verify that user 'core' can login with the configured password")
		logger.Infof("verifying node %s", node.GetName())
		bresp, err := node.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdValidator(user, password))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error in the login process in node %s:\n %s", node.GetName(), bresp)
		logger.Infof("OK!\n")

		exutil.By("Update the password value")
		patchErr := mc.Patch("json",
			fmt.Sprintf(`[{ "op": "add", "path": "/spec/config/passwd/users/0/passwordHash", "value": "%s"}]`, updatedPasswdHash))

		o.Expect(patchErr).NotTo(o.HaveOccurred(),
			"Error patching mc %s to update the 'core' user password")

		mcp.waitForComplete()

		logger.Infof("OK!\n")

		exutil.By("Verify that user 'core' can login with the new password")
		logger.Infof("verifying node %s", node.GetName())
		bresp, err = node.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdValidator(user, updatedPassword))
		o.Expect(err).NotTo(o.HaveOccurred(), "Error in the login process in node %s:\n %s", node.GetName(), bresp)
		logger.Infof("OK!\n")

		exutil.By("Remove the password")
		mc.deleteNoWait()
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify that user 'core' can not login using a password anymore")
		logger.Infof("verifying node %s", node.GetName())
		bresp, err = node.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdValidator(user, updatedPassword))
		o.Expect(err).To(o.HaveOccurred(), "User 'core' was able to login using a password in node %s, but it should not be possible:\n %s", node.GetName(), bresp)
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-60129-MCD create/update password with MachineConfig in RHEL nodes[Disruptive]", func() {
		var (
			mcName = "tc-60129-test-core-passwd"
		)

		allRhelNodes := NewNodeList(oc).GetAllRhelWokerNodesOrFail()
		if len(allRhelNodes) == 0 {
			g.Skip("There are no rhel worker nodes in this cluster")
		}

		allWorkerNodes := NewNodeList(oc).GetAllLinuxWorkerNodesOrFail()

		exutil.By("Create the 'core' user in RHEL nodes")
		for _, rhelWorker := range allRhelNodes {
			// we need to do this to avoid the loop variable to override our value
			if !rhelWorker.UserExists(user) {
				worker := rhelWorker
				defer func() { worker.UserDel(user) }()

				o.Expect(worker.UserAdd(user)).NotTo(o.HaveOccurred(),
					"Error creating user in node %s", worker.GetName())
			} else {
				logger.Infof("User %s already exists in node %s. Skip creation.", user, rhelWorker.GetName())
			}
		}

		exutil.By("Configure a password for 'core' user")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, MachineConfigPoolWorker)
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, user, passwordHash)}
		mc.skipWaitForMcp = true

		defer mc.delete()
		mc.create()

		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify that user 'core' can login with the configured password")
		for _, workerNode := range allWorkerNodes {
			logger.Infof("Verifying node %s", workerNode.GetName())
			bresp, err := workerNode.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdValidator(user, password))
			o.Expect(err).NotTo(o.HaveOccurred(), "Error in the login process in node %s:\n %s", workerNode.GetName(), bresp)
		}
		logger.Infof("OK!\n")

		exutil.By("Update the password value")
		patchErr := mc.Patch("json",
			fmt.Sprintf(`[{ "op": "add", "path": "/spec/config/passwd/users/0/passwordHash", "value": "%s"}]`, updatedPasswdHash))

		o.Expect(patchErr).NotTo(o.HaveOccurred(),
			"Error patching mc %s to update the 'core' user password")

		wMcp.waitForComplete()

		logger.Infof("OK!\n")

		exutil.By("Verify that user 'core' can login with the new password")
		for _, workerNode := range allWorkerNodes {
			logger.Infof("Verifying node %s", workerNode.GetName())
			bresp, err := workerNode.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdValidator(user, updatedPassword))
			o.Expect(err).NotTo(o.HaveOccurred(), "Error in the login process in node %s:\n %s", workerNode.GetName(), bresp)
		}
		logger.Infof("OK!\n")

		exutil.By("Remove the password")
		mc.deleteNoWait()
		wMcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify that user 'core' can not login using a password anymore")
		for _, workerNode := range allWorkerNodes {
			logger.Infof("Verifying node %s", workerNode.GetName())
			bresp, err := workerNode.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdValidator(user, updatedPassword))
			o.Expect(err).To(o.HaveOccurred(), "User 'core' was able to login using a password in node %s, but it should not be possible:\n %s", workerNode.GetName(), bresp)
		}
		logger.Infof("OK!\n")

	})

	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-72137-Create a password for a user different from 'core' user[Disruptive]", func() {
		var (
			mcName       = "mco-tc-59900-wrong-user-password"
			wrongUser    = "root"
			passwordHash = "fake-hash"

			expectedRDReason  = ""
			expectedRDMessage = regexp.QuoteMeta(`ignition passwd user section contains unsupported changes: non-core user`)
		)

		exutil.By("Create a password for a non-core user using a MC")

		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, wrongUser, passwordHash)}
		mc.skipWaitForMcp = true

		validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)
	})

	g.It("Author:sregidor-NonPreRelease-Longduration-High-59424-ssh keys can be found in new dir on RHCOS9 node [Disruptive]", func() {
		var (
			allCoreOsNodes = wMcp.GetCoreOsNodesOrFail()
			allMasters     = mMcp.GetNodesOrFail()
		)

		skipTestIfRHELVersion(allCoreOsNodes[0], "<", "9.0")

		exutil.By("Get currently configured authorizedkeys in the cluster")
		wMc, err := wMcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current configuration for worker pool")

		mMc, err := mMcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current configuration for master pool")

		workerKeys, err := wMc.GetAuthorizedKeysByUserAsList("core")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current authorizedkeys for user 'core' in worker pool")

		masterKeys, err := mMc.GetAuthorizedKeysByUserAsList("core")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current authorizedkeys for user 'core' in master pool")

		logger.Infof("Number of AuthorizedKeys configured for worker nodes: %d", len(workerKeys))
		logger.Infof("Number of AuthorizedKeys configured for master nodes: %d", len(masterKeys))
		logger.Infof("Ok!\n")

		exutil.By("Check the authorized key files in the nodes")

		logger.Infof("CHECKING AUTHORIZED KEYS FILE IN COREOS WORKER POOL")
		for _, worker := range allCoreOsNodes {
			logger.Infof("Checking authorized keys file in node:%s", worker.GetName())
			checkAuthorizedKeyInNode(worker, workerKeys)
			logger.Infof("Ok!\n")
		}

		logger.Infof("CHECKING AUTHORIZED KEYS FILE IN MASTER POOL")
		for _, master := range allMasters {
			logger.Infof("Checking authorized keys file in node:%s", master.GetName())
			checkAuthorizedKeyInNode(master, masterKeys)
			logger.Infof("Ok!\n")
		}
	})

	g.It("Author:sregidor-LEVEL0-WRS-NonPreRelease-Longduration-Critical-59426-V-BR.26-ssh keys can be updated in new dir on RHCOS9 node[Disruptive]", func() {

		var (
			mcName = "tc-59426-add-ssh-key"
			key1   = `ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDPmGf/sfIYog1KaHj50H0vaDRITn4Wa8RN9bgc2jj6SejvxhAWZVc4BrRst6BdhGr34IowkZmz76ba9jfa4nGm2HNd+CGqf6KmUhwPjF9oJNjy3z5zT2i903OZii35MUnJl056YXgKYpN96WAD5LVOKop/+7Soxq4PW8TtVZeSpHiPNI28XiIdyqGLzJerhlgPLZBsNO0JcVH1DYLd/c4fh5GDLutszZH/dzAX5RmvN1P/cHie+BnkbgNx91NbrOLTrV5m3nY2End5uGDl8zhaGQ2BX2TmnMqWyxYkYuzNmQFprHMNCCpqLshFGRvCFZGpc6L/72mlpcJubzBF0t5Z mco_test@redhat.com`
			key2   = `ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDf7nk9SKloQktDuu0DFDrWv8zRROnxKT04DQdz0RRWXwKyQWFbXi2t7MPkYHb+H7BfuCF8gd3BsfZbGenmRpHrm99bjbZWV6tyyyOWac88RGDXwTeSdcdgZoVDIQfW0S4/y7DP6uo6QGyZEh+s+VTGg8gcqm9L2GkjlA943UWUTyRIVQdex8qbtKdAI0NqYtAzuf1zYDGBob5/BdjT856dF7dDCJG36+d++VRXcyhE+SYxGdEC+OgYwRXjz3+J7XixvTAeY4DdGQOeppjOC/E+0TXh5T0m/+LfCJQCClSYvuxIKPkiMvmNHY4q4lOZUL1/FKIS2pn0P6KsqJ98JvqV mco_test2@redhat.com`

			user = ign32PaswdUser{Name: "core", SSHAuthorizedKeys: []string{key1, key2}}
		)

		node := mcp.GetCoreOsNodesOrFail()[0]
		skipTestIfRHELVersion(node, "<", "9.0")

		exutil.By("Get currently configured authorizedkeys in the cluster")
		currentMc, err := mcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current configuration for %s pool", mcp.GetName())

		initialKeys, err := currentMc.GetAuthorizedKeysByUserAsList("core")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current authorizedkeys for user 'core' in %s pool", mcp.GetName())

		logger.Infof("Number of initially configured AuthorizedKeys: %d", len(initialKeys))
		logger.Infof("OK!\n")

		exutil.By("Get start time and start collecting events.")

		startTime, dErr := node.GetDate()

		o.Expect(dErr).ShouldNot(o.HaveOccurred(), "Error getting date in node %s", node.GetName())
		o.Expect(node.IgnoreEventsBeforeNow()).NotTo(o.HaveOccurred(),
			"Error getting the latest event in node %s", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Create a new MC to deploy new authorized keys")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[%s]`, MarshalOrFail(user))}
		mc.skipWaitForMcp = true

		defer mc.delete()
		mc.create()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that nodes are not drained nor rebooted")
		log, err := exutil.GetSpecificPodLogs(oc, MachineConfigNamespace, MachineConfigDaemon, node.GetMachineConfigDaemon(), "")
		o.Expect(err).NotTo(o.HaveOccurred())
		o.Expect(log).Should(o.ContainSubstring("Changes do not require drain, skipping"))
		logger.Infof("OK!\n")

		exutil.By("Verify that the node was NOT rebooted")
		o.Expect(node.GetUptime()).Should(o.BeTemporally("<", startTime),
			"The node %s must NOT be rebooted after applying the configuration, but it was rebooted. Uptime date happened after the start config time.", node.GetName())
		logger.Infof("OK!\n")

		exutil.By("Check events to make sure that drain and reboot events were not triggered")
		nodeEvents, eErr := node.GetEvents()
		o.Expect(eErr).ShouldNot(o.HaveOccurred(), "Error getting drain events for node %s", node.GetName())
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Drain"), "Error, a Drain event was triggered but it shouldn't")
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Reboot"), "Error, a Reboot event was triggered but it shouldn't")
		logger.Infof("OK!\n")

		exutil.By("Check that all expected keys are present")
		checkAuthorizedKeyInNode(mcp.GetCoreOsNodesOrFail()[0], append(initialKeys, key1, key2))
		logger.Infof("OK!\n")

		exutil.By("Delete the MC with the new authorized keys")
		mc.delete()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the new authorized keys are removed but the original keys are still present")
		checkAuthorizedKeyInNode(node, initialKeys)
		logger.Infof("OK!\n")

	})
	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-62533-Passwd login must not work with ssh[Disruptive]", func() {
		var (
			mcName = "tc-62533-test-passwd-ssh-login"
		)

		allCoreos := mcp.GetCoreOsNodesOrFail()
		if len(allCoreos) == 0 {
			logger.Infof("No CoreOs nodes are configured in the %s pool. We use master pool for testing", mcp.GetName())
			mcp = mMcp
			allCoreos = mcp.GetCoreOsNodesOrFail()
		}

		node := allCoreos[0]

		exutil.By("Configure a password for 'core' user")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, user, passwordHash)}
		mc.skipWaitForMcp = true

		defer mc.delete()
		mc.create()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the password cannot be used to login to the cluster via ssh")
		logger.Infof("verifying node %s", node.GetName())
		bresp, err := node.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdSSHValidator(user))
		o.Expect(err).NotTo(o.HaveOccurred(), "Ssh login should not be allowed in node %s and should report a 'permission denied' error:\n %s", node.GetName(), bresp)
		logger.Infof("OK!\n")
	})
	g.It("Author:sregidor-NonPreRelease-Longduration-Medium-64986-Remove all ssh keys [Disruptive]", func() {
		var (
			sshMCName    = "99-" + mcp.GetName() + "-ssh"
			backupMCFile = filepath.Join(e2e.TestContext.OutputDir, "tc-64986-"+sshMCName+".backup.json")
		)

		exutil.By("Get currently configured authorizedkeys in the cluster")
		currentMc, err := mcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current configuration for %s pool", mcp.GetName())

		initialKeys, err := currentMc.GetAuthorizedKeysByUserAsList("core")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current authorizedkeys for user 'core' in %s pool", mcp.GetName())

		logger.Infof("Number of initially configured AuthorizedKeys: %d", len(initialKeys))

		if len(initialKeys) > 1 {
			logger.Infof("There is more than 1 ssh key configred in this cluster. Probably they have been added manually.")
			g.Skip("There are more than 1 ssh key configured. The cluster has been probably manually modified. Check the configured ssh keys before running this test.")
		}

		logger.Infof("OK!\n")

		exutil.By("Remove the ssh key MachineConfig")
		sshMC := NewMachineConfig(oc.AsAdmin(), sshMCName, mcp.GetName())

		// If the cluster was created with a ssh key, we remove it and force no sshkey in the cluster
		if sshMC.Exists() {
			logger.Infof("Save MC information in file: %s", backupMCFile)
			o.Expect(sshMC.ExportToFile(backupMCFile)).To(o.Succeed(),
				"It was not possible to save MC %s in file %s", sshMC.GetName(), backupMCFile)

			defer func() {
				logger.Infof("Restore the removed MC")
				if !sshMC.Exists() {
					OCCreate(oc.AsAdmin(), backupMCFile)
					logger.Infof("Wait for MCP to be updated")
					mcp.waitForComplete()
				}
			}()

			sshMC.delete()
			logger.Infof("OK!\n")

			exutil.By("Check that the nodes have the correct configuration for ssh keys. No key configured.")
			checkAuthorizedKeyInNode(mcp.GetCoreOsNodesOrFail()[0], []string{})
			logger.Infof("OK!\n")

			exutil.By("Restore the deleted MC")
			o.Expect(OCCreate(oc.AsAdmin(), backupMCFile)).To(o.Succeed(),
				"The deleted MC could not be restored")
			mcp.waitForComplete()
			logger.Infof("OK!\n")
		} else {
			logger.Infof("MachineConfig %s does not exist. No need to remove it", sshMC.GetName())
		}

		exutil.By("Check that the nodes have the correct configuration for ssh keys. Original keys.")
		checkAuthorizedKeyInNode(mcp.GetCoreOsNodesOrFail()[0], initialKeys)
		logger.Infof("OK!\n")
	})

	g.It("Author:sregidor-NonPreRelease-High-75552-apply ssh keys when root owns .ssh [Disruptive]", func() {
		var (
			node         = mcp.GetSortedNodesOrFail()[0]
			authKeysdDir = NewRemoteFile(node, "/home/core/.ssh/authorized_keys.d")
			sshDir       = NewRemoteFile(node, "/home/core/.ssh")
			mcName       = "tc-75552-ssh"
			key          = `ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDf7nk9SKloQktDuu0DFDrWv8zRROnxKT04DQdz0RRWXwKyQWFbXi2t7MPkYHb+H7BfuCF8gd3BsfZbGenmRpHrm99bjbZWV6tyyyOWac88RGDXwTeSdcdgZoVDIQfW0S4/y7DP6uo6QGyZEh+s+VTGg8gcqm9L2GkjlA943UWUTyRIVQdex8qbtKdAI0NqYtAzuf1zYDGBob5/BdjT856dF7dDCJG36+d++VRXcyhE+SYxGdEC+OgYwRXjz3+J7XixvTAeY4DdGQOeppjOC/E+0TXh5T0m/+LfCJQCClSYvuxIKPkiMvmNHY4q4lOZUL1/FKIS2pn0P6KsqJ98JvqV mco_test2@redhat.com`

			user = ign32PaswdUser{Name: "core", SSHAuthorizedKeys: []string{key}}
		)

		exutil.By("Get currently configured authorizedkeys in the cluster")
		currentMc, err := mcp.GetConfiguredMachineConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current configuration for %s pool", mcp.GetName())

		initialKeys, err := currentMc.GetAuthorizedKeysByUserAsList("core")
		o.Expect(err).NotTo(o.HaveOccurred(), "Error getting the current authorizedkeys for user 'core' in %s pool", mcp.GetName())

		logger.Infof("Number of initially configured AuthorizedKeys: %d", len(initialKeys))
		logger.Infof("OK!\n")

		exutil.By("Remove the authorized keys file from the node")
		o.Expect(authKeysdDir.Rm("-rf")).To(o.Succeed(),
			"Error removing %s", authKeysdDir)
		logger.Infof("OK!\n")

		exutil.By("Set root as the owner of the .ssh directory")
		o.Expect(sshDir.PushNewOwner("root:root")).To(o.Succeed(),
			"Error setting root owner in  %s", sshDir)
		logger.Infof("OK!\n")

		// For debugging purpose
		s, _ := node.DebugNodeWithChroot("ls", "-larth", "/home/core/.ssh")
		logger.Infof("list /home/core/.ssh: \n %s", s)

		exutil.By("Create a new MC to deploy new authorized keys")
		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[%s]`, MarshalOrFail(user))}
		mc.skipWaitForMcp = true

		defer mc.delete()
		mc.create()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that all expected keys are present and with the right permissions and owners")
		// This function checks the owners and the permissions in the .ssh and authorized_keys.d directories
		checkAuthorizedKeyInNode(mcp.GetCoreOsNodesOrFail()[0], append(initialKeys, key))
		logger.Infof("OK!\n")
	})
})

// getPasswdValidator returns the commands that need to be executed in an interactive expect shell to validate that a user can login
func getPasswdValidator(user, passwd string) []expect.Batcher {

	return []expect.Batcher{
		&expect.BExpT{R: "#", T: 120}, // wait for prompt. We wait 120 seconds here, because the debug pod can take some time to be run
		// in the rest of the commands we use the default timeout
		&expect.BSnd{S: "chroot /host\n"}, // execute the chroot command
		// &expect.BExp{R: "#"},               // wait for prompt
		&expect.BExp{R: ".*"}, // wait for any prompt or no prompt (sometimes it does not return a prompt)
		&expect.BSnd{S: fmt.Sprintf(`su %s -c "su %s -c 'echo OK'"`, user, user) + "\n"}, // run an echo command forcing the user authentication
		&expect.BExp{R: "[pP]assword:"},              // wait for password question
		&expect.BSnd{S: fmt.Sprintf("%s\n", passwd)}, // write the password
		&expect.BExp{R: `OK`},                        // wait for succeess message
	}
}

// getPasswdSSHValidator returns the commands that need to be executed in an interactive expect shell to validate that a user can NOT login via ssh
func getPasswdSSHValidator(user string) []expect.Batcher {

	return []expect.Batcher{
		&expect.BExpT{R: "#", T: 120}, // wait for prompt. We wait 120 seconds here, because the debug pod can take some time to be run
		&expect.BSnd{S: fmt.Sprintf("chroot /host ssh -o StrictHostKeyChecking=no %s@127.0.01\n", user)}, // run a ssh login command
		&expect.BExp{R: `Permission denied`}, // wait for the login to be rejected because of permission denied
	}
}

func checkAuthorizedKeyInNode(node Node, keys []string) {
	logger.Infof("Checking old file /home/core/.ssh/authorized_keys")
	rOldAuthorizedFile := NewRemoteFile(node, "/home/core/.ssh/authorized_keys")
	o.Expect(rOldAuthorizedFile.Fetch()).ShouldNot(o.Succeed(),
		"Old format authorized keys /home/core/.ssh/authorized_keys should not exist in node %s", node.GetName())

	// If no key exists and .ssh directory does not exist either, then we have nothing to validate
	if len(keys) == 0 {
		logger.Infof("No authorized key is configured for node %s. Checking .ssh directory.", node.GetName())
		rSSHDir := NewRemoteFile(node, "/home/core/.ssh")
		if rSSHDir.Fetch() != nil {
			logger.Infof("No authorized key is configured and /home/core/.ssh directory does not exist in node %s", node.GetName())
			return
		}
	}

	logger.Infof("Checking /home/core/.ssh")
	rSSHDir := NewRemoteFile(node, "/home/core/.ssh")
	o.Expect(rSSHDir.Fetch()).To(o.Succeed(), "/home/core/.ssh cannot be found in node %s", node.GetName())

	o.Expect(rSSHDir.GetUIDName()).To(o.Equal("core"), "The user owner of /home/core/.ssh should be 'core' user in node %s", node.GetName())
	o.Expect(rSSHDir.GetGIDName()).To(o.Equal("core"), "The group owner of /home/core/.ssh should be 'core' group in node %s", node.GetName())
	o.Expect(rSSHDir.GetNpermissions()).To(o.Equal("0700"), "Wrong permissions in /home/core/.ssh file in node %s", node.GetName())

	logger.Infof("Checking /home/core/.ssh/authorized_keys.d")
	rAuthKeysDir := NewRemoteFile(node, "/home/core/.ssh/authorized_keys.d")
	o.Expect(rAuthKeysDir.Fetch()).To(o.Succeed(), "/home/core/.ssh/authorized_keys.d cannot be found in node %s", node.GetName())

	o.Expect(rAuthKeysDir.GetUIDName()).To(o.Equal("core"), "The user owner of /home/core/.ssh/authorized_keys.d should be 'core' user in node %s", node.GetName())
	o.Expect(rAuthKeysDir.GetGIDName()).To(o.Equal("core"), "The group owner of /home/core/.ssh/authorized_keys.d should be 'core' group in node %s", node.GetName())
	o.Expect(rAuthKeysDir.GetNpermissions()).To(o.Equal("0700"), "Wrong permissions in /home/core/.ssh/authorized_keys.d directory in node %s", node.GetName())

	logger.Infof("Checking /home/core/.ssh/authorized_keys.d/ignition")
	rIgnition := NewRemoteFile(node, "/home/core/.ssh/authorized_keys.d/ignition")
	o.Expect(rIgnition.Fetch()).To(o.Succeed(), "/home/core/.ssh/authorized_keys.d/ignition cannot be found in node %s", node.GetName())

	o.Expect(rIgnition.GetUIDName()).To(o.Equal("core"), "The user owner of /home/core/.ssh/authorized_keys.d/ignition should be 'core' user in node %s", node.GetName())
	o.Expect(rIgnition.GetGIDName()).To(o.Equal("core"), "The group owner of /home/core/.ssh/authorized_keys.d/ignition should be 'core' group in node %s", node.GetName())
	o.Expect(rIgnition.GetNpermissions()).To(o.Equal("0600"), "Wrong permissions in /home/core/.ssh/authorized_keys.d/ignition file in node %s", node.GetName())

	if len(keys) > 0 {
		for _, key := range keys {
			o.Expect(rIgnition.GetTextContent()).To(o.ContainSubstring(key),
				"A expected key does not exist. Wrong content in /home/core/.ssh/authorized_keys.d/ignition file in node %s", node.GetName())
		}
	} else {
		o.Expect(rIgnition.GetTextContent()).To(o.BeEmpty(),
			"File should be empty, but it is not. Wrong content in /home/core/.ssh/authorized_keys.d/ignition file in node %s", node.GetName())
	}
}
