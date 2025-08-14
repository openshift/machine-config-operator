package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/josephburnett/jd/v2"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// irreconcilableDifferencesReportGenerator generates the report of mcfgv1.IrreconcilableChangeDiff given
// the target mcfgv1.MachineConfig.
// This entity does not perform the reporting and limits itself to the generation of the diff.
type irreconcilableDifferencesReportGenerator interface {
	GenerateReport(targetMachineConfig *mcfgv1.MachineConfig) ([]mcfgv1.IrreconcilableChangeDiff, error)
}

// irreconcilableDifferencesReportGeneratorImpl generates slices of mcfgv1.IrreconcilableChangeDiff with the
// irreconcilable differences between the original state of the node at install-time and a given MachineConfig.
type irreconcilableDifferencesReportGeneratorImpl struct {
	// installTimeParamsPaths paths to the JSON files containing the original MachineConfig used at node bootstrap.
	installTimeParamsPaths []string
}

// newIrreconcilableDifferencesReportGeneratorImpl creates a new instance of
// irreconcilableDifferencesReportGeneratorImpl
func newIrreconcilableDifferencesReportGeneratorImpl(
	installTimeParamsPaths []string,
) *irreconcilableDifferencesReportGeneratorImpl {
	return &irreconcilableDifferencesReportGeneratorImpl{
		installTimeParamsPaths: installTimeParamsPaths,
	}
}

// GenerateReport generates a slice of mcfgv1.IrreconcilableChangeDiff based on the install-time parameters
// defined in the files stored in irreconcilableDifferencesReportGeneratorImpl.installTimeParamsPaths and the
// provided mcfgv1.MachineConfig.
// This function retrieves the install-time parameters from disk, performs the conversion of both MachineConfigs
// to JSON and evaluate the differences of the non-reconcilable sections (sections managed by the MCD like files
// or systemd units are skipped) as a JSON diff.
// The function returns error if an issue finding the base MachineConfig or if a JSON conversion error happens.
func (i *irreconcilableDifferencesReportGeneratorImpl) GenerateReport(
	targetMachineConfig *mcfgv1.MachineConfig,
) ([]mcfgv1.IrreconcilableChangeDiff, error) {
	baseMc := i.fetchBaseInstallTimeMachineConfig()
	if baseMc == nil {
		return nil, errors.New("cannot locate and read the install-time configuration")
	}
	baseNode, err := getJSONNodeFromMC(baseMc)
	if err != nil {
		return nil, fmt.Errorf("failed to get the install-time configuration JsonNode: %w", err)
	}
	targetNode, err := getJSONNodeFromMC(targetMachineConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get the target configuration JsonNode: %w", err)
	}

	diff := baseNode.Diff(targetNode)
	if len(diff) == 0 {
		// No differences, ensure we report no differences
		return nil, nil
	}

	return createReport(diff), nil
}

// createReport from a json diff created by jd return the slice of mcfgv1.IrreconcilableChangeDiff to report.
func createReport(diff jd.Diff) []mcfgv1.IrreconcilableChangeDiff {
	var diffElements []mcfgv1.IrreconcilableChangeDiff
	for _, element := range diff {
		diffElements = append(diffElements, mcfgv1.IrreconcilableChangeDiff{
			FieldPath: getPathStringFromJSONPath(&element.Path),
			// JD DiffElement includes the path of the change at the beginning of the diff prefixed with '@'.
			// As we report the path in a different field we want to remove the path from the diff field.
			Diff: removeFirstAtLineExplicit(element.Render()),
		})
	}
	return diffElements
}

// getPathStringFromJSONPath stringifies a given jd.Path into jsonpath like notation.
// The notation is not RFC compliant, and it is a simplistic approach that just joins
// objects by dots and arrays indexes by square brackets and the array index.
func getPathStringFromJSONPath(path *jd.Path) string {
	builder := &strings.Builder{}
	for _, e := range *path {
		switch e := e.(type) {
		case jd.PathKey:
			builder.WriteString("." + string(e))
		case jd.PathIndex:
			builder.WriteString("[" + strconv.Itoa(int(e)) + "]")
		case jd.PathSet:
		case jd.PathMultiset:
		case jd.PathSetKeys:
		case jd.PathMultisetKeys:
		default:
			// Don't care about the rest of cases for path simple string representation
		}
	}
	return strings.TrimLeft(builder.String(), ".[]")
}

// getJSONNodeFromMC transforms the given mcfgv1.MachineConfig into a jd.JsonNode
// that can be compared later.
// This function takes care of removing  reconcilable changes before the JSON conversion.
func getJSONNodeFromMC(mc *mcfgv1.MachineConfig) (jd.JsonNode, error) {
	content, err := patchDaemonManagedFields(mc)
	if err != nil {
		return nil, fmt.Errorf("failed to patch MachineConfig for diff computing: %w", err)
	}
	node, err := jd.ReadJsonString(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse the MachineConfig as json: %w", err)
	}
	return node, nil
}

// removeFirstAtLineExplicit removes all the lines of the given string till a line starting with '@' is found.
// The line that starts with '@' is removed too.
func removeFirstAtLineExplicit(rendered string) string {
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "@") {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return rendered
}

// patchDaemonManagedFields removes from the given targetMachineConfig all the fields
// that the MCD handles to leave on it only what we consider irreconcilable and allow
// further comparison.
func patchDaemonManagedFields(targetMachineConfig *mcfgv1.MachineConfig) ([]byte, error) {
	ignConfig, err := ctrlcommon.ParseAndConvertConfig(targetMachineConfig.Spec.Config.Raw)
	if err != nil {
		return nil, err
	}
	ign := ctrlcommon.NewIgnConfig()
	ign.Storage.Disks = ignConfig.Storage.Disks
	ign.Storage.Raid = ignConfig.Storage.Raid
	ign.Storage.Filesystems = ignConfig.Storage.Filesystems
	ign.Storage.Luks = ignConfig.Storage.Luks

	rawConf, err := json.Marshal(ign)
	if err != nil {
		return nil, err
	}
	config := mcfgv1.MachineConfig{Spec: mcfgv1.MachineConfigSpec{Config: runtime.RawExtension{Raw: rawConf}}}
	return json.Marshal(config)
}

// fetchBaseInstallTimeMachineConfig reads from disk the mcfgv1.MachineConfig to be used as base for comparison.
// The search paths this function uses are defined by
// irreconcilableDifferencesReportGeneratorImpl.installTimeParamsPaths.
func (i *irreconcilableDifferencesReportGeneratorImpl) fetchBaseInstallTimeMachineConfig() *mcfgv1.MachineConfig {
	for _, path := range i.installTimeParamsPaths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var mc mcfgv1.MachineConfig
		if err := json.Unmarshal(content, &mc); err == nil {
			return &mc
		}
	}
	return nil
}

// NoOpIrreconcilableReporterImpl a no-op irreconcilable differences reporter that does nothing
// when CheckReportIrreconcilableDifferences is called.
type NoOpIrreconcilableReporterImpl struct{}

// NewNoOpIrreconcilableReporterImpl creates a new instance of NoOpIrreconcilableReporterImpl
func NewNoOpIrreconcilableReporterImpl() *NoOpIrreconcilableReporterImpl {
	return &NoOpIrreconcilableReporterImpl{}
}

// CheckReportIrreconcilableDifferences does nothing when called. Returned error is always nil.
func (r *NoOpIrreconcilableReporterImpl) CheckReportIrreconcilableDifferences(_ *mcfgv1.MachineConfig, _ string) error {
	return nil
}

// IrreconcilableReporterImpl handles creating irreconcilable differences reports for the MCN CR and pushing them
// on each node update.
type IrreconcilableReporterImpl struct {
	mcfgClient mcfgclientset.Interface
	// irreconcilableDifferencesReportGenerator creates the diff report itself.
	diffGenerator irreconcilableDifferencesReportGenerator
}

// newIrreconcilableReporter is the internal constructor of IrreconcilableReporterImpl that allows providing
// a non-default irreconcilableDifferencesReportGenerator.
func newIrreconcilableReporter(mcfgClient mcfgclientset.Interface,
	diffGenerator irreconcilableDifferencesReportGenerator,
) *IrreconcilableReporterImpl {
	return &IrreconcilableReporterImpl{
		mcfgClient:    mcfgClient,
		diffGenerator: diffGenerator,
	}
}

// NewIrreconcilableReporter creates a new instance of IrreconcilableReporterImpl.
func NewIrreconcilableReporter(
	mcfgClient mcfgclientset.Interface,
) *IrreconcilableReporterImpl {
	return newIrreconcilableReporter(
		mcfgClient,
		newIrreconcilableDifferencesReportGeneratorImpl(
			[]string{
				"/etc/mcs-machine-config-content.json",
			},
		),
	)
}

func (r *IrreconcilableReporterImpl) CheckReportIrreconcilableDifferences(
	targetMachineConfig *mcfgv1.MachineConfig,
	nodeName string,
) error {
	mcNode, err := r.mcfgClient.MachineconfigurationV1().
		MachineConfigNodes().
		Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		// no existing MCN found since no resource found, no error yet just create a new one
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("MCN for %s node does not exits. Skipping irreconcilable differences reporting",
				nodeName)
		}
		return err
	}

	diffs, err := r.diffGenerator.GenerateReport(targetMachineConfig)
	if err != nil {
		return fmt.Errorf("error generating the irreconcilable differences list of changes. %v", err)
	}
	mcNode.Status.IrreconcilableChanges = diffs

	if _, err = r.mcfgClient.MachineconfigurationV1().
		MachineConfigNodes().
		UpdateStatus(
			context.TODO(),
			mcNode,
			metav1.UpdateOptions{FieldManager: "machine-config-operator"},
		); err != nil {
		return fmt.Errorf("failed to update the %s node status with the latest irreconcilable differences: %w",
			nodeName, err)
	}
	return nil
}
