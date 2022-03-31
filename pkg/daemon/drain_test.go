package daemon

import (
	"fmt"
	"reflect"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/vincent-petithory/dataurl"
)

func TestIsDrainRequired(t *testing.T) {
	machineConfigs := map[string]*mcfgv1.MachineConfig{
		"mc1": helpers.NewMachineConfig("01-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
  prefix = ""
  location = "example.com/repo/test-img"
  mirror-by-digest-only = true

  [[registry.mirror]]
    location = "mirror.com/repo/test-img"

[[registry]]
  prefix = ""
  location = "example.com/repo1/test-img1"
  mirror-by-digest-only = true

  [[registry.mirror]]
    location = "mirror.com/repo1/test-img1"
`))),
				},
			},
		}}),

		"mc2": helpers.NewMachineConfig("02-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
prefix = ""
location = "example.com/repo/test-img"
mirror-by-digest-only = true

[[registry.mirror]]
	location = "mirror.com/repo/test-img"

[[registry]]
prefix = ""
location = "example.com/repo1/test-img1"
mirror-by-digest-only = true

[[registry.mirror]]
	location = "mirror.com/repo1/test-img1"

[[registry.mirror]]
	location = "mirror1.com/repo1/test-img1"
`))),
				},
			},
		}}),

		"mc3": helpers.NewMachineConfig("03-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
	prefix = ""
	location = "example.com/repo/test-img"
	mirror-by-digest-only = true

	[[registry.mirror]]
	location = "mirror.com/repo/test-img"
`))),
				},
			},
		}}),

		"mc4": helpers.NewMachineConfig("04-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com", "bar.com"]

[[registry]]
	prefix = ""
	location = "example.com/repo/test-img"
	mirror-by-digest-only = true

	[[registry.mirror]]
	location = "mirror.com/repo/test-img"
`))),
				},
			},
		}}),

		"mc5": helpers.NewMachineConfig("05-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
	prefix = ""
	location = "example.com/repo/test-img"
	mirror-by-digest-only = true

	[[registry.mirror]]
	location = "mirror.com/repo/test-img"
`))),
				},
			},
		}}),

		"mc6": helpers.NewMachineConfig("06-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
prefix = ""
location = "example.com/repo/test-img"
mirror-by-digest-only = true

[[registry.mirror]]
location = "mirror.com/repo/test-img"

[[registry]]
prefix = ""
location = "example.com/repo1/test-img1"
mirror-by-digest-only = false
`))),
				},
			},
		}}),

		"mc7": helpers.NewMachineConfig("07-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
prefix = ""
location = "example.com/repo/test-img"
mirror-by-digest-only = true

[[registry]]
prefix = ""
location = "example.com/repo1/test-img1"
mirror-by-digest-only = false

[[registry.mirror]]
location = "mirror.com/repo/test-img"
`))),
				},
			},
		}}),

		"mc8": helpers.NewMachineConfig("08-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
prefix = "bar.com"
location = "example.com/repo/test-img"
`))),
				},
			},
		}}),
		"mc9": helpers.NewMachineConfig("09-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
prefix = "bar.com"
location = "example.com/repo/test-img"
blocked = true
`))),
				},
			},
		}}),
		"mc10": helpers.NewMachineConfig("10-test", nil, "dummy://", []ign3types.File{{
			Node: ign3types.Node{
				Path: "/etc/containers/registries.conf",
			},
			FileEmbedded1: ign3types.FileEmbedded1{
				Contents: ign3types.Resource{
					Source: helpers.StrToPtr(dataurl.EncodeBytes([]byte(`
unqualified-search-registries = ["example.com", "foo.com"]

[[registry]]
prefix = ""
location = "example.com/repo/test-img"
`))),
				},
			},
		}}),
	}

	tests := []struct {
		actions        []string
		oldConfig      *mcfgv1.MachineConfig
		newConfig      *mcfgv1.MachineConfig
		expectedAction bool
	}{
		{
			// skip drain: only None action is present
			actions:        []string{postConfigChangeActionNone},
			oldConfig:      machineConfigs["mc1"],
			newConfig:      machineConfigs["mc1"],
			expectedAction: false,
		},
		{
			// perform drain: reboot action is present
			actions:        []string{postConfigChangeActionNone, postConfigChangeActionReboot},
			oldConfig:      machineConfigs["mc1"],
			newConfig:      machineConfigs["mc1"],
			expectedAction: true,
		},
		// below tests are run when only crio reload action is present
		{
			// skip drain: no changes in registry config
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc1"],
			newConfig:      machineConfigs["mc1"],
			expectedAction: false,
		},
		{
			// skip drain: only new registry added with mirror-by-digest-only set to true
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc3"],
			newConfig:      machineConfigs["mc1"],
			expectedAction: false,
		},
		{
			// perform drain: only new registry added with mirror-by-digest-only set to false
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc5"],
			newConfig:      machineConfigs["mc6"],
			expectedAction: true,
		},
		{
			// perform drain: one or more registry has been removed
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc1"],
			newConfig:      machineConfigs["mc3"],
			expectedAction: true,
		},
		{
			// skip drain: only new mirrors got added to the registry with mirror-by-digest-only set to true for registry
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc1"],
			newConfig:      machineConfigs["mc2"],
			expectedAction: false,
		},
		{
			// perform drain: only new mirrors got added to the registry with mirror-by-digest-only set to false for registry
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc6"],
			newConfig:      machineConfigs["mc7"],
			expectedAction: true,
		},
		{
			// perform drain: one or more mirror has been removed from a registry
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc2"],
			newConfig:      machineConfigs["mc1"],
			expectedAction: true,
		},
		{
			// perform drain: either item from unqualified-search-registries has been removed or ordering has been changed
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc4"],
			newConfig:      machineConfigs["mc3"],
			expectedAction: true,
		},
		{
			// skip drain: only additional unqualified-search-registries has been added
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc3"],
			newConfig:      machineConfigs["mc4"],
			expectedAction: false,
		},
		{
			// perform drain: prefix value of one or more registry has changed
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc8"],
			newConfig:      machineConfigs["mc10"],
			expectedAction: true,
		},
		{
			// perform drain: blocked value of one or more registry has changed
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc8"],
			newConfig:      machineConfigs["mc9"],
			expectedAction: true,
		},
		{
			// perform drain:  mirror-by-digest-only value of one or more registry has changed
			actions:        []string{postConfigChangeActionReloadCrio},
			oldConfig:      machineConfigs["mc1"],
			newConfig:      machineConfigs["mc6"],
			expectedAction: true,
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
			diffFileSet := ctrlcommon.CalculateConfigFileDiffs(&oldIgnConfig, &newIgnConfig)
			drain, err := isDrainRequired(test.actions, diffFileSet, getIgnitionFileDataReadFunc(&oldIgnConfig), getIgnitionFileDataReadFunc(&newIgnConfig))
			if !reflect.DeepEqual(test.expectedAction, drain) {
				t.Errorf("Failed determining drain behavior: expected: %v but result is: %v. Error: %v", test.expectedAction, drain, err)
			}
		})
	}

}
