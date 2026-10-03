package ignition

import (
	"fmt"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// File is one Ignition storage file decoded from a MachineConfig.
type File struct {
	Path      string
	Contents  []byte
	Mode      *int
	User      ign3types.NodeUser
	Group     ign3types.NodeGroup
	Overwrite *bool
	// Found is true when the path is present in the MachineConfig, including
	// when Contents is empty.
	Found bool
	// Err is set by ExtractAll when the path exists in Ignition but contents
	// could not be decoded (for example an unsupported append section).
	// ExtractFile returns that condition as a function error instead.
	Err error
}

// ExtractFile returns the Ignition file at path from mc.
// An empty file is Found with a zero-length Contents slice, not a miss.
func ExtractFile(mc *mcfgv1.MachineConfig, path string) (File, error) {
	if mc == nil {
		return File{}, fmt.Errorf("machineconfig is nil")
	}
	if path == "" {
		return File{}, fmt.Errorf("path must not be empty")
	}
	if len(mc.Spec.Config.Raw) == 0 {
		return File{Path: path}, nil
	}

	ign, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return File{}, fmt.Errorf("failed to parse Ignition in MachineConfig %s: %w", mc.Name, err)
	}

	for _, f := range ign.Storage.Files {
		if f.Path != path {
			continue
		}
		extracted, err := fileFromIgnition(mc.Name, f)
		if err != nil {
			return File{}, err
		}
		extracted.Path = path
		return extracted, nil
	}

	return File{Path: path}, nil
}

// ExtractAll returns every Ignition storage file in mc. Duplicate paths keep
// the first occurrence, matching ExtractFile. A decode or append failure on
// one path is recorded on that File and does not abort the rest.
func ExtractAll(mc *mcfgv1.MachineConfig) ([]File, error) {
	if mc == nil {
		return nil, fmt.Errorf("machineconfig is nil")
	}
	if len(mc.Spec.Config.Raw) == 0 {
		return nil, nil
	}

	ign, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Ignition in MachineConfig %s: %w", mc.Name, err)
	}

	seen := make(map[string]struct{}, len(ign.Storage.Files))
	out := make([]File, 0, len(ign.Storage.Files))
	for _, f := range ign.Storage.Files {
		if _, ok := seen[f.Path]; ok {
			continue
		}
		seen[f.Path] = struct{}{}
		extracted, err := fileFromIgnition(mc.Name, f)
		if err != nil {
			out = append(out, File{Path: f.Path, Found: true, Err: err})
			continue
		}
		out = append(out, extracted)
	}
	return out, nil
}

func fileFromIgnition(mcName string, f ign3types.File) (File, error) {
	if len(f.Append) > 0 {
		return File{}, fmt.Errorf("MachineConfig %s: file %q has an append section; append is not supported", mcName, f.Path)
	}
	// DecodeIgnitionFileContents uses dataurl.DecodeString, so both Ignition
	// encodings used in MachineConfigs (and in KCS workarounds) are handled:
	//   data:text/plain;charset=utf-8;base64,<data>
	//   data:,<percent-encoded-data>
	contents, err := ctrlcommon.DecodeIgnitionFileContents(f.Contents.Source, f.Contents.Compression)
	if err != nil {
		return File{}, fmt.Errorf("couldn't decode file %q in MachineConfig %s: %w", f.Path, mcName, err)
	}
	if contents == nil {
		contents = []byte{}
	}
	return File{
		Path:      f.Path,
		Contents:  contents,
		Mode:      copyMode(f.Mode),
		User:      f.User,
		Group:     f.Group,
		Overwrite: copyBool(f.Overwrite),
		Found:     true,
	}, nil
}

func copyMode(mode *int) *int {
	if mode == nil {
		return nil
	}
	copied := *mode
	return &copied
}

func copyBool(v *bool) *bool {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}
