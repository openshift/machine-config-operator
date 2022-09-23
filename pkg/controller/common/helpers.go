package common

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/clarketm/json"
	fcctbase "github.com/coreos/fcct/base/v0_1"
	"github.com/coreos/ign-converter/translate/v23tov30"
	"github.com/coreos/ign-converter/translate/v32tov22"
	"github.com/coreos/ign-converter/translate/v32tov31"
	ign2error "github.com/coreos/ignition/config/shared/errors"
	ign2 "github.com/coreos/ignition/config/v2_2"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign2_3 "github.com/coreos/ignition/config/v2_3"
	validate2 "github.com/coreos/ignition/config/validate"
	ign3error "github.com/coreos/ignition/v2/config/shared/errors"
	ign3_0 "github.com/coreos/ignition/v2/config/v3_0"
	ign3_1 "github.com/coreos/ignition/v2/config/v3_1"
	translate3_1 "github.com/coreos/ignition/v2/config/v3_1/translate"
	ign3_1types "github.com/coreos/ignition/v2/config/v3_1/types"
	ign3 "github.com/coreos/ignition/v2/config/v3_2"
	translate3 "github.com/coreos/ignition/v2/config/v3_2/translate"
	ign3types "github.com/coreos/ignition/v2/config/v3_2/types"
	validate3 "github.com/coreos/ignition/v2/config/validate"
	"github.com/ghodss/yaml"
	"github.com/golang/glog"
	"github.com/vincent-petithory/dataurl"
	kerr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned"
)

// strToPtr converts the input string to a pointer to itself
func strToPtr(s string) *string {
	return &s
}

// MergeMachineConfigs combines multiple machineconfig objects into one object.
// It sorts all the configs in increasing order of their name.
// It uses the Ignition config from first object as base and appends all the rest.
// Kernel arguments are concatenated.
// It defaults to the OSImageURL provided by the CVO but allows a MC provided OSImageURL to take precedence.
func MergeMachineConfigs(configs []*mcfgv1.MachineConfig, osImageURL string) (*mcfgv1.MachineConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	sort.SliceStable(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })

	var fips bool
	var kernelType string
	var outIgn ign3types.Config
	var err error

	if configs[0].Spec.Config.Raw == nil {
		outIgn = ign3types.Config{
			Ignition: ign3types.Ignition{
				Version: ign3types.MaxVersion.String(),
			},
		}
	} else {
		outIgn, err = ParseAndConvertConfig(configs[0].Spec.Config.Raw)
		if err != nil {
			return nil, err
		}
	}

	for idx := 1; idx < len(configs); idx++ {
		if configs[idx].Spec.Config.Raw != nil {
			mergedIgn, err := ParseAndConvertConfig(configs[idx].Spec.Config.Raw)
			if err != nil {
				return nil, err
			}
			outIgn = ign3.Merge(outIgn, mergedIgn)
		}
	}
	rawOutIgn, err := json.Marshal(outIgn)
	if err != nil {
		return nil, err
	}

	// Setting FIPS to true or kerneType to realtime in any MachineConfig takes priority in setting that field
	for _, cfg := range configs {
		if cfg.Spec.FIPS {
			fips = true
		}
		if cfg.Spec.KernelType == KernelTypeRealtime {
			kernelType = cfg.Spec.KernelType
		}
	}

	// If no MC sets kerneType, then set it to 'default' since that's what it is using
	if kernelType == "" {
		kernelType = KernelTypeDefault
	}

	kargs := []string{}
	for _, cfg := range configs {
		for _, arg := range cfg.Spec.KernelArguments {
			var present bool
			for _, val := range kargs {
				if val == arg {
					present = true
					break
				}
			}
			if !present {
				kargs = append(kargs, arg)
			}
		}
	}

	extensions := []string{}
	for _, cfg := range configs {
		extensions = append(extensions, cfg.Spec.Extensions...)
	}

	// Ensure that kernel-devel extension is applied only with default kernel.
	if kernelType != KernelTypeDefault {
		if InSlice("kernel-devel", extensions) {
			return nil, fmt.Errorf("installing kernel-devel extension is not supported with kernelType: %s", kernelType)
		}
	}

	// For layering, we want to let the user override OSImageURL again
	overriddenOSImageURL := ""
	for _, cfg := range configs {
		if cfg.Spec.OSImageURL != "" {
			overriddenOSImageURL = cfg.Spec.OSImageURL
		}
	}
	// Make sure it's obvious in the logs that it was overridden
	if overriddenOSImageURL != "" && overriddenOSImageURL != osImageURL {
		osImageURL = overriddenOSImageURL
	}

	return &mcfgv1.MachineConfig{
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL:      osImageURL,
			KernelArguments: kargs,
			Config: runtime.RawExtension{
				Raw: rawOutIgn,
			},
			FIPS:       fips,
			KernelType: kernelType,
			Extensions: extensions,
		},
	}, nil
}

// PointerConfig generates the stub ignition for the machine to boot properly
// NOTE: If you change this, you also need to change the pointer configuration in openshift/installer, see
// https://github.com/openshift/installer/blob/master/pkg/asset/ignition/machine/node.go#L20
func PointerConfig(ignitionHost string, rootCA []byte) (ign3types.Config, error) {
	configSourceURL := &url.URL{
		Scheme: "https",
		Host:   ignitionHost,
		Path:   "/config/{{.Role}}",
	}
	// we do decoding here as curly brackets are escaped to %7B and breaks golang's templates
	ignitionHostTmpl, err := url.QueryUnescape(configSourceURL.String())
	if err != nil {
		return ign3types.Config{}, err
	}
	CASource := dataurl.EncodeBytes(rootCA)
	return ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
			Config: ign3types.IgnitionConfig{
				Merge: []ign3types.Resource{{
					Source: &ignitionHostTmpl,
				}},
			},
			Security: ign3types.Security{
				TLS: ign3types.TLS{
					CertificateAuthorities: []ign3types.Resource{{
						Source: &CASource,
					}},
				},
			},
		},
	}, nil
}

// NewIgnConfig returns an empty ignition config with version set as latest version
func NewIgnConfig() ign3types.Config {
	return ign3types.Config{
		Ignition: ign3types.Ignition{
			Version: ign3types.MaxVersion.String(),
		},
	}
}

// WriteTerminationError writes to the Kubernetes termination log.
func WriteTerminationError(err error) {
	msg := err.Error()
	// Disable gosec here to avoid throwing
	// G306: Expect WriteFile permissions to be 0600 or less
	// #nosec
	ioutil.WriteFile("/dev/termination-log", []byte(msg), 0o644)
	glog.Fatal(msg)
}

// ConvertRawExtIgnitionToV3 ensures that the Ignition config in
// the RawExtension is spec v3.2, or translates to it.
func ConvertRawExtIgnitionToV3(inRawExtIgn *runtime.RawExtension) (runtime.RawExtension, error) {
	// This function is only used by the MCServer so we don't need to consider v3.0
	_, rptV3, errV3 := ign3.Parse(inRawExtIgn.Raw)
	if errV3 == nil && !rptV3.IsFatal() {
		// The rawExt is already on V3.2, no need to translate
		return *inRawExtIgn, nil
	}

	var converted3 ign3types.Config
	ignCfgV3_1, rptV3_1, errV3_1 := ign3_1.Parse(inRawExtIgn.Raw)
	if errV3_1 == nil && !rptV3_1.IsFatal() {
		converted3 = translate3.Translate(ignCfgV3_1)
	} else {
		ignCfg, rpt, err := ign2.Parse(inRawExtIgn.Raw)
		if err != nil || rpt.IsFatal() {
			return runtime.RawExtension{}, fmt.Errorf("parsing Ignition config spec v2.2 failed with error: %w\nReport: %v", err, rpt)
		}
		converted3, err = convertIgnition2to3(ignCfg)
		if err != nil {
			return runtime.RawExtension{}, fmt.Errorf("failed to convert config from spec v2.2 to v3.2: %w", err)
		}
	}

	outIgnV3, err := json.Marshal(converted3)
	if err != nil {
		return runtime.RawExtension{}, fmt.Errorf("failed to marshal converted config: %w", err)
	}

	outRawExt := runtime.RawExtension{}
	outRawExt.Raw = outIgnV3

	return outRawExt, nil
}

// ConvertRawExtIgnitionToV3_1 ensures that the Ignition config in
// the RawExtension is spec v3.1, or translates to it.
func ConvertRawExtIgnitionToV3_1(inRawExtIgn *runtime.RawExtension) (runtime.RawExtension, error) {
	rawExt, err := ConvertRawExtIgnitionToV3(inRawExtIgn)
	if err != nil {
		return runtime.RawExtension{}, err
	}

	ignCfgV3, rptV3, errV3 := ign3.Parse(rawExt.Raw)
	if errV3 != nil || rptV3.IsFatal() {
		return runtime.RawExtension{}, fmt.Errorf("parsing Ignition config failed with error: %w\nReport: %v", errV3, rptV3)
	}

	ignCfgV31, err := convertIgnition32to31(ignCfgV3)
	if err != nil {
		return runtime.RawExtension{}, err
	}

	outIgnV31, err := json.Marshal(ignCfgV31)
	if err != nil {
		return runtime.RawExtension{}, fmt.Errorf("failed to marshal converted config: %w", err)
	}

	outRawExt := runtime.RawExtension{}
	outRawExt.Raw = outIgnV31

	return outRawExt, nil
}

// ConvertRawExtIgnitionToV2 ensures that the Ignition config in
// the RawExtension is spec v2.2, or translates to it.
func ConvertRawExtIgnitionToV2(inRawExtIgn *runtime.RawExtension) (runtime.RawExtension, error) {
	ignCfg, rpt, err := ign3.Parse(inRawExtIgn.Raw)
	if err != nil || rpt.IsFatal() {
		return runtime.RawExtension{}, fmt.Errorf("parsing Ignition config spec v3.2 failed with error: %w\nReport: %v", err, rpt)
	}

	converted2, err := convertIgnition3to2(ignCfg)
	if err != nil {
		return runtime.RawExtension{}, fmt.Errorf("failed to convert config from spec v3.2 to v2.2: %w", err)
	}

	outIgnV2, err := json.Marshal(converted2)
	if err != nil {
		return runtime.RawExtension{}, fmt.Errorf("failed to marshal converted config: %w", err)
	}

	outRawExt := runtime.RawExtension{}
	outRawExt.Raw = outIgnV2

	return outRawExt, nil
}

// convertIgnition2to3 takes an ignition spec v2.2 config and returns a v3.2 config
func convertIgnition2to3(ign2config ign2types.Config) (ign3types.Config, error) {
	// only support writing to root file system
	fsMap := map[string]string{
		"root": "/",
	}

	// Workaround to get v2.3 as input for converter
	ign2_3config := ign2_3.Translate(ign2config)
	ign3_0config, err := v23tov30.Translate(ign2_3config, fsMap)
	if err != nil {
		return ign3types.Config{}, fmt.Errorf("unable to convert Ignition spec v2 config to v3: %w", err)
	}
	// Workaround to get a v3.2 config as output
	converted3 := translate3.Translate(translate3_1.Translate(ign3_0config))

	glog.V(4).Infof("Successfully translated Ignition spec v2 config to Ignition spec v3 config: %v", converted3)
	return converted3, nil
}

// convertIgnition3to2 takes an ignition spec v3.2 config and returns a v2.2 config
func convertIgnition3to2(ign3config ign3types.Config) (ign2types.Config, error) {
	converted2, err := v32tov22.Translate(ign3config)
	if err != nil {
		return ign2types.Config{}, fmt.Errorf("unable to convert Ignition spec v3 config to v2: %w", err)
	}
	glog.V(4).Infof("Successfully translated Ignition spec v3 config to Ignition spec v2 config: %v", converted2)

	return converted2, nil
}

// convertIgnition32to31 takes an ignition spec v3.2 config and returns a v3.1 config
func convertIgnition32to31(ign3config ign3types.Config) (ign3_1types.Config, error) {
	converted31, err := v32tov31.Translate(ign3config)
	if err != nil {
		return ign3_1types.Config{}, fmt.Errorf("unable to convert Ignition spec v3_2 config to v3_1: %w", err)
	}
	glog.V(4).Infof("Successfully translated Ignition spec v3_2 config to Ignition spec v3_1 config: %v", converted31)

	return converted31, nil
}

// ValidateIgnition wraps the underlying Ignition V2/V3 validation, but explicitly supports
// a completely empty Ignition config as valid.  This is because we
// want to allow MachineConfig objects which just have e.g. KernelArguments
// set, but no Ignition config.
// Returns nil if the config is valid (per above) or an error containing a Report otherwise.
func ValidateIgnition(ignconfig interface{}) error {
	switch cfg := ignconfig.(type) {
	case ign2types.Config:
		if reflect.DeepEqual(ign2types.Config{}, cfg) {
			return nil
		}
		if report := validate2.ValidateWithoutSource(reflect.ValueOf(cfg)); report.IsFatal() {
			return fmt.Errorf("invalid ignition V2 config found: %v", report)
		}
		return validateIgn2FileModes(cfg)
	case ign3types.Config:
		if reflect.DeepEqual(ign3types.Config{}, cfg) {
			return nil
		}
		if report := validate3.ValidateWithContext(cfg, nil); report.IsFatal() {
			return fmt.Errorf("invalid ignition V3 config found: %v", report)
		}
		return validateIgn3FileModes(cfg)
	default:
		return fmt.Errorf("unrecognized ignition type")
	}
}

// Validates that Ignition V2 file modes do not have special bits (sticky, setuid, setgid) set
// https://bugzilla.redhat.com/show_bug.cgi?id=2038240
func validateIgn2FileModes(cfg ign2types.Config) error {
	for _, file := range cfg.Storage.Files {
		if file.Mode != nil && os.FileMode(*file.Mode) > os.ModePerm {
			return fmt.Errorf("invalid mode %#o for %s, cannot exceed %#o", *file.Mode, file.Path, os.ModePerm)
		}
	}

	return nil
}

// Validates that Ignition V3 file modes do not have special bits (sticky, setuid, setgid) set
// https://bugzilla.redhat.com/show_bug.cgi?id=2038240
func validateIgn3FileModes(cfg ign3types.Config) error {
	for _, file := range cfg.Storage.Files {
		if file.Mode != nil && os.FileMode(*file.Mode) > os.ModePerm {
			return fmt.Errorf("invalid mode %#o for %s, cannot exceed %#o", *file.Mode, file.Path, os.ModePerm)
		}
	}

	return nil
}

// DecodeIgnitionFileContents returns uncompressed, decoded inline file contents.
// This function does not handle remote resources; it assumes they have already
// been fetched.
func DecodeIgnitionFileContents(source, compression *string) ([]byte, error) {
	var contentsBytes []byte

	// To allow writing of "empty" files we'll allow source to be nil
	if source != nil {
		source, err := dataurl.DecodeString(*source)
		if err != nil {
			return []byte{}, fmt.Errorf("could not decode file content string: %w", err)
		}
		if compression != nil {
			switch *compression {
			case "":
				contentsBytes = source.Data
			case "gzip":
				reader, err := gzip.NewReader(bytes.NewReader(source.Data))
				if err != nil {
					return []byte{}, fmt.Errorf("could not create gzip reader: %w", err)
				}
				defer reader.Close()
				contentsBytes, err = io.ReadAll(reader)
				if err != nil {
					return []byte{}, fmt.Errorf("failed decompressing: %w", err)
				}
			default:
				return []byte{}, fmt.Errorf("unsupported compression type %q", *compression)
			}
		} else {
			contentsBytes = source.Data
		}
	}
	return contentsBytes, nil
}

// InSlice search for an element in slice and return true if found, otherwise return false
func InSlice(elem string, slice []string) bool {
	for _, k := range slice {
		if k == elem {
			return true
		}
	}
	return false
}

// ValidateMachineConfig validates that given MachineConfig Spec is valid.
func ValidateMachineConfig(cfg mcfgv1.MachineConfigSpec) error {
	if !(cfg.KernelType == "" || cfg.KernelType == KernelTypeDefault || cfg.KernelType == KernelTypeRealtime) {
		return fmt.Errorf("kernelType=%s is invalid", cfg.KernelType)
	}

	if cfg.Config.Raw != nil {
		ignCfg, err := IgnParseWrapper(cfg.Config.Raw)
		if err != nil {
			return err
		}
		if err := ValidateIgnition(ignCfg); err != nil {
			return err
		}
	}
	return nil
}

// IgnParseWrapper parses rawIgn for both V2 and V3 ignition configs and returns
// a V2 or V3 Config or an error. This wrapper is necessary since V2 and V3 use different parsers.
func IgnParseWrapper(rawIgn []byte) (interface{}, error) {
	ignCfgV3_2, rptV3_2, errV3_2 := ign3.Parse(rawIgn)
	if errV3_2 == nil && !rptV3_2.IsFatal() {
		return ignCfgV3_2, nil
	}
	if errV3_2.Error() == ign3error.ErrUnknownVersion.Error() {
		ignCfgV3_1, rptV3_1, errV3_1 := ign3_1.Parse(rawIgn)
		if errV3_1 == nil && !rptV3_1.IsFatal() {
			return translate3.Translate(ignCfgV3_1), nil
		}
		// unlike spec v2 parsers, v3 parsers aren't chained by default so we need to try parsing as spec v3.0 as well
		if errV3_1.Error() == ign3error.ErrUnknownVersion.Error() {
			ignCfgV3_0, rptV3_0, errV3_0 := ign3_0.Parse(rawIgn)
			if errV3_0 == nil && !rptV3_0.IsFatal() {
				return translate3.Translate(translate3_1.Translate(ignCfgV3_0)), nil
			}

			if errV3_0.Error() == ign3error.ErrUnknownVersion.Error() {
				ignCfgV2, rptV2, errV2 := ign2.Parse(rawIgn)
				if errV2 == nil && !rptV2.IsFatal() {
					return ignCfgV2, nil
				}

				// If the error is still UnknownVersion it's not a 3.2/3.1/3.0 or 2.x config, thus unsupported
				if errV2.Error() == ign2error.ErrUnknownVersion.Error() {
					return ign3types.Config{}, fmt.Errorf("parsing Ignition config failed: unknown version. Supported spec versions: 2.2, 3.0, 3.1, 3.2")
				}
				return ign3types.Config{}, fmt.Errorf("parsing Ignition spec v2 failed with error: %w\nReport: %v", errV2, rptV2)
			}
			return ign3types.Config{}, fmt.Errorf("parsing Ignition config spec v3.0 failed with error: %w\nReport: %v", errV3_0, rptV3_0)
		}
		return ign3types.Config{}, fmt.Errorf("parsing Ignition config spec v3.1 failed with error: %w\nReport: %v", errV3_1, rptV3_1)
	}
	return ign3types.Config{}, fmt.Errorf("parsing Ignition config spec v3.2 failed with error: %w\nReport: %v", errV3_2, rptV3_2)
}

// ParseAndConvertConfig parses rawIgn for both V2 and V3 ignition configs and returns
// a V3 or an error.
func ParseAndConvertConfig(rawIgn []byte) (ign3types.Config, error) {
	ignconfigi, err := IgnParseWrapper(rawIgn)
	if err != nil {
		return ign3types.Config{}, fmt.Errorf("failed to parse Ignition config: %w", err)
	}

	switch typedConfig := ignconfigi.(type) {
	case ign3types.Config:
		return ignconfigi.(ign3types.Config), nil
	case ign2types.Config:
		ignconfv2, err := removeIgnDuplicateFilesUnitsUsers(ignconfigi.(ign2types.Config))
		if err != nil {
			return ign3types.Config{}, err
		}
		convertedIgnV3, err := convertIgnition2to3(ignconfv2)
		if err != nil {
			return ign3types.Config{}, fmt.Errorf("failed to convert Ignition config spec v2 to v3: %w", err)
		}
		return convertedIgnV3, nil
	default:
		return ign3types.Config{}, fmt.Errorf("unexpected type for ignition config: %v", typedConfig)
	}
}

// Internal error used for base64-decoding and gunzipping Ignition configs
var errConfigNotGzipped = fmt.Errorf("ignition config not gzipped")

// Decode, decompress, and deserialize an Ignition config file.
func ParseAndConvertGzippedConfig(rawIgn []byte) (ign3types.Config, error) {
	// Try to decode and decompress our payload
	out, err := decodeAndDecompressPayload(bytes.NewReader(rawIgn))
	if err == nil {
		// Our payload was decoded and decompressed, so parse it as Ignition.
		glog.V(2).Info("ignition config was base64-decoded and gunzipped successfully")
		return ParseAndConvertConfig(out)
	}

	// Our Ignition config is not base64-encoded, which means it might only be gzipped:
	// e.g.: $ gzip -9 ign_config.json
	var base64Err base64.CorruptInputError
	if errors.As(err, &base64Err) {
		glog.V(2).Info("ignition config was not base64 encoded, trying to gunzip ignition config")
		out, err = decompressPayload(bytes.NewReader(rawIgn))
		if err == nil {
			// We were able to decompress our payload, so let's try parsing it
			glog.V(2).Info("ignition config was gunzipped successfully")
			return ParseAndConvertConfig(out)
		}
	}

	// Our Ignition config is not gzipped, so let's try to serialize the raw Ignition directly.
	if errors.Is(err, errConfigNotGzipped) {
		glog.V(2).Info("ignition config was not gzipped")
		return ParseAndConvertConfig(rawIgn)
	}

	return ign3types.Config{}, fmt.Errorf("unable to read ignition config: %w", err)
}

// Attempts to base64-decode and/or decompresses a given byte array.
func decodeAndDecompressPayload(r io.Reader) ([]byte, error) {
	// Wrap the io.Reader in a base64 decoder (which implements io.Reader)
	base64Dec := base64.NewDecoder(base64.StdEncoding, r)
	out, err := decompressPayload(base64Dec)
	if err == nil {
		return out, nil
	}

	return nil, fmt.Errorf("unable to decode and decompress payload: %w", err)
}

// Checks if a given io.Reader contains known gzip headers and if so, gunzips
// the contents.
func decompressPayload(r io.Reader) ([]byte, error) {
	// Wrap our io.Reader in a bufio.Reader. This allows us to peek ahead to
	// determine if we have a valid gzip archive.
	in := bufio.NewReader(r)
	headerBytes, err := in.Peek(2)
	if err != nil {
		return nil, fmt.Errorf("could not peek: %w", err)
	}

	// gzipped files have a header in the first two bytes which contain a magic
	// number that indicate they are gzipped. We check if these magic numbers are
	// present as a quick and easy way to determine if our payload is gzipped.
	//
	// See: https://cs.opensource.google/go/go/+/refs/tags/go1.19:src/compress/gzip/gunzip.go;l=20-21
	if headerBytes[0] != 0x1f && headerBytes[1] != 0x8b {
		return nil, errConfigNotGzipped
	}

	gz, err := gzip.NewReader(in)
	if err != nil {
		return nil, fmt.Errorf("initialize gzip reader failed: %w", err)
	}

	defer gz.Close()

	data, err := ioutil.ReadAll(gz)
	if err != nil {
		return nil, fmt.Errorf("decompression failed: %w", err)
	}

	return data, nil
}

// Function to remove duplicated files/units/users from a V2 MC, since the translator
// (and ignition spec V3) does not allow for duplicated entries in one MC.
// This should really not change the actual final behaviour, since it keeps
// ordering into consideration and has contents from the highest alphanumeric
// MC's final version of a file.
// Note:
// Append is not considered since we do not allow for appending
// Units have one exception: dropins are concat'ed

func removeIgnDuplicateFilesUnitsUsers(ignConfig ign2types.Config) (ign2types.Config, error) {

	files := ignConfig.Storage.Files
	units := ignConfig.Systemd.Units
	users := ignConfig.Passwd.Users

	filePathMap := map[string]bool{}
	var outFiles []ign2types.File
	for i := len(files) - 1; i >= 0; i-- {
		// We do not actually support to other filesystems so we make the assumption that there is only 1 here
		path := files[i].Path
		if _, isDup := filePathMap[path]; isDup {
			continue
		}
		outFiles = append(outFiles, files[i])
		filePathMap[path] = true
	}

	unitNameMap := map[string]bool{}
	var outUnits []ign2types.Unit
	for i := len(units) - 1; i >= 0; i-- {
		unitName := units[i].Name
		if _, isDup := unitNameMap[unitName]; isDup {
			// this is a duplicated unit by name, so let's check for the dropins and append them
			if len(units[i].Dropins) > 0 {
				for j := range outUnits {
					if outUnits[j].Name == unitName {
						// outUnits[j] is the highest priority entry with this unit name
						// now loop over the new unit's dropins and append it if the name
						// isn't duplicated in the existing unit's dropins
						for _, newDropin := range units[i].Dropins {
							hasExistingDropin := false
							for _, existingDropins := range outUnits[j].Dropins {
								if existingDropins.Name == newDropin.Name {
									hasExistingDropin = true
									break
								}
							}
							if !hasExistingDropin {
								outUnits[j].Dropins = append(outUnits[j].Dropins, newDropin)
							}
						}
						continue
					}
				}
				glog.V(2).Infof("Found duplicate unit %v, appending dropin section", unitName)
			}
			continue
		}
		outUnits = append(outUnits, units[i])
		unitNameMap[unitName] = true
	}

	// Concat sshkey sections into the newest passwdUser in the list
	// We make the assumption that there is only one user: core
	// since that is the only supported user by design.
	// It's technically possible, though, to have created another user
	// during install time configs, since we only check the validity of
	// the passwd section if it was changed. Explicitly error in that case.
	if len(users) > 0 {
		outUser := users[len(users)-1]
		if outUser.Name != "core" {
			return ignConfig, fmt.Errorf("unexpected user with name: %v. Only core user is supported", outUser.Name)
		}
		for i := len(users) - 2; i >= 0; i-- {
			if users[i].Name != "core" {
				return ignConfig, fmt.Errorf("unexpected user with name: %v. Only core user is supported", users[i].Name)
			}
			for j := range users[i].SSHAuthorizedKeys {
				outUser.SSHAuthorizedKeys = append(outUser.SSHAuthorizedKeys, users[i].SSHAuthorizedKeys[j])
			}
		}
		// Ensure SSH key uniqueness
		ignConfig.Passwd.Users = []ign2types.PasswdUser{dedupePasswdUserSSHKeys(outUser)}
	}

	// outFiles and outUnits should now have all duplication removed
	ignConfig.Storage.Files = outFiles
	ignConfig.Systemd.Units = outUnits

	return ignConfig, nil
}

// TranspileCoreOSConfigToIgn transpiles Fedora CoreOS config to ignition
// internally it transpiles to Ign spec v3 config
func TranspileCoreOSConfigToIgn(files, units []string) (*ign3types.Config, error) {
	overwrite := true
	outConfig := ign3types.Config{}
	// Convert data to Ignition resources
	for _, contents := range files {
		f := new(fcctbase.File)
		if err := yaml.Unmarshal([]byte(contents), f); err != nil {
			return nil, fmt.Errorf("failed to unmarshal %q into struct: %w", contents, err)
		}
		f.Overwrite = &overwrite

		// Add the file to the config
		var ctCfg fcctbase.Config
		ctCfg.Storage.Files = append(ctCfg.Storage.Files, *f)
		ign3_0config, tSet, err := ctCfg.ToIgn3_0()
		if err != nil {
			return nil, fmt.Errorf("failed to transpile config to Ignition config %w\nTranslation set: %v", err, tSet)
		}
		ign3_2config := translate3.Translate(translate3_1.Translate(ign3_0config))
		outConfig = ign3.Merge(outConfig, ign3_2config)
	}

	for _, contents := range units {
		u := new(fcctbase.Unit)
		if err := yaml.Unmarshal([]byte(contents), u); err != nil {
			return nil, fmt.Errorf("failed to unmarshal systemd unit into struct: %w", err)
		}

		// Add the unit to the config
		var ctCfg fcctbase.Config
		ctCfg.Systemd.Units = append(ctCfg.Systemd.Units, *u)
		ign3_0config, tSet, err := ctCfg.ToIgn3_0()
		if err != nil {
			return nil, fmt.Errorf("failed to transpile config to Ignition config %w\nTranslation set: %v", err, tSet)
		}
		ign3_2config := translate3.Translate(translate3_1.Translate(ign3_0config))
		outConfig = ign3.Merge(outConfig, ign3_2config)
	}

	return &outConfig, nil
}

// MachineConfigFromIgnConfig creates a MachineConfig with the provided Ignition config
func MachineConfigFromIgnConfig(role, name string, ignCfg interface{}) (*mcfgv1.MachineConfig, error) {
	rawIgnCfg, err := json.Marshal(ignCfg)
	if err != nil {
		return nil, fmt.Errorf("error marshalling Ignition config: %w", err)
	}
	return MachineConfigFromRawIgnConfig(role, name, rawIgnCfg)
}

// MachineConfigFromRawIgnConfig creates a MachineConfig with the provided raw Ignition config
func MachineConfigFromRawIgnConfig(role, name string, rawIgnCfg []byte) (*mcfgv1.MachineConfig, error) {
	labels := map[string]string{
		mcfgv1.MachineConfigRoleLabelKey: role,
	}
	return &mcfgv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Name:   name,
		},
		Spec: mcfgv1.MachineConfigSpec{
			OSImageURL: "",
			Config: runtime.RawExtension{
				Raw: rawIgnCfg,
			},
		},
	}, nil
}

// GetManagedKey returns the managed key for sub-controllers, handling any migration needed
func GetManagedKey(pool *mcfgv1.MachineConfigPool, client mcfgclientset.Interface, prefix, suffix, deprecatedKey string) (string, error) {
	managedKey := fmt.Sprintf("%s-%s-generated-%s", prefix, pool.Name, suffix)
	// if we don't have a client, we're installing brand new, and we don't need to adjust for backward compatibility
	if client == nil {
		return managedKey, nil
	}
	if _, err := client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), managedKey, metav1.GetOptions{}); err == nil {
		return managedKey, nil
	}
	old, err := client.MachineconfigurationV1().MachineConfigs().Get(context.TODO(), deprecatedKey, metav1.GetOptions{})
	if err != nil && !kerr.IsNotFound(err) {
		return "", fmt.Errorf("could not get MachineConfig %q: %w", deprecatedKey, err)
	}
	// this means no previous CR config were here, so we can start fresh
	if kerr.IsNotFound(err) {
		return managedKey, nil
	}
	// if we're here, we'll grab the old CR config, dupe it and patch its name
	mc, err := MachineConfigFromRawIgnConfig(pool.Name, managedKey, old.Spec.Config.Raw)
	if err != nil {
		return "", err
	}
	_, err = client.MachineconfigurationV1().MachineConfigs().Create(context.TODO(), mc, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	err = client.MachineconfigurationV1().MachineConfigs().Delete(context.TODO(), deprecatedKey, metav1.DeleteOptions{})
	return managedKey, err
}

// Ensures SSH keys are unique for a given Ign 2 PasswdUser
// See: https://bugzilla.redhat.com/show_bug.cgi?id=1934176
func dedupePasswdUserSSHKeys(passwdUser ign2types.PasswdUser) ign2types.PasswdUser {
	// Map for checking for duplicates.
	knownSSHKeys := map[ign2types.SSHAuthorizedKey]bool{}

	// Preserve ordering of SSH keys.
	dedupedSSHKeys := []ign2types.SSHAuthorizedKey{}

	for _, sshKey := range passwdUser.SSHAuthorizedKeys {
		if _, isKnown := knownSSHKeys[sshKey]; isKnown {
			// We've seen this key before warn and move on.
			glog.Warningf("duplicate SSH public key found: %s", sshKey)
			continue
		}

		// We haven't seen this key before, add it.
		dedupedSSHKeys = append(dedupedSSHKeys, sshKey)
		knownSSHKeys[sshKey] = true
	}

	// Overwrite the keys with the deduped list.
	passwdUser.SSHAuthorizedKeys = dedupedSSHKeys

	return passwdUser
}

// CalculateConfigFileDiffs compares the files present in two ignition configurations and returns the list of files
// that are different between them
func CalculateConfigFileDiffs(oldIgnConfig, newIgnConfig *ign3types.Config) []string {
	// Go through the files and see what is new or different
	oldFileSet := make(map[string]ign3types.File)
	for _, f := range oldIgnConfig.Storage.Files {
		oldFileSet[f.Path] = f
	}
	newFileSet := make(map[string]ign3types.File)
	for _, f := range newIgnConfig.Storage.Files {
		newFileSet[f.Path] = f
	}
	diffFileSet := []string{}

	// First check if any files were removed
	for path := range oldFileSet {
		_, ok := newFileSet[path]
		if !ok {
			// debug: remove
			glog.Infof("File diff: %v was deleted", path)
			diffFileSet = append(diffFileSet, path)
		}
	}

	// Now check if any files were added/changed
	for path, newFile := range newFileSet {
		oldFile, ok := oldFileSet[path]
		if !ok {
			// debug: remove
			glog.Infof("File diff: %v was added", path)
			diffFileSet = append(diffFileSet, path)
		} else if !reflect.DeepEqual(oldFile, newFile) {
			// debug: remove
			glog.Infof("File diff: detected change to %v", newFile.Path)
			diffFileSet = append(diffFileSet, path)
		}
	}
	return diffFileSet
}

// NewIgnFile returns a simple ignition3 file from just path and file contents.
// It also ensures the compression field is set to the empty string, which is
// currently required for ensuring child configs that may be merged layer
// know that the input is not compressed.
//
// Note the default Ignition file mode is 0644, owned by root/root.
func NewIgnFile(path, contents string) ign3types.File {
	return NewIgnFileBytes(path, []byte(contents))
}

// NewIgnFileBytes is like NewIgnFile, but accepts binary data
func NewIgnFileBytes(path string, contents []byte) ign3types.File {
	mode := 0o644
	return ign3types.File{
		Node: ign3types.Node{
			Path: path,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Mode: &mode,
			Contents: ign3types.Resource{
				Source:      strToPtr(dataurl.EncodeBytes(contents)),
				Compression: strToPtr(""),
			},
		},
	}
}

// NewIgnFileBytesOverwriting is like NewIgnFileBytes, but overwrites existing files by default
func NewIgnFileBytesOverwriting(path string, contents []byte) ign3types.File {
	mode := 0o644
	overwrite := true
	return ign3types.File{
		Node: ign3types.Node{
			Path:      path,
			Overwrite: &overwrite,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Mode: &mode,
			Contents: ign3types.Resource{
				Source:      strToPtr(dataurl.EncodeBytes(contents)),
				Compression: strToPtr(""), // See https://github.com/coreos/butane/issues/332
			},
		},
	}
}

// GetIgnitionFileDataByPath retrieves the file data for a specified path from a given ignition config
func GetIgnitionFileDataByPath(config *ign3types.Config, path string) ([]byte, error) {
	for _, f := range config.Storage.Files {
		if path == f.Path {
			// Convert whatever we have to the actual bytes so we can inspect them
			if f.Contents.Source != nil {
				contents, err := dataurl.DecodeString(*f.Contents.Source)
				if err != nil {
					return nil, err
				}
				return contents.Data, err
			}
		}
	}
	return nil, nil
}

// GetNewestCertificatesFromPEMBundle breaks a pem-encoded bundle out into its component certificates
func GetCertificatesFromPEMBundle(pemBytes []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	// There can be multiple certificates in the file
	for {
		// Decode a block to parse
		block, rest := pem.Decode(pemBytes)
		// Once we get no more blocks, we've read all the certs
		if block == nil {
			break
		}
		// Right now we just care about certificates, not keys
		if block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				// This isn't fatal, *this* cert could just be junk, next one could be okay
				glog.Warningf("Failed to parse certificate: %v", err.Error())
			} else {
				certs = append(certs, cert)
			}
		}
		// Keep reading from where we left off
		pemBytes = rest
	}
	return certs, nil
}

// GetLongestValidCertificate returns the latest-expiring certificate from a given list of certificates
// whose Subject.CommonName also matches any of the given common-name prefixes
func GetLongestValidCertificate(certificateList []*x509.Certificate, subjectPrefixes []string) *x509.Certificate {
	// Sort is smallest-to-largest, so we're putting the cert with the latest expiry date at the top
	sort.Slice(certificateList, func(i, j int) bool {
		return certificateList[i].NotAfter.After(certificateList[j].NotAfter)
	})
	// For each certificate in our list
	for _, certificate := range certificateList {
		// Check it against our prefixes
		for _, prefix := range subjectPrefixes {
			// If it matches, this is the latest-expiring one since it's closest to the "top"
			if strings.HasPrefix(certificate.Subject.CommonName, prefix) {
				return certificate
			}
		}
	}
	return nil
}
