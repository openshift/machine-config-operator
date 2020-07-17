package daemon

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"

	"github.com/davecgh/go-spew/spew"
	yaml "github.com/ghodss/yaml"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/diff"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/stretchr/testify/assert"
)

const (
	testDir = "./testdata"
)

// TestCalculateActions attempts to verify the actions needed to apply the
// changes of reconcilable config changes.
func TestCalculateActions(t *testing.T) {

	tests := []struct {
		oldConfig       string
		newConfig       string
		expectedActions []actionResult
	}{{
		oldConfig:       "test-base",
		newConfig:       "test-base",
		expectedActions: []actionResult{},
	}, {
		oldConfig: "test-base",
		newConfig: "test-unit",
		expectedActions: []actionResult{
			RebootPostAction{Reason: "Systemd changed"},
		},
	}, {
		oldConfig: "test-base",
		newConfig: "test-icsp",
		expectedActions: []actionResult{
			ServicePostAction{
				Reason:        "Change to /etc/containers/registry.conf",
				ServiceName:   "crio.service",
				ServiceAction: "restart",
			},
		},
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("test#%d", idx), func(t *testing.T) {
			oldMC := readMachineConfig(t, test.oldConfig)
			newMC := readMachineConfig(t, test.newConfig)
			configDiff, _ := reconcilable(oldMC, newMC)

			actions := calculateActions(oldMC, newMC, configDiff)
			if len(actions) != len(test.expectedActions) {
				t.Fatal(spew.Sdump(actions))
			}
			for actionIdx, action := range actions {
				expected := test.expectedActions[actionIdx]
				if !equality.Semantic.DeepEqual(expected, action) {
					t.Error(diff.ObjectDiff(expected, action))
				}
			}

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
