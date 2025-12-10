package daemon

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"math/rand"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	opv1 "github.com/openshift/api/operator/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/daemon/osrelease"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vincent-petithory/dataurl"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func newMockDaemon() Daemon {
	// Create a Daemon instance with mocked clients
	return Daemon{
		mock:             true,
		name:             "nodeName",
		os:               osrelease.OperatingSystem{},
		kubeClient:       k8sfake.NewSimpleClientset(),
		bootedOSImageURL: "test",
	}
}

func setupTempDirWithEtc(t *testing.T) (string, func()) {
	t.Helper()

	// Make a temp dir to put the testing files in, and make sure we clean it up
	testDir := t.TempDir()

	// Stub out a test directory structure -- we need to create /etc so createOrigFile can use it
	etcDir := filepath.Join(testDir, "etc")
	err := os.MkdirAll(etcDir, 0755)
	require.Nil(t, err)

	oldOrigParentDirPath := origParentDirPath
	oldNoOrigParentDirPath := noOrigParentDirPath

	// Override these package variables so files get written to our testing location
	origParentDirPath = filepath.Join(testDir, origParentDirPath)
	noOrigParentDirPath = filepath.Join(testDir, noOrigParentDirPath)

	return testDir, func() {
		// Make sure path variables get put back for other tests
		origParentDirPath = oldOrigParentDirPath
		noOrigParentDirPath = oldNoOrigParentDirPath
	}
}

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

	passwdTestCases := []struct {
		name        string
		passwdUsers []ign3types.PasswdUser
		baseMC      *mcfgv1.MachineConfig
	}{
		{
			name: "SSH key changes recognized - Empty MC",
			passwdUsers: []ign3types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}},
			},
			baseMC: canonicalizeEmptyMC(nil),
		},
		{
			name: "SSH key changes recognized - New Key",
			passwdUsers: []ign3types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}},
			},
			baseMC: helpers.CreateMachineConfigFromIgnition(ign3types.Config{
				Ignition: ign3types.Ignition{
					Version: ign3types.MaxVersion.String(),
				},
				Passwd: ign3types.Passwd{
					Users: []ign3types.PasswdUser{
						{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}},
					},
				},
			}),
		},
		{
			name: "PasswordHash changes recognized - Empty MC",
			passwdUsers: []ign3types.PasswdUser{
				{Name: "core", PasswordHash: helpers.StrToPtr("testpass")},
			},
			baseMC: canonicalizeEmptyMC(nil),
		},
		{
			name: "PasswordHash changes recognized - Password Change",
			passwdUsers: []ign3types.PasswdUser{
				{Name: "core", PasswordHash: helpers.StrToPtr("testpass")},
			},
			baseMC: helpers.CreateMachineConfigFromIgnition(ign3types.Config{
				Ignition: ign3types.Ignition{
					Version: ign3types.MaxVersion.String(),
				},
				Passwd: ign3types.Passwd{
					Users: []ign3types.PasswdUser{
						{Name: "core", PasswordHash: helpers.StrToPtr("newtestpass")},
					},
				},
			}),
		},
	}

	for _, testCase := range passwdTestCases {
		t.Run(testCase.name, func(t *testing.T) {
			newIgnCfg := ctrlcommon.NewIgnConfig()
			newIgnCfg.Passwd.Users = testCase.passwdUsers
			newMC := helpers.CreateMachineConfigFromIgnition(newIgnCfg)

			diff, err = newMachineConfigDiff(testCase.baseMC, newMC)
			assert.Nil(t, err)
			assert.False(t, diff.isEmpty())
			assert.True(t, diff.passwd)
		})
	}

	oclTestCases := []struct {
		name          string
		oldMC         *mcfgv1.MachineConfig
		newMC         *mcfgv1.MachineConfig
		oclEnabled    bool
		revertFromOCL bool
		osUpdate      bool
	}{
		{
			name:  "Empty MCs",
			oldMC: canonicalizeEmptyMC(nil),
			newMC: canonicalizeEmptyMC(nil),
		},
		{
			name:  "Non-OCL MCs",
			oldMC: newConfig,
			newMC: newConfig,
		},
		{
			name:     "Non-OCL OS update",
			oldMC:    oldConfig,
			newMC:    newConfig,
			osUpdate: true,
		},
		{
			name:       "OCL enabled no OS update",
			oldMC:      embedOCLImageInMachineConfig("image-pullspec", oldConfig),
			newMC:      embedOCLImageInMachineConfig("image-pullspec", oldConfig),
			oclEnabled: true,
		},
		{
			name:       "OCL enabled with OS update",
			oldMC:      embedOCLImageInMachineConfig("image-pullspec", oldConfig),
			newMC:      embedOCLImageInMachineConfig("other-pullspec", oldConfig),
			oclEnabled: true,
			osUpdate:   true,
		},
		{
			name:       "OCL enabled but not rolled out yet",
			oldMC:      newConfig,
			newMC:      embedOCLImageInMachineConfig("image-pullspec", newConfig),
			osUpdate:   true,
			oclEnabled: true,
		},
		{
			name:          "Revert to non-OCL",
			oldMC:         embedOCLImageInMachineConfig("image-pullspec", newConfig),
			newMC:         newConfig,
			osUpdate:      true,
			revertFromOCL: true,
			oclEnabled:    true,
		},
	}

	for _, testCase := range oclTestCases {
		t.Run(testCase.name, func(t *testing.T) {
			diff, err := newMachineConfigDiff(testCase.oldMC, testCase.newMC)
			assert.NoError(t, err)

			assert.Equal(t, testCase.oclEnabled, diff.oclEnabled, "OCL enabled mismatch")
			assert.Equal(t, testCase.revertFromOCL, diff.revertFromOCL, "Revert from OCL mismatch")
			assert.Equal(t, testCase.osUpdate, diff.osUpdate, "OS Update mismatch")
		})
	}
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

	diff, err := reconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "add file", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(nil)
	diff, err = reconcilable(oldConfig, newConfig, nil)
	checkReconcilableResults(t, "remove all files", err)
	assert.Equal(t, diff.osUpdate, false)
	assert.Equal(t, diff.passwd, false)
	assert.Equal(t, diff.units, false)
	assert.Equal(t, diff.files, true)

	newConfig = newMachineConfigFromFiles(oldFiles)
	newConfig.Spec.OSImageURL = "example.com/rhel-coreos:new"
	diff, err = reconcilable(oldConfig, newConfig, nil)
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
			res := generateKargs(test.oldKargs, test.newKargs)

			if !reflect.DeepEqual(test.out, res) {
				t.Errorf("Failed kernel arguments processing: expected: %v but result is: %v", test.out, res)
			}
		})
	}
}

func TestWriteFiles(t *testing.T) {
	testDir, cleanup := setupTempDirWithEtc(t)
	defer cleanup()

	d := newMockDaemon()

	contents := []byte("hello world\n")
	encodedContents := dataurl.EncodeBytes(contents)

	// gzip contents
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	_, err := writer.Write(contents)
	assert.Nil(t, err)
	err = writer.Close()
	assert.Nil(t, err)
	gzippedContents := dataurl.EncodeBytes(buffer.Bytes())

	// use current user so test doesn't try to chown to root
	currentUser, err := user.Current()
	assert.Nil(t, err)
	currentUid, err := strconv.Atoi(currentUser.Uid)
	assert.Nil(t, err)
	currentGid, err := strconv.Atoi(currentUser.Gid)
	assert.Nil(t, err)

	filePath := filepath.Join(testDir, "test")
	node := ign3types.Node{
		Path:  filePath,
		User:  ign3types.NodeUser{ID: &currentUid},
		Group: ign3types.NodeGroup{ID: &currentGid},
	}

	mode := 420

	tests := []struct {
		name             string
		files            []ign3types.File
		expectedErr      error
		expectedContents []byte
	}{
		{
			name: "basic file write",
			files: []ign3types.File{{
				Node:          node,
				FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &encodedContents}, Mode: &mode},
			}},
			expectedContents: contents,
		},
		{
			name: "write file with compressed contents",
			files: []ign3types.File{{
				Node:          node,
				FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &gzippedContents, Compression: helpers.StrToPtr("gzip")}, Mode: &mode},
			}},
			expectedContents: contents,
		},
		{
			name: "try to write file with unsupported compression type",
			files: []ign3types.File{{
				Node:          node,
				FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &encodedContents, Compression: helpers.StrToPtr("xz")}, Mode: &mode},
			}},
			expectedErr: fmt.Errorf("could not decode file %q: %w", filePath, fmt.Errorf("unsupported compression type %q", "xz")),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := d.writeFiles(test.files, true)
			assert.Equal(t, test.expectedErr, err)
			if test.expectedContents != nil {
				fileContents, err := os.ReadFile(filePath)
				assert.Nil(t, err)
				assert.Equal(t, string(test.expectedContents), string(fileContents))
			}
			os.RemoveAll(filePath)
		})
	}
}

// This test provides a false sense of security. Given the combination of the
// mock mode in the MCD coupled with the inputs into this test, it effectively
// no-ops and does not test what we think it tests.
func TestUpdateSSHKeys(t *testing.T) {
	d := newMockDaemon()

	// Set up machineconfigs that are identical except for SSH keys
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234", "4567"}}
	newIgnCfg := ctrlcommon.NewIgnConfig()
	oldIgnConfig := ctrlcommon.NewIgnConfig()
	newIgnCfg.Passwd.Users = []ign3types.PasswdUser{tempUser}
	err := d.updateSSHKeys(newIgnCfg.Passwd.Users, oldIgnConfig.Passwd.Users)
	if err != nil {
		t.Errorf("Expected no error. Got %s.", err)

	}

	// if Users is empty, nothing should happen and no error should ever be generated
	newIgnCfg2 := ctrlcommon.NewIgnConfig()
	newIgnCfg2.Passwd.Users = []ign3types.PasswdUser{}
	err = d.updateSSHKeys(newIgnCfg2.Passwd.Users, oldIgnConfig.Passwd.Users)
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
	_, err := reconcilable(oldMcfg, newMcfg, nil)
	assert.NotNil(t, err, "Expected error. Relative Paths should fail general ignition validation")

	newIgnConfig.Storage.Files[0].Node.Path = "/home/core/test"
	newMcfg = helpers.CreateMachineConfigFromIgnition(newIgnConfig)
	diff, err := reconcilable(oldMcfg, newMcfg, nil)
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

	d := newMockDaemon()

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			ignCfg := ctrlcommon.NewIgnConfig()
			ignCfg.Systemd.Units = []ign3types.Unit{
				{
					Name: test.service,
					Dropins: []ign3types.Dropin{
						{
							Name:     test.dropin,
							Contents: helpers.StrToPtr("[Unit]"),
						},
						{
							Name:     "99-other.conf",
							Contents: helpers.StrToPtr("[Unit]"),
						},
					},
				},
				{
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

// Test to see if the correct action is calculated given a machineconfig diff
// i.e. whether we need to reboot and what actions need to be taken if no reboot is needed
func TestCalculatePostConfigChangeAction(t *testing.T) {
	files := map[string]ign3types.File{
		"pullsecret1":     ctrlcommon.NewIgnFile("/var/lib/kubelet/config.json", "kubelet conf 1\n"),
		"pullsecret2":     ctrlcommon.NewIgnFile("/var/lib/kubelet/config.json", "kubelet conf 2\n"),
		"registries1":     ctrlcommon.NewIgnFile("/etc/containers/registries.conf", "registries content 1\n"),
		"registries2":     ctrlcommon.NewIgnFile("/etc/containers/registries.conf", "registries content 2\n"),
		"randomfile1":     ctrlcommon.NewIgnFile("/etc/random-reboot-file", "test\n"),
		"randomfile2":     ctrlcommon.NewIgnFile("/etc/random-reboot-file", "test 2\n"),
		"kubeletCA1":      ctrlcommon.NewIgnFile("/etc/kubernetes/kubelet-ca.crt", "kubeletCA1\n"),
		"kubeletCA2":      ctrlcommon.NewIgnFile("/etc/kubernetes/kubelet-ca.crt", "kubeletCA2\n"),
		"policy1":         ctrlcommon.NewIgnFile("/etc/containers/policy.json", "policy1"),
		"policy2":         ctrlcommon.NewIgnFile("/etc/containers/policy.json", "policy2"),
		"containers-gpg1": ctrlcommon.NewIgnFile("/etc/machine-config-daemon/no-reboot/containers-gpg.pub", "containers-gpg1"),
		"containers-gpg2": ctrlcommon.NewIgnFile("/etc/machine-config-daemon/no-reboot/containers-gpg.pub", "containers-gpg2"),
		"restart-crio1":   ctrlcommon.NewIgnFile("/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt", "restart-crio1"),
		"restart-crio2":   ctrlcommon.NewIgnFile("/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt", "restart-crio2"),
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
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, nil, []ign3types.File{}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, nil, []ign3types.File{}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key2"}, []string{}, false, []string{}, "default", "dummy://"),
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
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, nil, []ign3types.File{files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, nil, []ign3types.File{files["registries2"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key2"}, []string{}, false, []string{}, "default", "dummy1://"),
			expectedAction: []string{postConfigChangeActionReboot},
		},
		{
			// test that adding a pull secret is none
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, nil, []ign3types.File{files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, nil, []ign3types.File{files["registries1"], files["pullsecret2"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			expectedAction: []string{postConfigChangeActionNone},
		},
		{
			// test that removing a registries is crio reload
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, nil, []ign3types.File{files["randomfile1"], files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, nil, []ign3types.File{files["randomfile1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			expectedAction: []string{postConfigChangeActionReloadCrio},
		},
		{
			// mixed test - final should be reboot due to kargs changes
			oldConfig:      helpers.NewMachineConfigExtended("00-test", nil, nil, []ign3types.File{files["registries1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key1"}, []string{}, false, []string{}, "default", "dummy://"),
			newConfig:      helpers.NewMachineConfigExtended("01-test", nil, nil, []ign3types.File{files["pullsecret2"], files["kubeletCA1"]}, []ign3types.Unit{}, []ign3types.SSHAuthorizedKey{"key2"}, []string{}, false, []string{"karg1"}, "default", "dummy://"),
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
		{
			// test that updating openshift-config-user-ca-bundle.crt is crio restart
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["restart-crio1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["restart-crio2"]}),
			expectedAction: []string{postConfigChangeActionRestartCrio}},
		{
			// test that updating openshift-config-user-ca-bundle.crt is crio restart and that it overrides a following crio reload
			oldConfig:      helpers.NewMachineConfig("00-test", nil, "dummy://", []ign3types.File{files["restart-crio1"]}),
			newConfig:      helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{files["restart-crio2"], files["containers-gpg1"]}),
			expectedAction: []string{postConfigChangeActionRestartCrio},
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
			diffFileSet := ctrlcommon.CalculateConfigFileDiffs(&oldIgnConfig, &newIgnConfig)
			calculatedAction, err := calculatePostConfigChangeAction(mcDiff, diffFileSet)

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
	if runtime.GOOS != "linux" {
		t.Skipf("Non-Linux OS %q detected, skipping test", runtime.GOOS)
	}

	testDir, cleanup := setupTempDirWithEtc(t)
	defer cleanup()

	// Write a file in the /tmp dir to test whether orig files are selectively preserved
	controlFile := filepath.Join(testDir, "control-file")
	err := os.WriteFile(controlFile, []byte("control file contents"), 0755)
	assert.Nil(t, err)

	// Back up the tmp file
	err = createOrigFile(controlFile, controlFile)
	assert.Nil(t, err)

	// Now try again and make sure it knows it's already backed up if it should be
	err = createOrigFile(controlFile, controlFile)
	assert.Nil(t, err)

	// Check whether there is an orig preservation for the path - OK: there is no back up for the tmp file
	_, err = os.Stat(origFileName(controlFile))
	assert.True(t, os.IsNotExist(err))
}

func TestFindClosestFilePolicyPathMatch(t *testing.T) {

	policyActions := map[string][]opv1.NodeDisruptionPolicyStatusAction{
		"Empty":       {},
		"None":        {{Type: opv1.NoneStatusAction}},
		"Reboot":      {{Type: opv1.RebootStatusAction}},
		"RestartCrio": {{Type: opv1.RestartStatusAction, Restart: &opv1.RestartService{ServiceName: "crio.service"}}},
		"ReloadCrio":  {{Type: opv1.ReloadStatusAction, Reload: &opv1.ReloadService{ServiceName: "crio.service"}}}}

	tests := []struct {
		diffPath             string
		filePolicies         []opv1.NodeDisruptionPolicyStatusFile
		expectedPathFound    bool
		expectedActionsFound []opv1.NodeDisruptionPolicyStatusAction
	}{
		{
			// test that an file with no policies returns no actions
			diffPath: "/etc/mco/test",
			filePolicies: []opv1.NodeDisruptionPolicyStatusFile{
				{
					Path:    "/etc/example",
					Actions: policyActions["None"],
				},
			},
			expectedPathFound:    false,
			expectedActionsFound: policyActions["Empty"],
		},
		{
			// test that a file with a valid policy returns the correct actions
			diffPath: "/etc/mco/test1",
			filePolicies: []opv1.NodeDisruptionPolicyStatusFile{
				{
					Path:    "/etc/mco/test1",
					Actions: policyActions["None"],
				},
				{
					Path:    "/etc/mco/test2",
					Actions: policyActions["Reboot"],
				},
			},
			expectedPathFound:    true,
			expectedActionsFound: policyActions["None"],
		},
		{
			// test that a file with multiple path policies returns the correct actions(closest match)
			diffPath: "/etc/mco/f1/f2/example",
			filePolicies: []opv1.NodeDisruptionPolicyStatusFile{
				{
					Path:    "/etc/mco/f1",
					Actions: policyActions["None"],
				},
				{
					Path:    "/etc/mco/f1/f2",
					Actions: policyActions["Reboot"],
				},
				{
					Path:    "/etc/mco/f1/f2/f3",
					Actions: policyActions["ReloadCrio"],
				},
			},
			expectedPathFound:    true,
			expectedActionsFound: policyActions["Reboot"],
		},
		{
			// test that a file with multiple path policies returns the correct actions when there is an exact match
			diffPath: "/etc/mco/f1/f2/f3",
			filePolicies: []opv1.NodeDisruptionPolicyStatusFile{
				{
					Path:    "/etc/mco/f1",
					Actions: policyActions["None"],
				},
				{
					Path:    "/etc/mco/f1/f2",
					Actions: policyActions["Reboot"],
				},
				{
					Path:    "/etc/mco/f1/f2/f3",
					Actions: policyActions["ReloadCrio"],
				},
			},
			expectedPathFound:    true,
			expectedActionsFound: policyActions["ReloadCrio"],
		},
		{
			// test that a file with a partial parent dir match, but no actual dir match does not return any action
			diffPath: "/etc/mco/f1/f2/f3",
			filePolicies: []opv1.NodeDisruptionPolicyStatusFile{
				{
					Path:    "/etc/mco/f1/test",
					Actions: policyActions["None"],
				},
				{
					Path:    "/etc/mco/f1/f2/test",
					Actions: policyActions["Reboot"],
				},
				{
					Path:    "/etc/mco/f1/f2/f3/f4",
					Actions: policyActions["ReloadCrio"],
				},
			},
			expectedPathFound:    false,
			expectedActionsFound: policyActions["Empty"],
		},
	}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {

			pathFound, actionsFound := ctrlcommon.FindClosestFilePolicyPathMatch(test.diffPath, test.filePolicies)

			if !reflect.DeepEqual(test.expectedPathFound, pathFound) {
				t.Errorf("Failed finding node disruption file policy action: expected: %v but result is: %v.", test.expectedPathFound, pathFound)
			}
			if !reflect.DeepEqual(test.expectedActionsFound, actionsFound) {
				t.Errorf("Failed calculating node disruption file policy action: expected: %v but result is: %v.", test.expectedActionsFound, actionsFound)
			}
		})
	}
}

// Assisted by: Cursor
func TestVerifyExtensionPackagesInDeployment(t *testing.T) {
	tests := []struct {
		name               string
		requiredPackages   []string
		deploymentPackages []string
		expectError        bool
		errorContains      string
	}{
		{
			name:               "no required packages, no deployment packages",
			requiredPackages:   []string{},
			deploymentPackages: []string{},
			expectError:        false,
		},
		{
			name:               "no required packages, some deployment packages",
			requiredPackages:   []string{},
			deploymentPackages: []string{"some-package", "another-package"},
			expectError:        false,
		},
		{
			name:               "all required packages present",
			requiredPackages:   []string{"crun-wasm"},
			deploymentPackages: []string{"crun-wasm", "other-package"},
			expectError:        false,
		},
		{
			name:               "multiple required packages all present",
			requiredPackages:   []string{"NetworkManager-libreswan", "libreswan"},
			deploymentPackages: []string{"NetworkManager-libreswan", "libreswan", "other-package"},
			expectError:        false,
		},
		{
			name:               "missing single required package",
			requiredPackages:   []string{"crun-wasm"},
			deploymentPackages: []string{"other-package"},
			expectError:        true,
			errorContains:      "crun-wasm",
		},
		{
			name:               "missing multiple required packages",
			requiredPackages:   []string{"NetworkManager-libreswan", "libreswan"},
			deploymentPackages: []string{"other-package"},
			expectError:        true,
			errorContains:      "NetworkManager-libreswan",
		},
		{
			name:               "partial packages present - missing one",
			requiredPackages:   []string{"NetworkManager-libreswan", "libreswan"},
			deploymentPackages: []string{"NetworkManager-libreswan", "other-package"},
			expectError:        true,
			errorContains:      "libreswan",
		},
		{
			name:               "two-node-ha extension packages all present",
			requiredPackages:   []string{"pacemaker", "pcs", "fence-agents-all"},
			deploymentPackages: []string{"pacemaker", "pcs", "fence-agents-all"},
			expectError:        false,
		},
		{
			name:               "two-node-ha extension packages partially present",
			requiredPackages:   []string{"pacemaker", "pcs", "fence-agents-all"},
			deploymentPackages: []string{"pacemaker", "pcs"},
			expectError:        true,
			errorContains:      "fence-agents-all",
		},
		{
			name:               "required packages present with extra packages in deployment",
			requiredPackages:   []string{"usbguard"},
			deploymentPackages: []string{"usbguard", "random-package-1", "random-package-2"},
			expectError:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyExtensionPackagesInDeployment(tt.requiredPackages, tt.deploymentPackages)

			if tt.expectError {
				assert.Error(t, err, "expected error but got none")
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, "error message should contain expected text")
				}
			} else {
				assert.NoError(t, err, "unexpected error")
			}
		})
	}
}

func TestGenerateExtensionsArgs(t *testing.T) {
	tests := []struct {
		name         string
		installedSet sets.Set[string]
		extensions   []string
		expected     []string
	}{
		{
			name:         "no extensions installed, no extensions required",
			installedSet: sets.New[string](),
			extensions:   []string{},
			expected:     []string{},
		},
		{
			name:         "no extensions installed, install wasm",
			installedSet: sets.New[string](),
			extensions:   []string{"wasm"},
			expected:     []string{constants.RPMOSTreeInstallArg, "crun-wasm"},
		},
		{
			name:         "no extensions installed, install multiple extensions",
			installedSet: sets.New[string](),
			extensions:   []string{"wasm", "ipsec"},
			expected:     []string{constants.RPMOSTreeInstallArg, "NetworkManager-libreswan", constants.RPMOSTreeInstallArg, "crun-wasm", constants.RPMOSTreeInstallArg, "libreswan"},
		},
		{
			name:         "wasm already installed, require wasm",
			installedSet: sets.New("crun-wasm"),
			extensions:   []string{"wasm"},
			expected:     []string{},
		},
		{
			name:         "wasm installed, no extensions required",
			installedSet: sets.New("crun-wasm"),
			extensions:   []string{},
			expected:     []string{constants.RPMOSTreeUninstallArg, "crun-wasm"},
		},
		{
			name:         "wasm and ipsec installed, only wasm required",
			installedSet: sets.New("crun-wasm", "NetworkManager-libreswan", "libreswan"),
			extensions:   []string{"wasm"},
			expected:     []string{constants.RPMOSTreeUninstallArg, "NetworkManager-libreswan", constants.RPMOSTreeUninstallArg, "libreswan"},
		},
		{
			name:         "some packages installed, switch to different extension",
			installedSet: sets.New("crun-wasm"),
			extensions:   []string{"ipsec"},
			expected:     []string{constants.RPMOSTreeInstallArg, "NetworkManager-libreswan", constants.RPMOSTreeInstallArg, "libreswan", constants.RPMOSTreeUninstallArg, "crun-wasm"},
		},
		{
			name:         "complex scenario with two-node-ha",
			installedSet: sets.New("pacemaker", "crun-wasm"),
			extensions:   []string{"two-node-ha", "usbguard"},
			expected:     []string{constants.RPMOSTreeInstallArg, "fence-agents-all", constants.RPMOSTreeInstallArg, "pcs", constants.RPMOSTreeInstallArg, "usbguard", constants.RPMOSTreeUninstallArg, "crun-wasm"},
		},
		{
			name:         "non-extension packages installed should not be uninstalled",
			installedSet: sets.New("random-package", "another-package"),
			extensions:   []string{"wasm"},
			expected:     []string{constants.RPMOSTreeInstallArg, "crun-wasm"},
		},
		{
			name:         "all supported extensions",
			installedSet: sets.New[string](),
			extensions:   []string{"two-node-ha", "wasm", "ipsec", "usbguard", "kerberos", "kernel-devel", "sandboxed-containers", "sysstat"},
			expected:     []string{constants.RPMOSTreeInstallArg, "NetworkManager-libreswan", constants.RPMOSTreeInstallArg, "crun-wasm", constants.RPMOSTreeInstallArg, "fence-agents-all", constants.RPMOSTreeInstallArg, "kata-containers", constants.RPMOSTreeInstallArg, "kernel-devel", constants.RPMOSTreeInstallArg, "kernel-headers", constants.RPMOSTreeInstallArg, "krb5-workstation", constants.RPMOSTreeInstallArg, "libkadm5", constants.RPMOSTreeInstallArg, "libreswan", constants.RPMOSTreeInstallArg, "pacemaker", constants.RPMOSTreeInstallArg, "pcs", constants.RPMOSTreeInstallArg, "sysstat", constants.RPMOSTreeInstallArg, "usbguard"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create MachineConfig with the test extensions
			newConfig := &mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Extensions: tt.extensions,
				},
			}

			result := generateExtensionsArgs(tt.installedSet, newConfig)

			// Sort both slices for comparison since order of packages within install/uninstall groups may vary
			assert.ElementsMatch(t, tt.expected, result, "generateExtensionsArgs result mismatch")
		})
	}
}
