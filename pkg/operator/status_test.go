package operator

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	cov1helpers "github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestIsMachineConfigPoolConfigurationValid(t *testing.T) {
	configNotFound := errors.New("Config Not Found")
	type config struct {
		name    string
		version string
	}

	tests := []struct {
		knownConfigs []config
		source       []string
		generated    string

		err error
	}{{
		err: errors.New("configuration for pool dummy-pool is empty"),
	}, {
		generated: "g",
		err:       errors.New("list of MachineConfigs that were used to generate configuration for pool dummy-pool is empty"),
	}, {
		knownConfigs: nil,
		source:       []string{"c-0", "u-0"},
		generated:    "g",
		err:          configNotFound,
	}, {
		knownConfigs: []config{{
			name: "g",
		}},
		source:    []string{"c-0", "u-0"},
		generated: "g",
		err:       errors.New("g must be created by controller version v2"),
	}, {
		knownConfigs: []config{{
			name:    "g",
			version: "v1",
		}},
		source:    []string{"c-0", "u-0"},
		generated: "g",
		err:       errors.New("controller version mismatch for g expected v2 has v1"),
	}, {
		knownConfigs: []config{{
			name:    "g",
			version: "v2",
		}, {
			name:    "c-0",
			version: "v1",
		}, {
			name: "u-0",
		}},
		source:    []string{"c-0", "u-0"},
		generated: "g",
		err:       errors.New("controller version mismatch for c-0 expected v2 has v1"),
	}, {
		knownConfigs: []config{{
			name:    "g",
			version: "v2",
		}, {
			name:    "c-0",
			version: "v2",
		}, {
			name: "u-0",
		}},
		source:    []string{"c-0", "u-0"},
		generated: "g",
		err:       nil,
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
						return &mcfgv1.MachineConfig{
							ObjectMeta: metav1.ObjectMeta{
								Name:        c.name,
								Annotations: annos,
							},
						}, nil
					}
				}
				return nil, configNotFound
			}

			source := []corev1.ObjectReference{}
			for _, s := range test.source {
				source = append(source, corev1.ObjectReference{Name: s})
			}

			err := isMachineConfigPoolConfigurationValid(&mcfgv1.MachineConfigPool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "dummy-pool",
				},
				Status: mcfgv1.MachineConfigPoolStatus{
					Configuration: mcfgv1.MachineConfigPoolStatusConfiguration{ObjectReference: corev1.ObjectReference{Name: test.generated}, Source: source},
				},
			}, "v2", getter)
			if !reflect.DeepEqual(err, test.err) {
				t.Fatalf("expected %v got %v", test.err, err)
			}
		})
	}
}

func TestIsControllerConfigCompleted(t *testing.T) {
	tests := []struct {
		obsrvdGen int64
		completed bool
		running   bool
		failing   bool

		err error
	}{{
		obsrvdGen: 0,
		err:       errors.New("status for ControllerConfig dummy is being reported for 0, expecting it for 1"),
	}, {
		obsrvdGen: 1,
		running:   true,
		err:       errors.New("ControllerConfig has not completed: as completed(false) running(true) failing(false)"),
	}, {
		obsrvdGen: 1,
		completed: true,
	}, {
		obsrvdGen: 1,
		completed: true,
		running:   true,
		err:       errors.New("ControllerConfig has not completed: as completed(true) running(true) failing(false)"),
	}, {
		obsrvdGen: 1,
		failing:   true,
		err:       errors.New("ControllerConfig has not completed: as completed(false) running(false) failing(true)"),
	}}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("#%d", idx), func(t *testing.T) {
			getter := func(name string) (*mcfgv1.ControllerConfig, error) {
				var conds []mcfgv1.ControllerConfigStatusCondition
				if test.completed {
					conds = append(conds, *mcfgv1.NewControllerConfigStatusCondition(mcfgv1.TemplateContollerCompleted, corev1.ConditionTrue, "", ""))
				}
				if test.running {
					conds = append(conds, *mcfgv1.NewControllerConfigStatusCondition(mcfgv1.TemplateContollerRunning, corev1.ConditionTrue, "", ""))
				}
				if test.failing {
					conds = append(conds, *mcfgv1.NewControllerConfigStatusCondition(mcfgv1.TemplateContollerFailing, corev1.ConditionTrue, "", ""))
				}
				return &mcfgv1.ControllerConfig{
					ObjectMeta: metav1.ObjectMeta{Generation: 1, Name: name},
					Status: mcfgv1.ControllerConfigStatus{
						ObservedGeneration: test.obsrvdGen,
						Conditions:         conds,
					},
				}, nil
			}

			err := isControllerConfigCompleted(&mcfgv1.ControllerConfig{ObjectMeta: metav1.ObjectMeta{Generation: 1, Name: "dummy"}}, getter)
			if !reflect.DeepEqual(err, test.err) {
				t.Fatalf("expected %v got %v", test.err, err)
			}
		})
	}
}

type mockMCPLister struct{}

func (mcpl *mockMCPLister) List(selector labels.Selector) (ret []*mcfgv1.MachineConfigPool, err error) {
	return nil, nil
}
func (mcpl *mockMCPLister) Get(name string) (ret *mcfgv1.MachineConfigPool, err error) {
	return nil, nil
}

type mockClusterOperatorsClient struct {
	co *configv1.ClusterOperator
}

func (coc *mockClusterOperatorsClient) Create(co *configv1.ClusterOperator) (*configv1.ClusterOperator, error) {
	return coc.co, nil
}
func (coc *mockClusterOperatorsClient) UpdateStatus(co *configv1.ClusterOperator) (*configv1.ClusterOperator, error) {
	return coc.co, nil
}
func (coc *mockClusterOperatorsClient) Get(name string, options metav1.GetOptions) (*configv1.ClusterOperator, error) {
	return coc.co, nil
}

func TestOperatorSyncStatus(t *testing.T) {
	type syncCase struct {
		syncFuncs          []syncFunc
		expectOperatorFail bool
		cond               []configv1.ClusterOperatorStatusCondition
		operatorVersions   []configv1.OperandVersion
		inClusterBringUp   bool
	}
	for idx, testCase := range []struct {
		syncs []syncCase
	}{
		// 0. test that Failing status clears out if the next sync call is successful
		{
			syncs: []syncCase{
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return errors.New("got err") },
						},
					},
					expectOperatorFail: true,
				},
				{
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
						},

						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return nil },
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
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return nil },
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
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return nil },
						},
					},
				},
			},
		},
		// 2. test that progressing is true while moving forward in versions
		{
			syncs: []syncCase{
				{
					operatorVersions: []configv1.OperandVersion{
						{
							Name:    "operator",
							Version: "test-version-2",
						},
					},
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return nil },
						},
					},
				},
			},
		},
		// 3. test that if progressing fails, we report available=false because state of the operator
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
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return nil },
						},
					},
				},
				{
					operatorVersions: []configv1.OperandVersion{
						{
							Name:    "operator",
							Version: "test-version-2",
						},
					},
					cond: []configv1.ClusterOperatorStatusCondition{
						{
							Type:   configv1.OperatorProgressing,
							Status: configv1.ConditionTrue,
						},
						{
							Type:   configv1.OperatorAvailable,
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionTrue,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return errors.New("mock error") },
						},
					},
				},
			},
		},
		// 4. test that if progressing fails during bringup, we still report failing and not available
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
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionTrue,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return errors.New("error") },
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
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionTrue,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return errors.New("error") },
						},
					},
				},
			},
		},
		// 5. test status flipping between available and failing
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
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return nil },
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
							Status: configv1.ConditionFalse,
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionTrue,
						},
					},
					expectOperatorFail: true,
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return errors.New("error") },
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
						},
						{
							Type:   configv1.OperatorFailing,
							Status: configv1.ConditionFalse,
						},
					},
					syncFuncs: []syncFunc{
						{
							name: "fn1",
							fn:   func(config renderConfig) error { return nil },
						},
					},
				},
			},
		},
	} {
		optr := &Operator{}
		optr.vStore = newVersionStore()
		optr.vStore.Set("operator", fmt.Sprintf("test-version"))
		optr.mcpLister = &mockMCPLister{}
		co := &configv1.ClusterOperator{}
		cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse})
		cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse})
		cov1helpers.SetStatusCondition(&co.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorFailing, Status: configv1.ConditionFalse})
		optr.configClient = &mockClusterOperatorsClient{co: co}

		for j, sync := range testCase.syncs {
			optr.inClusterBringup = sync.inClusterBringUp
			if sync.operatorVersions == nil {
				co.Status.Versions = []configv1.OperandVersion{
					{
						Name:    "operator",
						Version: "test-version",
					},
				}
			} else {
				co.Status.Versions = sync.operatorVersions
			}
			err := optr.syncAll(renderConfig{}, sync.syncFuncs)
			if sync.expectOperatorFail {
				assert.NotNil(t, err, "test case %d, sync call %d, expected an error", idx, j)
			} else {
				assert.Nil(t, err, "test case %d, expected no error", idx, j)
			}
			for _, cond := range sync.cond {
				var condition configv1.ClusterOperatorStatusCondition
				for _, coCondition := range co.Status.Conditions {
					if cond.Type == coCondition.Type {
						condition = coCondition
						break
					}
				}
				assert.Equal(t, cond.Status, condition.Status, "test case %d, sync call %d, expected condition %v to be %v, but got %v", idx, j, condition.Type, cond.Status, condition.Status)
			}
		}
	}
}
