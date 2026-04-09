package internalreleaseimage

import (
	"strings"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAggregateReleaseStatus_AllNodesAvailable(t *testing.T) {
	releaseName := "ocp-release-bundle-4.22.0-x86_64"

	// Create 3 master MCNs, all with Available=True for the release
	mcns := []*mcfgv1.MachineConfigNode{
		createMCNWithReleaseStatus("master-0", "master", releaseName, map[string]bool{
			"Available": true,
			"Degraded":  false,
		}),
		createMCNWithReleaseStatus("master-1", "master", releaseName, map[string]bool{
			"Available": true,
			"Degraded":  false,
		}),
		createMCNWithReleaseStatus("master-2", "master", releaseName, map[string]bool{
			"Available": true,
			"Degraded":  false,
		}),
	}

	// Aggregate
	result := aggregateReleaseStatus(releaseName, mcns, nil)

	// Verify
	if result.Name != releaseName {
		t.Errorf("Expected name %s, got %s", releaseName, result.Name)
	}

	// Check Available condition
	availableCond := findCondition(result.Conditions, "Available")
	if availableCond == nil {
		t.Fatal("Available condition not found")
	}
	if availableCond.Status != metav1.ConditionTrue {
		t.Errorf("Expected Available=True, got %s", availableCond.Status)
	}
	expectedMsg := "Release ocp-release-bundle-4.22.0-x86_64 is available on all 3 nodes"
	if availableCond.Message != expectedMsg {
		t.Errorf("Expected message '%s', got '%s'", expectedMsg, availableCond.Message)
	}

	// Check Degraded condition
	degradedCond := findCondition(result.Conditions, "Degraded")
	if degradedCond == nil {
		t.Fatal("Degraded condition not found")
	}
	if degradedCond.Status != metav1.ConditionFalse {
		t.Errorf("Expected Degraded=False, got %s", degradedCond.Status)
	}
}

func TestAggregateReleaseStatus_PartiallyAvailable(t *testing.T) {
	releaseName := "ocp-release-bundle-4.22.0-x86_64"

	// Create 3 master MCNs, only 2 available
	mcns := []*mcfgv1.MachineConfigNode{
		createMCNWithReleaseStatus("master-0", "master", releaseName, map[string]bool{
			"Available": true,
			"Degraded":  false,
		}),
		createMCNWithReleaseStatus("master-1", "master", releaseName, map[string]bool{
			"Available": true,
			"Degraded":  false,
		}),
		createMCNWithReleaseStatus("master-2", "master", releaseName, map[string]bool{
			"Available":  false,
			"Installing": true,
		}),
	}

	result := aggregateReleaseStatus(releaseName, mcns, nil)

	// Check Available=False
	availableCond := findCondition(result.Conditions, "Available")
	if availableCond.Status != metav1.ConditionFalse {
		t.Errorf("Expected Available=False, got %s", availableCond.Status)
	}
	expectedMsg := "Release ocp-release-bundle-4.22.0-x86_64 is available on 2/3 nodes"
	if availableCond.Message != expectedMsg {
		t.Errorf("Expected message '%s', got '%s'", expectedMsg, availableCond.Message)
	}

	// Check Installing condition exists
	installingCond := findCondition(result.Conditions, "Installing")
	if installingCond == nil {
		t.Fatal("Installing condition not found")
	}
	if installingCond.Status != metav1.ConditionTrue {
		t.Errorf("Expected Installing=True, got %s", installingCond.Status)
	}
}

func TestAggregateReleaseStatus_OneDegraded(t *testing.T) {
	releaseName := "ocp-release-bundle-4.22.0-x86_64"

	mcns := []*mcfgv1.MachineConfigNode{
		createMCNWithReleaseStatus("master-0", "master", releaseName, map[string]bool{
			"Available": true,
		}),
		createMCNWithReleaseStatus("master-1", "master", releaseName, map[string]bool{
			"Available": true,
		}),
		createMCNWithReleaseStatus("master-2", "master", releaseName, map[string]bool{
			"Degraded": true,
		}),
	}

	result := aggregateReleaseStatus(releaseName, mcns, nil)

	// Check Degraded=True
	degradedCond := findCondition(result.Conditions, "Degraded")
	if degradedCond.Status != metav1.ConditionTrue {
		t.Errorf("Expected Degraded=True, got %s", degradedCond.Status)
	}

	// Message should list the degraded node
	if !strings.Contains(degradedCond.Message, "master-2") {
		t.Errorf("Expected message to contain 'master-2', got '%s'", degradedCond.Message)
	}
}

func TestFilterMasterMCNs(t *testing.T) {
	mcns := []*mcfgv1.MachineConfigNode{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "master-0"},
			Spec:       mcfgv1.MachineConfigNodeSpec{Pool: mcfgv1.MCOObjectReference{Name: "master"}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-0"},
			Spec:       mcfgv1.MachineConfigNodeSpec{Pool: mcfgv1.MCOObjectReference{Name: "worker"}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "master-1"},
			Spec:       mcfgv1.MachineConfigNodeSpec{Pool: mcfgv1.MCOObjectReference{Name: "master"}},
		},
	}

	result := filterMasterMCNs(mcns)

	if len(result) != 2 {
		t.Errorf("Expected 2 master nodes, got %d", len(result))
	}

	for _, mcn := range result {
		if mcn.Spec.Pool.Name != "master" {
			t.Errorf("Expected master pool, got %s", mcn.Spec.Pool.Name)
		}
	}
}

func TestFindReleaseInMCN(t *testing.T) {
	releaseName := "ocp-release-bundle-4.22.0-x86_64"
	mcn := createMCNWithReleaseStatus("master-0", "master", releaseName, map[string]bool{
		"Available": true,
	})

	// Should find the release
	found := findReleaseInMCN(mcn, releaseName)
	if found == nil {
		t.Fatal("Expected to find release, got nil")
	}
	if found.Name != releaseName {
		t.Errorf("Expected name %s, got %s", releaseName, found.Name)
	}

	// Should not find non-existent release
	notFound := findReleaseInMCN(mcn, "ocp-release-bundle-4.99.0-x86_64")
	if notFound != nil {
		t.Error("Expected nil for non-existent release")
	}
}

// Helper functions

func createMCNWithReleaseStatus(name, pool, releaseName string, conditionStatuses map[string]bool) *mcfgv1.MachineConfigNode {
	conditions := []metav1.Condition{}

	for condType, status := range conditionStatuses {
		condStatus := metav1.ConditionFalse
		if status {
			condStatus = metav1.ConditionTrue
		}
		conditions = append(conditions, metav1.Condition{
			Type:   condType,
			Status: condStatus,
		})
	}

	return &mcfgv1.MachineConfigNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: mcfgv1.MachineConfigNodeSpec{
			Pool: mcfgv1.MCOObjectReference{
				Name: pool,
			},
		},
		Status: mcfgv1.MachineConfigNodeStatus{
			InternalReleaseImage: mcfgv1.MachineConfigNodeStatusInternalReleaseImage{
				Releases: []mcfgv1.MachineConfigNodeStatusInternalReleaseImageRef{
					{
						Name:       releaseName,
						Image:      "quay.io/openshift-release-dev/ocp-release@sha256:abc123",
						Conditions: conditions,
					},
				},
			},
		},
	}
}

func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i, cond := range conditions {
		if cond.Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

