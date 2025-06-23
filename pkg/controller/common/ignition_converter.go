package common

import (
	"errors"
	"fmt"

	"github.com/coreos/go-semver/semver"
	"github.com/coreos/ign-converter/translate/v23tov30"
	"github.com/coreos/ign-converter/translate/v32tov22"
	"github.com/coreos/ign-converter/translate/v32tov31"
	"github.com/coreos/ign-converter/translate/v33tov32"
	"github.com/coreos/ign-converter/translate/v34tov33"
	"github.com/coreos/ign-converter/translate/v35tov34"
	ign2types "github.com/coreos/ignition/config/v2_2/types"
	ign23 "github.com/coreos/ignition/config/v2_3"
	v30types "github.com/coreos/ignition/v2/config/v3_0/types"
	v31translate "github.com/coreos/ignition/v2/config/v3_1/translate"
	v31types "github.com/coreos/ignition/v2/config/v3_1/types"
	v32translate "github.com/coreos/ignition/v2/config/v3_2/translate"
	v32types "github.com/coreos/ignition/v2/config/v3_2/types"
	v33translate "github.com/coreos/ignition/v2/config/v3_3/translate"
	v33types "github.com/coreos/ignition/v2/config/v3_3/types"
	v34translate "github.com/coreos/ignition/v2/config/v3_4/translate"
	v34types "github.com/coreos/ignition/v2/config/v3_4/types"
	v35translate "github.com/coreos/ignition/v2/config/v3_5/translate"
	v35types "github.com/coreos/ignition/v2/config/v3_5/types"
)

var (
	errIgnitionConverterWrongSourceType       = errors.New("wrong source type for the conversion")
	errIgnitionConverterUnknownVersion        = errors.New("the requested version is unknown")
	errIgnitionConverterUnsupportedConversion = errors.New("the requested conversion is not supported")
	ignitionConverter                         = newIgnitionConverter(buildConverterList())
)

// conversionTuple represents the tuple of versions a conversion has, source and destination
type conversionTuple struct {
	// sourceVersion the source version of the ignition conversion
	sourceVersion semver.Version

	// targetVersion the target/destination version of the ignition conversion
	targetVersion semver.Version
}

func (t conversionTuple) String() string {
	return fmt.Sprintf("source: %v target: %v", t.sourceVersion, t.targetVersion)
}

// IsUp returns true if the tuple represents an upwards conversion and returns false otherwise.
func (t conversionTuple) IsUp() bool {
	return t.sourceVersion.Compare(t.targetVersion) <= 0
}

// ignitionVersionConverter exposes Ignition version conversion logic for a given version tuple
type ignitionVersionConverter interface {
	// Convert Converts the given Ignition config instance to the target one (designated by the target version
	// of the Versions tuple). If the conversion fails or the source type is not the expected one an error
	// is returned.
	Convert(source any) (any, error)

	// Versions returns the versions conversion tuple the converter implements.
	Versions() conversionTuple
}

type baseConverter struct {
	tuple conversionTuple
}

// converterFn represents a conversion function that may fail
type converterFn func(source any) (any, error)

// converterFn represents a generic typed conversion function that may fail
type converterTypedFn[S any, T any] func(source S) (T, error)

// converterFn represents a generic typed conversion function that always succeeds
type converterTypedNoErrorFn[S any, T any] func(source S) T

type functionConverter struct {
	baseConverter
	conversionFunc converterFn
}

// newFunctionConverter builds a function based converter for the given sourceVersion and targetVersion
func newFunctionConverter(sourceVersion, targetVersion semver.Version, conversionFunc converterFn) *functionConverter {
	return &functionConverter{
		baseConverter: baseConverter{
			tuple: conversionTuple{sourceVersion: sourceVersion, targetVersion: targetVersion},
		},
		conversionFunc: conversionFunc,
	}
}

// newFunctionConverterTyped builds an Ignition version converter for the given sourceVersion and targetVersion,
// based on a generic function.
func newFunctionConverterTyped[S any, T any](sourceVersion, targetVersion semver.Version, conversionFunc converterTypedFn[S, T]) *functionConverter {
	return newFunctionConverter(
		sourceVersion, targetVersion,
		func(source any) (any, error) {
			if input, ok := source.(S); ok {
				return conversionFunc(input)
			}
			var empty T
			return empty, errIgnitionConverterWrongSourceType
		})
}

// newFunctionConverterTypedNoError builds an Ignition version converter for the given sourceVersion and targetVersion,
// based on a generic error-free conversion function.
func newFunctionConverterTypedNoError[S any, T any](sourceVersion, targetVersion semver.Version, conversionFunc converterTypedNoErrorFn[S, T]) *functionConverter {
	return newFunctionConverterTyped(sourceVersion, targetVersion, func(s S) (T, error) { return conversionFunc(s), nil })
}

func (c *functionConverter) Convert(source any) (any, error) {
	result, err := c.conversionFunc(source)
	if err != nil && errors.Is(err, errIgnitionConverterWrongSourceType) {
		return result, fmt.Errorf("conversion from %v to %v failed: %w", c.tuple.sourceVersion, c.tuple.targetVersion, err)
	}
	return result, err
}

func (c *functionConverter) Versions() conversionTuple {
	return c.tuple
}

// buildConverterList creates a list that contains all the MCO supported [ignitionVersionConverter] for each
// of the supported version maps.
func buildConverterList() []ignitionVersionConverter {
	return []ignitionVersionConverter{
		// v3.5 -> v3.4
		newFunctionConverterTyped[v35types.Config, v34types.Config](v35types.MaxVersion, v34types.MaxVersion, v35tov34.Translate),
		// v3.4 -> v3.5
		newFunctionConverterTypedNoError[v34types.Config, v35types.Config](v34types.MaxVersion, v35types.MaxVersion, v35translate.Translate),
		// v3.4 -> v3.3
		newFunctionConverterTyped[v34types.Config, v33types.Config](v34types.MaxVersion, v33types.MaxVersion, v34tov33.Translate),
		// v3.3 -> v3.4
		newFunctionConverterTypedNoError[v33types.Config, v34types.Config](v33types.MaxVersion, v34types.MaxVersion, v34translate.Translate),
		// v3.3 -> v3.2
		newFunctionConverterTyped[v33types.Config, v32types.Config](v33types.MaxVersion, v32types.MaxVersion, v33tov32.Translate),
		// v3.2 -> v3.3
		newFunctionConverterTypedNoError[v32types.Config, v33types.Config](v32types.MaxVersion, v33types.MaxVersion, v33translate.Translate),
		// v3.2 -> v3.1
		newFunctionConverterTyped[v32types.Config, v31types.Config](v32types.MaxVersion, v31types.MaxVersion, v32tov31.Translate),
		// v3.2 -> v2.2
		newFunctionConverterTyped[v32types.Config, ign2types.Config](v32types.MaxVersion, ign2types.MaxVersion, v32tov22.Translate),
		// v2.2 -> v3.2
		newFunctionConverterTyped[ign2types.Config, v32types.Config](
			ign2types.MaxVersion, v32types.MaxVersion,
			func(source ign2types.Config) (v32types.Config, error) {
				v30, err := v23tov30.Translate(ign23.Translate(source), map[string]string{
					"root": "/",
				})
				if err != nil {
					return v32types.Config{}, fmt.Errorf("unable to convert Ignition spec v2 config to v3: %w", err)
				}
				return v32translate.Translate(v31translate.Translate(v30)), nil
			},
		),
		// v3.0 -> v3.2
		newFunctionConverterTypedNoError[v30types.Config, v32types.Config](
			v30types.MaxVersion, v32types.MaxVersion,
			func(source v30types.Config) v32types.Config {
				return v32translate.Translate(v31translate.Translate(source))
			},
		)}
}

// converterConversionPath holds the list of ignitionVersionConverter that must be called to convert from the
// source version of conversionTuple to its target version.
type converterConversionPath struct {
	// The versions tuple the path describes
	tuple conversionTuple

	// Slice of  ignitionVersionConverter to call to convert to the target version
	converterChain []ignitionVersionConverter
}

// newConverterConversionPathFromExisting creates a new converterConversionPath copying all the converters from the given
// conversionPath.
func newConverterConversionPathFromExisting(conversionPath *converterConversionPath, conv ignitionVersionConverter) *converterConversionPath {
	newConversionPath := &converterConversionPath{
		tuple: conversionTuple{sourceVersion: conversionPath.tuple.sourceVersion, targetVersion: conv.Versions().targetVersion},
		// Ensure we make a copy of slice's underlying array
		// Note: a simple append doesn't work if the underlying memory of the slice is still big enough to hold the new
		//       converter. We need to warranty that the slice is a copy, the call to make.
		converterChain: append(make([]ignitionVersionConverter, 0, len(conversionPath.converterChain)), conversionPath.converterChain...),
	}
	newConversionPath.converterChain = append(newConversionPath.converterChain, conv)
	return newConversionPath
}

// IgnitionConverter an Ignition configuration version converter
type IgnitionConverter interface {
	// Convert performs the Ignition conversion of source (that uses Ignition version sourceVersion)
	// to the version given by targetVersion.
	// The conversion may fail with errIgnitionConverterUnsupportedConversion if there is no available conversion
	// for the source-target version tuple.
	Convert(source any, sourceVersion, targetVersion semver.Version) (any, error)

	// GetSupportedMinorVersion retrieves the [semver.Version] that performs a translation of the given version.
	// The method may return errIgnitionConverterUnknownVersion if the given version minor version (X.Y) does not
	// have a compatible version in the converter.
	GetSupportedMinorVersion(version semver.Version) (semver.Version, error)

	// GetSupportedMinorVersions retrieves the list of supported minor versions (X.Y)
	GetSupportedMinorVersions() []string
}

type ignitionConverterImpl struct {
	// Map of conversionTuple to converterConversionPath for each supported version
	converterPaths map[conversionTuple]*converterConversionPath

	// Slice of supported versions
	supportedVersions []*semver.Version
}

// newIgnitionConverter creates an instance of ignitionConverterImpl given a list of ignitionVersionConverter.
func newIgnitionConverter(converters []ignitionVersionConverter) *ignitionConverterImpl {
	instance := &ignitionConverterImpl{
		converterPaths:    map[conversionTuple]*converterConversionPath{},
		supportedVersions: make([]*semver.Version, 0),
	}

	// ignitionVersionConverter grouped by source version
	versionsConverters := map[semver.Version][]ignitionVersionConverter{}

	// supportedVersions used to ensure the final versions list has no duplicates
	supportedVersions := map[semver.Version]struct{}{}
	for _, conv := range converters {
		versionsTuple := conv.Versions()
		instance.registerConverterVersions(versionsTuple, supportedVersions)

		sourceVersion := versionsTuple.sourceVersion
		if _, ok := versionsConverters[sourceVersion]; !ok {
			versionsConverters[sourceVersion] = []ignitionVersionConverter{}
		}
		versionsConverters[sourceVersion] = append(versionsConverters[sourceVersion], conv)
	}
	for ver := range versionsConverters {
		instance.recursiveConverterRegistration(ver, versionsConverters, nil)
	}
	semver.Sort(instance.supportedVersions)
	return instance
}

// registerConverterVersions adds both source and target, version from tuple to the given versionsMap if not already present.
func (m *ignitionConverterImpl) registerConverterVersions(tuple conversionTuple, versionsMap map[semver.Version]struct{}) {
	if _, ok := versionsMap[tuple.sourceVersion]; !ok {
		versionsMap[tuple.sourceVersion] = struct{}{}
		m.supportedVersions = append(m.supportedVersions, &tuple.sourceVersion)
	}
	if _, ok := versionsMap[tuple.targetVersion]; !ok {
		versionsMap[tuple.targetVersion] = struct{}{}
		m.supportedVersions = append(m.supportedVersions, &tuple.targetVersion)
	}
}

// recursiveConverterRegistration recursively walks all the converters in convertersMap to build
// up all the possible converterConversionPath. Each created converterConversionPath is added to the
// ignitionConverterImpl's converterPaths map.
func (m *ignitionConverterImpl) recursiveConverterRegistration(version semver.Version, convertersMap map[semver.Version][]ignitionVersionConverter, conversionPath *converterConversionPath) {
	conversions, exists := convertersMap[version]
	if !exists {
		// The version has no converter that translates **from**
		// In other words: Conversions from the version exists but there are no
		// conversion from that version
		return
	}
	for _, conv := range conversions {
		tuple := conversionTuple{sourceVersion: version, targetVersion: conv.Versions().targetVersion}
		if conversionPath != nil && (conversionPath.tuple.IsUp() == tuple.IsUp()) {
			// Continuation of the conversion chain:
			//   - Create a new chain and save it pointing to the current version
			//   - Recursively continue using the new chain as start
			newChain := newConverterConversionPathFromExisting(conversionPath, conv)
			if _, ok := m.converterPaths[newChain.tuple]; !ok {
				m.converterPaths[newChain.tuple] = newChain
				m.recursiveConverterRegistration(newChain.tuple.targetVersion, convertersMap, newChain)
			}
		} else if conversionPath == nil {
			// Start of the conversion chain
			chain := &converterConversionPath{tuple: tuple, converterChain: []ignitionVersionConverter{conv}}
			if _, ok := m.converterPaths[chain.tuple]; !ok {
				m.converterPaths[chain.tuple] = chain
				m.recursiveConverterRegistration(chain.tuple.targetVersion, convertersMap, chain)
			}
		}
	}
}

func (m *ignitionConverterImpl) Convert(source any, sourceVersion, targetVersion semver.Version) (any, error) {
	if sourceVersion.Equal(targetVersion) {
		return source, nil
	}

	tuple := conversionTuple{sourceVersion: sourceVersion, targetVersion: targetVersion}
	conversionPath, ok := m.converterPaths[tuple]
	if !ok {
		return nil, fmt.Errorf("conversion for source version %v to target version %v is not supported %w", sourceVersion, targetVersion, errIgnitionConverterUnsupportedConversion)
	}

	var err error
	result := source
	for _, conv := range conversionPath.converterChain {
		if result, err = conv.Convert(result); err != nil {
			break
		}
	}
	return result, err
}

func (m *ignitionConverterImpl) GetSupportedMinorVersion(version semver.Version) (semver.Version, error) {
	minorVersion := semver.Version{Major: version.Major, Minor: version.Minor}
	for _, ver := range m.supportedVersions {
		if ver.Equal(minorVersion) {
			return *ver, nil
		}
	}
	return semver.Version{}, errIgnitionConverterUnknownVersion
}

func (m *ignitionConverterImpl) GetSupportedMinorVersions() []string {
	var minorVersions []string
	for _, version := range m.supportedVersions {
		minorVersions = append(minorVersions, fmt.Sprintf("%d.%d", version.Major, version.Minor))
	}
	return minorVersions
}

// IgnitionConverterSingleton retrieves the singleton instance of the Ignition converter
func IgnitionConverterSingleton() IgnitionConverter {
	return ignitionConverter
}
