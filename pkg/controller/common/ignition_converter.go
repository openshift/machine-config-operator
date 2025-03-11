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

type converterFn func(source any) (any, error)
type converterTypedFn[S any, T any] func(source S) (T, error)
type converterTypedNoErrorFn[S any, T any] func(source S) T
type conversionTuple struct {
	sourceVersion semver.Version
	targetVersion semver.Version
}

func (t conversionTuple) String() string {
	return fmt.Sprintf("source: %v target: %v", t.sourceVersion, t.targetVersion)
}

func (t conversionTuple) IsUp() bool {
	return t.sourceVersion.Compare(t.targetVersion) <= 0
}

type ignitionVersionConverter interface {
	Convert(source any) (any, error)
	Versions() conversionTuple
}

type baseConverter struct {
	tuple conversionTuple
}

type functionConverter struct {
	baseConverter
	conversionFunc converterFn
}

func newFunctionConverter(sourceVersion semver.Version, targetVersion semver.Version, conversionFunc converterFn) *functionConverter {
	return &functionConverter{
		baseConverter: baseConverter{
			tuple: conversionTuple{sourceVersion: sourceVersion, targetVersion: targetVersion},
		},
		conversionFunc: conversionFunc,
	}
}

func newFunctionConverterTyped[S any, T any](sourceVersion semver.Version, targetVersion semver.Version, conversionFunc converterTypedFn[S, T]) *functionConverter {
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

func newFunctionConverterTypedNoError[S any, T any](sourceVersion semver.Version, targetVersion semver.Version, conversionFunc converterTypedNoErrorFn[S, T]) *functionConverter {
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

type converterConversionPath struct {
	tuple          conversionTuple
	converterChain []ignitionVersionConverter
}

func newConverterConversionPathFromExisting(conversionPath *converterConversionPath, conv ignitionVersionConverter) *converterConversionPath {
	newConversionPath := &converterConversionPath{
		tuple: conversionTuple{sourceVersion: conversionPath.tuple.sourceVersion, targetVersion: conv.Versions().targetVersion},
		// Ensure we make a copy of slice's underlying array
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
	Convert(source any, sourceVersion semver.Version, targetVersion semver.Version) (any, error)

	// GetSupportedMinorVersion retrieves the [semver.Version] that performs a translation of the given version.
	// The method may return errIgnitionConverterUnknownVersion if the given version minor version (X.Y) does not
	// have a compatible version in the converter.
	GetSupportedMinorVersion(version semver.Version) (semver.Version, error)

	// GetSupportedMinorVersions retrieves the list of supported minor versions (X.Y)
	GetSupportedMinorVersions() []string
}

type ignitionConverterImpl struct {
	converterPaths    map[conversionTuple]*converterConversionPath
	supportedVersions []*semver.Version
}

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

func (m *ignitionConverterImpl) Convert(source any, sourceVersion semver.Version, targetVersion semver.Version) (any, error) {
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
