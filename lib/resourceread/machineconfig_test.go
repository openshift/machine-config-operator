package resourceread

import (
	"encoding/json"
	"fmt"
	"testing"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	testMCKind = "MachineConfig"
	testMCName = "test-file"
	testMCVer  = "machineconfiguration.openshift.io/v1"
)

// TestReadMachineConfig test machine configs being read against
// ReadMachineConfigV1() and ReadMachineConfigV1OrDie().
func TestReadMachineConfig(t *testing.T) {
	tcs := []struct {
		desc      string
		wantError bool
		mc        *mcfgv1.MachineConfig
	}{
		{
			desc:      "test valid machine config",
			wantError: false,
			mc: &mcfgv1.MachineConfig{
				metav1.TypeMeta{
					Kind:       testMCKind,
					APIVersion: testMCVer,
				},
				metav1.ObjectMeta{
					Name: testMCName,
				},
				mcfgv1.MachineConfigSpec{},
			},
		},
		{
			desc:      "test nil machine config",
			wantError: true,
		},
		{
			desc:      "test invalid machine config",
			wantError: true,
			mc: &mcfgv1.MachineConfig{
				metav1.TypeMeta{
					Kind:       "invalidMachineConfig",
					APIVersion: "invalidAPIVersion",
				},
				metav1.ObjectMeta{
					Name: "test invalid machine config",
				},
				mcfgv1.MachineConfigSpec{},
			},
		},
	}

	for _, tc := range tcs {
		var b []byte
		if tc.mc != nil {
			jb, err := json.Marshal(tc.mc)
			if err != nil {
				t.Errorf("test %q failed to encode machine config: %v", tc.desc, err)
			} else if jb == nil {
				t.Errorf("test %q failed to get valid json from machine config", tc.desc)
			}
			b = jb
		}

		// Test reading with out panic
		nmc, err := ReadMachineConfigV1(b)
		if tc.wantError && err == nil {
			t.Errorf("test %q failed: expected error, got nil", tc.desc)
		} else if !tc.wantError {
			if nmc == nil {
				t.Errorf("test %q did not return a valid machine config", tc.desc)
			}
			if err != nil {
				t.Errorf("test %q returned unexpected error: %v", tc.desc, err)
			}
		}

		// Now test with panic
		defer func() {
			err, ok := recover().(error)
			if ok {
				fmt.Printf("%v", err)
				if tc.wantError && err == nil {
					t.Errorf("test %q should have pancied on called to ReadMachineConfigV1OrDie", tc.desc)
				}
			}
		}()

		nmc = ReadMachineConfigV1OrDie(b)
		if nmc == nil {
			t.Errorf("test %q did not return a valid machine config", tc.desc)
		}
	}
}
