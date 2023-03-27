package e2e_shared_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
)

type MachineConfigTestOpts struct {
	AdditionalTestCases []helpers.MachineConfigTestCase
}

type machineConfigTest struct {
	MachineConfigTestOpts
}

func (m *machineConfigTest) Setup(t *testing.T)    {}
func (m *machineConfigTest) Teardown(t *testing.T) {}

// Runs the common MachineConfig tests.
func (m *machineConfigTest) Run(t *testing.T) {
	cs := framework.NewClientSet("")

	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	if helpers.IsSNO(nodes.Items[0]) {
		t.Logf("Running in SNO mode")
		m.runSNO(t, cs, nodes.Items[0])
	} else {
		t.Logf("Running in HA mode")
		m.runHA(t, cs)
	}
}

// Runs the tests in a single-node context (i.e., runs the test batches
// sequentially against a single node).
func (m *machineConfigTest) runSNO(t *testing.T, cs *framework.ClientSet, node corev1.Node) {
	for i, testBatch := range m.getTestBatches(t, cs) {
		i := i
		testBatch := testBatch
		testName := fmt.Sprintf("Batch-%d", i)
		mcName := fmt.Sprintf("mc-testcase-batch-%d", i)
		t.Run(testName, func(t *testing.T) {
			testBatch.Run(t, cs, node, mcName)
		})
	}
}

// Runs the test batches in a high-availability (HA) context (i.e., shards each
// test batch across all available worker nodes).
func (m *machineConfigTest) runHA(t *testing.T, cs *framework.ClientSet) {
	nl, err := framework.NewNodeLeaser(cs.CoreV1Interface, "worker")
	require.NoError(t, err)

	for i, testBatch := range m.getTestBatches(t, cs) {
		i := i
		testBatch := testBatch
		testName := fmt.Sprintf("Batch-%d", i)
		mcName := fmt.Sprintf("mc-testcase-batch-%d", i)
		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			node, releaseFunc, err := nl.GetNodeWithReleaseFunc(t)
			require.NoError(t, err)
			t.Cleanup(releaseFunc)

			testBatch.Run(t, cs, *node, mcName)
		})
	}
}

// Adds any additional test cases into an additional test case.
func (m *machineConfigTest) getTestBatches(t *testing.T, cs *framework.ClientSet) []helpers.MachineConfigTestCases {
	batches := []helpers.MachineConfigTestCases{
		{
			// This test must be run without the testExtensions test on the same node
			// since the two are mutually exclusive.
			testKernelType(cs),
		},
		{
			testKernelArguments(cs),
			testExtensions(t, cs),
		},
	}

	if len(m.AdditionalTestCases) > 0 {
		addlTestCases := helpers.MachineConfigTestCases{}
		for _, testCase := range m.AdditionalTestCases {
			addlTestCases = append(addlTestCases, testCase)
		}

		batches = append(batches, addlTestCases)
	}

	return batches
}

// Sets up the extensions test case.
func testExtensions(t *testing.T, cs *framework.ClientSet) helpers.MachineConfigTestCase {
	var expectedPackages []string

	isOKD, err := helpers.IsOKDCluster(cs)
	require.NoError(t, err)

	if isOKD {
		// OKD does not support grouped extensions yet, so installing kernel-devel will not also pull in kernel-headers
		// "sandboxed-containers" extension is not available on OKD
		// "kerberos" extension is not available on OKD
		expectedPackages = []string{"usbguard", "kernel-devel"}
	} else {
		expectedPackages = []string{"usbguard", "kernel-devel", "kernel-headers", "kata-containers", "krb5-workstation", "libkadm5"}
	}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("extensions-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			Extensions: []string{"usbguard", "kerberos", "kernel-devel", "sandboxed-containers"},
		},
	}

	applyAssert := func(t *testing.T, node corev1.Node) {
		args := append([]string{"chroot", "/rootfs", "rpm", "-q"}, expectedPackages...)
		installedPackages := helpers.ExecCmdOnNode(t, cs, node, args...)

		for _, v := range expectedPackages {
			if !strings.Contains(installedPackages, v) {
				t.Fatalf("Node %s doesn't have expected extensions", node.Name)
			}
		}

		t.Logf("Node %s has expected extensions installed", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		args := append([]string{"chroot", "/rootfs", "rpm", "-qa"}, expectedPackages...)
		installedPackages := helpers.ExecCmdOnNode(t, cs, node, args...)

		for _, v := range expectedPackages {
			if strings.Contains(installedPackages, v) {
				t.Fatalf("Node %s did not rollback successfully", node.Name)
			}
		}

		t.Logf("Node %s has successfully rolled back", node.Name)
	}

	return helpers.MachineConfigTestCase{
		MC:             mc,
		Name:           "Extensions",
		ApplyAssert:    applyAssert,
		RollbackAssert: rollbackAssert,
	}
}

// Sets up the kernel arguments test case.
func testKernelArguments(cs *framework.ClientSet) helpers.MachineConfigTestCase {
	kernelArgs := []string{"nosmt", "foo=bar", "foo=baz", " baz=test bar=hello world"}

	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelArguments: kernelArgs,
		},
	}

	applyAssert := func(t *testing.T, node corev1.Node) {
		kargs := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
		for _, v := range kernelArgs {
			if !strings.Contains(kargs, v) {
				t.Fatalf("Missing %q in kargs: %q", v, kargs)
			}
		}
		t.Logf("Node %s has expected kargs", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		kargs := helpers.ExecCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
		for _, v := range kernelArgs {
			if strings.Contains(kargs, v) {
				t.Fatalf("Found unexpected kernel arg %q", v)
			}
		}
		t.Logf("Node %s has no unexpected kargs", node.Name)
	}

	return helpers.MachineConfigTestCase{
		Name:           "Kernel Arguments",
		MC:             mc,
		ApplyAssert:    applyAssert,
		RollbackAssert: rollbackAssert,
	}
}

// Sets up the kernel type test case.
func testKernelType(cs *framework.ClientSet) helpers.MachineConfigTestCase {
	mc := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kerneltype-%s", uuid.NewUUID()),
			Labels: helpers.MCLabelForRole("infra"),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelType: "realtime",
		},
	}

	applyAssert := func(t *testing.T, node corev1.Node) {
		kernelInfo := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
		if !strings.Contains(kernelInfo, "kernel-rt-core") {
			t.Fatalf("Node %s doesn't have expected kernel", node.Name)
		}
		t.Logf("Node %s has expected kernel", node.Name)
	}

	rollbackAssert := func(t *testing.T, node corev1.Node) {
		kernelInfo := helpers.ExecCmdOnNode(t, cs, node, "chroot", "/rootfs", "rpm", "-qa", "kernel-rt-core")
		if strings.Contains(kernelInfo, "kernel-rt-core") {
			t.Fatalf("Node %s did not rollback successfully", node.Name)
		}
		t.Logf("Node %s has successfully rolled back", node.Name)
	}

	return helpers.MachineConfigTestCase{
		MC:             mc,
		Name:           "Kernel Type",
		ApplyAssert:    applyAssert,
		RollbackAssert: rollbackAssert,
		SkipOnOKD:      true,
	}
}
