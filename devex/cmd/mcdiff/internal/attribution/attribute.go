package attribution

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// Writer is one MachineConfig that sets a given Ignition file path.
type Writer struct {
	// MachineConfigName is metadata.name of the fragment that set the path.
	MachineConfigName string
	// Mode is the Ignition file mode, if set.
	Mode *int
	// ContentSHA256 is the hex SHA-256 of the decoded file contents.
	ContentSHA256 string
}

// Result is the merge-order attribution of a file path across source MachineConfigs.
type Result struct {
	Path string
	// Writers are fragments that set Path, in MergeMachineConfigs order.
	Writers []Writer
	// LastWriter is the fragment that wins the Ignition merge for Path.
	// Nil if no source MachineConfig sets the path.
	LastWriter *Writer
}

// Attribute reports which source MachineConfigs supply path, using the same
// merge order as ctrlcommon.MergeMachineConfigs. Expected file bytes should
// still be read from the rendered MachineConfig; this only names writers.
func Attribute(path string, sources []*mcfgv1.MachineConfig) (*Result, error) {
	if path == "" {
		return nil, fmt.Errorf("path must not be empty")
	}

	ordered, err := sortForMerge(sources)
	if err != nil {
		return nil, err
	}

	out := &Result{Path: path}
	for _, mc := range ordered {
		writer, found, err := writerFromMachineConfig(mc, path)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out.Writers = append(out.Writers, writer)
	}
	if len(out.Writers) > 0 {
		last := out.Writers[len(out.Writers)-1]
		out.LastWriter = &last
	}
	return out, nil
}

// sortForMerge copies configs and orders them the way MergeMachineConfigs does:
// worker-role fragments by name, then all other fragments by name.
func sortForMerge(configs []*mcfgv1.MachineConfig) ([]*mcfgv1.MachineConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}

	var workerConfigs, otherConfigs []*mcfgv1.MachineConfig
	for _, config := range configs {
		if config == nil {
			return nil, fmt.Errorf("nil MachineConfig in source list")
		}
		if config.ObjectMeta.Labels == nil {
			return nil, fmt.Errorf("cannot find label in MachineConfig %s", config.ObjectMeta.Name)
		}
		if config.ObjectMeta.Labels[ctrlcommon.MachineConfigRoleLabel] == ctrlcommon.MachineConfigPoolWorker {
			workerConfigs = append(workerConfigs, config)
			continue
		}
		otherConfigs = append(otherConfigs, config)
	}
	sort.SliceStable(workerConfigs, func(i, j int) bool { return workerConfigs[i].Name < workerConfigs[j].Name })
	sort.SliceStable(otherConfigs, func(i, j int) bool { return otherConfigs[i].Name < otherConfigs[j].Name })
	return append(workerConfigs, otherConfigs...), nil
}

func writerFromMachineConfig(mc *mcfgv1.MachineConfig, path string) (Writer, bool, error) {
	if len(mc.Spec.Config.Raw) == 0 {
		return Writer{}, false, nil
	}

	ign, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return Writer{}, false, fmt.Errorf("failed to parse Ignition in MachineConfig %s: %w", mc.Name, err)
	}

	file, found := fileByPath(ign, path)
	if !found {
		return Writer{}, false, nil
	}
	if len(file.Append) > 0 {
		return Writer{}, false, fmt.Errorf("MachineConfig %s: file %q has an append section; append is not supported", mc.Name, path)
	}

	contents, err := ctrlcommon.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
	if err != nil {
		return Writer{}, false, fmt.Errorf("couldn't decode file %q in MachineConfig %s: %w", path, mc.Name, err)
	}

	sum := sha256.Sum256(contents)
	return Writer{
		MachineConfigName: mc.Name,
		Mode:              copyMode(file.Mode),
		ContentSHA256:     hex.EncodeToString(sum[:]),
	}, true, nil
}

func fileByPath(ign ign3types.Config, path string) (ign3types.File, bool) {
	for _, f := range ign.Storage.Files {
		if f.Path == path {
			return f, true
		}
	}
	return ign3types.File{}, false
}

func copyMode(mode *int) *int {
	if mode == nil {
		return nil
	}
	copied := *mode
	return &copied
}
