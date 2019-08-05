package e2e_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	igntypes "github.com/coreos/ignition/config/v2_2/types"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/e2e/framework"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
	require.Nil(t, err)

	for _, pod := range mcdList.Items {
		res, err := cs.Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: "machine-config-daemon",
		}).DoRaw()
		require.Nil(t, err)
		for _, line := range strings.Split(string(res), "\n") {
			if strings.Contains(line, "Unable to rotate token") {
				t.Fatalf("found token rotation failure message: %s", line)
			}
		}
	}
}

func mcLabelForRole(role string) map[string]string {
	mcLabels := make(map[string]string)
	mcLabels["machineconfiguration.openshift.io/role"] = role
	return mcLabels
}

func mcLabelForWorkers() map[string]string {
	return mcLabelForRole("worker")
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

func createMCToAddFileForRole(name, role, filename, data, fs string) *mcfgv1.MachineConfig {
	// create a dummy MC
	mcadd := &mcfgv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name: fmt.Sprintf("%s-%s", name, uuid.NewUUID()),
		// TODO(runcom): hardcoded to workers for safety
		Labels: mcLabelForRole(role),
	}
	ignConfig := ctrlcommon.NewIgnConfig()
	ignFile := createIgnFile(filename, "data:,"+data, fs, 420)
	ignConfig.Storage.Files = append(ignConfig.Storage.Files, ignFile)
	mcadd.Spec.Config = ignConfig
	return mcadd
}

func createMCToAddFile(name, filename, data, fs string) *mcfgv1.MachineConfig {
	return createMCToAddFileForRole(name, "worker", filename, data, fs)
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
	if err := wait.Poll(2*time.Second, 20*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get(pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcp.Status.Configuration.Name != target {
			return false, nil
		}
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolUpdated) {
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
	for i := 0; i < 3; i++ {
		startTime := time.Now()
		mcadd := createMCToAddFile("add-a-file", fmt.Sprintf("/etc/mytestconf%d", i), "test", "root")

		// create the dummy MC now
		_, err := cs.MachineConfigs().Create(mcadd)
		if err != nil {
			t.Errorf("failed to create machine config %v", err)
		}

		t.Logf("Created %s", mcadd.Name)
		renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
		require.Nil(t, err)
		err = waitForPoolComplete(t, cs, "worker", renderedConfig)
		require.Nil(t, err)
		nodes, err := getNodesByRole(cs, "worker")
		require.Nil(t, err)
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
	mcadd := &mcfgv1.MachineConfig{}
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
	require.Nil(t, err, "failed to create MC")
	t.Logf("Created %s", mcadd.Name)

	// grab the latest worker- MC
	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
	require.Nil(t, err)
	err = waitForPoolComplete(t, cs, "worker", renderedConfig)
	require.Nil(t, err)
	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)
	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		require.Nil(t, err)
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
	kargsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("kargs-%s", uuid.NewUUID()),
			Labels: mcLabelForWorkers(),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config:          ctrlcommon.NewIgnConfig(),
			KernelArguments: []string{"nosmt", "foo=bar"},
		},
	}

	_, err := cs.MachineConfigs().Create(kargsMC)
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
		mcd, err := mcdForNode(cs, &node)
		require.Nil(t, err)
		mcdName := mcd.ObjectMeta.Name
		kargsBytes, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"cat", "/rootfs/proc/cmdline").CombinedOutput()
		require.Nil(t, err)
		kargs := string(kargsBytes)
		for _, v := range kargsMC.Spec.KernelArguments {
			if !strings.Contains(kargs, v) {
				t.Fatalf("Missing '%s' in kargs", v)
			}
		}
		t.Logf("Node %s has expected kargs", node.Name)
	}
}

func getNodesByRole(cs *framework.ClientSet, role string) ([]corev1.Node, error) {
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
	require.Nil(t, err, "failed to create machine config")

	// verify the pool goes degraded
	if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolDegraded) {
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
		if mcfgv1.IsMachineConfigPoolConditionFalse(mcp.Status.Conditions, mcfgv1.MachineConfigPoolRenderDegraded) {
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

	// create a MC that contains a valid ignition config but is not reconcilable
	mcadd := createMCToAddFile("add-a-file", "/etc/mytestconfs", "test", "root")
	mcadd.Spec.Config.Networkd = igntypes.Networkd{
		Units: []igntypes.Networkdunit{
			igntypes.Networkdunit{
				Name:     "test.network",
				Contents: "test contents",
			},
		},
	}

	// grab the initial machineconfig used by the worker pool
	// this MC is gonna be the one which is going to be reapplied once the bad MC is deleted
	// and we need it for the final check
	mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	require.Nil(t, err)
	workerOldMc := mcp.Status.Configuration.Name

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(mcadd)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcadd.Name)
	require.Nil(t, err)

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
		if mcfgv1.IsMachineConfigPoolConditionTrue(mcp.Status.Conditions, mcfgv1.MachineConfigPoolNodeDegraded) && mcp.Status.DegradedMachineCount >= 1 {
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

func TestDontDeleteRPMFiles(t *testing.T) {
	cs := framework.NewClientSet("")
	bumpPoolMaxUnavailableTo(t, cs, 3)

	mcHostFile := createMCToAddFile("modify-host-file", "/etc/motd", "mco-test", "root")

	// grab the initial machineconfig used by the worker pool
	// this MC is gonna be the one which is going to be reapplied once the previous MC is deleted
	mcp, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	require.Nil(t, err)
	workerOldMc := mcp.Status.Configuration.Name

	// create the dummy MC now
	_, err = cs.MachineConfigs().Create(mcHostFile)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	renderedConfig, err := waitForRenderedConfig(t, cs, "worker", mcHostFile.Name)
	require.Nil(t, err)

	// wait for the mcp to go back to previous config
	if err := waitForPoolComplete(t, cs, "worker", renderedConfig); err != nil {
		t.Fatal(err)
	}

	// now delete the bad MC and watch the nodes reconciling as expected
	if err := cs.MachineConfigs().Delete(mcHostFile.Name, &metav1.DeleteOptions{}); err != nil {
		t.Error(err)
	}

	// wait for the mcp to go back to previous config
	if err := waitForPoolComplete(t, cs, "worker", workerOldMc); err != nil {
		t.Fatal(err)
	}

	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)

	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], workerOldMc)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		require.Nil(t, err)
		mcdName := mcd.ObjectMeta.Name

		found, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"cat", "/rootfs/etc/motd").CombinedOutput()
		if err != nil {
			t.Fatalf("unable to read test file on daemon: %s got: %s got err: %v", mcdName, found, err)
		}
		if strings.Contains(string(found), "mco-test") {
			t.Fatalf("updated file doesn't contain expected data, got %s", found)
		}
	}
}

func TestFIPS(t *testing.T) {
	cs := framework.NewClientSet("")
	bumpPoolMaxUnavailableTo(t, cs, 3)
	fipsMC := &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("fips-%s", uuid.NewUUID()),
			Labels: mcLabelForWorkers(),
		},
		Spec: mcfgv1.MachineConfigSpec{
			Config: ctrlcommon.NewIgnConfig(),
			FIPS:   true,
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
		require.Nil(t, err)
		mcdName := mcd.ObjectMeta.Name
		fipsBytes, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"chroot", "/rootfs", "fips-mode-setup", "--check").CombinedOutput()
		require.Nil(t, err)
		fips := string(fipsBytes)
		if !strings.Contains(fips, "FIPS mode is enabled") {
			t.Fatalf("FIPS hasn't been enabled on node %s: %s", node.Name, fips)
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
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], workerOldMc)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		require.Nil(t, err)
		mcdName := mcd.ObjectMeta.Name
		fipsBytes, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", mcdName,
			"chroot", "/rootfs", "fips-mode-setup", "--check").CombinedOutput()
		require.Nil(t, err)
		fips := string(fipsBytes)
		if !strings.Contains(fips, "FIPS mode is disabled") {
			t.Fatalf("FIPS hasn't been disabled on node %s: %s", node.Name, fips)
		}
		t.Logf("Node %s has expected FIPS mode", node.Name)
	}
}

func TestCustomPool(t *testing.T) {
	cs := framework.NewClientSet("")

	nodes, err := getNodesByRole(cs, "worker")
	require.Nil(t, err)
	require.NotEmpty(t, nodes)
	infraNode := nodes[0]
	out, err := exec.Command("oc", "label", "node", infraNode.Name, "node-role.kubernetes.io/infra=").CombinedOutput()
	require.Nil(t, err, "unable to label worker node %s with infra: %s", infraNode.Name, string(out))

	infraMCP := &mcfgv1.MachineConfigPool{}
	infraMCP.Name = "infra"
	nodeSelector := metav1.LabelSelector{}
	infraMCP.Spec.NodeSelector = &nodeSelector
	infraMCP.Spec.NodeSelector.MatchLabels = make(map[string]string)
	infraMCP.Spec.NodeSelector.MatchLabels["node-role.kubernetes.io/infra"] = ""
	mcSelector := metav1.LabelSelector{}
	infraMCP.Spec.MachineConfigSelector = &mcSelector
	infraMCP.Spec.MachineConfigSelector.MatchExpressions = []metav1.LabelSelectorRequirement{
		metav1.LabelSelectorRequirement{
			Key:      "machineconfiguration.openshift.io/role",
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{"worker", "infra"},
		},
	}
	_, err = cs.MachineConfigPools().Create(infraMCP)
	require.Nil(t, err)

	infraMC := createMCToAddFileForRole("infra-host-file", "infra", "/etc/mco-custom-pool", "mco-custom-pool", "root")
	_, err = cs.MachineConfigs().Create(infraMC)
	require.Nil(t, err)
	renderedConfig, err := waitForRenderedConfig(t, cs, "infra", infraMC.Name)
	require.Nil(t, err)
	err = waitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)

	nodes, err = getNodesByRole(cs, "infra")
	require.Nil(t, err)
	require.Len(t, nodes, 1)

	for _, node := range nodes {
		assert.Equal(t, node.Annotations[constants.CurrentMachineConfigAnnotationKey], renderedConfig)
		assert.Equal(t, node.Annotations[constants.MachineConfigDaemonStateAnnotationKey], constants.MachineConfigDaemonStateDone)
		mcd, err := mcdForNode(cs, &node)
		require.Nil(t, err)
		mcdName := mcd.ObjectMeta.Name
		out, err := exec.Command("oc", "rsh", "-n", "openshift-machine-config-operator", "-c", "machine-config-daemon", mcdName,
			"cat", "/rootfs/etc/mco-custom-pool").CombinedOutput()
		require.Nil(t, err, "failed to cat test file: %s", string(out))
		if string(out) != "mco-custom-pool" {
			t.Fatalf("Unexpected infra MC content on node %s: %s", node.Name, out)
		}
		t.Logf("Node %s has expected infra MC content", node.Name)
	}

	out, err = exec.Command("oc", "label", "node", infraNode.Name, "node-role.kubernetes.io/infra-").CombinedOutput()
	require.Nil(t, err, "unable to remove infra label from node %s: %s", infraNode.Name, string(out))

	workerMCP, err := cs.MachineConfigPools().Get("worker", metav1.GetOptions{})
	require.Nil(t, err)
	if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := cs.Nodes().Get(infraNode.Name, metav1.GetOptions{})
		require.Nil(t, err)
		if node.Annotations[constants.DesiredMachineConfigAnnotationKey] != workerMCP.Spec.Configuration.Name {
			return false, nil
		}
		return true, nil
	}); err != nil {
		t.Errorf("infra node hasn't moved back to worker config: %v", err)
	}
	err = waitForPoolComplete(t, cs, "infra", renderedConfig)
	require.Nil(t, err)
}
