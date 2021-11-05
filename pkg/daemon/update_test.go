package daemon

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/vincent-petithory/dataurl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestTruncate(t *testing.T) {
	assert.Equal(t, truncate("", 10), "")
	assert.Equal(t, truncate("", 1), "")
	assert.Equal(t, truncate("a", 1), "a")
	assert.Equal(t, truncate("abcde", 1), "a [4 more chars]")
	assert.Equal(t, truncate("abcde", 4), "abcd [1 more chars]")
	assert.Equal(t, truncate("abcde", 7), "abcde")
	assert.Equal(t, truncate("abcde", 5), "abcde")
}

func TestRunCmdSync(t *testing.T) {
	err := runCmdSync("echo", "hello", "world")
	assert.Nil(t, err)

	err = runCmdSync("false", "something")
	assert.NotNil(t, err)
}

// TestUpdateOS verifies the return errors from attempting to update the OS follow expectations
func TestUpdateOS(t *testing.T) {
	// expectedError is the error we will use when expecting an error to return
	expectedError := fmt.Errorf("broken")

	// testClient is the NodeUpdaterClient mock instance that will front
	// calls to update the host.
	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
	}

	// Create a Daemon instance with mocked clients
	d := Daemon{
		mock:              true,
		name:              "nodeName",
		os:                OperatingSystem{},
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}

	// Set up machineconfigs to pass to updateOS.
	mcfg := &mcfgv1.MachineConfig{}
	// differentMcfg has a different OSImageURL so it will force Daemon.UpdateOS
	// to trigger an update of the operatingsystem (as fronted by our testClient)
	differentMcfg := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "somethingDifferent",
		},
	}

	// This should be a no-op
	if err := d.updateOS(mcfg, ""); err != nil {
		t.Errorf("Expected no error. Got %s.", err)
	}
	// Second call should return an error
	if err := d.updateOS(differentMcfg, ""); err == expectedError {
		t.Error("Expected an error. Got none.")
	}
}

// TestReconcilable attempts to verify the conditions in which configs would and would not be
// reconcilable. Welcome to the longest unittest you've ever read.
func TestReconcilable(t *testing.T) {
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	// oldConfig is the current config of the fake system
	oldConfig := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg := ctrlcommon.NewIgnConfig()

	// Set improper version
	newIgnCfg.Ignition.Version = "4.0.0"

	// newConfig is the config that is being requested to apply to the system
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	// Verify Ignition version mismatch react as expected
	_, isReconcilable := reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Ignition", isReconcilable)
	//reset to proper Ignition version
	newIgnCfg.Ignition.Version = ign3types.MaxVersion.String()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Ignition", isReconcilable)

	// Verify Disk changes react as expected
	oldIgnCfg.Storage.Disks = []ign3types.Disk{
		ign3types.Disk{
			Device: "/one",
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Disk", isReconcilable)

	// Match storage disks
	newIgnCfg.Storage.Disks = oldIgnCfg.Storage.Disks
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Disk", isReconcilable)

	// Verify Filesystems changes react as expected
	oldIgnCfg.Storage.Filesystems = []ign3types.Filesystem{
		ign3types.Filesystem{
			Device: "/dev/sda1",
			Format: helpers.StrToPtr("ext4"),
			Path:   helpers.StrToPtr("/foo/bar"),
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Filesystem", isReconcilable)

	// Match Storage filesystems
	newIgnCfg.Storage.Filesystems = oldIgnCfg.Storage.Filesystems
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Filesystem", isReconcilable)

	// Verify Raid changes react as expected
	oldIgnCfg.Storage.Raid = []ign3types.Raid{
		ign3types.Raid{
			Name:  "data",
			Level: "stripe",
		},
	}
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "Raid", isReconcilable)

	// Match storage raid
	newIgnCfg.Storage.Raid = oldIgnCfg.Storage.Raid
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "Raid", isReconcilable)

	// Verify Passwd Groups changes unsupported
	oldIgnCfg = ctrlcommon.NewIgnConfig()
	oldConfig = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	newIgnCfg = ctrlcommon.NewIgnConfig()
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)

	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "PasswdGroups", isReconcilable)

	tempGroup := ign3types.PasswdGroup{}
	tempGroup.Name = "testGroup"
	newIgnCfg.Passwd.Groups = []ign3types.PasswdGroup{tempGroup}
	newConfig = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, isReconcilable = reconcilable(oldConfig, newConfig)
	checkIrreconcilableResults(t, "PasswdGroups", isReconcilable)
}

func TestMachineConfigDiff(t *testing.T) {
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	oldConfig := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	oldConfig.ObjectMeta = metav1.ObjectMeta{Name: "oldconfig"}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	newConfig.ObjectMeta = metav1.ObjectMeta{Name: "newconfig"}
	diff, err := newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.True(t, diff.isEmpty())

	newConfig.Spec.OSImageURL = "quay.io/example/foo@sha256:b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c"
	diff, err = newMachineConfigDiff(oldConfig, newConfig)
	assert.Nil(t, err)
	assert.False(t, diff.isEmpty())
	assert.True(t, diff.osUpdate)

	emptyMc := canonicalizeEmptyMC(nil)
	otherEmptyMc := canonicalizeEmptyMC(nil)
	emptyMc.Spec.KernelArguments = nil
	otherEmptyMc.Spec.KernelArguments = []string{}
	diff, err = newMachineConfigDiff(emptyMc, otherEmptyMc)
	assert.Nil(t, err)
	assert.True(t, diff.isEmpty())
}

func newTestIgnitionFile(i uint) ign3types.File {
	mode := 0644
	return ign3types.File{Node: ign3types.Node{Path: fmt.Sprintf("/etc/config%d", i)},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: helpers.StrToPtr(fmt.Sprintf("data:,config%d", i))}, Mode: &mode}}
}

func newMachineConfigFromFiles(files []ign3types.File) *mcfgv1.MachineConfig {
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newIgnCfg.Storage.Files = files
	newConfig := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	return newConfig
}

func TestReconcilableDiff(t *testing.T) {
	var oldFiles []ign3types.File
	nOldFiles := uint(10)
	for i := uint(0); i < nOldFiles; i++ {
		oldFiles = append(oldFiles, newTestIgnitionFile(uint(i)))
	}
	oldConfig := newMachineConfigFromFiles(oldFiles)
	newConfig := newMachineConfigFromFiles(append(oldFiles, newTestIgnitionFile(nOldFiles+1)))

	diff, err := reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "add file", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(nil)
	diff, err = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "remove all files", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(oldFiles)
	newConfig.Spec.OSImageURL = "example.com/machine-os-content:new"
	diff, err = reconcilable(oldConfig, newConfig)
	checkReconcilableResults(t, "os update", err)
	assert.Equal(t, diff.osUpdate, true)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, false)
}

func TestKernelAguments(t *testing.T) {
	tests := []struct {
		oldKargs []string
		newKargs []string
		out      []string
	}{
		{
			oldKargs: nil,
			newKargs: []string{"hello=world"},
			out:      []string{"--append=hello=world"},
		},
		{
			oldKargs: []string{"hello=world"},
			newKargs: nil,
			out:      []string{"--delete=hello=world"},
		},
		{
			oldKargs: []string{"foo", "bar=1", "hello=world"},
			newKargs: []string{"hello=world"},
			out:      []string{"--delete=foo", "--delete=bar=1", "--delete=hello=world", "--append=hello=world"},
		},
		{
			oldKargs: []string{"foo", "bar=1 hello=world", "baz"},
			newKargs: []string{"foo", "bar=1", "hello=world"},
			out: []string{"--delete=foo", "--delete=bar=1", "--delete=hello=world", "--delete=baz",
				"--append=foo", "--append=bar=1", "--append=hello=world"},
		},
		{
			oldKargs: []string{" baz=test bar=\"hello world\""},
			newKargs: []string{" baz=test bar=\"hello world\"", "foo"},
			out: []string{"--delete=baz=test", "--delete=bar=\"hello world\"",
				"--append=baz=test", "--append=bar=\"hello world\"", "--append=foo"},
		},
		{
			oldKargs: []string{"hugepagesz=1G hugepages=4", "hugepagesz=2M hugepages=4"},
			newKargs: []string{"hugepagesz=1G hugepages=4", "hugepagesz=2M hugepages=6"},
			out: []string{"--delete=hugepagesz=1G", "--delete=hugepages=4", "--delete=hugepagesz=2M", "--delete=hugepages=4",
				"--append=hugepagesz=1G", "--append=hugepages=4", "--append=hugepagesz=2M", "--append=hugepages=6"},
		},
	}

	rand.Seed(time.Now().UnixNano())
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			oldIgnCfg := ctrlcommon.NewIgnConfig()
			oldMcfg := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
			oldMcfg.Spec.KernelArguments = test.oldKargs

			newIgnCfg := ctrlcommon.NewIgnConfig()
			newMcfg := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
			newMcfg.Spec.KernelArguments = test.newKargs

			res := generateKargs(oldMcfg, newMcfg)

			if !reflect.DeepEqual(test.out, res) {
				t.Errorf("Failed kernel arguments processing: expected: %v but result is: %v", test.out, res)
			}
		})
	}
}

func TestReconcilableSSH(t *testing.T) {
	// Check that updating SSH Key of user core supported
	oldIgnCfg := ctrlcommon.NewIgnConfig()
	oldMcfg := helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	tempUser1 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newIgnCfg.Passwd.Users = []ign3types.PasswdUser{tempUser1}
	newMcfg := helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg := reconcilable(oldMcfg, newMcfg)
	checkReconcilableResults(t, "SSH", errMsg)

	// 	Check that updating User with User that is not core is not supported
	tempUser2 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}}
	oldIgnCfg.Passwd.Users = append(oldIgnCfg.Passwd.Users, tempUser2)
	oldMcfg = helpers.CreateMachineConfigFromIgnition(oldIgnCfg)
	tempUser3 := ign3types.PasswdUser{Name: "another user", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}}
	newIgnCfg.Passwd.Users[0] = tempUser3
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot make updates if any other Passwd.User field is changed.
	tempUser4 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}, HomeDir: helpers.StrToPtr("somedir")}
	newIgnCfg.Passwd.Users[0] = tempUser4
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that we cannot add a user or have len(Passwd.Users)> 1
	tempUser5 := ign3types.PasswdUser{Name: "some user", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}}
	newIgnCfg.Passwd.Users = append(newIgnCfg.Passwd.Users, tempUser5)
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	// check that user is not attempting to remove the only sshkey from core user
	tempUser6 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{}}
	newIgnCfg.Passwd.Users[0] = tempUser6
	newIgnCfg.Passwd.Users = newIgnCfg.Passwd.Users[:len(newIgnCfg.Passwd.Users)-1]
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)

	//check that empty Users does not cause panic
	newIgnCfg.Passwd.Users = nil
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnCfg)
	_, errMsg = reconcilable(oldMcfg, newMcfg)
	checkIrreconcilableResults(t, "SSH", errMsg)
}

func TestUpdateSSHKeys(t *testing.T) {
	// testClient is the NodeUpdaterClient mock instance that will front
	// calls to update the host.
	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
	}

	// Create a Daemon instance with mocked clients
	d := Daemon{
		mock:              true,
		name:              "nodeName",
		os:                OperatingSystem{},
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}
	// Set up machineconfigs that are identical except for SSH keys
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234", "4567"}}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	newIgnCfg.Passwd.Users = []ign3types.PasswdUser{tempUser}
	err := d.updateSSHKeys(newIgnCfg.Passwd.Users)
	if err != nil {
		t.Errorf("Expected no error. Got %s.", err)

	}

	// if Users is empty, nothing should happen and no error should ever be generated
	newIgnCfg2 := ctrlcommon.NewIgnConfig()
	newIgnCfg2.Passwd.Users = []ign3types.PasswdUser{}
	err = d.updateSSHKeys(newIgnCfg2.Passwd.Users)
	if err != nil {
		t.Errorf("Expected no error. Got: %s", err)
	}
}

// This test should fail until Ignition validation enabled.
// Ignition validation does not permit writing files to relative paths.
func TestInvalidIgnConfig(t *testing.T) {
	oldIgnConfig := ctrlcommon.NewIgnConfig()
	oldMcfg := helpers.CreateMachineConfigFromIgnition(oldIgnConfig)

	// create file to write that contains an impermissable relative path
	tempFileContents := ign3types.Resource{Source: helpers.StrToPtr("data:,hello%20world%0A")}
	tempMode := 420
	newIgnConfig := ctrlcommon.NewIgnConfig()
	newIgnFile := ign3types.File{
		Node:          ign3types.Node{Path: "home/core/test"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: tempFileContents, Mode: &tempMode},
	}
	newIgnConfig.Storage.Files = append(newIgnConfig.Storage.Files, newIgnFile)
	newMcfg := helpers.CreateMachineConfigFromIgnition(newIgnConfig)
	_, err := reconcilable(oldMcfg, newMcfg)
	assert.NotNil(t, err, "Expected error. Relative Paths should fail general ignition validation")

	newIgnConfig.Storage.Files[0].Node.Path = "/home/core/test"
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnConfig)
	diff, err := reconcilable(oldMcfg, newMcfg)
	assert.Nil(t, err, "Expected no error. Absolute paths should not fail general ignition validation")
	assert.Equal(t, diff.files, true)
}

func TestDropinCheck(t *testing.T) {
	tests := []struct {
		service  string
		dropin   string
		path     string
		expected bool
	}{
		{
			service:  "kubelet.service",
			dropin:   "10-foo.conf",
			path:     "/etc/systemd/system/kubelet.service.d/10-foo.conf",
			expected: true,
		},
		{
			service:  "kubelet.service",
			dropin:   "10-foo.conf",
			path:     "/usr/etc/systemd/system/kubelet.service.d/10-foo.conf",
			expected: false,
		},
		{
			service:  "kubelet.service",
			dropin:   "10-foo.conf",
			path:     "/etc/systemd/system/crio.service.d/10-foo.conf",
			expected: false,
		},
		{
			service:  "kubelet.service",
			dropin:   "10-foo.conf",
			path:     "/etc/systemd/system/kubelet.service.d/20-bar.conf",
			expected: false,
		},
	}

	testClient := RpmOstreeClientMock{
		GetBootedOSImageURLReturns: []GetBootedOSImageURLReturn{},
	}

	d := Daemon{
		mock:              true,
		name:              "nodeName",
		os:                OperatingSystem{},
		NodeUpdaterClient: testClient,
		kubeClient:        k8sfake.NewSimpleClientset(),
		bootedOSImageURL:  "test",
	}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			ignCfg := ctrlcommon.NewIgnConfig()
			ignCfg.Systemd.Units = []ign3types.Unit{
				ign3types.Unit{
					Name: test.service,
					Dropins: []ign3types.Dropin{
						ign3types.Dropin{
							Name:     test.dropin,
							Contents: helpers.StrToPtr("[Unit]"),
						},
						ign3types.Dropin{
							Name:     "99-other.conf",
							Contents: helpers.StrToPtr("[Unit]"),
						},
					},
				},
				ign3types.Unit{
					Name: "other.service",
				},
			}

			actual := d.isPathInDropins(test.path, &ignCfg.Systemd)

			if !reflect.DeepEqual(test.expected, actual) {
				t.Errorf("Failed stale file check: expected: %v but result is: %v", test.expected, actual)
			}
		})
	}
}

func newFile(path, contents string) ign3types.File {
	return ign3types.File{
		Node: ign3types.Node{
			Path: path,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(contents))),
			},
		},
	}
}

// Test to see if the correct action is calculated given a machineconfig diff
// i.e. whether we need to reboot and what actions need to be taken if no reboot is needed
func TestCalculatePostConfigChangeAction(t *testing.T) {
	files := map[string]ign3types.File{
		"pullsecret1":     newFile("/var/lib/kubelet/config.json", "kubelet conf 1\n"),
		"pullsecret2":     newFile("/var/lib/kubelet/config.json", "kubelet conf 2\n"),
		"registries1":     newFile("/etc/containers/registries.conf", "registries content 1\n"),
		"registries2":     newFile("/etc/containers/registries.conf", "registries content 2\n"),
		"randomfile1":     newFile("/etc/random-reboot-file", "test\n"),
		"randomfile2":     newFile("/etc/random-reboot-file", "test 2\n"),
		"kubeletCA1":      newFile("/etc/kubernetes/kubelet-ca.crt", "kubeletCA1\n"),
		"kubeletCA2":      newFile("/etc/kubernetes/kubelet-ca.crt", "kubeletCA2\n"),
		"policy1":         newFile("/etc/containers/policy.json", "policy1"),
		"policy2":         newFile("/etc/containers/policy.json", "policy2"),
		"containers-gpg1": newFile("/etc/machine-config-daemon/no-reboot/containers-gpg.pub", "containers-gpg1"),
		"containers-gpg2": newFile("/etc/machine-config-daemon/no-reboot/containers-gpg.pub", "containers-gpg2"),
	}

	tests := []struct {
		oldConfig      *mcfgv1.MachineConfig
		newConfig      *mcfgv1.MachineConfig
		expectedAction []string
	}{
		{
			// test that a normal file change is reboot
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["randomfile1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["randomfile2"]}),
			expectedAction: []string{postConfigChangeActionReboot},
		},
		{
			// test that a pull secret change is none
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["pullsecret1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["pullsecret2"]}),
			expectedAction: []string{postConfigChangeActionNone},
		},
		{
			// test that a SSH key change is none
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, []ign3types.File{}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, []ign3types.File{}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key2"}, []string{}, false, []string{}, "default", "dummy://"),
			expectedAction: []string{postConfigChangeActionNone},
		},
		{
			// test that a registries change is reload
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["registries1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["registries2"]}),
			expectedAction: []string{postConfigChangeActionReloadCrio},
		},
		{
			// test that a kubelet CA change is none
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["kubeletCA1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["kubeletCA2"]}),
			expectedAction: []string{postConfigChangeActionNone},
		},
		{
			// test that a registries change (reload) overwrites pull secret (none)
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["registries1"], files["pullsecret1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["registries2"], files["pullsecret2"]}),
			expectedAction: []string{postConfigChangeActionReloadCrio},
		},
		{
			// test that a osImage change (reboot) overwrites registries (reload) and SSH keys (none)
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, []ign3types.File{files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, []ign3types.File{files["registries2"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key2"}, []string{}, false, []string{}, "default", "dummy1://"),
			expectedAction: []string{postConfigChangeActionReboot},
		},
		{
			// test that adding a pull secret is none
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, []ign3types.File{files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, []ign3types.File{files["registries1"], files["pullsecret2"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			expectedAction: []string{postConfigChangeActionNone},
		},
		{
			// test that removing a registries is crio reload
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, []ign3types.File{files["randomfile1"], files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, []ign3types.File{files["randomfile1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			expectedAction: []string{postConfigChangeActionReloadCrio},
		},
		{
			// mixed test - final should be reboot due to kargs changes
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, []ign3types.File{files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, []ign3types.File{files["pullsecret2"], files["kubeletCA1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key2"}, []string{}, false, []string{"karg1"}, "default", "dummy://"),
			expectedAction: []string{postConfigChangeActionReboot},
		},
		{
			// test that updating policy.json is crio reload
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["policy1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["policy2"]}),
			expectedAction: []string{postConfigChangeActionReloadCrio},
		},
		{
			// test that updating containers-gpg.pub is crio reload
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["containers-gpg1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["containers-gpg2"]}),
			expectedAction: []string{postConfigChangeActionReloadCrio},
		},
	}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			oldIgnConfig, err := ctrlcommon.ParseAndConvertConfig(test.oldConfig.Spec.Config.Raw)
			if err != nil {
				t.Errorf("parsing old Ignition config failed: %v", err)
			}
			newIgnConfig, err := ctrlcommon.ParseAndConvertConfig(test.newConfig.Spec.Config.Raw)
			if err != nil {
				t.Errorf("parsing new Ignition config failed: %v", err)
			}
			mcDiff, err := newMachineConfigDiff(test.oldConfig, test.newConfig)
			if err != nil {
				t.Errorf("error creating machineConfigDiff: %v", err)
			}
			calculatedAction, err := calculatePostConfigChangeAction(mcDiff, oldIgnConfig, newIgnConfig)

			if !reflect.DeepEqual(test.expectedAction, calculatedAction) {
				t.Errorf("Failed calculating config change action: expected: %v but result is: %v. Error: %v", test.expectedAction, calculatedAction, err)
			}
		})
	}
}

// checkReconcilableResults is a shortcut for verifying results that should be reconcilable
func checkReconcilableResults(t *testing.T, key string, reconcilableError error) {
	if reconcilableError != nil {
		t.Errorf("%s values should be reconcilable. Received error: %v", key, reconcilableError)
	}
}

// checkIrreconcilableResults is a shortcut for verifing results that should be irreconcilable
func checkIrreconcilableResults(t *testing.T, key string, reconcilableError error) {
	if reconcilableError == nil {
		t.Errorf("Different %s values should not be reconcilable.", key)
	}
}

func TestRunGetOut(t *testing.T) {
	o, err := runGetOut("true")
	assert.Nil(t, err)
	assert.Equal(t, len(o), 0)

	o, err = runGetOut("false")
	assert.NotNil(t, err)

	o, err = runGetOut("echo", "hello")
	assert.Nil(t, err)
	assert.Equal(t, string(o), "hello\n")

	// base64 encode "oops" so we can't match on the command arguments
	o, err = runGetOut("/bin/sh", "-c", "echo hello; echo b29wcwo= | base64 -d 1>&2; exit 1")
	assert.Error(t, err)
	errtext := err.Error()
	assert.Contains(t, errtext, "exit status 1\noops\n")

	o, err = runGetOut("/usr/bin/test-failure-to-exec-this-should-not-exist", "arg")
	assert.Error(t, err)
}

// TestOriginalFileBackupRestore tests backikg up and restoring original files (files that are present in the base image and
// get overwritten by a machine configuration)
func TestOriginalFileBackupRestore(t *testing.T) {

	// Make a temp dir to put the testing files in, and make sure we clean it up
	testDir, err := ioutil.TempDir("", "mco-test")
	assert.Nil(t, err)
	defer os.RemoveAll(testDir)

	// Stub out a test directory structure -- we need to create /etc so createOrigFile can use it
	etcDir := filepath.Join(testDir, "etc")
	err = os.MkdirAll(etcDir, 0755)
	assert.Nil(t, err)

	// Make sure path variables get put back for other tests
	defer func(o string) { origParentDirPath = o }(origParentDirPath)
	defer func(o string) { noOrigParentDirPath = o }(noOrigParentDirPath)

	// Override these package variables so files get written to our testing location
	origParentDirPath = filepath.Join(testDir, origParentDirPath)
	noOrigParentDirPath = filepath.Join(testDir, noOrigParentDirPath)

	// Write a normal file as a control to make sure normal case works
	controlFile := filepath.Join(testDir, "control-file")
	err = ioutil.WriteFile(controlFile, []byte("control file contents"), 0755)
	assert.Nil(t, err)

	// Back up that normal file
	err = createOrigFile(controlFile, controlFile)
	assert.Nil(t, err)

	// Now try again and make sure it knows it's already backed up
	err = createOrigFile(controlFile, controlFile)
	assert.Nil(t, err)

	// Restore the normal file
	err = restorePath(controlFile)
	assert.Nil(t, err)

	// The normal file worked, try it with a symlink
	// Write a file we can point a symlink at
	err = ioutil.WriteFile(filepath.Join(testDir, "target-file"), []byte("target file contents"), 0755)
	assert.Nil(t, err)

	// Make a relative symlink
	relativeSymlink := filepath.Join(testDir, "etc", "relative-symlink-to-target-file")
	relativeSymlinkTarget := filepath.Join("..", "target-file")
	err = os.Symlink(relativeSymlinkTarget, relativeSymlink)
	assert.Nil(t, err)

	// Back up the relative symlink
	err = createOrigFile(relativeSymlink, relativeSymlink)
	assert.Nil(t, err)

	// Remove the symlink and write a file over it
	fileOverSymlink := filepath.Join(testDir, "etc", "relative-symlink-to-target-file")
	err = os.Remove(fileOverSymlink)
	assert.Nil(t, err)
	err = ioutil.WriteFile(fileOverSymlink, []byte("replacement contents"), 0755)
	assert.Nil(t, err)

	// Try to back it up again make sure it knows it's already backed up
	err = createOrigFile(relativeSymlink, relativeSymlink)
	assert.Nil(t, err)

	// Finally, make sure we can restore the relative symlink if we rollback
	err = restorePath(relativeSymlink)
	assert.Nil(t, err)

}
