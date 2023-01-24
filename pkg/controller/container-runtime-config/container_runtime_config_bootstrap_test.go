package containerruntimeconfig

import (
	"testing"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAddKubeletCfgAfterBootstrapKubeletCfg(t *testing.T) {
	t.Parallel()

	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		platform := platform
		t.Run(string(platform), func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			f.newController()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			pools := []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			}
			// ctrcfg for bootstrap mode
			ctrcfg := newContainerRuntimeConfig("log-level", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, pools[0])
			f.mccrLister = append(f.mccrLister, ctrcfg)
			f.objects = append(f.objects, ctrcfg)

			mcs, err := RunContainerRuntimeBootstrap("../../../templates", []*mcfgv1.ContainerRuntimeConfig{ctrcfg}, cc, pools)
			require.NoError(t, err)
			require.Len(t, mcs, 1)

			// add ctrcfg1 after bootstrap
			ctrcfg1 := newContainerRuntimeConfig("log-level-master", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			f.mccrLister = append(f.mccrLister, ctrcfg1)
			f.objects = append(f.objects, ctrcfg1)
			c := f.newController()
			err = c.syncHandler(getKey(ctrcfg1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			// resync ctrcfg and check the managedKey
			c = f.newController()
			err = c.syncHandler(getKey(ctrcfg, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			val := ctrcfg.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)
		})
	}
}
