package daemon

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/clarketm/json"
	"github.com/containers/image/pkg/sysregistriesv2"
	yaml "github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/vincent-petithory/dataurl"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"

	igntypes "github.com/coreos/ignition/v2/config/v3_1/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	"github.com/google/go-cmp/cmp"
)

const (
	testDir = "./testdata"
)

// Test prefix handling, required to facilitate unit testing of
// calculateActions()
func TestStrategyLookup(t *testing.T) {
	aPrefix := "/somewhere/else"
	_, err := lookupStrategy("", "/etc/containers/registries.conf")
	assert.Nil(t, err)

	_, err = lookupStrategy(aPrefix, filepath.Join(aPrefix, "/etc/containers/registries.conf"))
	assert.Nil(t, err)
}

// TestCalculateActions attempts to verify the actions needed to apply the
// changes of reconcilable config changes.
func TestCalculateActions(t *testing.T) {
	tests := []struct {
		name            string
		mConfig         string
		modifyConfig    func(t *testing.T, prefix string, ignCfg igntypes.Config) igntypes.Config
		expectedActions []ActionResult
	}{{
		name:    "no-op",
		mConfig: "test-base",
		modifyConfig: func(t *testing.T, prefix string, ignCfg igntypes.Config) igntypes.Config {
			return ignCfg
		},
		expectedActions: []ActionResult{},
	}, {
		name:    "modified unit",
		mConfig: "test-base",
		modifyConfig: func(t *testing.T, prefix string, ignCfg igntypes.Config) igntypes.Config {
			newValue := false
			ignCfg.Systemd.Units[0].Enabled = &newValue
			return ignCfg
		},
		expectedActions: []ActionResult{
			RebootPostAction{Reason: "Systemd configuration changed"},
		},
	}, {
		mConfig: "test-base",
		name:    "inotify change",
		modifyConfig: func(t *testing.T, prefix string, ignCfg igntypes.Config) igntypes.Config {
			for i := range ignCfg.Storage.Files {
				f := &ignCfg.Storage.Files[i]
				if f.Path == filepath.Join(prefix, "/etc/sysctl.d/inotify.conf") {
					newData := []byte("fs.inotify.max_user_watches = 65530\nfs.inotify.max_user_instances = 8192")
					configdu := dataurl.New(newData, "text/plain")
					configdu.Encoding = dataurl.EncodingASCII
					data := configdu.String()
					f.Contents.Source = &data
				}
			}

			return ignCfg
		},
		expectedActions: []ActionResult{
			RebootPostAction{Reason: "Default strategy for applying changes to \"/etc/sysctl.d/inotify.conf\""},
		},
	}, {
		mConfig: "test-base",
		name:    "icsp url change",
		modifyConfig: func(t *testing.T, prefix string, ignCfg igntypes.Config) igntypes.Config {
			for i := range ignCfg.Storage.Files {
				f := &ignCfg.Storage.Files[i]
				if f.Path == filepath.Join(prefix, "/etc/containers/registries.conf") {
					tomlConf := sysregistriesv2.V2RegistriesConf{
						UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
						Registries: []sysregistriesv2.Registry{
							{
								Endpoint: sysregistriesv2.Endpoint{
									Location: "registry.product.example.org/ocp/4.2-DATE-VERSION",
								},
								MirrorByDigestOnly: true,
								Mirrors: []sysregistriesv2.Endpoint{
									{Location: "registry.mirror.example.com/ocp"},
								},
							},
							{
								Endpoint: sysregistriesv2.Endpoint{
									Location: "registry.product.example.org/ocp/release",
								},
								MirrorByDigestOnly: true,
								Mirrors: []sysregistriesv2.Endpoint{
									{Location: "registry.mirror.example.com/ocp4"},
								},
							},
						},
					}

					data := encodeRegistries(t, tomlConf)
					f.Contents.Source = &data
				}
			}

			return ignCfg
		},
		expectedActions: []ActionResult{
			ServicePostAction{
				Reason:        "Change to /etc/containers/registries.conf",
				ServiceName:   "crio.service",
				ServiceAction: "restart",
			},
		},
	}, {
		mConfig: "test-base",
		name:    "icsp and systemd change",
		modifyConfig: func(t *testing.T, prefix string, ignCfg igntypes.Config) igntypes.Config {
			newValue := false
			ignCfg.Systemd.Units[0].Enabled = &newValue
			for i := range ignCfg.Storage.Files {
				f := &ignCfg.Storage.Files[i]
				if f.Path == filepath.Join(prefix, "/etc/containers/registries.conf") {
					tomlConf := sysregistriesv2.V2RegistriesConf{}

					data := encodeRegistries(t, tomlConf)
					f.Contents.Source = &data
				}

			}

			return ignCfg
		},
		expectedActions: []ActionResult{
			RebootPostAction{Reason: "Systemd configuration changed"},
		},
	}, {
		mConfig: "test-base",
		name:    "icsp and inotify change",
		modifyConfig: func(t *testing.T, prefix string, ignCfg igntypes.Config) igntypes.Config {
			for i := range ignCfg.Storage.Files {
				f := &ignCfg.Storage.Files[i]
				if f.Path == filepath.Join(prefix, "/etc/containers/registries.conf") {
					tomlConf := sysregistriesv2.V2RegistriesConf{
						UnqualifiedSearchRegistries: []string{"registry.access.redhat.com", "docker.io"},
						Registries: []sysregistriesv2.Registry{},
					}

					data := encodeRegistries(t, tomlConf)
					f.Contents.Source = &data

				} else if f.Path == filepath.Join(prefix, "/etc/sysctl.d/inotify.conf") {
					newData := []byte("fs.inotify.max_user_watches = 65530\nfs.inotify.max_user_instances = 8192")
					configdu := dataurl.New(newData, "text/plain")
					configdu.Encoding = dataurl.EncodingASCII
					data := configdu.String()
					f.Contents.Source = &data
				}

			}

			return ignCfg
		},
		expectedActions: []ActionResult{
			RebootPostAction{Reason: "Default strategy for applying changes to \"/etc/sysctl.d/inotify.conf\""},
		},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("test#%d_%s", idx, test.name), func(t *testing.T) {
			tempDir, err := ioutil.TempDir(os.TempDir(), "mco-apply-")
			assert.Nil(t, err)

			oldMC := readMachineConfig(t, test.mConfig)
			fixCfg, err := ctrlcommon.ParseAndConvertConfig(oldMC.Spec.Config.Raw)
			assert.Nil(t, err)

			// Re-write all File paths with a prefix
			//
			// Part of identifying actions that need to be performed
			// involves CheckV3Files which looks at the disk. We
			// don't want to write to the real '/', and relative
			// paths are not legal Ignition entries, so append a
			// prefix to a temporary directory.
			//
			for i := range fixCfg.Storage.Files {
				f := &fixCfg.Storage.Files[i]
				f.Path = filepath.Join(tempDir, f.Path)
			}

			// Cannot use writeFiles as it wants to write to
			// /etc/machine-config-daemon when populating an empty
			// filesystem
			err = populateFiles(fixCfg.Storage.Files)
			assert.Nil(t, err)

			fixedIgnCfg, err := json.Marshal(fixCfg)
			assert.Nil(t, err)

			oldMC.Spec.Config = runtime.RawExtension{
				Raw: fixedIgnCfg,
			}

			newMC := oldMC.DeepCopy()

			ignCfg, err := ctrlcommon.ParseAndConvertConfig(newMC.Spec.Config.Raw)
			assert.Nil(t, err)

			newIgnCfg := test.modifyConfig(t, tempDir, ignCfg)
			rawIgnCfg, err := json.Marshal(newIgnCfg)
			assert.Nil(t, err)

			newMC.Spec.Config = runtime.RawExtension{
				Raw: rawIgnCfg,
			}

			failed := false
			configDiff, _ := reconcilable(oldMC, newMC)
			actions := calculateActions(tempDir, oldMC, newMC, configDiff)
			if len(actions) != len(test.expectedActions) {
				failed = true

			} else if ! cmp.Equal(test.expectedActions, actions) {
				failed = true
			}
			if failed {
				t.Error(diff.ObjectDiff(test.expectedActions, actions))
			} else {
				for _, action := range actions {
					fmt.Printf("Executing action [%v]\n", action.Describe())
				}
			}
			os.Remove(tempDir)
		})
	}

}

func readMachineConfig(t *testing.T, file string) *mcfgv1.MachineConfig {
	mcPath := filepath.Join(testDir, "machine-configs", file+".yaml")
	mcData, err := ioutil.ReadFile(mcPath)
	assert.Nil(t, err)
	mc := new(mcfgv1.MachineConfig)
	err = yaml.Unmarshal([]byte(mcData), mc)
	assert.Nil(t, err)
	return mc
}

func encodeRegistries(t *testing.T, rConf sysregistriesv2.V2RegistriesConf) string {
	var newData bytes.Buffer
	encoder := toml.NewEncoder(&newData)
	err := encoder.Encode(rConf)
	assert.Nil(t, err)

	configdu := dataurl.New(newData.Bytes(), "text/plain")
	configdu.Encoding = dataurl.EncodingASCII
	return configdu.String()
}

func populateFiles(files []igntypes.File) error {
	for _, file := range files {
		contents, err := dataurl.DecodeString(*file.Contents.Source)
		if err != nil {
			return err
		}
		mode := defaultFilePermissions
		if file.Mode != nil {
			mode = os.FileMode(*file.Mode)
		}

		if err := writeFileAtomically(file.Path, contents.Data, defaultDirectoryPermissions, mode, -1, -1); err != nil {
			return err
		}
	}
	return nil
}
