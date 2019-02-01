package e2e_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/machine-config-operator/cmd/common"
)

func TestOSImageURL(t *testing.T) {
	cb, err := common.NewClientBuilder("")
	if err != nil {
		t.Fatalf("%#v", err)
	}
	mcClient := cb.MachineConfigClientOrDie("mc-file-add")

	// grab the latest worker- MC
	mcp, err := mcClient.MachineconfigurationV1().MachineConfigPools().Get("worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("%#v", err)
	}

	mc, err := mcClient.MachineconfigurationV1().MachineConfigs().Get(mcp.Status.Configuration.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("%#v", err)
	}

	if mc.Spec.OSImageURL == "" {
		t.Fatalf("Empty OSImageURL for %s", mc.Name)
	}

	// grab the latest master- MC
	mcp, err = mcClient.MachineconfigurationV1().MachineConfigPools().Get("master", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("%#v", err)
	}
	mc, err = mcClient.MachineconfigurationV1().MachineConfigs().Get(mcp.Status.Configuration.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("%#v", err)
	}

	if mc.Spec.OSImageURL == "" {
		t.Fatalf("Empty OSImageURL for %s", mc.Name)
	}
}
