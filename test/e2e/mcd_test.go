package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/openshift/machine-config-operator/cmd/common"
	mcv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
)

// Test case for https://github.com/openshift/machine-config-operator/issues/358
func TestMCDToken(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil {
		t.Errorf("%#v", err)
	}
	k := cb.KubeClientOrDie("mcd-token-test")

	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String(),
	}

	mcdList, err := k.CoreV1().Pods("openshift-machine-config-operator").List(listOptions)
	if err != nil {
		t.Fatalf("%#v", err)
	}

	for _, pod := range mcdList.Items {
		res, err := k.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &v1.PodLogOptions{}).DoRaw()
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

func mcRoleLabelForWorkers() map[string]string {
	return mcRoleLabelFor("worker")
}

func mcRoleLabelFor(role string) map[string]string {
	mcLabels := make(map[string]string)
	mcLabels["machineconfiguration.openshift.io/role"] = role
	return mcLabels
}

func createMCFile(path, content string, mode int) ignv2_2types.File {
	return ignv2_2types.File{
		FileEmbedded1: ignv2_2types.FileEmbedded1{
			Contents: ignv2_2types.FileContents{
				Source: content,
			},
			Mode: &mode,
		},
		Node: ignv2_2types.Node{
			Filesystem: "root",
			Path:       path,
		},
	}
}

func createMC(name, role string, files []ignv2_2types.File) *mcv1.MachineConfig {
	mc := &mcv1.MachineConfig{}
	mc.ObjectMeta = metav1.ObjectMeta{
		Name:   name,
		Labels: mcRoleLabelFor(role),
	}
	mc.Spec = mcv1.MachineConfigSpec{
		Config: ignv2_2types.Config{
			Ignition: ignv2_2types.Ignition{
				Version: "2.2.0",
			},
			Storage: ignv2_2types.Storage{
				Files: files,
			},
		},
	}
	return mc
}

func getGeneratedMCFromMCName(mcClient mcfgclientset.Interface, mcName, role string) (string, error) {
	var newMCName string
	err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		mcp, err := mcClient.MachineconfigurationV1().MachineConfigPools().Get(role, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcp.Status.Configuration.Source {
			if mc.Name == mcName {
				newMCName = mcp.Status.Configuration.Name
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return "", err
	}
	return newMCName, nil
}

func waitForMCDeployedOnNodes(kubeClient kubernetes.Interface, mcName string, nodeCount int) error {
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"k8s-app": "machine-config-daemon"}).String(),
	}

	var seen int
	err := wait.Poll(3*time.Second, 5*time.Minute, func() (bool, error) {
		// TODO(runcom): we need to select mcd for just a given role for the nodeCount to be really true
		mcdList, err := kubeClient.CoreV1().Pods("openshift-machine-config-operator").List(listOptions)
		if err != nil {
			return false, err
		}

		for _, pod := range mcdList.Items {
			res, err := kubeClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &v1.PodLogOptions{}).DoRaw()
			if err != nil {
				// do not error out, we may be rebooting, that's why we list at every iteration
				return false, nil
			}
			for _, line := range strings.Split(string(res), "\n") {
				if strings.Contains(line, "completed update for config "+mcName) {
					if seen == nodeCount {
						return true, nil
					}
					seen++
					return false, nil
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("machine config didn't result in file being on any node: %v, rolled on just %d out of %d", err, seen, nodeCount)
	}
	return nil
}

func waitForMCDeployed(kubeClient kubernetes.Interface, mcName string) error {
	return waitForMCDeployedOnNodes(kubeClient, mcName, 1)
}

func TestMCDeployed(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil {
		t.Errorf("%#v", err)
	}
	mcClient := cb.MachineConfigClientOrDie("mc-file-add")
	k := cb.KubeClientOrDie("mc-file-add")

	mcName := fmt.Sprintf("00-0add-a-file-%s", uuid.NewUUID())
	role := "worker"
	mcadd := createMC(mcName, role, []ignv2_2types.File{createMCFile("/etc/mytestconf", "data:,test", 420)})

	// create the dummy MC now
	_, err = mcClient.MachineconfigurationV1().MachineConfigs().Create(mcadd)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	newMCName, err := getGeneratedMCFromMCName(mcClient, mcName, role)
	if err != nil {
		t.Error(err)
	}

	err = waitForMCDeployed(k, newMCName)
	if err != nil {
		t.Errorf("error waiting for the new MC to be deployed %v", err)
	}
}

// sshWithCommand execs ssh to the specified ip, run the command provided
// and returns the combined output and an error
func sshWithCommand(t *testing.T, ip string, command []string) (string, string, error) {
	sshKeyPath := os.Getenv("KUBE_SSH_KEY_PATH")
	sshIdentityOpt := ""
	if sshKeyPath != "" {
		sshIdentityOpt = "-i" + sshKeyPath
	}
	sshOpts := []string{
		"-oUserKnownHostsFile=/dev/null",
		"-oStrictHostKeyChecking=no",
	}
	if sshIdentityOpt != "" {
		sshOpts = append(sshOpts, sshIdentityOpt)
	}
	sshOpts = append(sshOpts, "core@"+ip)
	sshOpts = append(sshOpts, command...)

	t.Logf(`running "ssh %s"`, strings.Join(sshOpts, " "))
	cmd := exec.Command("ssh", sshOpts...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("error ssh'ing into node %q: %v, %v", ip, err, stderr.String())
	}
	return stdout.String(), stderr.String(), nil
}

// XXX: this function now just returns the bastion IP and name till we work something out
//      like https://github.com/kubernetes/kubernetes/blob/master/test/e2e/framework/ssh.go
func pickNodeNameAndIPWithExternalIP(kubeClient kubernetes.Interface) (string, string, error) {
	nodes, err := kubeClient.CoreV1().Nodes().List(metav1.ListOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to list nodes %v", err)
	}
	var (
		nodeName string
		nodeIP   string
	)
	bastionIP := strings.TrimRight(os.Getenv("KUBE_SSH_BASTION"), ":22")
	fmt.Printf("bastion ip %q\n", bastionIP)
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Address == bastionIP {
				nodeIP = bastionIP
				nodeName = node.Name
				break
			}
			// just pick a master with an external IP
			if addr.Type == v1.NodeExternalIP {
				nodeIP = addr.Address
				nodeName = node.Name
				// we don't break here cause we still prefer to have the bastion
				// if it's there
			}

		}
	}
	return nodeName, nodeIP, nil
}

func TestSSHAccessedAnnotation(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil {
		t.Errorf("%#v", err)
	}
	k := cb.KubeClientOrDie("test-ssh-accessed")

	nodeName, nodeIP, err := pickNodeNameAndIPWithExternalIP(k)
	if err != nil {
		t.Errorf("failed to pick a node %v", err)
	}

	node, err := k.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
	if err != nil {
		t.Errorf("cannot get node %q: %v", nodeName, err)
	}
	sshAnnotation, ok := node.ObjectMeta.Annotations[daemon.MachineConfigDaemonSSHAccessAnnotationKey]
	if ok && sshAnnotation == daemon.MachineConfigDaemonSSHAccessValue {
		t.Errorf("node %q has ssh/accessed annotation but it shouldn't", nodeName)
	}

	_, _, err = sshWithCommand(t, nodeIP, []string{"true"})
	if err != nil {
		t.Error(err)
	}
	defer clearOutAnnotationFromNode(t, k, nodeName, daemon.MachineConfigDaemonSSHAccessAnnotationKey)

	err = wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
		node, err := k.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		sshAnnotation, ok := node.ObjectMeta.Annotations[daemon.MachineConfigDaemonSSHAccessAnnotationKey]
		if !ok {
			return false, nil
		}
		if sshAnnotation == daemon.MachineConfigDaemonSSHAccessValue {
			return true, nil
		}
		return false, nil
	})
}

func clearOutAnnotationFromNode(t *testing.T, k kubernetes.Interface, nodeName, key string) {
	node, err := k.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
	if err != nil {
		t.Error(err)
	}
	oldData, err := json.Marshal(node)
	if err != nil {
		t.Error(err)
	}
	delete(node.ObjectMeta.Annotations, daemon.MachineConfigDaemonSSHAccessAnnotationKey)
	newData, err := json.Marshal(node)
	if err != nil {
		t.Error(err)
	}
	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, v1.Node{})
	if err != nil {
		t.Error(err)
	}
	_, err = k.CoreV1().Nodes().Patch(node.Name, types.StrategicMergePatchType, patchBytes)
	if err != nil {
		t.Error(err)
	}
}

// Test case for https://github.com/openshift/machine-config-operator/issues/372
func TestMCDeployedNoSSHAccessedAfterReboot(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil {
		t.Errorf("%#v", err)
	}
	mcClient := cb.MachineConfigClientOrDie("no-ssh-reboot")
	k := cb.KubeClientOrDie("no-ssh-reboot")

	mcName := fmt.Sprintf("00-0add-a-file-%s", uuid.NewUUID())
	role := "worker"
	mcadd := createMC(mcName, role, []ignv2_2types.File{createMCFile("/etc/mytestconf", "data:,test", 420)})

	// create the dummy MC now
	_, err = mcClient.MachineconfigurationV1().MachineConfigs().Create(mcadd)
	if err != nil {
		t.Errorf("failed to create machine config %v", err)
	}

	newMCName, err := getGeneratedMCFromMCName(mcClient, mcName, role)
	if err != nil {
		t.Error(err)
	}

	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"node-role.kubernetes.io/worker": ""}).String(),
	}
	nodes, err := k.CoreV1().Nodes().List(listOptions)
	if err != nil {
		t.Errorf("failed to list nodes %v", err)
	}

	err = waitForMCDeployedOnNodes(k, newMCName, len(nodes.Items))
	if err != nil {
		t.Errorf("error waiting for the new MC to be deployed %v", err)
	}

	for _, node := range nodes.Items {
		sshAnnotation, ok := node.ObjectMeta.Annotations[daemon.MachineConfigDaemonSSHAccessAnnotationKey]
		if ok && sshAnnotation == daemon.MachineConfigDaemonSSHAccessValue {
			t.Errorf("node %q has the ssh/annotation but it shouldn't", node.Name)
		}
	}
}

// Test case for https://github.com/openshift/machine-config-operator/pull/375
func TestSSHAccessedOnDegraded(t *testing.T) {
	// TODO(runcom): we can't really degraded nodes right now, hold on and find a way
	// to degrade just a test pool or something like that
}
