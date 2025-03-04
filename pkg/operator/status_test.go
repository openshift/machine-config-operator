package operator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	corelisterv1 "k8s.io/client-go/listers/core/v1"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/clock"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/uuid"

	apicfgv1 "github.com/openshift/api/config/v1"
	configv1 "github.com/openshift/api/config/v1"
	features "github.com/openshift/api/features"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeconfigclientset "github.com/openshift/client-go/config/clientset/versioned/fake"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	cov1helpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestIsMachineConfigPoolConfigurationValid(t *testing.T) {
	configNotFound := errors.New("Config Not Found")
	type config struct {
		name                 string
		version              string
		releaseVersion       string
		osimageurlOverridden bool
	}

	tests := []struct {
		knownConfigs []config
		source       []string
		testurl      string
		generated    string

		err error
	}{{
		err: errors.New("configuration spec for pool dummy-pool is empty: <unknown>"),
	}, {
		generated: "g",
		err:       errors.New("list of MachineConfigs that were used to generate configuration for pool dummy-pool is empty: <unknown>"),
	}, {
		knownConfigs: nil,
		source:       []string{"c-0", "u-0"},
		testurl:      "myurl",
		generated:    "g",
		err:          configNotFound,
	}, {
		knownConfigs: []config{{
			name: "g",
		}},
		source:    []string{"c-0", "u-0"},
		testurl:   "myurl",
		generated: "g",
		err:       errors.New("g must be created by controller version v2: <unknown>"),
	}, {
		knownConfigs: []config{{
			name:    "g",
			version: "v1",
		}},
		source:    []string{"c-0", "u-0"},
		testurl:   "myurl",
		generated: "g",
		err:       errors.New("controller version mismatch for g expected v2 has v1: <unknown>"),
	}, {
		knownConfigs: []config{{
			name:           "g",
			version:        "v2",
			releaseVersion: "rv2",
		}, {
			name:           "c-0",
			version:        "v1",
			releaseVersion: "rv2",
		}, {
			name: "u-0",
		}},
		source:    []string{"c-0", "u-0"},
		testurl:   "myurl",
		generated: "g",
		err:       errors.New("controller version mismatch for c-0 expected v2 has v1: <unknown>"),
	}, {
		knownConfigs: []config{{
			name:           "g",
			version:        "v2",
			releaseVersion: "rv2",
		}, {
			name:           "c-0",
			version:        "v2",
			releaseVersion: "rv2",
		}, {
			name: "u-0",
		}},
		source:    []string{"c-0", "u-0"},
		testurl:   "myurl",
		generated: "g",
		err:       nil,
	}, {
		knownConfigs: []config{{
			name:           "g",
			version:        "v2",
			releaseVersion: "rv2",
		}, {
			name:           "c-0",
			version:        "v2",
			releaseVersion: "rv2",
		}, {
			name: "u-0",
		}},
		source:    []string{"c-0", "u-0"},
		testurl:   "wrongurl",
		generated: "g",
		err:       errors.New("osImageURL mismatch for dummy-pool in g expected: myurl got: wrongurl"),
	}, {
		// This is specifically testing that we can make sure the operator status check
		// allows a mismatched URL if it's a user override for Phase 0 layering
		knownConfigs: []config{{
			name:                 "g",
			version:              "v2",
			releaseVersion:       "rv2",
			osimageurlOverridden: true,
		}, {
			name:           "c-0",
			version:        "v2",
			releaseVersion: "rv2",
		}, {
			name: "u-0",
		}},
		source:    []string{"c-0", "u-0"},
		testurl:   "overriddenurl",
		generated: "g",
		err:       nil,
	},
		{
			knownConfigs: []config{{
				name:           "g",
				version:        "v2",
				releaseVersion: "rv1",
			}, {
				name:           "c-0",
				version:        "v2",
				releaseVersion: "rv1",
			}, {
				name: "u-0",
			}},
			source:    []string{"c-0", "u-0"},
			testurl:   "myurl",
			generated: "g",
			err:       errors.New("release image version mismatch for dummy-pool in g expected: rv2 got: rv1"),
		}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case #%d", idx), func(t *testing.T) {
			getter := func(name string) (*mcfgv1.MachineConfig, error) {
				for _, c := range test.knownConfigs {
					if c.name == name {
						annos := map[string]string{}
						if c.version != "" {
							annos[ctrlcommon.GeneratedByControllerVersionAnnotationKey] = c.version
						}
						if c.releaseVersion != "" {
							annos[ctrlcommon.ReleaseImageVersionAnnotationKey] = c.releaseVersion
						}
						if c.osimageurlOverridden {
							annos[ctrlcommon.OSImageURLOverriddenKey] = "true"
						}
						return &mcfgv1.MachineConfig{
							ObjectMeta: metav1.ObjectMeta{
								Name:        c.name,
								Annotations: annos,
							},
							Spec: mcfgv1.MachineConfigSpec{OSImageURL: test.testurl},
						}, nil
					}
				}
				return nil, configNotFound
			}

			source := []corev1.ObjectReference{}
			for _, s := range test.source {
				source = append(source, corev1.ObjectReference{Name: s})
			}

			fgAccess := featuregates.NewHardcodedFeatureGateAccess(
				[]apicfgv1.FeatureGateName{
					features.FeatureGateMachineConfigNodes,
					features.FeatureGatePinnedImages,
				},
				[]apicfgv1.FeatureGateName{},
			)
			fg, err := fgAccess.CurrentFeatureGates()
			if err != nil {
				t.Fatal(err)
			}

			err = isMachineConfigPoolConfigurationValid(fg, &mcfgv1.MachineConfigPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "dummy-pool",
				},
				Spec: mcfgv1.MachineConfigPoolSpec{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: test.generated}, Source: source},
				},
				Status: mcfgv1.MachineConfigPoolStatus{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: test.generated}, Source: source},
				},
			}, "v2", "rv2", "myurl", getter)
			if !reflect.DeepEqual(err, test.err) {
				t.Fatalf("expected %v got %v", test.err, err)
			}
		})
	}
}

type mockMCPLister struct {
	pools []*mcfgv1.MachineConfigPool
}

func (mcpl *mockMCPLister) List(selector labels.Selector) (ret []*mcfgv1.MachineConfigPool, err error) {
	return mcpl.pools, nil
}
func (mcpl *mockMCPLister) Get(name string) (ret *mcfgv1.MachineConfigPool, err error) {
	if mcpl.pools == nil {
		return nil, nil
	}
	for _, pool := range mcpl.pools {
		if pool.Name == name {
			return pool, nil
		}

	}
	return nil, nil
}

func TestOperatorSyncStatus(t *testing.T) {
	type syncCase struct {
		syncFuncs          []syncFunc
		expectOperatorFail bool
		cond               []configv1.ClusterOperatorStatusCondition
		nextVersion        string
		inClusterBringUp   bool
	}
	for idx, testCase := range []struct {
		syncs []syncCase
	}{
		// 0. test that Degraded status clears out if the next sync call is successful
		{
			syncs: []syncCase{
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionTrue,
							Reason: "fn1Failed",
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return errors.New("got err") },
						},
					},
					expectOperatorFail: true,
				},
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},

						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return nil },
						},
					},
				},
			},
		},
		// 1. test that progressing is true while bootstrapping and false after the first sync
		{
			syncs: []syncCase{
				{
					inClusterBringUp: true,
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return nil },
						},
					},
				},
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return nil },
						},
					},
				},
			},
		},
		// 2. test that progressing is true while moving forward in versions
		{
			syncs: []syncCase{
				{
					nextVersion: "test-version-2",
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return nil },
						},
					},
				},
			},
		},
		// 3. test that if progressing fails, we report degraded=false because state of the operator
		//    might have changed in the various sync calls
		{
			syncs: []syncCase{
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return nil },
						},
					},
				},
				{
					nextVersion: "test-version-2",
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionTrue,
							Reason: "fn1Failed",
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return errors.New("mock error") },
						},
					},
				},
			},
		},
		// 4. test that if progressing fails during bringup, we still report degraded
		{
			syncs: []syncCase{
				{
					inClusterBringUp: true,
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionTrue,
							Reason: "fn1Failed",
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return errors.New("error") },
						},
					},
				},
				{
					// we're still bringing up and we need to set this in mock
					inClusterBringUp: true,
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionTrue,
							Reason: "fn1Failed",
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return errors.New("error") },
						},
					},
				},
			},
		},
		// 5. test status flipping between not degraded and degraded
		{
			syncs: []syncCase{
				{
					inClusterBringUp: true,
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return nil },
						},
					},
				},
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionTrue,
							Reason: "fn1Failed",
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return errors.New("error") },
						},
					},
				},
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
						{
							Type:   configv1.OperatorDegraded,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorUpgradeable,
							Status: configv1.ConditionTrue,
							Reason: asExpectedReason,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config *renderConfig, co *configv1.ClusterOperator) error { return nil },
						},
					},
				},
			},
		},
	} {
		optr := &Operator{
			eventRecorder: &record.FakeRecorder{},
			fgAccessor: featuregates.NewHardcodedFeatureGateAccess(
				[]configv1.FeatureGateName{features.FeatureGatePinnedImages}, []configv1.FeatureGateName{},
			),
		}
		optr.vStore = newVersionStore()
		optr.mcpLister = &mockMCPLister{
			pools: []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
				helpers.NewMachineConfigPool("workers", nil, helpers.WorkerSelector, "v0"),
			},
		}

		nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		optr.nodeLister = corelisterv1.NewNodeLister(nodeIndexer)
		nodeIndexer.Add(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "first-node", Labels: map[string]string{"node-role/worker": ""}},
			Status: corev1.NodeStatus{
				NodeInfo: corev1.NodeSystemInfo{
					KubeletVersion: "v1.21",
				},
			},
		})

		coName := fmt.Sprintf("test-%s", uuid.NewUUID())
		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: coName}}
		cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse}, clock.RealClock{})
		cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse}, clock.RealClock{})
		cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse}, clock.RealClock{})
		cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorUpgradeable, Status: configv1.ConditionUnknown}, clock.RealClock{})
		co.Status.Versions = append(co.Status.Versions, configv1.OperandVersion{Name: "operator", Version: "test-version"})
		optr.name = coName
		kasOperator := &configv1.ClusterOperator{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver"},
			Status: configv1.ClusterOperatorStatus{
				Versions: []configv1.OperandVersion{
					{Name: "kube-apiserver", Version: "1.21"},
				},
			},
		}

		operatorIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		optr.clusterOperatorLister = configlistersv1.NewClusterOperatorLister(operatorIndexer)
		operatorIndexer.Add(co)
		operatorIndexer.Add(kasOperator)

		configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
		optr.mcoCmLister = corelisterv1.NewConfigMapLister(configMapIndexer)

		for j, sync := range testCase.syncs {
			optr.inClusterBringup = sync.inClusterBringUp
			if sync.nextVersion != "" {
				optr.vStore.Set("operator", sync.nextVersion)
			} else {
				optr.vStore.Set("operator", "test-version")
			}
			optr.configClient = fakeconfigclientset.NewSimpleClientset(co, kasOperator)
			err := optr.syncAll(sync.syncFuncs)
			if sync.expectOperatorFail {
				assert.NotNil(t, err, "test case %d, sync call %d, expected an error", idx, j)
			} else {
				assert.Nil(t, err, "test case %d, expected no error", idx, j)
			}
			o, err := optr.configClient.ConfigV1().ClusterOperators().Get(context.TODO(), coName, metav1.GetOptions{})
			assert.Nil(t, err)
			for _, cond := range sync.cond {
				var condition configv1.ClusterOperatorStatusCondition
				for _, coCondition := range o.Status.Conditions {
					if cond.Type == coCondition.Type {
						condition = coCondition
						break
					}
				}
				assert.Equal(t, cond.Status, condition.Status, "test case %d, sync call %d, expected status for condition %v to be %v, but got %v", idx, j, condition.Type, cond.Status, condition.Status)
				assert.Equal(t, cond.Reason, condition.Reason, "test case %d, sync call %d, expected reason for condition %v to be %v, but got %v", idx, j, condition.Type, cond.Reason, condition.Reason)
			}
		}
	}
}

func TestInClusterBringUpStayOnErr(t *testing.T) {
	optr := &Operator{
		eventRecorder: &record.FakeRecorder{},
		fgAccessor: featuregates.NewHardcodedFeatureGateAccess(
			[]configv1.FeatureGateName{features.FeatureGatePinnedImages}, []configv1.FeatureGateName{},
		),
	}
	optr.vStore = newVersionStore()
	optr.vStore.Set("operator", "test-version")
	optr.mcpLister = &mockMCPLister{
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("workers", nil, helpers.WorkerSelector, "v0"),
		},
	}
	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.nodeLister = corelisterv1.NewNodeLister(nodeIndexer)
	nodeIndexer.Add(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "first-node", Labels: map[string]string{"node-role/worker": ""}},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion: "v1.21",
			},
		},
	})
	co := &configv1.ClusterOperator{}
	kasOperator := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver"},
		Status: configv1.ClusterOperatorStatus{
			Versions: []configv1.OperandVersion{
				{Name: "kube-apiserver", Version: "1.21"},
			},
		},
	}
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse}, clock.RealClock{})
	optr.configClient = fakeconfigclientset.NewSimpleClientset(co, kasOperator)
	optr.inClusterBringup = true

	operatorIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.clusterOperatorLister = configlistersv1.NewClusterOperatorLister(operatorIndexer)
	operatorIndexer.Add(co)
	operatorIndexer.Add(kasOperator)

	configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.mcoCmLister = corelisterv1.NewConfigMapLister(configMapIndexer)

	fn1 := func(config *renderConfig, co *configv1.ClusterOperator) error {
		return errors.New("mocked fn1")
	}
	err := optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.NotNil(t, err, "expected syncAll to fail")

	assert.True(t, optr.inClusterBringup)

	fn1 = func(config *renderConfig, co *configv1.ClusterOperator) error {
		return nil
	}
	err = optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.Nil(t, err, "expected syncAll to pass")

	assert.False(t, optr.inClusterBringup)
}

func TestKubeletSkewUnSupported(t *testing.T) {
	kasOperator := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver"},
		Status: configv1.ClusterOperatorStatus{
			Versions: []configv1.OperandVersion{
				{Name: "kube-apiserver", Version: "1.21"},
			},
		},
	}
	optr := &Operator{
		eventRecorder: &record.FakeRecorder{},
		fgAccessor: featuregates.NewHardcodedFeatureGateAccess(
			[]configv1.FeatureGateName{features.FeatureGatePinnedImages}, []configv1.FeatureGateName{},
		),
	}
	optr.vStore = newVersionStore()
	optr.vStore.Set("operator", "test-version")
	optr.mcpLister = &mockMCPLister{
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("workers", nil, helpers.WorkerSelector, "v0"),
		},
	}
	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.nodeLister = corelisterv1.NewNodeLister(nodeIndexer)
	nodeIndexer.Add(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "first-node", Labels: map[string]string{"node-role/worker": ""}},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion: "v1.18",
			},
		},
	})

	co := &configv1.ClusterOperator{}
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse}, clock.RealClock{})
	fakeClient := fakeconfigclientset.NewSimpleClientset(co, kasOperator)
	optr.configClient = fakeClient
	optr.inClusterBringup = true

	operatorIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.clusterOperatorLister = configlistersv1.NewClusterOperatorLister(operatorIndexer)
	operatorIndexer.Add(co)
	operatorIndexer.Add(kasOperator)

	configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.mcoCmLister = corelisterv1.NewConfigMapLister(configMapIndexer)

	fn1 := func(config *renderConfig, co *configv1.ClusterOperator) error {
		return errors.New("mocked fn1")
	}
	err := optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.NotNil(t, err, "expected syncAll to fail")

	assert.True(t, optr.inClusterBringup)

	fn1 = func(config *renderConfig, co *configv1.ClusterOperator) error {
		return nil
	}
	err = optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.Nil(t, err, "expected syncAll to pass")

	assert.False(t, optr.inClusterBringup)

	var lastUpdate clientgotesting.UpdateAction
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			lastUpdate = action.(clientgotesting.UpdateAction)
		}
	}
	if lastUpdate == nil {
		t.Fatal("missing update")
	}
	operatorStatus := lastUpdate.GetObject().(*configv1.ClusterOperator)
	var upgradeable *configv1.ClusterOperatorStatusCondition
	for _, condition := range operatorStatus.Status.Conditions {
		if condition.Type == configv1.OperatorUpgradeable {
			upgradeable = &condition
			break
		}
	}
	if upgradeable == nil {
		t.Fatal("missing condition")
	}
	if upgradeable.Status != configv1.ConditionTrue {
		t.Fatal(upgradeable)
	}
	if upgradeable.Message != "One or more nodes have an unsupported kubelet version skew. Please see `oc get nodes` for details and upgrade all nodes so that they have a kubelet version of at least 1.19." {
		t.Fatal(upgradeable)
	}
	if upgradeable.Reason != "KubeletSkewUnsupported" {
		t.Fatal(upgradeable)
	}
}

func TestCustomPoolKubeletSkewUnSupported(t *testing.T) {
	customSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", "")
	kasOperator := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver"},
		Status: configv1.ClusterOperatorStatus{
			Versions: []configv1.OperandVersion{
				{Name: "kube-apiserver", Version: "1.21"},
			},
		},
	}
	optr := &Operator{
		eventRecorder: &record.FakeRecorder{},
		fgAccessor: featuregates.NewHardcodedFeatureGateAccess(
			[]configv1.FeatureGateName{features.FeatureGatePinnedImages}, []configv1.FeatureGateName{},
		),
	}
	optr.vStore = newVersionStore()
	optr.vStore.Set("operator", "test-version")
	optr.mcpLister = &mockMCPLister{
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("workers", nil, helpers.WorkerSelector, "v0"),
			helpers.NewMachineConfigPool("custom", nil, customSelector, "v0"),
		},
	}
	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.nodeLister = corelisterv1.NewNodeLister(nodeIndexer)
	nodeIndexer.Add(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "custom", Labels: map[string]string{"node-role/custom": ""}},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion: "v1.18",
			},
		},
	})

	co := &configv1.ClusterOperator{}
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse}, clock.RealClock{})
	fakeClient := fakeconfigclientset.NewSimpleClientset(co, kasOperator)
	optr.configClient = fakeClient
	optr.inClusterBringup = true

	operatorIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.clusterOperatorLister = configlistersv1.NewClusterOperatorLister(operatorIndexer)
	operatorIndexer.Add(co)
	operatorIndexer.Add(kasOperator)

	configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.mcoCmLister = corelisterv1.NewConfigMapLister(configMapIndexer)

	fn1 := func(config *renderConfig, co *configv1.ClusterOperator) error {
		return errors.New("mocked fn1")
	}
	err := optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.NotNil(t, err, "expected syncAll to fail")

	assert.True(t, optr.inClusterBringup)

	fn1 = func(config *renderConfig, co *configv1.ClusterOperator) error {
		return nil
	}
	err = optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.Nil(t, err, "expected syncAll to pass")

	assert.False(t, optr.inClusterBringup)

	var lastUpdate clientgotesting.UpdateAction
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			lastUpdate = action.(clientgotesting.UpdateAction)
		}
	}
	if lastUpdate == nil {
		t.Fatal("missing update")
	}
	operatorStatus := lastUpdate.GetObject().(*configv1.ClusterOperator)
	var upgradeable *configv1.ClusterOperatorStatusCondition
	for _, condition := range operatorStatus.Status.Conditions {
		if condition.Type == configv1.OperatorUpgradeable {
			upgradeable = &condition
			break
		}
	}
	if upgradeable == nil {
		t.Fatal("missing condition")
	}
	if upgradeable.Status != configv1.ConditionTrue {
		t.Fatal(upgradeable)
	}
	if upgradeable.Message != "One or more nodes have an unsupported kubelet version skew. Please see `oc get nodes` for details and upgrade all nodes so that they have a kubelet version of at least 1.19." {
		t.Fatal(upgradeable)
	}
	if upgradeable.Reason != "KubeletSkewUnsupported" {
		t.Fatal(upgradeable)
	}
}

func TestKubeletSkewSupported(t *testing.T) {
	kasOperator := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-apiserver"},
		Status: configv1.ClusterOperatorStatus{
			Versions: []configv1.OperandVersion{
				{Name: "kube-apiserver", Version: "1.21"},
			},
		},
	}
	optr := &Operator{
		eventRecorder: &record.FakeRecorder{},
		fgAccessor: featuregates.NewHardcodedFeatureGateAccess(
			[]configv1.FeatureGateName{features.FeatureGatePinnedImages}, []configv1.FeatureGateName{},
		),
	}
	optr.vStore = newVersionStore()
	optr.vStore.Set("operator", "test-version")
	optr.mcpLister = &mockMCPLister{
		pools: []*mcfgv1.MachineConfigPool{
			helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			helpers.NewMachineConfigPool("workers", nil, helpers.WorkerSelector, "v0"),
		},
	}
	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.nodeLister = corelisterv1.NewNodeLister(nodeIndexer)
	nodeIndexer.Add(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "first-node", Labels: map[string]string{"node-role/worker": ""}},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion: "v1.20",
			},
		},
	})

	co := &configv1.ClusterOperator{}
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse}, clock.RealClock{})
	cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse}, clock.RealClock{})
	fakeClient := fakeconfigclientset.NewSimpleClientset(co, kasOperator)
	optr.configClient = fakeClient
	optr.inClusterBringup = true

	operatorIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.clusterOperatorLister = configlistersv1.NewClusterOperatorLister(operatorIndexer)
	operatorIndexer.Add(co)
	operatorIndexer.Add(kasOperator)

	configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	optr.mcoCmLister = corelisterv1.NewConfigMapLister(configMapIndexer)

	fn1 := func(config *renderConfig, co *configv1.ClusterOperator) error {
		return errors.New("mocked fn1")
	}
	err := optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.NotNil(t, err, "expected syncAll to fail")

	assert.True(t, optr.inClusterBringup)

	fn1 = func(config *renderConfig, co *configv1.ClusterOperator) error {
		return nil
	}
	err = optr.syncAll([]syncFunc{{name: "mock1", fn: fn1}})
	assert.Nil(t, err, "expected syncAll to pass")

	assert.False(t, optr.inClusterBringup)

	var lastUpdate clientgotesting.UpdateAction
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "update" {
			lastUpdate = action.(clientgotesting.UpdateAction)
		}
	}
	if lastUpdate == nil {
		t.Fatal("missing update")
	}
	operatorStatus := lastUpdate.GetObject().(*configv1.ClusterOperator)
	var upgradeable *configv1.ClusterOperatorStatusCondition
	for _, condition := range operatorStatus.Status.Conditions {
		if condition.Type == configv1.OperatorUpgradeable {
			upgradeable = &condition
			break
		}
	}
	if upgradeable == nil {
		t.Fatal("missing condition")
	}
	if upgradeable.Status != configv1.ConditionTrue {
		t.Fatal(upgradeable)
	}
	if upgradeable.Message != "" {
		t.Fatal(upgradeable)
	}
	if upgradeable.Reason != "AsExpected" {
		t.Fatal(upgradeable)
	}
}

func TestGetMinorKubeletVersion(t *testing.T) {
	tcs := []struct {
		version      string
		minor        int
		expectNilErr bool
	}{
		{"v1.20.1", 20, true},
		{"v1.20.1+abc0", 20, true},
		{"v1.20.1+0123", 20, true},
		{"v1.20.1-rc", 20, true},
		{"v1.20.1-rc.1", 20, true},
		{"v1.20.1-rc+abc123", 20, true},
		{"v1.20.1-rc.0+abc123", 20, true},
		{"v1.20.1", 20, true},
		{"1.20.1", 20, true},
		{"1.20", 20, true},
		{"12", 0, false},
		{".xy", 0, false},
		{"1.xy.1", 0, false},
	}
	for _, tc := range tcs {
		minorV, err := getMinorKubeletVersion(tc.version)
		if tc.expectNilErr && err != nil {
			t.Errorf("test %q failed: unexpected error %v", tc.version, err)
			continue
		}
		if !tc.expectNilErr && err == nil {
			t.Errorf("test %q failed: expected error, got nil ", tc.version)
			continue
		}
		if tc.expectNilErr {
			assert.Equal(t, tc.minor, minorV, fmt.Sprintf("failed test %q", tc.version))
		}
	}
}
