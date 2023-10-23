package build

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestPoolState(t *testing.T) {
	t.Parallel()

	mcp := helpers.NewMachineConfigPoolBuilder("worker").WithMachineConfig("rendered-worker-1").MachineConfigPool()
	mcp.Spec.Configuration.Source = []corev1.ObjectReference{
		{
			Name: "mc-1",
			Kind: "MachineConfig",
		},
		{
			Name: "mc-2",
			Kind: "MachineConfig",
		},
	}

	assert.NotEqual(t, mcp.Spec.Configuration, mcp.Status.Configuration)

	ps := newPoolState(mcp)

	ps.SetImagePullspec("registry.host.com/org/repo:tag")
	assert.True(t, ps.HasOSImage())
	assert.Equal(t, "registry.host.com/org/repo:tag", ps.GetOSImage())
}

func TestPoolStateBuildRefs(t *testing.T) {
	t.Parallel()

	mcRefs := []corev1.ObjectReference{
		{
			Name: "mc-1",
			Kind: "MachineConfig",
		},
		{
			Name: "mc-2",
			Kind: "MachineConfig",
		},
	}

	mcp := helpers.NewMachineConfigPoolBuilder("worker").WithMachineConfig("rendered-worker-1").MachineConfigPool()
	mcp.Spec.Configuration.Source = append(mcp.Spec.Configuration.Source, mcRefs...)

	assert.NotEqual(t, mcp.Spec.Configuration, mcp.Status.Configuration)

	buildPodRef := corev1.ObjectReference{
		Kind: "Pod",
		Name: "build-pod",
	}

	buildRef := corev1.ObjectReference{
		Kind: "Build",
		Name: "build",
	}

	buildRefTests := []struct {
		buildRef    corev1.ObjectReference
		errExpected bool
	}{
		{
			buildRef: buildPodRef,
		},
		{
			buildRef: buildRef,
		},
		{
			buildRef: corev1.ObjectReference{
				Kind: "MachineConfig",
				Name: "mc-1",
			},
			errExpected: true,
		},
	}

	for _, buildRefTest := range buildRefTests {
		t.Run("BuildRefTest", func(t *testing.T) {
			ps := newPoolState(mcp)
			if buildRefTest.errExpected {
				assert.Error(t, ps.AddBuildObjectRef(buildRefTest.buildRef))
				return
			}

			assert.NoError(t, ps.AddBuildObjectRef(buildRefTest.buildRef), "initial insertion should not error")
			assert.Equal(t, append(mcp.Spec.Configuration.Source, buildRefTest.buildRef), ps.MachineConfigPool().Spec.Configuration.Source)
			assert.Equal(t, append(mcp.Spec.Configuration.Source, buildRefTest.buildRef), ps.MachineConfigPool().Status.Configuration.Source)

			assert.NotEqual(t, mcp.Spec.Configuration.Source, ps.MachineConfigPool().Spec.Configuration.Source, "should not mutate the underlying MCP")
			assert.NotEqual(t, mcp.Status.Configuration.Source, ps.MachineConfigPool().Status.Configuration.Source, "should not mutate the underlying MCP")

			assert.Equal(t, []corev1.ObjectReference{buildRefTest.buildRef}, ps.GetBuildObjectRefs(), "expected build refs to include the inserted ref")

			assert.True(t, ps.HasBuildObjectRef(buildRefTest.buildRef))
			assert.True(t, ps.HasBuildObjectRefName(buildRefTest.buildRef.Name))
			assert.False(t, ps.HasBuildObjectRef(mcRefs[0]), "MachineConfigs should not match build objects")
			assert.False(t, ps.HasBuildObjectRefName(mcRefs[0].Name), "MachineConfigs should not match build objects")

			assert.Error(t, ps.AddBuildObjectRef(buildPodRef), "should not be able to insert more than one build ref")
			assert.Error(t, ps.AddBuildObjectRef(buildRef), "should not be able to insert more than one build ref")
			ps.DeleteObjectRef(buildRefTest.buildRef)
			assert.Equal(t, mcp.Spec.Configuration.Source, ps.pool.Spec.Configuration.Source)

			assert.NoError(t, ps.AddBuildObjectRef(buildRefTest.buildRef))
			ps.DeleteBuildRefByName(buildRefTest.buildRef.Name)
			assert.False(t, ps.HasBuildObjectRef(buildRefTest.buildRef))
			assert.False(t, ps.HasBuildObjectRefName(buildRefTest.buildRef.Name))
			assert.Equal(t, mcp.Spec.Configuration.Source, ps.pool.Spec.Configuration.Source)

			assert.NoError(t, ps.AddBuildObjectRef(buildRefTest.buildRef))
			ps.DeleteAllBuildRefs()
			assert.False(t, ps.HasBuildObjectRef(buildRefTest.buildRef))
			assert.False(t, ps.HasBuildObjectRefName(buildRefTest.buildRef.Name))
			assert.Equal(t, mcp.Spec.Configuration.Source, ps.pool.Spec.Configuration.Source)

			outMCP := ps.MachineConfigPool()
			assert.Equal(t, outMCP.Spec.Configuration, outMCP.Status.Configuration)
		})
	}
}

func TestPoolStateConditions(t *testing.T) {
	t.Parallel()

	mcp := helpers.NewMachineConfigPoolBuilder("worker").WithMachineConfig("rendered-worker-1").MachineConfigPool()
	ps := newPoolState(mcp)

	conditionTests := []struct {
		condType  mcfgv1.MachineConfigPoolConditionType
		checkFunc func() bool
	}{
		{
			condType:  mcfgv1.MachineConfigPoolBuildFailed,
			checkFunc: ps.IsBuildFailure,
		},
		{
			condType:  mcfgv1.MachineConfigPoolBuildPending,
			checkFunc: ps.IsBuildPending,
		},
		{
			condType:  mcfgv1.MachineConfigPoolBuildSuccess,
			checkFunc: ps.IsBuildSuccess,
		},
		{
			condType:  mcfgv1.MachineConfigPoolBuilding,
			checkFunc: ps.IsBuilding,
		},
	}

	for _, condTest := range conditionTests {
		t.Run(string(condTest.condType), func(t *testing.T) {
			ps.SetBuildConditions([]mcfgv1.MachineConfigPoolCondition{
				{
					Status: corev1.ConditionTrue,
					Type:   condTest.condType,
				},
			})

			assert.True(t, condTest.checkFunc())

			ps.SetBuildConditions([]mcfgv1.MachineConfigPoolCondition{
				{
					Status: corev1.ConditionFalse,
					Type:   condTest.condType,
				},
			})

			assert.False(t, condTest.checkFunc())
		})
	}

	ps.ClearAllBuildConditions()
	buildConditionTypes := getMachineConfigPoolBuildConditions()
	for _, condition := range mcp.Status.Conditions {
		for _, conditionType := range buildConditionTypes {
			if conditionType == condition.Type {
				t.Fatalf("expected not to find any build conditions, found: %v", conditionType)
			}
		}
	}
}
