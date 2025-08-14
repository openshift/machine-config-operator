package daemon

import (
	"context"
	"encoding/json"
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
	"k8s.io/klog/v2"
)

type NoOpIrreconcilableReporterImpl struct{}

func NewNoOpIrreconcilableReporterImpl() *NoOpIrreconcilableReporterImpl {
	return &NoOpIrreconcilableReporterImpl{}
}

func (r *NoOpIrreconcilableReporterImpl) CheckReportIrreconcilableDifferences(_ *mcfgv1.MachineConfig, _ string) error {
	return nil
}

type IrreconcilableReporterImpl struct {
	mcfgClient mcfgclientset.Interface
}

func NewIrreconcilableReporter(
	mcfgClient mcfgclientset.Interface,
) *IrreconcilableReporterImpl {
	return &IrreconcilableReporterImpl{
		mcfgClient: mcfgClient,
	}
}

func (r *IrreconcilableReporterImpl) CheckReportIrreconcilableDifferences(targetMachineConfig *mcfgv1.MachineConfig, nodeName string) error {
	klog.Errorf("@@@@ CheckReportIrreconcilableDifferences called for: %s %s", targetMachineConfig.Name, nodeName)
	mcNode, err := r.mcfgClient.MachineconfigurationV1().MachineConfigNodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("@@@@ CheckReportIrreconcilableDifferences err fetching MCN for: %s %s %v", targetMachineConfig.Name, nodeName, err)
		// no existing MCN found since no resource found, no error yet just create a new one
		if apierrors.IsNotFound(err) {
			klog.Warningf("MCN for %s node does not exits. Skipping irreconcilable differences reporting", nodeName)
			return nil
		}
		return err
	}

	baseMc := fetchBaseInstallTimeMachineConfig()
	if baseMc == nil {
		klog.Warningf("Cannot locate and read the install-time configuration")
		return nil
	}
	baseNode, err := getJsonNodeFromMC(baseMc)
	if err != nil {
		return fmt.Errorf("failed to get the install-time configuration JsonNode: %w", err)
	}
	targetNode, err := getJsonNodeFromMC(targetMachineConfig)
	if err != nil {
		return fmt.Errorf("failed to get the target configuration JsonNode: %w", err)
	}

	diff := baseNode.Diff(targetNode)
	if len(diff) == 0 {
		// No differences, ensure we report no differences
		mcNode.Status.IrreconcilableChanges = nil
	} else {
		mcNode.Status.IrreconcilableChanges = createReport(diff)
	}

	data, err := json.Marshal(mcNode)
	if err != nil {
		return fmt.Errorf("@@@@ CheckReportIrreconcilableDifferences: failed to marshal the install-time configuration JsonNode: %w", err)
	} else {
		klog.Errorf("@@@@ CheckReportIrreconcilableDifferences diff: %s", string(data))
	}

	if _, err = r.mcfgClient.MachineconfigurationV1().
		MachineConfigNodes().
		UpdateStatus(context.TODO(), mcNode, metav1.UpdateOptions{FieldManager: "machine-config-operator"}); err != nil {
		return fmt.Errorf("failed to update the %s node status with the latest irreconcilable differences: %w", nodeName, err)
	}
	return nil
}

func createReport(diff jd.Diff) []mcfgv1.IrreconcilableChangeDiff {
	var diffElements []mcfgv1.IrreconcilableChangeDiff
	for _, element := range diff {
		diffElements = append(diffElements, mcfgv1.IrreconcilableChangeDiff{
			FieldPath: getPathStringFromJsonPath(&element.Path),
			Diff:      removeFirstAtLineExplicit(element.Render()),
		})
	}
	return diffElements
}

func getPathStringFromJsonPath(path *jd.Path) string {
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

func getJsonNodeFromMC(mc *mcfgv1.MachineConfig) (jd.JsonNode, error) {
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

func removeFirstAtLineExplicit(rendered string) string {
	if rendered == "" {
		return ""
	}

	lines := strings.Split(rendered, "\n")

	index := 0
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "@") {
			index = i
		}
	}

	return strings.Join(lines[index:], "\n")
}

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

func fetchBaseInstallTimeMachineConfig() *mcfgv1.MachineConfig {
	readPaths := []string{
		"/etc/mcs-machine-config-content.json",
		"/etc/machine-config-daemon/installconfig",
	}
	for _, path := range readPaths {
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
