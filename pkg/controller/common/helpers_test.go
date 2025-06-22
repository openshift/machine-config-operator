package common

import (
	"github.com/coreos/go-semver/semver"
	"reflect"
	"strings"
	"testing"

	"github.com/clarketm/json"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign3utils "github.com/coreos/ignition/v2/config/util"
	ign3 "github.com/coreos/ignition/v2/config/v3_5"
	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	validate3 "github.com/coreos/ignition/v2/config/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/common/fixtures"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestTranspileCoreOSConfig(t *testing.T) {
	kubeletConfig := `
mode: 0644
path: "/etc/kubernetes/kubelet.conf"
contents:
  inline: |
    kind: KubeletConfiguration
    apiVersion: kubelet.config.k8s.io/v1beta1
`
	auditConfig := `
mode: 0644
path: "/etc/audit/rules.d/mco-audit.rules"
contents:
  inline: |
    -a exclude,always -F msgtype=NETFILTER_CFG
`
	kubeletService := `name: kubelet.service
enabled: true
contents: | 
  [Unit]
  Description=kubelet
  [Service]
  ExecStart=/usr/bin/hyperkube
`
	crioDropin := `
name: crio.service
dropins:
  - name: 10-mco-default-madv.conf
    contents: |
      [Service]
      Environment="GODEBUG=x509ignoreCN=0,madvdontneed=1"
`
	dockerDropin := `
name: docker.socket
dropins:
- name: mco-disabled.conf
  contents: |
    [Unit]
    ConditionPathExists=/enoent
`
	config, err := TranspileCoreOSConfigToIgn([]string{kubeletConfig, auditConfig}, []string{kubeletService, crioDropin, dockerDropin})
	require.Nil(t, err)
	if report := validate3.ValidateWithContext(config, nil); report.IsFatal() {
		t.Fatalf("invalid ignition V3 config found: %v", report)
	}
	require.Equal(t, len(config.Storage.Files), 2)
	require.True(t, strings.HasPrefix(*config.Storage.Files[0].Contents.Source, "data:,kind%3A%20KubeletConfiguration%0Aapi"))
	require.Equal(t, len(config.Systemd.Units), 3)
}

func TestValidateIgnition(t *testing.T) {
	// Test that an empty ignition config returns nil
	testIgn2Config := ign2types.Config{}
	isValid := ValidateIgnition(testIgn2Config)
	require.Nil(t, isValid)

	// Test that an invalid ignition config returns and error
	tempUser1 := ign2types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign2types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn2Config.Passwd.Users = []ign2types.PasswdUser{tempUser1}
	isValid = ValidateIgnition(testIgn2Config)
	require.NotNil(t, isValid)

	// Test that a valid ignition config returns nil
	testIgn2Config.Ignition.Version = "2.0.0"
	ign2Mode := 420
	ign2File := ign2types.File{Node: ign2types.Node{Filesystem: "root", Path: "/etc/testfileconfig"},
		FileEmbedded1: ign2types.FileEmbedded1{Mode: &ign2Mode, Contents: ign2types.FileContents{Source: "data:,helloworld"}}}
	testIgn2Config.Storage.Files = []ign2types.File{ign2File}
	isValid = ValidateIgnition(testIgn2Config)
	require.Nil(t, isValid)

	// Test that an empty ignition config returns nil
	testIgn3Config := ign3types.Config{}
	isValid2 := ValidateIgnition(testIgn3Config)
	require.Nil(t, isValid2)

	// Test that an invalid ignition config returns an error
	tempUser2 := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn3Config.Passwd.Users = []ign3types.PasswdUser{tempUser2}
	isValid2 = ValidateIgnition(testIgn3Config)
	require.NotNil(t, isValid2)

	// Test that a valid ignition config returns nil
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	mode := 420
	testfiledata := "data:,greatconfigstuff"
	tempFile := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata}, Mode: &mode}}
	testIgn3Config.Storage.Files = append(testIgn3Config.Storage.Files, tempFile)
	isValid2 = ValidateIgnition(testIgn3Config)
	require.Nil(t, isValid2)

	// Test that file modes do not have special bits (sticky, setuid, setgid) set
	// https://bugzilla.redhat.com/show_bug.cgi?id=2038240
	invalidMode := 0o1777

	testIgn3Config.Storage.Files[0].Mode = &invalidMode
	isValid2 = ValidateIgnition(testIgn3Config)
	require.NotNil(t, isValid2)

	testIgn2Config.Storage.Files[0].Mode = &invalidMode
	isValid2 = ValidateIgnition(testIgn2Config)
	require.NotNil(t, isValid2)

	// Test that an invalid typed config will fail
	testInvalid := "test"
	isValid3 := ValidateIgnition(testInvalid)
	require.NotNil(t, isValid3)
}

// TestIgnitionConverterGetSupportedMinorVersions tests that the Ignition converter properly returns
// the expected supported minor versions in a sorted slice of strings
func TestIgnitionConverterGetSupportedMinorVersions(t *testing.T) {
	converter := newIgnitionConverter(buildConverterList())
	supported := []string{"2.2", "3.0", "3.1", "3.2", "3.3", "3.4", "3.5"}
	assert.Equal(t, supported, converter.GetSupportedMinorVersions())
}

// TestIgnitionConverterGetSupportedMinorVersion
func TestIgnitionConverterGetSupportedMinorVersion(t *testing.T) {
	converter := newIgnitionConverter(buildConverterList())
	v350 := semver.New("3.5.0")
	v352 := semver.New("3.5.2")
	matchingVersion, err := converter.GetSupportedMinorVersion(*v350)
	assert.NoError(t, err)
	assert.True(t, matchingVersion.Equal(*v350))

	matchingMinorVersion, err := converter.GetSupportedMinorVersion(*v352)
	assert.NoError(t, err)
	assert.True(t, matchingMinorVersion.Equal(*v350))

	_, err = converter.GetSupportedMinorVersion(*semver.New("7.7.7"))
	assert.ErrorIs(t, err, errIgnitionConverterUnknownVersion)
}

// TestIgnitionConverterConvert tests that the Ignition converter is able to handle the expected
// conversions and that is able to handle error conditions
func TestIgnitionConverterConvert(t *testing.T) {
	ign3Config := ign3types.Config{
		Ignition: ign3types.Ignition{Version: ign3types.MaxVersion.String()},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}},
			},
		},
	}

	ign2Config := ign2types.Config{
		Ignition: ign2types.Ignition{Version: ign2types.MaxVersion.String()},
		Passwd: ign2types.Passwd{
			Users: []ign2types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign2types.SSHAuthorizedKey{"5678", "abc"}},
			},
		},
	}

	converter := newIgnitionConverter(buildConverterList())
	tests := []struct {
		name          string
		inputConfig   any
		inputVersion  string
		outputVersion string
		err           error
	}{
		{
			name:          "Conversion from 2.2 to 3.5",
			inputConfig:   ign2Config,
			inputVersion:  "2.2.0",
			outputVersion: "3.5.0",
		},
		{
			name:          "Conversion from 3.5 to 2.2",
			inputConfig:   ign3Config,
			inputVersion:  "3.5.0",
			outputVersion: "2.2.0",
		},
		{
			name:          "Conversion from 3.5 to 3.4",
			inputConfig:   ign3Config,
			inputVersion:  "3.5.0",
			outputVersion: "3.4.0",
		},
		{
			name:          "Conversion from 3.5 to 3.3",
			inputConfig:   ign3Config,
			inputVersion:  "3.5.0",
			outputVersion: "3.3.0",
		},
		{
			name:          "Conversion from 3.5 to 3.2",
			inputConfig:   ign3Config,
			inputVersion:  "3.5.0",
			outputVersion: "3.2.0",
		},
		{
			name:          "Conversion from 3.5 to 3.1",
			inputConfig:   ign3Config,
			inputVersion:  "3.5.0",
			outputVersion: "3.1.0",
		},
		{
			name:          "Conversion wrong source version",
			inputConfig:   ign2Config,
			inputVersion:  "3.5.0",
			outputVersion: "3.1.0",
			err:           errIgnitionConverterWrongSourceType,
		}, {
			name:          "Conversion not supported",
			inputConfig:   ign3Config,
			inputVersion:  "3.1.0",
			outputVersion: "3.5.0",
			err:           errIgnitionConverterUnsupportedConversion,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := converter.Convert(testCase.inputConfig, *semver.New(testCase.inputVersion), *semver.New(testCase.outputVersion))
			if testCase.err == nil {
				if testCase.outputVersion == testCase.inputVersion {
					assert.Equal(t, testCase.inputConfig, result)
				} else {
					serialized, err := json.Marshal(result)
					assert.NoError(t, err)
					// Note: Despite 2.2 uses a different type the version field can be fetched using the v3 functions
					version, report, err := ign3utils.GetConfigVersion(serialized)
					assert.NoError(t, err)
					assert.False(t, report.IsFatal())
					assert.Equal(t, testCase.outputVersion, version.String())
				}
			} else {
				assert.ErrorIs(t, err, testCase.err)
			}

		})
	}
}

func TestParseAndConvert(t *testing.T) {
	// Make a new Ign3.2 config
	testIgn3Config := ign3types.Config{}
	tempUser := ign3types.PasswdUser{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "abc"}}
	testIgn3Config.Passwd.Users = []ign3types.PasswdUser{tempUser}
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion

	// Make a Ign2 comp config
	testIgn2Config := ign2types.Config{}
	tempUser2SSHKeys := []ign2types.SSHAuthorizedKey{
		"5678",
		"5678", // Purposely duplicated.
		"abc",
	}
	tempUser2 := ign2types.PasswdUser{Name: "core", SSHAuthorizedKeys: tempUser2SSHKeys}
	testIgn2Config.Passwd.Users = []ign2types.PasswdUser{tempUser2}
	testIgn2Config.Ignition.Version = "2.2.0"

	// turn v2.2 config into a raw []byte
	rawIgn := helpers.MarshalOrDie(testIgn2Config)
	// check that it was parsed successfully
	convertedIgn, err := ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn3Config, convertedIgn)

	// turn v3.1 config into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.2 cfg
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.1 cfg
	testIgn3Config.Ignition.Version = "3.1.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully back to the default version
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.0 cfg
	testIgn3Config.Ignition.Version = "3.0.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully back to the default version
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.3 cfg
	testIgn3Config.Ignition.Version = "3.3.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully back to the default version
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.4 cfg
	testIgn3Config.Ignition.Version = "3.4.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully back to the default version
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a valid Ign 3.5 cfg
	testIgn3Config.Ignition.Version = "3.5.0"
	// turn it into a raw []byte
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it was parsed successfully back to the default version
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.Nil(t, err)
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	assert.Equal(t, testIgn3Config, convertedIgn)

	// Make a bad Ign3 cfg
	testIgn3Config.Ignition.Version = "21.0.0"
	rawIgn = helpers.MarshalOrDie(testIgn3Config)
	// check that it failed since this is an invalid cfg
	convertedIgn, err = ParseAndConvertConfig(rawIgn)
	require.NotNil(t, err)
	assert.Equal(t, ign3types.Config{}, convertedIgn)
}

func TestMergeMachineConfigs(t *testing.T) {
	// variable setup
	cconfig := &mcfgv1.ControllerConfig{}
	cconfig.Spec.OSImageURL = "testURL"
	cconfig.Spec.BaseOSContainerImage = "newformatURL"
	fips := true
	kargs := []string{"testKarg", "kargFromIgnitionDowngrade"}
	extensions := []string{"testExtensions"}

	// Test that a singular base config that sets FIPS also sets other defaults correctly
	machineConfigFIPS := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "fips",
			Labels: map[string]string{MachineConfigRoleLabel: MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			FIPS: fips,
		},
	}
	inMachineConfigs := []*mcfgv1.MachineConfig{machineConfigFIPS}
	mergedMachineConfig, err := MergeMachineConfigs(inMachineConfigs, cconfig)
	require.Nil(t, err)

	// check that the outgoing config does have the version string set,
	// despite not having a MC with an ignition conf
	outIgn := ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
	}
	rawOutIgn, err := json.Marshal(outIgn)
	require.Nil(t, err)
	expectedMachineConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      GetDefaultBaseImageContainer(&cconfig.Spec),
			KernelArguments: []string{},
			Config: runtime.RawExtension{
				Raw: rawOutIgn,
			},
			FIPS:       fips,
			KernelType: KernelTypeDefault,
			Extensions: []string{},
		},
	}
	assert.Equal(t, *mergedMachineConfig, *expectedMachineConfig)

	// Test that all other configs can also be set properly

	// we previously prevented OSImageURL from being overridden via
	// machineconfig, but now that we're doing layering, we want to
	// give that functionality back, so make sure we can override it
	machineConfigOSImageURL := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "osimageurl",
			Labels: map[string]string{MachineConfigRoleLabel: MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "overriddenURL",
		},
	}
	machineConfigKernelArgs := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kargs",
			Labels: map[string]string{MachineConfigRoleLabel: MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: kargs,
		},
	}
	machineConfigKernelType := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "kerneltype",
			Labels: map[string]string{MachineConfigRoleLabel: MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			KernelType: KernelTypeRealtime,
		},
	}
	machineConfigExtensions := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "extension",
			Labels: map[string]string{MachineConfigRoleLabel: MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Extensions: extensions,
		},
	}

	machineConfigIgnSSHUser := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"1234"}},
			},
		},
	}, "ssh", MachineConfigPoolWorker)

	machineConfigIgnPasswdHashUser := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{Name: "core", PasswordHash: helpers.StrToPtr("testpass")},
			},
		},
	}, "passwd", MachineConfigPoolWorker)

	// we added some v3 specific logic for kargs, make sure we didn't break the v2 path
	machineConfigIgnV2Merge := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign2types.Config{
		Ignition: ign2types.Ignition{
			Version: ign2types.MaxVersion.String(),
		},
	}, "v2", MachineConfigPoolWorker)

	machineConfigIgn := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Passwd: ign3types.Passwd{
			Users: []ign3types.PasswdUser{
				{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678"}},
			},
		},
	}, "ssh", MachineConfigPoolWorker)

	// Now merge all of the above
	inMachineConfigs = []*mcfgv1.MachineConfig{
		machineConfigOSImageURL,
		machineConfigKernelArgs,
		machineConfigKernelType,
		machineConfigExtensions,
		machineConfigIgn,
		machineConfigFIPS,
		machineConfigIgnPasswdHashUser,
		machineConfigIgnSSHUser,
		machineConfigIgnV2Merge,
	}

	mergedMachineConfig, err = MergeMachineConfigs(inMachineConfigs, cconfig)
	require.Nil(t, err)

	expectedMachineConfig = &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      "overriddenURL",
			KernelArguments: kargs,
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ign3types.Config{
					Ignition: ign3types.Ignition{
						Version: ign3types.MaxVersion.String(),
					},
					Passwd: ign3types.Passwd{
						Users: []ign3types.PasswdUser{
							{Name: "core", SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"5678", "1234"}, PasswordHash: helpers.StrToPtr("testpass")},
						},
					},
				}),
			},
			FIPS:       true,
			KernelType: KernelTypeRealtime,
			Extensions: extensions,
		},
	}
	assert.Equal(t, *mergedMachineConfig, *expectedMachineConfig)

	// Test that custom pool configuration can overwrite base pool configuration
	// Also test that other alphanumeric ordering is preserved
	filePath1 := "/etc/test1"
	filePath2 := "/etc/test2"
	mode := 420
	testDataOld := "data:,old"
	testDataNew := "data:,new"

	machineConfigWorker1 := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: []ign3types.File{
				helpers.CreateUncompressedIgn3File(filePath1, testDataOld, mode),
			},
		},
	}, "aaa", MachineConfigPoolWorker)
	machineConfigWorker2 := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: []ign3types.File{
				helpers.CreateUncompressedIgn3File(filePath1, testDataNew, mode),
			},
		},
	}, "bbb", MachineConfigPoolWorker)

	filePath2Gzipped, err := helpers.CreateGzippedIgn3File(filePath2, testDataOld, mode)
	require.Nil(t, err)
	machineConfigWorker3 := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: []ign3types.File{filePath2Gzipped},
		},
	}, "ddd", MachineConfigPoolWorker)
	machineConfigInfra := helpers.CreateMachineConfigFromIgnitionWithMetadata(ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: []ign3types.File{
				helpers.CreateUncompressedIgn3File(filePath2, testDataNew, mode),
			},
		},
	}, "ccc", "infra")

	inMachineConfigs = []*mcfgv1.MachineConfig{
		machineConfigInfra,
		machineConfigWorker1,
		machineConfigWorker2,
		machineConfigWorker3,
	}

	cconfig = &mcfgv1.ControllerConfig{}
	mergedMachineConfig, err = MergeMachineConfigs(inMachineConfigs, cconfig)
	require.Nil(t, err)

	// The expectation here is that the merged config contains the MCs with name bbb (overrides aaa due to name) and ccc (overrides ddd due to pool)
	expectedMachineConfig = &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: []string{},
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ign3types.Config{
					Ignition: ign3types.Ignition{
						Version: ign3types.MaxVersion.String(),
					},
					Storage: ign3types.Storage{
						Files: []ign3types.File{
							{
								FileEmbedded1: ign3types.FileEmbedded1{
									Contents: ign3types.Resource{
										Source: &testDataNew,
									},
									Mode: &mode,
								},
								Node: ign3types.Node{
									Path:      filePath1,
									Overwrite: boolToPtr(true),
									User: ign3types.NodeUser{
										Name: helpers.StrToPtr("root"),
									},
								},
							},
							{
								FileEmbedded1: ign3types.FileEmbedded1{
									Contents: ign3types.Resource{
										Source: &testDataNew,
									},
									Mode: &mode,
								},
								Node: ign3types.Node{
									Path:      filePath2,
									Overwrite: boolToPtr(true),
									User: ign3types.NodeUser{
										Name: helpers.StrToPtr("root"),
									},
								},
							},
						},
					},
				}),
			},
			FIPS:       false,
			KernelType: KernelTypeDefault,
			Extensions: []string{},
		},
	}
	assert.Equal(t, *mergedMachineConfig, *expectedMachineConfig)

}

func TestRemoveIgnDuplicateFilesAndUnits(t *testing.T) {
	mode := 420
	testDataOld := "data:,old"
	testDataNew := "data:,new"
	testIgn2Config := ign2types.Config{}

	// file test, add a duplicate file and see if the newest one is preserved
	fileOld := ign2types.File{
		Node: ign2types.Node{
			Filesystem: "root", Path: "/etc/testfileconfig",
		},
		FileEmbedded1: ign2types.FileEmbedded1{
			Contents: ign2types.FileContents{
				Source: testDataOld,
			},
			Mode: &mode,
		},
	}
	testIgn2Config.Storage.Files = append(testIgn2Config.Storage.Files, fileOld)

	fileNew := ign2types.File{
		Node: ign2types.Node{
			Filesystem: "root", Path: "/etc/testfileconfig",
		},
		FileEmbedded1: ign2types.FileEmbedded1{
			Contents: ign2types.FileContents{
				Source: testDataNew,
			},
			Mode: &mode,
		},
	}
	testIgn2Config.Storage.Files = append(testIgn2Config.Storage.Files, fileNew)

	// unit test, add three units and three dropins with the same name as follows:
	// unitOne:
	//    contents: old
	//    dropin:
	//        name: one
	//        contents: old
	// unitTwo:
	//    dropin:
	//        name: one
	//        contents: new
	// unitThree:
	//    contents: new
	//    dropin:
	//        name: two
	//        contents: new
	// Which should result in:
	// unitFinal:
	//    contents: new
	//    dropin:
	//      - name: one
	//        contents: new
	//      - name: two
	//        contents: new
	//
	unitName := "testUnit"
	dropinNameOne := "one"
	dropinNameTwo := "two"
	dropinOne := ign2types.SystemdDropin{
		Contents: testDataOld,
		Name:     dropinNameOne,
	}
	dropinTwo := ign2types.SystemdDropin{
		Contents: testDataNew,
		Name:     dropinNameOne,
	}
	dropinThree := ign2types.SystemdDropin{
		Contents: testDataNew,
		Name:     dropinNameTwo,
	}

	unitOne := ign2types.Unit{
		Contents: testDataOld,
		Name:     unitName,
	}
	unitOne.Dropins = append(unitOne.Dropins, dropinOne)
	testIgn2Config.Systemd.Units = append(testIgn2Config.Systemd.Units, unitOne)

	unitTwo := ign2types.Unit{
		Name: unitName,
	}
	unitTwo.Dropins = append(unitTwo.Dropins, dropinTwo)
	testIgn2Config.Systemd.Units = append(testIgn2Config.Systemd.Units, unitTwo)

	unitThree := ign2types.Unit{
		Contents: testDataNew,
		Name:     unitName,
	}
	unitThree.Dropins = append(unitThree.Dropins, dropinThree)
	testIgn2Config.Systemd.Units = append(testIgn2Config.Systemd.Units, unitThree)

	convertedIgn2Config, err := removeIgnDuplicateFilesUnitsUsers(testIgn2Config)
	require.Nil(t, err)

	expectedIgn2Config := ign2types.Config{}
	expectedIgn2Config.Storage.Files = append(expectedIgn2Config.Storage.Files, fileNew)
	unitExpected := ign2types.Unit{
		Contents: testDataNew,
		Name:     unitName,
	}
	unitExpected.Dropins = append(unitExpected.Dropins, dropinThree)
	unitExpected.Dropins = append(unitExpected.Dropins, dropinTwo)
	expectedIgn2Config.Systemd.Units = append(expectedIgn2Config.Systemd.Units, unitExpected)

	assert.Equal(t, expectedIgn2Config, convertedIgn2Config)
}

// TestSetDefaultFileOverwrite ensures that if no default overwrite is provided, MergeMachineConfigs defaults it to true
// Otherwise, the user-provided value is preserved.
func TestSetDefaultFileOverwrite(t *testing.T) {
	// Set up Files entries
	mode := 420
	testfiledata := "data:,test"
	tempFileNoDefault := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig1"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata}, Mode: &mode}}
	tempFileOvewriteTrue := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig1", Overwrite: boolToPtr(true)},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata}, Mode: &mode}}
	tempFileOverwriteFalse := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig2", Overwrite: boolToPtr(false)},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata}, Mode: &mode}}

	// Set up two Ignition configs, one with overwrite: no default, overwrite: false (to be passed to MergeMachineConfigs)
	// and one with a overwrite: true, overwrite: false (the expected output)
	testIgn3ConfigPreMerge := ign3types.Config{}
	testIgn3ConfigPreMerge.Ignition.Version = InternalMCOIgnitionVersion
	testIgn3ConfigPreMerge.Storage.Files = append(testIgn3ConfigPreMerge.Storage.Files, tempFileNoDefault)
	testIgn3ConfigPreMerge.Storage.Files = append(testIgn3ConfigPreMerge.Storage.Files, tempFileOverwriteFalse)

	testIgn3ConfigPostMerge := ign3types.Config{}
	testIgn3ConfigPostMerge.Ignition.Version = InternalMCOIgnitionVersion
	testIgn3ConfigPostMerge.Storage.Files = append(testIgn3ConfigPostMerge.Storage.Files, tempFileOvewriteTrue)
	testIgn3ConfigPostMerge.Storage.Files = append(testIgn3ConfigPostMerge.Storage.Files, tempFileOverwriteFalse)

	// Convert and create the expected pre-merge config
	rawOutIgnPreMerge, err := json.Marshal(testIgn3ConfigPreMerge)
	machineConfigPreMerge := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "overwrite",
			Labels: map[string]string{"machineconfiguration.openshift.io/role": MachineConfigPoolWorker},
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: rawOutIgnPreMerge,
			},
		},
	}
	require.Nil(t, err)

	cconfig := &mcfgv1.ControllerConfig{}
	mergedMachineConfig, err := MergeMachineConfigs([]*mcfgv1.MachineConfig{machineConfigPreMerge}, cconfig)
	require.Nil(t, err)

	// Convert and create the expected post-merge config
	rawOutIgnPostMerge, err := json.Marshal(testIgn3ConfigPostMerge)
	require.Nil(t, err)
	expectedMachineConfig := &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: []string{},
			KernelType:      KernelTypeDefault,
			Extensions:      []string{},
			Config: runtime.RawExtension{
				Raw: rawOutIgnPostMerge,
			},
		},
	}
	assert.Equal(t, *mergedMachineConfig, *expectedMachineConfig)
}

// TestIgnitionMergeCompressed tests https://github.com/coreos/butane/issues/332
func TestIgnitionMergeCompressed(t *testing.T) {
	testIgn3Config := ign3types.Config{}
	testIgn3Config.Ignition.Version = InternalMCOIgnitionVersion
	mode := 420
	testfiledata := "data:;base64,H4sIAAAAAAAAA0vLz+cCAKhlMn4EAAAA"
	compression := "gzip"
	tempFile := ign3types.File{Node: ign3types.Node{Path: "/etc/testfileconfig"},
		FileEmbedded1: ign3types.FileEmbedded1{Contents: ign3types.Resource{Source: &testfiledata, Compression: &compression}, Mode: &mode}}
	testIgn3Config.Storage.Files = append(testIgn3Config.Storage.Files, tempFile)

	testIgn3Config2 := ign3types.Config{}
	testIgn3Config2.Ignition.Version = InternalMCOIgnitionVersion
	testIgn3Config2.Storage.Files = append(testIgn3Config2.Storage.Files, NewIgnFile("/etc/testfileconfig", "hello world"))

	merged := ign3.Merge(testIgn3Config, testIgn3Config2)
	assert.NotNil(t, merged)
	mergedFile := merged.Storage.Files[0]
	contents, err := DecodeIgnitionFileContents(mergedFile.Contents.Source, mergedFile.Contents.Compression)
	require.NoError(t, err)
	assert.Equal(t, string(contents), "hello world")
}

func TestCalculateConfigFileDiffs(t *testing.T) {
	var testIgn3ConfigOld ign3types.Config
	var testIgn3ConfigNew ign3types.Config

	oldTempFile := NewIgnFile("/etc/kubernetes/kubelet-ca.crt", "oldcertificates")
	newTempFile := NewIgnFile("/etc/kubernetes/kubelet-ca.crt", "newcertificates")

	// Make an "old" config with the existing file in it
	testIgn3ConfigOld.Ignition.Version = InternalMCOIgnitionVersion
	testIgn3ConfigOld.Storage.Files = append(testIgn3ConfigOld.Storage.Files, oldTempFile)

	// Make a "new" config with a change to that file
	testIgn3ConfigNew.Ignition.Version = InternalMCOIgnitionVersion
	testIgn3ConfigNew.Storage.Files = append(testIgn3ConfigNew.Storage.Files, newTempFile)

	// If it works, it should notice the file changed
	expectedDiffFileSet := []string{"/etc/kubernetes/kubelet-ca.crt"}
	actualDiffFileSet := CalculateConfigFileDiffs(&testIgn3ConfigOld, &testIgn3ConfigNew)
	unchangedDiffFileset := CalculateConfigFileDiffs(&testIgn3ConfigOld, &testIgn3ConfigOld)

	if !reflect.DeepEqual(expectedDiffFileSet, actualDiffFileSet) {
		t.Errorf("Actual file diff: %s did not match expected: %s", actualDiffFileSet, expectedDiffFileSet)
	}

	if !reflect.DeepEqual(unchangedDiffFileset, []string{}) {
		t.Errorf("File changes detected where there should have been none: %s", unchangedDiffFileset)
	}
}

func TestParseAndConvertGzippedConfig(t *testing.T) {
	testCases := []struct {
		name     string
		ignBytes []byte
	}{
		{
			name:     "Compressed and Encoded Ignition",
			ignBytes: fixtures.CompressedAndEncodedIgnConfig,
		},
		{
			name:     "Compressed Ignition",
			ignBytes: fixtures.CompressedIgnConfig,
		},
		{
			name:     "Uncompressed Ignition",
			ignBytes: fixtures.IgnConfig,
		},
	}

	expectedIgnition := ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
		Storage: ign3types.Storage{
			Files: []ign3types.File{
				helpers.CreateUncompressedIgn3File("/etc/hello-worker", "data:,hello%20world%0A", 420),
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			parsedIgn, err := ParseAndConvertGzippedConfig(testCase.ignBytes)
			assert.Nil(t, err)
			assert.Equal(t, expectedIgnition, parsedIgn)
		})
	}
}

func TestValidateMachineConfigExtensions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		extensions  []string
		errExpected bool
	}{
		{
			name:       "Supported",
			extensions: []string{"sysstat", "sandboxed-containers"},
		},
		{
			name:        "Unsupported",
			extensions:  []string{"unsupported1", "unsupported2"},
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			mcfgSpec := mcfgv1.MachineConfigSpec{
				Extensions: testCase.extensions,
			}

			err := ValidateMachineConfigExtensions(mcfgSpec)

			if testCase.errExpected {
				assert.Error(t, err)
				for _, ext := range testCase.extensions {
					assert.Contains(t, err.Error(), ext)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetPackagesForSupportedExtensions(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		extensions       []string
		expectedPackages []string
		errExpected      bool
	}{
		{
			name:        "Unsupported extension",
			extensions:  []string{"unsupported"},
			errExpected: true,
		},
		{
			name:             "Supported single multi-package extension",
			extensions:       []string{"two-node-ha"},
			expectedPackages: []string{"pacemaker", "pcs", "fence-agents-all"},
		},
		{
			name:             "Supported single package extension",
			extensions:       []string{"wasm"},
			expectedPackages: []string{"crun-wasm"},
		},
		{
			name:             "Supported single multi-package extension",
			extensions:       []string{"kerberos"},
			expectedPackages: []string{"krb5-workstation", "libkadm5"},
		},
		{
			name:             "Supported multiple multi-package extensions",
			extensions:       []string{"ipsec", "kerberos"},
			expectedPackages: []string{"NetworkManager-libreswan", "libreswan", "krb5-workstation", "libkadm5"},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			pkgs, err := GetPackagesForSupportedExtensions(testCase.extensions)
			if testCase.errExpected {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, testCase.expectedPackages, pkgs)
		})
	}
}
