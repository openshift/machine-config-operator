package e2e_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	ignv2_2types "github.com/coreos/ignition/config/v2_2/types"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/openshift/machine-config-operator/cmd/common"
	mcv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
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

func mcLabelForWorkers() map[string]string {
	mcLabels := make(map[string]string)
	mcLabels["machineconfiguration.openshift.io/role"] = "worker"
	return mcLabels
}

func createIgnFile(path, content string, mode int) ignv2_2types.File {
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

func createMCToAddFile(name, filename, data string) *mcv1.MachineConfig {
	// create a dummy MC
	mcName := fmt.Sprintf("%s-%s", name, uuid.NewUUID())
	mcadd := &mcv1.MachineConfig{}
	mcadd.ObjectMeta = metav1.ObjectMeta{
		Name: mcName,
		// TODO(runcom): hardcoded to workers for safety
		Labels: mcLabelForWorkers(),
	}
	mcadd.Spec = mcv1.MachineConfigSpec{
		Config: ignv2_2types.Config{
			Ignition: ignv2_2types.Ignition{
				Version: "2.2.0",
			},
			Storage: ignv2_2types.Storage{
				Files: []ignv2_2types.File{
					createIgnFile(filename, "data:,"+data, 420),
				},
			},
		},
	}
	return mcadd
}

func TestMCDeployed(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil {
		t.Errorf("%#v", err)
	}
	mcClient := cb.MachineConfigClientOrDie("mc-file-add")
	k := cb.KubeClientOrDie("mc-file-add")

	for i := 0; i < 10; i++ {
		mcadd := createMCToAddFile("add-a-file", fmt.Sprintf("/etc/mytestconf%d", i), "test")

		// create the dummy MC now
		_, err = mcClient.MachineconfigurationV1().MachineConfigs().Create(mcadd)
		if err != nil {
			t.Errorf("failed to create machine config %v", err)
		}

		// grab the latest worker- MC
		var newMCName string
		if err := wait.PollImmediate(2*time.Second, 5*time.Minute, func() (bool, error) {
			mcp, err := mcClient.MachineconfigurationV1().MachineConfigPools().Get("worker", metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			for _, mc := range mcp.Status.Configuration.Source {
				if mc.Name == mcadd.Name {
					newMCName = mcp.Status.Configuration.Name
					return true, nil
				}
			}
			return false, nil
		}); err != nil {
			t.Errorf("machine config hasn't been picked by the pool: %v", err)
		}
		visited := make(map[string]bool)
		if err := wait.Poll(2*time.Second, 5*time.Minute, func() (bool, error) {
			nodes, err := getNodesByRole(k, "worker")
			if err != nil {
				return false, err
			}
			for _, node := range nodes {
				if visited[node.Name] {
					continue
				}
				if node.Annotations[constants.CurrentMachineConfigAnnotationKey] == newMCName {
					visited[node.Name] = true
					if len(visited) == len(nodes) {
						return true, nil
					}
					continue
				}
			}
			return false, nil
		}); err != nil {
			t.Errorf("machine config didn't result in file being on any worker: %v", err)
		}
	}
}

func getNodesByRole(k kubernetes.Interface, role string) ([]v1.Node, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{fmt.Sprintf("node-role.kubernetes.io/%s", role): ""}).String(),
	}
	nodes, err := k.CoreV1().Nodes().List(listOptions)
	if err != nil {
		return nil, err
	}
	return nodes.Items, nil
}
