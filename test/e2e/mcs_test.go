package e2e

import (
	"context"
	"fmt"
	"testing"

	"github.com/coreos/go-semver/semver"
	ign3 "github.com/coreos/ignition/v2/config/v3_2"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	ign3_3 "github.com/coreos/ignition/v2/config/v3_3"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
)

// TestIgn33Kargs ensures that Kernel arguments are available in the Ingition
// payloads when the client requests a 3.3.0 Spec.
func TestIgn33Kargs(t *testing.T) {
	cs := framework.NewClientSet("")

	// Create a Dummy machine config specifying a Kernel Argument.
	mcName := fmt.Sprintf("99-ign3cfg-worker-%s", uuid.NewUUID())
	mcadd := &mcfgv1.MachineConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "machineconfiguration.openshift.io/v1",
			Kind:       "machineconfig",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   mcName,
			Labels: helpers.MCLabelForRole("worker"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			KernelArguments: []string{
				"bootarg1",
				"bootarg2",
			},
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(
					ign3types.Config{
						Ignition: ign3types.Ignition{
							Version: "3.2.0",
						},
						Passwd: ign3types.Passwd{
							Users: []ign3types.PasswdUser{
								{
									Name:              "core",
									SSHAuthorizedKeys: []ign3types.SSHAuthorizedKey{"testKey"},
								},
							},
						},
					}),
			},
		},
	}

	_, err := cs.MachineConfigs().Create(context.TODO(), mcadd, metav1.CreateOptions{})
	require.Nil(t, err, "failed to create MC")
	t.Logf("Created %s", mcadd.Name)

	// grab the latest worker- MC
	renderedConfig, err := helpers.WaitForRenderedConfig(t, cs, "worker", mcadd.Name)
	require.Nil(t, err)
	err = helpers.WaitForPoolComplete(t, cs, "worker", renderedConfig)
	require.Nil(t, err)

	// Request a 3.3.0 from the MCS
	ver33 := semver.New("3.3.0")
	data, err := helpers.GetIgntionFromMCS(cs, t, ver33, "worker")
	require.Nil(t, err)
	ret33, _, err := ign3_3.ParseCompatibleVersion(data)
	require.Nil(t, err)
	require.Equal(t, ret33.Ignition.Version, ver33.String())

	// 3.3.0 should include the Kargs in the Ignition Kernel spec
	for _, mcKarg := range mcadd.Spec.KernelArguments {
		found := false
		for _, ignKarg := range ret33.KernelArguments.ShouldExist {
			if string(ignKarg) == mcKarg {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ignition 3.3.0 is missing expected karg: %s", mcKarg)
		}
	}

	// Request a 3.2.0 from the MCS. It should return a 3.2.0 Ignition.
	ver32 := semver.New("3.2.0")
	data, err = helpers.GetIgntionFromMCS(cs, t, ver32, "worker")
	require.Nil(t, err)
	ret32, _, err := ign3.ParseCompatibleVersion(data)
	require.Nil(t, err)
	require.Equal(t, ret32.Ignition.Version, ver32.String())

}
