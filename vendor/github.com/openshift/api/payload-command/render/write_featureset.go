package render

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

var (
	clusterProfileToShortName = map[features.ClusterProfileName]string{
		features.Hypershift:  "Hypershift",
		features.SelfManaged: "SelfManagedHA",
	}
)

// versionGroup represents a group of consecutive versions with identical FeatureGate content
type versionGroup struct {
	startVersion uint64
	endVersion   uint64
	content      *features.FeatureGateEnabledDisabled
}

// versionRangeString returns a string representation of the version range for filenames
func (vg versionGroup) versionRangeString() string {
	if vg.startVersion == vg.endVersion {
		return fmt.Sprintf("%d", vg.startVersion)
	}
	return fmt.Sprintf("%d-%d", vg.startVersion, vg.endVersion)
}

func (vg versionGroup) versions() []string {
	versions := []string{}

	for version := vg.startVersion; version <= vg.endVersion; version++ {
		versions = append(versions, fmt.Sprintf("%d", version))
	}
	return versions
}

// featureGateContentEqual compares two FeatureGateEnabledDisabled structs for content equality
func featureGateContentEqual(a, b *features.FeatureGateEnabledDisabled) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Create sorted slices of feature gate names for comparison
	aEnabledNames := sets.New(a.Enabled...)
	bEnabledNames := sets.New(b.Enabled...)

	if !aEnabledNames.Equal(bEnabledNames) {
		return false
	}

	aDisabledNames := sets.New(a.Disabled...)
	bDisabledNames := sets.New(b.Disabled...)
	if !aDisabledNames.Equal(bDisabledNames) {
		return false
	}

	return true
}

// consolidateVersions groups consecutive versions with identical content
func consolidateVersions(
	statusByVersionByClusterProfileByFeatureSet map[uint64]map[features.ClusterProfileName]map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled,
	clusterProfile features.ClusterProfileName,
	featureSetName configv1.FeatureSet,
) []versionGroup {
	var versionContents []versionGroup
	for version, byClusterProfile := range statusByVersionByClusterProfileByFeatureSet {
		if byFeatureSet, exists := byClusterProfile[clusterProfile]; exists {
			if content, exists := byFeatureSet[featureSetName]; exists {
				versionContents = append(versionContents, versionGroup{
					startVersion: version,
					content:      content,
				})
			}
		}
	}

	// Sort by version
	sort.Slice(versionContents, func(i, j int) bool {
		return versionContents[i].startVersion < versionContents[j].startVersion
	})

	var groups []versionGroup

	currentGroup := versionContents[0]
	currentGroup.endVersion = currentGroup.startVersion

	for i := 1; i < len(versionContents); i++ {
		curr := versionContents[i]

		// Check if current version has same content as current group and is consecutive
		if featureGateContentEqual(currentGroup.content, curr.content) &&
			curr.startVersion == currentGroup.endVersion+1 {
			// Extend current group
			currentGroup.endVersion = curr.startVersion
		} else {
			// Finalize current group and start new one
			groups = append(groups, currentGroup)
			currentGroup = curr
			currentGroup.endVersion = currentGroup.startVersion
		}
	}

	// Add the final group
	groups = append(groups, currentGroup)

	return groups
}

// generateConsolidatedFilename creates filename for a version group
func generateConsolidatedFilename(group versionGroup, clusterProfile features.ClusterProfileName, featureSet configv1.FeatureSet) string {
	clusterProfileShort := clusterProfileToShortName[clusterProfile]
	featureSetStr := featureSetName(featureSet)

	return fmt.Sprintf("featureGate-%s-%s-%s.yaml", group.versionRangeString(), clusterProfileShort, featureSetStr)
}

func featureSetName(featureSetName configv1.FeatureSet) string {
	if featureSetName == "" {
		return "Default"
	}
	return string(featureSetName)
}

type profileFeatureSet struct {
	clusterProfile features.ClusterProfileName
	featureSetName configv1.FeatureSet
}

// WriteFeatureSets holds values to drive the render command.
type WriteFeatureSets struct {
	PayloadVersion string
	AssetOutputDir string
}

func (o *WriteFeatureSets) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.PayloadVersion, "payload-version", o.PayloadVersion, "Version that will eventually be placed into ClusterOperator.status.  This normally comes from the CVO set via env var: OPERATOR_IMAGE_VERSION.")
	fs.StringVar(&o.AssetOutputDir, "asset-output-dir", o.AssetOutputDir, "Output path for rendered manifests.")
}

// Validate verifies the inputs.
func (o *WriteFeatureSets) Validate() error {
	return nil
}

// Complete fills in missing values before command execution.
func (o *WriteFeatureSets) Complete() error {
	return nil
}

// Run contains the logic of the render command.
func (o *WriteFeatureSets) Run() error {
	err := os.MkdirAll(o.AssetOutputDir, 0755)
	if err != nil {
		return err
	}

	statusByVersionByClusterProfileByFeatureSet := features.AllFeatureSets()
	newLegacyFeatureGates := sets.Set[string]{}

	// First, collect legacy feature gates from all versions (unchanged from original logic)
	for _, statusByClusterProfileByFeatureSet := range statusByVersionByClusterProfileByFeatureSet {
		for _, byFeatureSet := range statusByClusterProfileByFeatureSet {
			for _, featureGateStatuses := range byFeatureSet {
				for _, curr := range featureGateStatuses.Enabled {
					if curr.EnhancementPR == "FeatureGate predates 4.18" {
						newLegacyFeatureGates.Insert(string(curr.FeatureGateAttributes.Name))
					}
				}
				for _, curr := range featureGateStatuses.Disabled {
					if curr.EnhancementPR == "FeatureGate predates 4.18" {
						newLegacyFeatureGates.Insert(string(curr.FeatureGateAttributes.Name))
					}
				}
			}
		}
	}

	// Create consolidated groups by combining cluster profiles and feature sets
	consolidatedGroups := make(map[profileFeatureSet][]versionGroup)

	// Gather all cluster profiles and feature sets to iterate over
	allClusterProfiles := features.AllClusterProfiles
	allFeatureSets := configv1.AllFixedFeatureSets

	for _, clusterProfile := range allClusterProfiles {
		for _, featureSetName := range allFeatureSets {
			key := profileFeatureSet{
				clusterProfile: clusterProfile,
				featureSetName: featureSetName,
			}
			groups := consolidateVersions(statusByVersionByClusterProfileByFeatureSet, clusterProfile, featureSetName)
			if len(groups) > 0 {
				consolidatedGroups[key] = groups
			}
		}
	}

	// Generate files for each consolidated group
	for groupKey, versionGroups := range consolidatedGroups {
		for _, group := range versionGroups {
			currentDetails := FeaturesGateDetailsFromFeatureSets(group.content, o.PayloadVersion)

			featureGateInstance := &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						// we can't do this because it will get the manifest included by the CVO and that isn't what we want
						// this makes it interesting to indicate which cluster-profile the cluster-config-operator should use
						//string(clusterProfile): "true",
						string(groupKey.clusterProfile):      "false-except-for-the-config-operator",
						"release.openshift.io/major-version": strings.Join(group.versions(), ","),
						"release.openshift.io/feature-set":   featureSetName(groupKey.featureSetName),
					},
				},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: groupKey.featureSetName,
					},
				},
				Status: configv1.FeatureGateStatus{
					FeatureGates: []configv1.FeatureGateDetails{
						*currentDetails,
					},
				},
			}

			featureGateOutBytes := writeFeatureGateV1OrDie(featureGateInstance)
			featureSetFileName := generateConsolidatedFilename(group, groupKey.clusterProfile, groupKey.featureSetName)

			destFile := filepath.Join(o.AssetOutputDir, featureSetFileName)
			if err := os.WriteFile(destFile, []byte(featureGateOutBytes), 0644); err != nil {
				return fmt.Errorf("error writing FeatureGate manifest: %w", err)
			}
		}
	}

	if illegalNewFeatureGates := newLegacyFeatureGates.Difference(legacyFeatureGates); len(illegalNewFeatureGates) > 0 {
		shameText := `If you are reading this, it is because you have tried to bypass the enhancement check for a new feature and caught by the backstop check.
Take the time to write up what you're going to accomplish and how we'll know it works in https://github.com/openshift/enhancements/.
If we don't know what we're trying to build and we don't know how to confirm it works as designed, we cannot expect to be successful delivering new features.
Complaints can be directed to @deads2k, approvers must not merge this PR.`
		err := fmt.Errorf(shameText+"\nFeatureGates: %v", strings.Join(illegalNewFeatureGates.UnsortedList(), ", "))
		return err

	}

	return nil
}
