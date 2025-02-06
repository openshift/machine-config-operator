package daemon

import (
	"context"
	"testing"

	apicfgv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/machine-config-operator/pkg/upgrademonitor"

	features "github.com/openshift/api/features"
	"github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	informers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeinformers "k8s.io/client-go/informers"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	mcopfake "github.com/openshift/client-go/operator/clientset/versioned/fake"
	"github.com/openshift/machine-config-operator/pkg/helpers"
)

type upgradeMonitorTestCase struct {
	name               string
	err                bool
	parentCondition    *upgrademonitor.Condition
	childCondition     *upgrademonitor.Condition
	parentStatus       metav1.ConditionStatus
	childStatus        metav1.ConditionStatus
	expectedConditions []metav1.Condition
}

func TestUpgradeMonitor(t *testing.T) {
	testCases := []upgradeMonitorTestCase{
		{
			name: "testUpdated",
			err:  false,
			parentCondition: &upgrademonitor.Condition{
				State:   v1alpha1.MachineConfigNodeUpdated,
				Reason:  "Updated",
				Message: "Node Updated",
			},
			childCondition: nil,
			parentStatus:   metav1.ConditionTrue,
			childStatus:    metav1.ConditionFalse,
			expectedConditions: []metav1.Condition{
				{
					Type:               string(v1alpha1.MachineConfigNodeUpdated),
					Message:            "Node Updated",
					Reason:             "Updated",
					LastTransitionTime: metav1.Now(),
					Status:             metav1.ConditionTrue,
				},
			},
		},
		{
			name: "testUpdating",
			err:  false,
			parentCondition: &upgrademonitor.Condition{
				State:   v1alpha1.MachineConfigNodeUpdateExecuted,
				Reason:  "Updating",
				Message: "Node Updating",
			},
			childCondition: &upgrademonitor.Condition{
				State:   v1alpha1.MachineConfigNodeUpdateFilesAndOS,
				Reason:  "FilesAndOS",
				Message: "Applied Files and OS",
			},
			parentStatus: metav1.ConditionUnknown,
			childStatus:  metav1.ConditionTrue,
			expectedConditions: []metav1.Condition{
				{
					Type:               string(v1alpha1.MachineConfigNodeUpdateExecuted),
					Message:            "Node Updating",
					Reason:             "Updating",
					LastTransitionTime: metav1.Now(),
					Status:             metav1.ConditionUnknown,
				},
				{
					Type:               string(v1alpha1.MachineConfigNodeUpdateFilesAndOS),
					Message:            "Applied new Files and OS",
					Reason:             "FilesAndOS",
					LastTransitionTime: metav1.Now(),
					Status:             metav1.ConditionTrue,
				},
			},
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			testCase.run(t)
		})
	}
}

// Runs the test case
func (tc upgradeMonitorTestCase) run(t *testing.T) {
	stopCh := make(chan struct{})
	defer close(stopCh)
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	f.kubeobjects = []runtime.Object{}
	f.client = fake.NewSimpleClientset(f.objects...)
	f.kubeclient = k8sfake.NewSimpleClientset(f.kubeobjects...)

	f.oclient = mcopfake.NewSimpleClientset(f.objects...)
	fgAccess := featuregates.NewHardcodedFeatureGateAccess(
		[]apicfgv1.FeatureGateName{
			features.FeatureGateMachineConfigNodes,
		},
		[]apicfgv1.FeatureGateName{},
	)

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	k8sI := kubeinformers.NewSharedInformerFactory(f.kubeclient, noResyncPeriodFunc())

	d, err := New(nil)
	if err != nil {
		f.t.Fatalf("can't bring up daemon: %v", err)
	}
	d.ClusterConnect("node_name_test",
		f.kubeclient,
		f.client,
		i.Machineconfiguration().V1().MachineConfigs(),
		k8sI.Core().V1().Nodes(),
		i.Machineconfiguration().V1().ControllerConfigs(),
		i.Machineconfiguration().V1().MachineConfigPools(),
		f.oclient,
		false,
		"",
		fgAccess,
	)

	d.mcListerSynced = alwaysReady
	d.nodeListerSynced = alwaysReady
	d.mcpListerSynced = alwaysReady

	i.Start(stopCh)
	i.WaitForCacheSync(stopCh)
	k8sI.Start(stopCh)
	k8sI.WaitForCacheSync(stopCh)

	for _, mc := range f.mcLister {
		i.Machineconfiguration().V1().MachineConfigs().Informer().GetIndexer().Add(mc)
	}

	for _, n := range f.nodeLister {
		k8sI.Core().V1().Nodes().Informer().GetIndexer().Add(n)
	}

	for _, n := range f.nodeLister {
		// TODO: Potentially consolidate down defining of `primaryPool` & `pool`
		primaryPool, err := helpers.GetPrimaryPoolForNode(d.mcpLister, n)
		if err != nil {
			f.t.Fatalf("Error getting primary pool for node: %v", n.Name)
		}
		var pool string = primaryPool.Name

		err = upgrademonitor.GenerateAndApplyMachineConfigNodes(tc.parentCondition, tc.childCondition, tc.parentStatus, tc.childStatus, n, d.mcfgClient, d.featureGatesAccessor, pool)
		if err != nil {
			f.t.Fatalf("Could not generate and apply MCN %v", err)
		}

		mcn, err := d.mcfgClient.MachineconfigurationV1alpha1().MachineConfigNodes().Get(context.TODO(), n.Name, metav1.GetOptions{})
		if err != nil {
			f.t.Fatalf("can't bring up daemon: %v", err)
		}

		for _, expectedCond := range tc.expectedConditions {
			for _, cond := range mcn.Status.Conditions {
				if cond.Type == expectedCond.Type {
					if cond.Status != expectedCond.Status {
						f.t.Fatalf("Conditions do not match %s an %s", string(cond.Status), string(expectedCond.Status))
					}
				}
			}
		}
	}
}
