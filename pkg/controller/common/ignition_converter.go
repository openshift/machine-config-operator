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
	ign2_3 "github.com/coreos/ignition/config/v2_3"
	v30types "github.com/coreos/ignition/v2/config/v3_0/types"
	translate3_1 "github.com/coreos/ignition/v2/config/v3_1/translate"
	v31types "github.com/coreos/ignition/v2/config/v3_1/types"
	translate3_2 "github.com/coreos/ignition/v2/config/v3_2/translate"
	v32types "github.com/coreos/ignition/v2/config/v3_2/types"
	v33translate "github.com/coreos/ignition/v2/config/v3_3/translate"
	v33types "github.com/coreos/ignition/v2/config/v3_3/types"
	v34translate "github.com/coreos/ignition/v2/config/v3_4/translate"
	v34types "github.com/coreos/ignition/v2/config/v3_4/types"
	v35translate "github.com/coreos/ignition/v2/config/v3_5/translate"
	v35types "github.com/coreos/ignition/v2/config/v3_5/types"
)

var (
	errIgnitionConverterWrongSourceType = errors.New("wrong source type for the conversion")
	ignitionConverter                   = newIgnitionConverter(buildConverterList())
)

type converterFn func(source any) (any, error)
type conversionTuple struct {
	sourceVersion semver.Version
	targetVersion semver.Version
}

func (t conversionTuple) String() string {
	return fmt.Sprintf("Source: %v Target: %v", t.sourceVersion, t.targetVersion)
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

func (c *functionConverter) Convert(source any) (any, error) {
	result, err := c.conversionFunc(source)
	if err != nil && errors.Is(err, errIgnitionConverterWrongSourceType) {
		return nil, fmt.Errorf("conversion from %v to %v failed: %w", c.tuple.sourceVersion, c.tuple.targetVersion, err)
	}
	return result, err
}

func (c *functionConverter) Versions() conversionTuple {
	return c.tuple
}

func buildConverterList() []ignitionVersionConverter {
	return []ignitionVersionConverter{
		newFunctionConverter(
			// v3.5 -> v3.4
			v35types.MaxVersion, v34types.MaxVersion,
			func(source any) (any, error) {
				if v35, ok := source.(v35types.Config); ok {
					return v35tov34.Translate(v35)
				}
				return v34types.Config{}, errIgnitionConverterWrongSourceType
			}),
		newFunctionConverter(
			// v3.4 -> v3.5
			v34types.MaxVersion, v35types.MaxVersion,
			func(source any) (any, error) {
				if v34, ok := source.(v34types.Config); ok {
					return v35translate.Translate(v34), nil
				}
				return v35types.Config{}, errIgnitionConverterWrongSourceType
			}),
		newFunctionConverter(
			// v3.4 -> v3.3
			v34types.MaxVersion, v33types.MaxVersion,
			func(source any) (any, error) {
				if v34, ok := source.(v34types.Config); ok {
					return v34tov33.Translate(v34)
				}
				return v33types.Config{}, errIgnitionConverterWrongSourceType
			},
		),
		newFunctionConverter(
			// v3.3 -> v3.4
			v33types.MaxVersion, v34types.MaxVersion,
			func(source any) (any, error) {
				if v33, ok := source.(v33types.Config); ok {
					return v34translate.Translate(v33), nil
				}
				return v34types.Config{}, errIgnitionConverterWrongSourceType
			},
		),
		newFunctionConverter(
			// v3.3 -> v3.2
			v33types.MaxVersion, v32types.MaxVersion,
			func(source any) (any, error) {
				if v33, ok := source.(v33types.Config); ok {
					return v33tov32.Translate(v33)
				}
				return v32types.Config{}, errIgnitionConverterWrongSourceType
			},
		),
		newFunctionConverter(
			// v3.2 -> v3.3
			v32types.MaxVersion, v33types.MaxVersion,
			func(source any) (any, error) {
				if v32, ok := source.(v32types.Config); ok {
					return v33translate.Translate(v32), nil
				}
				return v33types.Config{}, errIgnitionConverterWrongSourceType
			},
		),
		newFunctionConverter(
			// v3.2 -> v3.1
			v32types.MaxVersion, v31types.MaxVersion,
			func(source any) (any, error) {
				if v32, ok := source.(v32types.Config); ok {
					return v32tov31.Translate(v32)
				}
				return v31types.Config{}, errIgnitionConverterWrongSourceType
			},
		),
		newFunctionConverter(
			// v3.2 -> v2.2
			v32types.MaxVersion, ign2types.MaxVersion,
			func(source any) (any, error) {
				if v32, ok := source.(v32types.Config); ok {
					return v32tov22.Translate(v32)
				}
				return v34types.Config{}, errIgnitionConverterWrongSourceType
			},
		),
		newFunctionConverter(
			// v2.2 -> v3.2
			ign2types.MaxVersion, v32types.MaxVersion,
			func(source any) (any, error) {
				if v22, ok := source.(ign2types.Config); ok {
					v23 := ign2_3.Translate(v22)
					v30, err := v23tov30.Translate(v23, map[string]string{
						"root": "/",
					})
					if err != nil {
						return v32types.Config{}, fmt.Errorf("unable to convert Ignition spec v2 config to v3: %w", err)
					}
					return translate3_2.Translate(translate3_1.Translate(v30)), nil
				}
				return v32types.Config{}, errIgnitionConverterWrongSourceType
			},
		),
		newFunctionConverter(
			// v3.0 -> v3.2
			v30types.MaxVersion, v32types.MaxVersion,
			func(source any) (any, error) {
				if v30, ok := source.(v30types.Config); ok {
					return translate3_2.Translate(translate3_1.Translate(v30)), nil
				}
				return v32types.Config{}, errors.New("cannot convert")
			},
		)}
}

type converterConversionPath struct {
	tuple          conversionTuple
	converterChain []ignitionVersionConverter
}

func newConverterConversionPathFromExisting(conversionPath *converterConversionPath, conv ignitionVersionConverter) *converterConversionPath {
	return &converterConversionPath{
		tuple:          conversionTuple{sourceVersion: conversionPath.tuple.sourceVersion, targetVersion: conv.Versions().targetVersion},
		converterChain: append(conversionPath.converterChain, conv),
	}
}

type IgnitionConverter interface {
	Convert(source any, sourceVersion semver.Version, targetVersion semver.Version) (any, error)
	GetSupportedMinorVersion(version *semver.Version) *semver.Version
	GetSupportedMinorVersions() []string
}

type ignitionConverterImpl struct {
	converters        []ignitionVersionConverter
	converterPaths    map[conversionTuple]*converterConversionPath
	supportedVersions []*semver.Version
}

func newIgnitionConverter(converters []ignitionVersionConverter) *ignitionConverterImpl {
	instance := &ignitionConverterImpl{
		converters:        converters,
		converterPaths:    map[conversionTuple]*converterConversionPath{},
		supportedVersions: make([]*semver.Version, 0),
	}
	temp := map[semver.Version][]ignitionVersionConverter{}
	supportedVersions := map[semver.Version]struct{}{}
	for _, conv := range instance.converters {
		versionsTuple := conv.Versions()
		instance.registerConverterVersions(versionsTuple, supportedVersions)

		sourceVersion := versionsTuple.sourceVersion
		_, ok := temp[sourceVersion]
		if !ok {
			temp[sourceVersion] = []ignitionVersionConverter{}
		}
		temp[sourceVersion] = append(temp[sourceVersion], conv)
	}
	for ver := range temp {
		instance.recursiveConverterRegistration(ver, temp, nil)
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
		return
	}
	for _, conv := range conversions {
		converterVersions := conv.Versions()
		tuple := conversionTuple{sourceVersion: version, targetVersion: converterVersions.targetVersion}
		if conversionPath != nil && (conversionPath.tuple.IsUp() == tuple.IsUp()) {
			newChain := newConverterConversionPathFromExisting(conversionPath, conv)
			if _, ok := m.converterPaths[newChain.tuple]; !ok {
				m.converterPaths[newChain.tuple] = newChain
				m.recursiveConverterRegistration(newChain.tuple.targetVersion, convertersMap, newChain)
			}
		} else if conversionPath == nil {
			chain := &converterConversionPath{tuple: tuple, converterChain: []ignitionVersionConverter{conv}}
			if _, ok := m.converterPaths[chain.tuple]; !ok {
				m.converterPaths[tuple] = chain
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
		return nil, fmt.Errorf("conversion for source version %s to target version %s is not supported", sourceVersion.String(), targetVersion.String())
	}
	result := source
	for _, conv := range conversionPath.converterChain {
		var err error
		result, err = conv.Convert(result)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (m *ignitionConverterImpl) GetSupportedMinorVersion(version *semver.Version) *semver.Version {
	if version == nil {
		return nil
	}
	minorVersion := semver.Version{Major: version.Major, Minor: version.Minor}
	for _, ver := range m.supportedVersions {
		if ver.Equal(minorVersion) {
			return ver
		}
	}
	return nil
}

func (m *ignitionConverterImpl) GetSupportedMinorVersions() []string {
	var minorVersions []string
	for _, version := range m.supportedVersions {
		minorVersions = append(minorVersions, fmt.Sprintf("%d.%d", version.Major, version.Minor))
	}
	return minorVersions
}

func IgnitionConverterSingleton() IgnitionConverter {
	return ignitionConverter
}
