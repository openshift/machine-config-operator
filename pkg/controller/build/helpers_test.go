package build

import (
	"context"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/fixtures"
	commonconsts "github.com/openshift/machine-config-operator/pkg/controller/common/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateOnClusterBuildConfig(t *testing.T) {
	t.Parallel()

	newMosc := func() *mcfgv1alpha1.MachineOSConfig {
		lobj := fixtures.NewObjectsForTest("worker")
		return lobj.MachineOSConfig
	}

	testCases := []struct {
		name            string
		errExpected     bool
		secretsToDelete []string
		mosc            func() *mcfgv1alpha1.MachineOSConfig
	}{
		{
			name: "happy path",
			mosc: newMosc,
		},
		{
			name:            "missing secret",
			secretsToDelete: []string{"current-image-pull-secret"},
			mosc:            newMosc,
			errExpected:     true,
		},
		{
			name: "missing MachineOSConfig",
			mosc: func() *mcfgv1alpha1.MachineOSConfig {
				mosc := newMosc()
				mosc.Name = "other-machineosconfig"
				mosc.Spec.MachineConfigPool.Name = "other-machineconfigpool"
				return mosc
			},
			errExpected: true,
		},
		{
			name: "malformed image pullspec",
			mosc: func() *mcfgv1alpha1.MachineOSConfig {
				mosc := newMosc()
				mosc.Spec.BuildInputs.RenderedImagePushspec = "malformed-image-pullspec"
				return mosc
			},
			errExpected: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			kubeclient, mcfgclient, lobj, _ := fixtures.GetClientsForTest(t)

			_, err := mcfgclient.MachineconfigurationV1alpha1().MachineOSConfigs().Create(context.TODO(), testCase.mosc(), metav1.CreateOptions{})
			require.NoError(t, err)

			for _, secret := range testCase.secretsToDelete {
				err := kubeclient.CoreV1().Secrets(commonconsts.MCONamespace).Delete(context.TODO(), secret, metav1.DeleteOptions{})
				require.NoError(t, err)
			}

			err = ValidateOnClusterBuildConfig(kubeclient, mcfgclient, []*mcfgv1.MachineConfigPool{lobj.MachineConfigPool})
			if testCase.errExpected {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
