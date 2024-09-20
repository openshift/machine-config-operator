package fixtures

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	testhelpers "github.com/openshift/machine-config-operator/test/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

type LayeredObjectsForTest struct {
	MachineConfigPool *mcfgv1.MachineConfigPool
	MachineConfigs    []*mcfgv1.MachineConfig
	MachineOSConfig   *mcfgv1alpha1.MachineOSConfig
	MachineOSBuild    *mcfgv1alpha1.MachineOSBuild
}

func (l *LayeredObjectsForTest) ToRuntimeObjects() []runtime.Object {
	out := []runtime.Object{l.MachineConfigPool}

	for _, item := range l.MachineConfigs {
		out = append(out, item)
	}

	return out
}

func NewLayeredObjectsForTest(poolName string) LayeredObjectsForTest {
	renderedConfigName := fmt.Sprintf("rendered-%s-1", poolName)
	layeredBuilder := testhelpers.NewLayeredBuilder(poolName).
		WithDesiredConfig(renderedConfigName)

	layeredBuilder.MachineOSConfigBuilder().
		WithMachineConfigPool(poolName).
		WithBaseImagePullSecret("base-image-pull-secret").
		WithRenderedImagePushSecret("final-image-push-secret").
		WithCurrentImagePullSecret("current-image-pull-secret").
		WithRenderedImagePushspec("registry.hostname.com/org/repo:latest").
		WithCurrentImagePullspec(fmt.Sprintf("registry.hostname.com/org/repo:%s", renderedConfigName))

	childConfigNames := []string{}
	for i := 1; i <= 5; i++ {
		childConfigNames = append(childConfigNames, fmt.Sprintf("%s-config-%d", poolName, i))
	}

	nodeRoleLabel := fmt.Sprintf("node-role.kubernetes.io/%s", poolName)
	nodeSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, nodeRoleLabel, "")

	layeredBuilder.MachineConfigPoolBuilder().
		WithChildConfigs(childConfigNames).
		WithNodeSelector(nodeSelector)

	mcp := layeredBuilder.MachineConfigPool()
	mosc := layeredBuilder.MachineOSConfig()
	mosb := layeredBuilder.MachineOSBuild()
	mosb.Name = fmt.Sprintf("%s-afc35db0f874c9bfdc586e6ba39f1504", poolName)

	mosbLabels, err := labels.ConvertSelectorToLabelsMap(utils.MachineOSBuildSelector(mosc, mcp).String())
	if err != nil {
		panic(err)
	}

	mosb.Labels = mosbLabels

	return LayeredObjectsForTest{
		MachineConfigPool: mcp,
		MachineOSConfig:   mosc,
		MachineOSBuild:    mosb,
		MachineConfigs:    newMachineConfigsFromPool(mcp),
	}
}
