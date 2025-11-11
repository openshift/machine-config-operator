package extended

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	expect "github.com/google/goexpect"
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"golang.org/x/crypto/ssh"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

var _ = g.Describe("[sig-mco][Suite:openshift/machine-config-operator/disruptive][Serial][Disruptive] MCO password", func() {
	defer g.GinkgoRecover()

	var (
		oc   = exutil.NewCLI("mco-password", exutil.KubeConfigPath())
		user string
		wMcp *MachineConfigPool
		mMcp *MachineConfigPool
		// Compact compatible MCP. If the node is compact/SNO this variable will be the master pool, else it will be the worker pool
		mcp *MachineConfigPool
	)

	g.JustBeforeEach(func() {
		user = "core"
		wMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolWorker)
		mMcp = NewMachineConfigPool(oc.AsAdmin(), MachineConfigPoolMaster)
		mcp = GetCompactCompatiblePool(oc.AsAdmin())

		PreChecks(oc)
	})

	g.It("PolarionID:59417-MCD create/update password with MachineConfig in CoreOS nodes", func() {
		var (
			mcName   = "tc-59417-test-core-passwd"
			mcc      = NewController(oc.AsAdmin()).IgnoreLogsBeforeNowOrFail()
			silentOC = oc.AsAdmin()

			password          = exutil.GetRandomString()
			passwordHash      = OrFail[string](getHashPasswd(password))
			updatedPassword   = password + "-2"
			updatedPasswdHash = OrFail[string](getHashPasswd(updatedPassword))

			expectedReboot = false
		)
		silentOC.NotShowInfo()

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

		mc := NewMachineConfig(silentOC, mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, user, passwordHash)}
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		mc.create()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check MCD logs to make sure drain and reboot are skipped")
		checkDrainAction(expectedReboot, node, mcc)
		checkRebootAction(expectedReboot, node, startTime)
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
		mc.Delete()
		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Verify that user 'core' can not login using a password anymore")
		logger.Infof("verifying node %s", node.GetName())
		bresp, err = node.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdValidator(user, updatedPassword))
		o.Expect(err).To(o.HaveOccurred(), "User 'core' was able to login using a password in node %s, but it should not be possible:\n %s", node.GetName(), bresp)
		logger.Infof("OK!\n")

	})

	g.It("PolarionID:72137-Create a password for a user different from 'core' user", func() {
		var (
			mcName       = "mco-tc-59900-wrong-user-password"
			wrongUser    = "root"
			passwordHash = fmt.Sprintf("fake-hash-%s", exutil.GetRandomString())

			expectedRDReason  = ""
			expectedRDMessage = regexp.QuoteMeta(`ignition passwd user section contains unsupported changes: non-core user`)
		)

		exutil.By("Create a password for a non-core user using a MC")

		mc := NewMachineConfig(oc.AsAdmin(), mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, wrongUser, passwordHash)}
		mc.skipWaitForMcp = true

		validateMcpRenderDegraded(mc, mcp, expectedRDMessage, expectedRDReason)
	})

	// https://issues.redhat.com/browse/MCO-1696
	g.It("PolarionID:59424-ssh keys can be found in new dir on RHCOS9 node", func() {
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

	g.It("PolarionID:59426-ssh keys can be updated in new dir on RHCOS9 node", func() {

		var (
			mcName            = "tc-59426-add-ssh-key"
			privKey1, pubKey1 = GenerateSSHKeyPairOrFail()
			privKey2, pubKey2 = GenerateSSHKeyPairOrFail()

			user = ign32PaswdUser{Name: "core", SSHAuthorizedKeys: []string{pubKey1, pubKey2}}

			node = mcp.GetCoreOsNodesOrFail()[0]
			mcc  = NewController(oc.AsAdmin()).IgnoreLogsBeforeNowOrFail()

			expectedReboot = false
		)

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

		defer mc.DeleteWithWait()
		mc.create()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that nodes are not drained nor rebooted")
		checkDrainAction(expectedReboot, node, mcc)
		logger.Infof("OK!\n")

		exutil.By("Verify that the node was NOT rebooted")
		checkRebootAction(expectedReboot, node, startTime)
		logger.Infof("OK!\n")

		exutil.By("Check events to make sure that drain and reboot events were not triggered")
		nodeEvents, eErr := node.GetEvents()
		o.Expect(eErr).ShouldNot(o.HaveOccurred(), "Error getting drain events for node %s", node.GetName())
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Drain"), "Error, a Drain event was triggered but it shouldn't")
		o.Expect(nodeEvents).NotTo(HaveEventsSequence("Reboot"), "Error, a Reboot event was triggered but it shouldn't")
		logger.Infof("OK!\n")

		exutil.By("Check that all expected keys are present")
		checkAuthorizedKeyInNode(mcp.GetCoreOsNodesOrFail()[0], append(initialKeys, pubKey1, pubKey2))
		logger.Infof("OK!\n")

		exutil.By("Check that we can really login to the nodes")
		logger.Infof("Checking first ssh key")
		checkSSHAccessInNode(node, "core", privKey1)
		logger.Infof("Checking second ssh key")
		checkSSHAccessInNode(node, "core", privKey2)
		logger.Infof("OK!\n")

		exutil.By("Delete the MC with the new authorized keys")
		mc.DeleteWithWait()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the new authorized keys are removed but the original keys are still present")
		checkAuthorizedKeyInNode(node, initialKeys)
		logger.Infof("OK!\n")

	})

	g.It("PolarionID:62533-Passwd login must not work with ssh", func() {
		var (
			mcName       = "tc-62533-test-passwd-ssh-login"
			password     = exutil.GetRandomString()
			passwordHash = OrFail[string](getHashPasswd(password))
			silentOC     = oc.AsAdmin()
		)
		silentOC.NotShowInfo()

		allCoreos := mcp.GetCoreOsNodesOrFail()
		if len(allCoreos) == 0 {
			logger.Infof("No CoreOs nodes are configured in the %s pool. We use master pool for testing", mcp.GetName())
			mcp = mMcp
			allCoreos = mcp.GetCoreOsNodesOrFail()
		}

		node := allCoreos[0]

		exutil.By("Configure a password for 'core' user")
		mc := NewMachineConfig(silentOC, mcName, mcp.GetName())
		mc.parameters = []string{fmt.Sprintf(`PWDUSERS=[{"name":"%s", "passwordHash": "%s" }]`, user, passwordHash)}
		mc.skipWaitForMcp = true

		defer mc.DeleteWithWait()
		mc.create()

		mcp.waitForComplete()
		logger.Infof("OK!\n")

		exutil.By("Check that the password cannot be used to login to the cluster via ssh")
		logger.Infof("verifying node %s", node.GetName())
		bresp, err := node.ExecuteDebugExpectBatch(DefaultExpectTimeout, getPasswdSSHValidator(user))
		o.Expect(err).NotTo(o.HaveOccurred(), "Ssh login should not be allowed in node %s and should report a 'permission denied' error:\n %s", node.GetName(), bresp)
		logger.Infof("OK!\n")
	})

	g.It("PolarionID:64986-Remove all ssh keys", func() {
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

			sshMC.DeleteWithWait()
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

	g.It("PolarionID:75552-apply ssh keys when root owns .ssh", func() {
		var (
			node         = mcp.GetSortedNodesOrFail()[0]
			authKeysdDir = NewRemoteFile(node, "/home/core/.ssh/authorized_keys.d")
			sshDir       = NewRemoteFile(node, "/home/core/.ssh")
			mcName       = "tc-75552-ssh"

			_, key = GenerateSSHKeyPairOrFail()
			user   = ign32PaswdUser{Name: "core", SSHAuthorizedKeys: []string{key}}
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

		defer mc.DeleteWithWait()
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
		&expect.BSnd{S: fmt.Sprintf("chroot /host ssh -o StrictHostKeyChecking=no %s@127.0.0.1\n", user)}, // run a ssh login command
		&expect.BExp{R: `Permission denied`}, // wait for the login to be rejected because of permission denied
	}
}

// getSSHKeyValidator returns the commands that need to be executed in an interactive expect shell to validate that a user can login using a private key
func getSSHKeyValidator(user, privKeyPath string) []expect.Batcher {

	return []expect.Batcher{
		&expect.BExpT{R: "#", T: 120}, // wait for prompt. We wait 120 seconds here, because the debug pod can take some time to be run
		&expect.BSnd{S: fmt.Sprintf("chroot /host ssh -i %s -o StrictHostKeyChecking=no %s@127.0.0.1\n echo 'OK'", privKeyPath, user)}, // run a ssh login command
		&expect.BExp{R: `OK`}, // wait for succeess message

	}
}

// GenerateSSHKeyPair returns private and public ssh keys
func GenerateSSHKeyPair() (privateKey, publicKey string, err error) {
	logger.Infof("Generating SSH keys")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}

	privatePEM := pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		},
	)

	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	publicSSH := ssh.MarshalAuthorizedKey(pub)

	return string(privatePEM), string(publicSSH), nil
}

// GenerateSSHKeyPairOrFail retunrs private and public ssh keys and fails the test if there is an error
func GenerateSSHKeyPairOrFail() (privateKey, publicKey string) {
	privKey, pubKey, err := GenerateSSHKeyPair()
	o.Expect(err).NotTo(o.HaveOccurred(), "Error generating ssh keys")

	return privKey, pubKey
}

func checkSSHAccessInNode(node Node, user, privKey string) {
	tmpPrivKeyPath := "/tmp/tmp-" + exutil.GetRandomString()
	remotePrivSSH := NewRemoteFile(node, tmpPrivKeyPath)
	defer func() {
		o.Expect(remotePrivSSH.Rm()).To(o.Succeed(),
			"Error removing remote temporary key")
	}()

	o.Expect(remotePrivSSH.Create([]byte(privKey), 0o600)).To(o.Succeed(),
		"Error creating the remote temporary file with the private key")

	bresp, err := node.ExecuteDebugExpectBatch(DefaultExpectTimeout, getSSHKeyValidator(user, tmpPrivKeyPath))
	o.Expect(err).NotTo(o.HaveOccurred(), "Error in the login process using ssh key in node %s:\n %s", node.GetName(), bresp)
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

// getHashPasswd returns the 512 hashed password
func getHashPasswd(password string) (string, error) {
	cmd := exec.Command("openssl", "passwd", "-6", password)

	out, err := cmd.Output()
	if err != nil {
		logger.Errorf("Cannot generate the password")
		return "", err
	}

	hash := strings.TrimSpace(string(out))

	return hash, err
}
