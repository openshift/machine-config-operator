package e2e_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestKernelArguments(t *testing.T) {
	cs := framework.NewClientSet("")

	// Get initial MachineConfig used by the worker pool so that we can rollback to it later on
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	require.Nil(t, err)
	workerOldMC := mcp.Status.Configuration.Name

	kargsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: mcLabelForWorkers(),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: runtime.RawExtension{
				Raw: helpers.MarshalOrDie(ctrlcommon.NewIgnConfig()),
			},
			KernelArguments: []string{"nosmt", "foo=bar", " baz=test bar=\"hello world\""},
		},
	}

	_, err = cs.MachineConfigs().Create(context.TODO(), kargsMC, metav1.CreateOptions{})
	require.Nil(t, err)
	t.Logf("Created %s", kargsMC.Name)
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", kargsMC.Name)
	require.Nil(t, err)
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}
	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		kargs := execCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
		expectedKernelArgs := []string{"nosmt", "foo=bar", "baz=test", "\"bar=hello world\""}
		for _, v := range expectedKernelArgs {
			if !strings.Contains(kargs, v) {
				t.Fatalf("Missing %q in kargs: %q", v, kargs)
			}
		}
		t.Logf("Node %s has expected kargs", node.Name)
	}

	// Delete the applied kargs MachineConfig to make sure rollback works fine
	if err := cs.MachineConfigs().Delete(context.TODO(), kargsMC.Name, metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	t.Logf("Deleted MachineConfig %s", kargsMC.Name)

	// Wait for the mcp to rollback to previous config
	if err := waitForPoolComplete(t, cs, "worker", workerOldMC); err != nil {
		t.Fatalf("Pool did not complete: %v", err)
	}

	// Re-fetch the worker nodes for updated annotations
	nodes, err = getNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], workerOldMC)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		kargs := execCmdOnNode(t, cs, node, "cat", "/rootfs/proc/cmdline")
		expectedKernelArgs := []string{"nosmt", "foo=bar", "baz=test", "\"bar=hello world\""}
		for _, v := range expectedKernelArgs {
			if strings.Contains(kargs, v) {
				t.Fatalf("Unwanted %q in kargs: %q", v, kargs)
			}
		}
		t.Logf("Node %s don't have kargs", node.Name)
	}
}

func mcLabelForRole(role string) map[string]string {
	mcLabels := make(map[string]string)
	mcLabels[mcfgv1.MachineConfigRoleLabelKey] = role
	return mcLabels
}

func mcLabelForWorkers() map[string]string {
	return mcLabelForRole("worker")
}

func mcdForNode(cs *framework.ClientSet, node *corev1.Node) (*corev1.Pod, error) {
	// find the MCD pod that has spec.nodeNAME = node.Name and get its name:
	listOptions := metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}).String(),
	}
	listOptions.LabelSelector = labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String()

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(context.TODO(), listOptions)
	if err != nil {
		return nil, err
	}
	if len(mcdList.Items) != 1 {
		if len(mcdList.Items) == 0 {
			return nil, fmt.Errorf("Failed to find MCD for node %s", node.Name)
		}
		return nil, fmt.Errorf("Too many (%d) MCDs for node %s", len(mcdList.Items), node.Name)
	}
	return &mcdList.Items[0], nil
}
