package operator

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
