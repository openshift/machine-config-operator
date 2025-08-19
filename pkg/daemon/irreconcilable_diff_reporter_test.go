package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakeclientmachineconfigv1 "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestGenerateReport(t *testing.T) {
	tests := []struct {
		name           string
		baseMC         mcfgv1.MachineConfig
		targetMC       mcfgv1.MachineConfig
		expectedDiffs  []mcfgv1.IrreconcilableChangeDiff
		expectedError  string
		createBaseFile bool
	}{
		{
			name: "disk device change",
			baseMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}]
							}
						}`),
					},
				},
			},
			targetMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sdb", "wipeTable": true}]
							}
						}`),
					},
				},
			},
			expectedDiffs: []mcfgv1.IrreconcilableChangeDiff{
				{
					FieldPath: "spec.config.storage.disks[0].device",
					Diff:      "\"/dev/sdb\"",
				},
			},
			expectedError:  "",
			createBaseFile: true,
		},
		{
			name: "no differences - identical configs",
			baseMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}]
							}
						}`),
					},
				},
			},
			targetMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}]
							}
						}`),
					},
				},
			},
			expectedDiffs:  nil,
			expectedError:  "",
			createBaseFile: true,
		},
		{
			name: "multiple changes",
			baseMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}],
								"filesystems": [{"device": "/dev/sda1", "format": "ext4"}]
							}
						}`),
					},
				},
			},
			targetMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sdb", "wipeTable": false}],
								"filesystems": [{"device": "/dev/sdb1", "format": "xfs"}]
							}
						}`),
					},
				},
			},
			expectedDiffs: []mcfgv1.IrreconcilableChangeDiff{
				{
					FieldPath: "spec.config.storage.disks[0].device",
					Diff:      "\"/dev/sdb\"",
				},
				{
					FieldPath: "spec.config.storage.disks[0].wipeTable",
					Diff:      "false",
				},
				{
					FieldPath: "spec.config.storage.filesystems[0].device",
					Diff:      "\"/dev/sdb1\"",
				},
				{
					FieldPath: "spec.config.storage.filesystems[0].format",
					Diff:      "\"xfs\"",
				},
			},
			expectedError:  "",
			createBaseFile: true,
		},
		{
			name: "systemd and files changes - should be ignored",
			baseMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}],
								"files": [{
									"path": "/etc/example.conf",
									"contents": {"source": "data:,original"}
								}]
							},
							"systemd": {
								"units": [{
									"name": "example.service",
									"enabled": true,
									"contents": "[Unit]\nDescription=Example\n"
								}]
							}
						}`),
					},
				},
			},
			targetMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}],
								"files": [{
									"path": "/etc/example.conf",
									"contents": {"source": "data:,modified"}
								}, {
									"path": "/etc/newfile.conf",
									"contents": {"source": "data:,new"}
								}]
							},
							"systemd": {
								"units": [{
									"name": "example.service",
									"enabled": false,
									"contents": "[Unit]\nDescription=Modified Example\n"
								}, {
									"name": "newservice.service",
									"enabled": true,
									"contents": "[Unit]\nDescription=New Service\n"
								}]
							}
						}`),
					},
				},
			},
			expectedDiffs:  nil,
			expectedError:  "",
			createBaseFile: true,
		},
		{
			name: "base config file doesn't exist - should error",
			baseMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}]
							}
						}`),
					},
				},
			},
			targetMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sdb", "wipeTable": true}]
							}
						}`),
					},
				},
			},
			expectedDiffs:  nil,
			expectedError:  "cannot locate and read the install-time configuration",
			createBaseFile: false,
		},
		{
			name: "invalid JSON in target config - should error",
			baseMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`{
							"ignition": {"version": "3.4.0"},
							"storage": {
								"disks": [{"device": "/dev/sda", "wipeTable": true}]
							}
						}`),
					},
				},
			},
			targetMC: mcfgv1.MachineConfig{
				Spec: mcfgv1.MachineConfigSpec{
					Config: runtime.RawExtension{
						Raw: []byte(`invalid json content`),
					},
				},
			},
			expectedDiffs:  nil,
			expectedError:  "failed to get the target configuration JsonNode",
			createBaseFile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp directory and base config path
			tempDir := t.TempDir()
			baseConfigPath := filepath.Join(tempDir, "base-config.json")

			// Only create the base file if specified
			if tt.createBaseFile {
				baseData, err := json.Marshal(tt.baseMC)
				require.NoError(t, err)
				err = os.WriteFile(baseConfigPath, baseData, 0644)
				require.NoError(t, err)
			}

			// Generate the report
			report, err := newIrreconcilableDifferencesReportGeneratorImpl(
				[]string{baseConfigPath},
			).GenerateReport(&tt.targetMC)

			// Check error expectation
			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				return
			}
			require.NoError(t, err)

			// Check if no differences expected
			if tt.expectedDiffs == nil {
				assert.Nil(t, report)
				return
			}

			// Verify expected differences
			require.Len(t, report, len(tt.expectedDiffs))

			for i, expectedDiff := range tt.expectedDiffs {
				assert.Equal(t, expectedDiff.FieldPath, report[i].FieldPath)
				assert.Contains(t, report[i].Diff, expectedDiff.Diff)
			}
		})
	}
}

type stubDiffGenerator struct {
	diffs []mcfgv1.IrreconcilableChangeDiff
}

func (s *stubDiffGenerator) GenerateReport(_ *mcfgv1.MachineConfig) ([]mcfgv1.IrreconcilableChangeDiff, error) {
	return s.diffs, nil
}

func TestCheckReportIrreconcilableDifferencesEmptyDiff(t *testing.T) {
	nodeName := "test-node"
	fakeClient := fakeclientmachineconfigv1.NewSimpleClientset(
		[]runtime.Object{
			&mcfgv1.MachineConfigNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Status:     mcfgv1.MachineConfigNodeStatus{},
			},
		}...,
	)

	err := newIrreconcilableReporter(fakeClient, &stubDiffGenerator{}).
		CheckReportIrreconcilableDifferences(
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "test-config"},
			},
			nodeName,
		)

	require.NoError(t, err)

	updatedMCN, err := fakeClient.MachineconfigurationV1().
		MachineConfigNodes().
		Get(context.TODO(), nodeName, metav1.GetOptions{})

	require.NoError(t, err)
	assert.Nil(t, updatedMCN.Status.IrreconcilableChanges)
}

func TestCheckReportIrreconcilableDifferencesWithDiffs(t *testing.T) {
	nodeName := "test-node"
	fakeClient := fakeclientmachineconfigv1.NewSimpleClientset(
		[]runtime.Object{
			&mcfgv1.MachineConfigNode{
				ObjectMeta: metav1.ObjectMeta{Name: nodeName},
				Status:     mcfgv1.MachineConfigNodeStatus{},
			},
		}...,
	)

	err := newIrreconcilableReporter(
		fakeClient,
		&stubDiffGenerator{
			[]mcfgv1.IrreconcilableChangeDiff{
				{
					FieldPath: "storage.disks[0].device",
					Diff:      "- /dev/sda\n+ /dev/sdb",
				},
			},
		},
	).
		CheckReportIrreconcilableDifferences(
			&mcfgv1.MachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "test-config"},
			},
			nodeName,
		)

	require.NoError(t, err)
	updatedMCN, err := fakeClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, updatedMCN.Status.IrreconcilableChanges)
	assert.Len(t, updatedMCN.Status.IrreconcilableChanges, 1)
	assert.Equal(t, "storage.disks[0].device", updatedMCN.Status.IrreconcilableChanges[0].FieldPath)
	assert.Equal(t, "- /dev/sda\n+ /dev/sdb", updatedMCN.Status.IrreconcilableChanges[0].Diff)
}
