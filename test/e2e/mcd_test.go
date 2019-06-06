package e2e_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	mcv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Test case for https://github.com/openshift/machine-config-operator/issues/358
func TestMCDToken(t *testing.T) {
	cs := framework.NewClientSet("")

	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String(),
	}

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(listOptions)
	if err != nil {
		t.Fatalf("%#v", err)
	}

	for _, pod := range mcdList.Items {
		res, err := cs.Pods(pod.Namespace).GetLogs(pod.Name, &v1.PodLogOptions{}).DoRaw()
		if err != nil {
			t.Errorf("%s", err)
		}
		for _, line := range strings.Split(string(res), "\n") {
			if strings.Contains(line, "Unable to rotate token") {
				t.Fatalf("found token rotation failure message: %s", line)
			}
		}
	}
}

func mcLabelForWorkers() map[string]string {
	mcLabels := make(map[string]string)
	mcLabels["machineconfiguration.openshift.io/role"] = "worker"
	return mcLabels
}

func createIgnFile(path, content, fs string, mode int) igntypes.File {
	return igntypes.File{
		FileEmbedded1: igntypes.FileEmbedded1{
			Contents: igntypes.FileContents{
				Source: content,
			},
			Mode: &mode,
		},
		Node: igntypes.Node{
			Filesystem: fs,
			Path:       path,
		},
	}
}

func createMCToAddFile(name, filename, data, fs string) *mcv1.MachineConfig {
	// create a dummy MC
	mcadd := &mcv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name: fmt.Sprintf("%s-%s", name, uuid.NewUUID()),
		// TODO(runcom): hardcoded to workers for safety
		Labels: mcLabelForWorkers(),
	}

	ignConfig := ctrlcommon.NewIgnConfig()
	ignFile := createIgnFile(filename, "data:,"+data, fs, 420)
	ignConfig.Storage.Files = append(ignConfig.Storage.Files, ignFile)
	mcadd.Spec.Config = ignConfig
	return mcadd
}

// waitForRenderedConfig polls a MachineConfigPool until it has
// included the given mcName in its config, and returns the new
// rendered config name.
func waitForRenderedConfig(t *testing.T, cs *framework.ClientSet, pool, mcName string) (string, error) {
	var renderedConfig string
	startTime := time.Now()
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcp.Spec.Configuration.Source {
			if mc.Name == mcName {
				renderedConfig = mcp.Spec.Configuration.Name
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return "", errors.Wrapf(err, "machine config %s hasn't been picked by pool %s", mcName, pool)
	}
	t.Logf("Pool %s has rendered config %s with %s (waited %v)", pool, mcName, renderedConfig, time.Since(startTime))
	return renderedConfig, nil
}

// waitForPoolComplete polls a pool until it has completed an update to target
func waitForPoolComplete(t *testing.T, cs *framework.ClientSet, pool, target string) error {
	startTime := time.Now()
	if err := wait.Poll(2*time.Second, 10*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcp.Status.Configuration.Name != target {
			return false, nil
		}
		if mcv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcv1.MachineConfigPoolUpdated) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return errors.Wrapf(err, "pool %s didn't report updated to %s", pool, target)
	}
	t.Logf("Pool %s has completed %s (waited %v)", pool, target, time.Since(startTime))
	return nil
}

func TestMCDeployed(t *testing.T) {
	cs := framework.NewClientSet("")
	bumpPoolMaxUnavailableTo(t, cs, 3)

	// TODO: bring this back to 10
	for i := 0; i < 5; i++ {
		startTime := time.Now()
		mcadd := createMCToAddFile("add-a-file", fmt.Sprintf("/etc/mytestconf%d", i), "test", "root")

		// create the dummy MC now
		_, err := cs.MachineConfigs().Create(mcadd)
		if err != nil {
			t.Errorf("failed to create machine config %v", err)
		}

		t.Logf("Created %s", mcadd.Name)
		renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
		if err != nil {
			t.Errorf("%v", err)
		}
		if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
			t.Fatal(err)
		}
		nodes, err := getNodesByRole(cs, "worker")
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range nodes {
			assert.Equal(t, renderedConfig, node.Annotations[constants.CurrentMachineConfigAnnotationKey])
			assert.Equal(t, constants.MachineConfigDaemonStateDone, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey])
		}
		t.Logf("All nodes updated with %s (%s elapsed)", mcadd.Name, time.Since(startTime))
	}
}

func bumpPoolMaxUnavailableTo(t *testing.T, cs *framework.ClientSet, max int) {
	pool, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	require.Nil(t, err)
	old, err := json.Marshal(pool)
	require.Nil(t, err)
	maxUnavailable := intstr.FromInt(max)
	pool.Spec.MaxUnavailable = &maxUnavailable
	new, err := json.Marshal(pool)
	require.Nil(t, err)
	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(old, new, old)
	require.Nil(t, err)
	_, err = cs.MachineConfigPools().Patch("worker", types.MergePatchType, patch)
	require.Nil(t, err)
}

func mcdForNode(cs *framework.ClientSet, node *corev1.Node) (*corev1.Pod, error) {
	// find the MCD pod that has spec.nodeNAME = node.Name and get its name:
	listOptions := metav1.ListOptions{
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": node.Name}).String(),
	}
	listOptions.LabelSelector = labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String()

	mcdList, err := cs.Pods("openshift-machine-config-operator").List(listOptions)
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

func TestUpdateSSH(t *testing.T) {
	cs := framework.NewClientSet("")
	bumpPoolMaxUnavailableTo(t, cs, 3)

	// create a dummy MC with an sshKey for user Core
	mcName := fmt.Sprintf("sshkeys-worker-%s", uuid.NewUUID())
	mcadd := &mcv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name:   mcName,
		Labels: mcLabelForWorkers(),
	}
	// create a new MC that adds a valid user & ssh keys
	tempUser := igntypes.PasswdUser{
		Name: "core",
		SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{
			"1234_test",
			"abc_test",
		},
	}
	ignConfig := ctrlcommon.NewIgnConfig()
	ignConfig.Passwd.Users = append(ignConfig.Passwd.Users, tempUser)
	mcadd.Spec.Config = ignConfig

	_, err := cs.MachineConfigs().Create(mcadd)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}
	t.Logf("Created %s", mcadd.Name)

	// grab the latest worker- MC
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}
	nodes, err := getNodesByRole(cs, "worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		if err != nil {
			t.Fatal(err)
		}
		mcdName := mcd.ObjectMeta.Name

		// now rsh into that daemon and grep the authorized key file to check if 1234_test was written
		// must do both commands in same shell, combine commands into one exec.Command()
		found, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"grep", "1234_test", "/rootfs/home/core/.ssh/authorized_keys").CombinedOutput()
		if err != nil {
			t.Fatalf("unable to read authorized_keys on daemon: %s got: %s got err: %v", mcdName, found, err)
		}
		if !strings.Contains(string(found), "1234_test") {
			t.Fatalf("updated ssh keys not found in authorized_keys, got %s", found)
		}
		t.Logf("Node %s has SSH key", node.Name)
	}
}

func TestKernelArguments(t *testing.T) {
	cs := framework.NewClientSet("")
	bumpPoolMaxUnavailableTo(t, cs, 3)
	kargsMC := &mcv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: mcLabelForWorkers(),
		},
		Spec: mcv1.MachineConfigSpec{
			Config:          ctrlcommon.NewIgnConfig(),
			KernelArguments: []string{"nosmt", "foo=bar"},
		},
	}

	_, err := cs.MachineConfigs().Create(kargsMC)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Created %s", kargsMC.Name)
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", kargsMC.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}
	nodes, err := getNodesByRole(cs, "worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		if err != nil {
			t.Fatal(err)
		}
		mcdName := mcd.ObjectMeta.Name
		kargsBytes, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"cat", "/rootfs/proc/cmdline").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		kargs := string(kargsBytes)
		for _, v := range kargsMC.Spec.KernelArguments {
			if !strings.Contains(kargs, v) {
				t.Fatalf("Missing '%s' in kargs", v)
			}
		}
		t.Logf("Node %s has expected kargs", node.Name)
	}
}

func getNodesByRole(cs *framework.ClientSet, role string) ([]v1.Node, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{fmt.Sprintf("node-role.kubernetes.io/%s", role): ""}).String(),
	}
	nodes, err := cs.Nodes().List(listOptions)
	if err != nil {
		return nil, err
	}
	return nodes.Items, nil
}

func TestPoolDegradedOnFailToRender(t *testing.T) {
	cs := framework.NewClientSet("")

	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test", "")
	mcadd.Spec.Config.Ignition.Version = "" // invalid, won't render

	// create the dummy MC now
	_, err := cs.MachineConfigs().Create(mcadd)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	// verify the pool goes degraded
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcv1.MachineConfigPoolDegraded) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config pool never switched to Degraded on failure to render: %v", err)
	}

	// now delete the bad MC and watch pool flipping back to not degraded
	if err := cs.MachineConfigs().Delete(mcadd.Name, &metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcv1.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcv1.MachineConfigPoolDegraded) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config pool never switched back to Degraded=False: %v", err)
	}
}

func TestReconcileAfterBadMC(t *testing.T) {
	cs := framework.NewClientSet("")
	bumpPoolMaxUnavailableTo(t, cs, 3)

	// create a bad MC w/o a filesystem field which is going to fail reconciling
	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test", "")

	// grab the initial machineconfig used by the worker pool
	// this MC is gonna be the one which is going to be reapplied once the bad MC is deleted
	// and we need it for the final check
	mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	if err != nil {
		t.Error(err)
	}
	workerOldMc := mcp.Status.Configuration.Name

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(mcadd)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
	if err != nil {
		t.Errorf("%v", err)
	}

	// verify that one node picked the above up
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		nodes, err := getNodesByRole(cs, "worker")
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.Annotations[constants.DesiredMachineConfigAnnotationKey] == renderedConfig &&
				node.Annotations[constants.MachineConfigDaemonStateAnnotationKey] != constants.MachineConfigDaemonStateDone {
				// just check that we have the annotation here, w/o strings checking anything that can flip fast causing flakes
				if node.Annotations[constants.MachineConfigDaemonReasonAnnotationKey] != "" {
					return true, nil
				}
			}
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config hasn't been picked by any MCD: %v", err)
	}

	// verify that we got indeed an unavailable machine in the pool
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcv1.MachineConfigPoolDegraded) && mcp.Status.DegradedMachineCount >= 1 {
			return true, nil
		}
		return false, nil
	}); err != nil {
		t.Errorf("worker pool isn't reporting degraded with a bad MC: %v", err)
	}

	// now delete the bad MC and watch the nodes reconciling as expected
	if err := cs.MachineConfigs().Delete(mcadd.Name, &metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	if err := waitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
		t.Fatal(err)
	}

	visited := make(map[string]bool)
	if err := wait.Poll(2*time.Second, 30*time.Minute, func() (bool, error) {
		nodes, err := getNodesByRole(cs, "worker")
		if err != nil {
			return false, err
		}
		mcp, err = cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.Annotations[constants.CurrentMachineConfigAnnotationKey] == workerOldMc &&
				node.Annotations[constants.DesiredMachineConfigAnnotationKey] == workerOldMc &&
				node.Annotations[constants.MachineConfigDaemonStateAnnotationKey] == constants.MachineConfigDaemonStateDone {
				visited[node.Name] = true
				if len(visited) == len(nodes) {
					if mcp.Status.UnavailableMachineCount == 0 && mcp.Status.ReadyMachineCount == int32(len(nodes)) &&
						mcp.Status.UpdatedMachineCount == int32(len(nodes)) {
						return true, nil
					}
				}
				continue
			}
		}
		return false, nil
	}); err != nil {
		t.Errorf("machine config didn't roll back on any worker: %v", err)
	}
}

func TestFIPS(t *testing.T) {
	cs := framework.NewClientSet("")
	bumpPoolMaxUnavailableTo(t, cs, 3)
	fipsMC := &mcv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("fips-%s", uuid.NewUUID()),
			Labels: mcLabelForWorkers(),
		},
		Spec: mcv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
			Fips:   true,
		},
	}

	mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	if err != nil {
		t.Error(err)
	}
	workerOldMc := mcp.Status.Configuration.Name

	_, err = cs.MachineConfigs().Create(fipsMC)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Created %s", fipsMC.Name)
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", fipsMC.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}
	nodes, err := getNodesByRole(cs, "worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		if err != nil {
			t.Fatal(err)
		}
		mcdName := mcd.ObjectMeta.Name
		fipsBytes, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"fips-mode-setup --check").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		fips := string(fipsBytes)
		if !strings.Contains(fips, "FIPS mode is enabled") {
			t.Fatalf("FIPS hasn't been enabled")
		}
		t.Logf("Node %s has expected FIPS mode", node.Name)
	}

	if err := cs.MachineConfigs().Delete(fipsMC.Name, &metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}
	if err := waitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
		t.Fatal(err)
	}

	nodes, err = getNodesByRole(cs, "worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		if err != nil {
			t.Fatal(err)
		}
		mcdName := mcd.ObjectMeta.Name
		fipsBytes, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"fips-mode-setup --check").CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		fips := string(fipsBytes)
		if !strings.Contains(fips, "FIPS mode is disabled") {
			t.Fatalf("FIPS hasn't been disabled")
		}
		t.Logf("Node %s has expected FIPS mode", node.Name)
	}
}
