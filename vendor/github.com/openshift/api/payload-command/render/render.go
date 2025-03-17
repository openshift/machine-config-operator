package render

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blang/semver/v4"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	assets "github.com/openshift/api/payload-command/render/renderassets"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
)

// RenderOpts holds values to drive the render command.
type RenderOpts struct {
	ImageProvidedManifestDir      string
	RenderedManifestInputFilename string
	PayloadVersion                string
	AssetOutputDir                string
	UnprefixedClusterProfile      string
}

func (o *RenderOpts) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.RenderedManifestInputFilename, "rendered-manifest-dir", o.RenderedManifestInputFilename,
		"files or directories containing yaml or json manifests that will be created via cluster-bootstrapping.")
	fs.StringVar(&o.ImageProvidedManifestDir, "image-manifests", o.ImageProvidedManifestDir, "Directory containing the manifest templates provided by the image.")
	fs.StringVar(&o.PayloadVersion, "payload-version", o.PayloadVersion, "Version that will eventually be placed into ClusterOperator.status.  This normally comes from the CVO set via env var: OPERATOR_IMAGE_VERSION.")
	fs.StringVar(&o.AssetOutputDir, "asset-output-dir", o.AssetOutputDir, "Output path for rendered manifests.")
	fs.StringVar(&o.UnprefixedClusterProfile, "cluster-profile", o.UnprefixedClusterProfile, "self-managed-high-availability, single-node-developer, ibm-cloud-managed")
}

// Validate verifies the inputs.
func (o *RenderOpts) Validate() error {
	switch o.UnprefixedClusterProfile {
	case "":
		// to be disallowed soonish
	case "self-managed-high-availability", "ibm-cloud-managed":
		// ok
	default:
		return fmt.Errorf("--cluster-profile must be one of self-managed-high-availability, single-node-developer, ibm-cloud-managed")
	}

	return nil
}

// Complete fills in missing values before command execution.
func (o *RenderOpts) Complete() error {
	// TODO cluster-config-operator improperly assumes all single node clusters are this single-node-developer.  apparently single node is something different.
	// TODO once cluster-config-operator is fixed, this line can be removed, but big rocks first.
	if o.UnprefixedClusterProfile == "single-node-developer" {
		o.UnprefixedClusterProfile = "self-managed-high-availability"
	}
	return nil
}

// Run contains the logic of the render command.
func (o *RenderOpts) Run() error {
	featureSet := ""
	featureGateFiles, err := featureGateManifests([]string{o.RenderedManifestInputFilename})
	if err != nil {
		return fmt.Errorf("problem with featuregate manifests: %w", err)
	}

	nodeConfigManifests, err := nodeConfigManifests([]string{o.RenderedManifestInputFilename})
	if err != nil {
		return fmt.Errorf("problem with node config manifests: %w", err)
	}
	var minimumKubeletVersion *semver.Version
	for _, manifest := range nodeConfigManifests {
		uncastObj, err := manifest.GetDecodedObj()
		if err != nil {
			return fmt.Errorf("error decoding FeatureGate: %w", err)
		}
		nodeConfig := &configv1.Node{}
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(uncastObj.(*unstructured.Unstructured).Object, nodeConfig)
		if err != nil {
			return fmt.Errorf("error converting NodeConfig: %w", err)
		}
		// TODO FIXME: how do we handle multiple?
		if nodeConfig.Spec.MinimumKubeletVersion != "" {
			v, err := semver.Parse(nodeConfig.Spec.MinimumKubeletVersion)
			if err != nil {
				return fmt.Errorf("failed to parse provided minimum kubelet version: %w", err)
			}
			minimumKubeletVersion = &v
		}
	}

	clusterProfileAnnotationName := fmt.Sprintf("include.release.openshift.io/%s", o.UnprefixedClusterProfile)

	for _, featureGateFile := range featureGateFiles {
		uncastObj, err := featureGateFile.GetDecodedObj()
		if err != nil {
			return fmt.Errorf("error decoding FeatureGate: %w", err)
		}
		featureGates := &configv1.FeatureGate{}
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(uncastObj.(*unstructured.Unstructured).Object, featureGates)
		if err != nil {
			return fmt.Errorf("error converting FeatureGate: %w", err)
		}
		if featureGates.Annotations == nil {
			featureGates.Annotations = map[string]string{}
		}

		if featureGates.Spec.FeatureSet == configv1.CustomNoUpgrade {
			featureSet = string(featureGates.Spec.FeatureSet)
			renderedFeatureGates, err := renderCustomNoUpgradeFeatureGate(featureGates, features.ClusterProfileName(clusterProfileAnnotationName), o.PayloadVersion)
			if err != nil {
				return err
			}
			featureGateOutBytes := writeFeatureGateV1OrDie(renderedFeatureGates)
			if err := os.WriteFile(featureGateFile.OriginalFilename, []byte(featureGateOutBytes), 0644); err != nil {
				return fmt.Errorf("error writing FeatureGate manifest: %w", err)
			}
			continue
		}

		// if the manifest has cluster profiles specified, the manifest's list must include the configured clusterprofile.
		manifestClusterProfiles := clusterProfilesFrom(featureGates.Annotations)
		switch {
		case len(manifestClusterProfiles) > 0 && !manifestClusterProfiles.Has(clusterProfileAnnotationName):
			return fmt.Errorf("manifest has cluster-profile preferences (%v) that do not contain the configured clusterProfile: %q",
				manifestClusterProfiles.UnsortedList(), clusterProfileAnnotationName)
		case len(manifestClusterProfiles) == 0 && len(clusterProfileAnnotationName) != 0:
			featureGates.Annotations[clusterProfileAnnotationName] = "true"
		}

		featureGateStatus, err := features.FeatureSets(features.ClusterProfileName(clusterProfileAnnotationName), featureGates.Spec.FeatureSet)
		if err != nil {
			return fmt.Errorf("unable to resolve featureGateStatus: %w", err)
		}
		currentDetails := FeaturesGateDetailsFromFeatureSets(featureGateStatus, o.PayloadVersion, minimumKubeletVersion, featureGates.Spec.FeatureSet)

		featureGates.Status.FeatureGates = []configv1.FeatureGateDetails{*currentDetails}

		featureGateOutBytes := writeFeatureGateV1OrDie(featureGates)
		if err := os.WriteFile(featureGateFile.OriginalFilename, []byte(featureGateOutBytes), 0644); err != nil {
			return fmt.Errorf("error writing FeatureGate manifest: %w", err)
		}
		featureSet = string(featureGates.Spec.FeatureSet)
	}

	err = assets.SubstituteAndCopyFiles(
		o.ImageProvidedManifestDir,
		filepath.Join(o.AssetOutputDir, "manifests"),
		featureSet,
		o.UnprefixedClusterProfile,
		nil,
	)
	if err != nil {
		return fmt.Errorf("failed to substitute and copy files: %w", err)
	}

	return nil
}

func renderCustomNoUpgradeFeatureGate(in *configv1.FeatureGate, clusterProfile features.ClusterProfileName, payloadVersion string) (*configv1.FeatureGate, error) {
	if in.Spec.FeatureSet != configv1.CustomNoUpgrade {
		return nil, fmt.Errorf("not CustomNoUpgrade")
	}
	for _, forceEnabled := range in.Spec.CustomNoUpgrade.Enabled {
		if inListOfNames(in.Spec.CustomNoUpgrade.Disabled, forceEnabled) {
			return nil, fmt.Errorf("trying to enable and disable %q", forceEnabled)
		}
	}

	ret := in.DeepCopy()

	// if possible, set the payload version to ease usage during install of different versions
	switch {
	case len(in.Status.FeatureGates) > 1:
		return in, nil
	case len(in.Status.FeatureGates) == 1 && len(in.Status.FeatureGates[0].Version) != 0:
		return in, nil

	case len(in.Status.FeatureGates) == 1 && len(in.Status.FeatureGates[0].Version) == 0:
		ret.Status.FeatureGates[0].Version = payloadVersion
	case len(in.Status.FeatureGates) == 0:
		ret.Status.FeatureGates = append(ret.Status.FeatureGates, configv1.FeatureGateDetails{
			Version:  payloadVersion,
			Enabled:  []configv1.FeatureGateAttributes{},
			Disabled: []configv1.FeatureGateAttributes{},
		})
	}

	defaultFeatureGates, err := features.FeatureSets(clusterProfile, configv1.Default)
	if err != nil {
		return nil, err
	}

	enabled := []configv1.FeatureGateAttributes{}
	disabled := []configv1.FeatureGateAttributes{}
	if in.Spec.CustomNoUpgrade != nil {
		enabled = []configv1.FeatureGateAttributes{}
		for _, forceEnabled := range in.Spec.CustomNoUpgrade.Enabled {
			enabled = append(enabled, configv1.FeatureGateAttributes{Name: forceEnabled})
		}
		for _, defaultEnabled := range defaultFeatureGates.Enabled {
			if !inListOfNames(in.Spec.CustomNoUpgrade.Disabled, defaultEnabled.FeatureGateAttributes.Name) {
				enabled = append(enabled, defaultEnabled.FeatureGateAttributes)
			}
		}

		disabled = []configv1.FeatureGateAttributes{}
		for _, forceDisabled := range in.Spec.CustomNoUpgrade.Disabled {
			disabled = append(disabled, configv1.FeatureGateAttributes{Name: forceDisabled})
		}
		for _, defaultDisabled := range defaultFeatureGates.Disabled {
			if !inListOfNames(in.Spec.CustomNoUpgrade.Enabled, defaultDisabled.FeatureGateAttributes.Name) {
				disabled = append(disabled, defaultDisabled.FeatureGateAttributes)
			}
		}
	} else {
		for _, defaultEnabled := range defaultFeatureGates.Enabled {
			enabled = append(enabled, defaultEnabled.FeatureGateAttributes)
		}
		for _, defaultDisabled := range defaultFeatureGates.Disabled {
			disabled = append(disabled, defaultDisabled.FeatureGateAttributes)
		}
	}

	// sort for stability
	sort.Sort(byName(enabled))
	sort.Sort(byName(disabled))
	ret.Status.FeatureGates[0].Enabled = enabled
	ret.Status.FeatureGates[0].Disabled = disabled

	return ret, nil
}

func inListOfNames(haystack []configv1.FeatureGateName, needle configv1.FeatureGateName) bool {
	for _, curr := range haystack {
		if curr == needle {
			return true
		}
	}
	return false
}

func clusterProfilesFrom(annotations map[string]string) sets.Set[string] {
	ret := sets.New[string]()
	for k, v := range annotations {
		if strings.HasPrefix(k, "include.release.openshift.io/") && v == "true" {
			ret.Insert(k)
		}
	}
	return ret
}

func featureGateManifests(renderedManifestInputFilenames []string) (assets.RenderedManifests, error) {
	inputManifests, err := loadManifests(renderedManifestInputFilenames)
	if err != nil {
		return nil, err
	}
	featureGates := inputManifests.ListManifestOfType(configv1.GroupVersion.WithKind("FeatureGate"))
	if len(featureGates) == 0 {
		return nil, fmt.Errorf("no FeatureGates found in manfest dir: %v", renderedManifestInputFilenames)
	}

	return featureGates, nil
}

func nodeConfigManifests(renderedManifestInputFilenames []string) (assets.RenderedManifests, error) {
	inputManifests, err := loadManifests(renderedManifestInputFilenames)
	if err != nil {
		return nil, err
	}
	return inputManifests.ListManifestOfType(configv1.GroupVersion.WithKind("Node")), nil
}

func loadManifests(renderedManifestInputFilenames []string) (assets.RenderedManifests, error) {
	if len(renderedManifestInputFilenames) == 0 {
		return nil, fmt.Errorf("cannot return FeatureGate without rendered manifests")
	}

	inputManifests := assets.RenderedManifests{}
	for _, filename := range renderedManifestInputFilenames {
		manifestContent, err := assets.LoadFilesRecursively(filename)
		if err != nil {
			return nil, fmt.Errorf("failed loading rendered manifest inputs from %q: %w", filename, err)
		}
		for manifestFile, content := range manifestContent {
			inputManifests = append(inputManifests, assets.RenderedManifest{
				OriginalFilename: filepath.Join(filename, manifestFile),
				Content:          content,
			})
		}
	}
	return inputManifests, nil
}

func FeaturesGateDetailsFromFeatureSets(featureGateStatus *features.FeatureGateEnabledDisabled, currentVersion string, minimumKubeletVersion *semver.Version, featureSet configv1.FeatureSet) *configv1.FeatureGateDetails {
	currentDetails := configv1.FeatureGateDetails{
		Version: currentVersion,
	}
	skippedForVersion := map[configv1.FeatureGateName]bool{}
	for _, gateName := range featureGateStatus.Enabled {
		// Skip adding if we have a RequiredMinimumComponentVersion, as we'll handle that below
		if len(gateName.FeatureGateAttributes.RequiredMinimumComponentVersions) != 0 && featureSet == configv1.Default {
			skippedForVersion[gateName.FeatureGateAttributes.Name] = false
		} else {
			currentDetails.Enabled = append(currentDetails.Enabled, *gateName.FeatureGateAttributes.DeepCopy())
		}
	}
	for _, gateName := range featureGateStatus.Disabled {
		// Skip adding if we have a RequiredMinimumComponentVersion, as we'll handle that below
		if len(gateName.FeatureGateAttributes.RequiredMinimumComponentVersions) != 0 && featureSet == configv1.Default {
			skippedForVersion[gateName.FeatureGateAttributes.Name] = false
		} else {
			currentDetails.Disabled = append(currentDetails.Disabled, *gateName.FeatureGateAttributes.DeepCopy())
		}
	}

	if minimumKubeletVersion != nil {
		for versionStr, attrs := range featureGateStatus.EnabledGivenMinimumVersion[configv1.MinimumComponentKubelet] {
			gateVersion, err := semver.Parse(versionStr)
			if err != nil {
				// Programming error, as these are built into the binary in features/feature.go
				panic(err)
			}
			if minimumKubeletVersion.LTE(gateVersion) {
				for _, attr := range attrs {
					currentDetails.Enabled = append(currentDetails.Enabled, attr)
					skippedForVersion[attr.Name] = true
				}
			} else {
				for _, attr := range attrs {
					currentDetails.Disabled = append(currentDetails.Disabled, attr)
					skippedForVersion[attr.Name] = true
				}
			}
		}
	} else {
		for _, attrs := range featureGateStatus.EnabledGivenMinimumVersion[configv1.MinimumComponentKubelet] {
			for _, attr := range attrs {
				currentDetails.Disabled = append(currentDetails.Disabled, attr)
				skippedForVersion[attr.Name] = true
			}
		}
	}

	for name, missed := range skippedForVersion {
		if !missed {
			// Programming error: a gate was registered as skipped but not addressed
			panic(fmt.Errorf("Missed feature gate name %s when constructing featureGateStatus", name))
		}
	}
	if minimumKubeletVersion != nil {
		currentDetails.RenderedMinimumComponentVersions = []configv1.MinimumComponentVersion{
			{
				Component: configv1.MinimumComponentKubelet,
				Version:   minimumKubeletVersion.String(),
			},
		}
	}

	// sort for stability
	sort.Sort(byName(currentDetails.Enabled))
	sort.Sort(byName(currentDetails.Disabled))

	return &currentDetails
}

type byName []configv1.FeatureGateAttributes

func (a byName) Len() int      { return len(a) }
func (a byName) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byName) Less(i, j int) bool {
	if strings.Compare(string(a[i].Name), string(a[j].Name)) < 0 {
		return true
	}
	return false
}

func featuresGatesFromFeatureSets(knownFeatureSets map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled, featureGates *configv1.FeatureGate) ([]configv1.FeatureGateName, []configv1.FeatureGateName, error) {
	if featureGates.Spec.FeatureSet == configv1.CustomNoUpgrade {
		if featureGates.Spec.FeatureGateSelection.CustomNoUpgrade != nil {
			completeEnabled, completeDisabled := completeFeatureGates(knownFeatureSets, featureGates.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled, featureGates.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled)
			return completeEnabled, completeDisabled, nil
		}
		return []configv1.FeatureGateName{}, []configv1.FeatureGateName{}, nil
	}

	featureSet, ok := knownFeatureSets[featureGates.Spec.FeatureSet]
	if !ok {
		return []configv1.FeatureGateName{}, []configv1.FeatureGateName{}, fmt.Errorf(".spec.featureSet %q not found", featureSet)
	}

	completeEnabled, completeDisabled := completeFeatureGates(knownFeatureSets, toFeatureGateNames(featureSet.Enabled), toFeatureGateNames(featureSet.Disabled))
	return completeEnabled, completeDisabled, nil
}

func toFeatureGateNames(in []features.FeatureGateDescription) []configv1.FeatureGateName {
	out := []configv1.FeatureGateName{}
	for _, curr := range in {
		out = append(out, curr.FeatureGateAttributes.Name)
	}

	return out
}

// completeFeatureGates identifies every known feature and ensures that is explicitly on or explicitly off
func completeFeatureGates(knownFeatureSets map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled, enabled, disabled []configv1.FeatureGateName) ([]configv1.FeatureGateName, []configv1.FeatureGateName) {
	specificallyEnabledFeatureGates := sets.New[configv1.FeatureGateName]()
	specificallyEnabledFeatureGates.Insert(enabled...)

	knownFeatureGates := sets.New[configv1.FeatureGateName]()
	knownFeatureGates.Insert(enabled...)
	knownFeatureGates.Insert(disabled...)
	for _, known := range knownFeatureSets {
		for _, curr := range known.Disabled {
			knownFeatureGates.Insert(curr.FeatureGateAttributes.Name)
		}
		for _, curr := range known.Enabled {
			knownFeatureGates.Insert(curr.FeatureGateAttributes.Name)
		}
	}

	return enabled, knownFeatureGates.Difference(specificallyEnabledFeatureGates).UnsortedList()
}
