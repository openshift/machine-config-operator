package render

import (
	"flag"
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
	"path/filepath"
)

var (
	clusterProfileToShortName = map[features.ClusterProfileName]string{
		features.Hypershift:  "Hypershift",
		features.SelfManaged: "SelfManagedHA",
	}
)

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

	statusByClusterProfileByFeatureSet := features.AllFeatureSets()
	for clusterProfile, byFeatureSet := range statusByClusterProfileByFeatureSet {
		for featureSetName, featureGateStatuses := range byFeatureSet {
			currentDetails := FeaturesGateDetailsFromFeatureSets(featureGateStatuses, o.PayloadVersion)

			featureGateInstance := &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster",
					Annotations: map[string]string{
						// we can't do this because it will get the manifest included by the CVO and that isn't what we want
						// this makes it interesting to indicate which cluster-profile the cluster-config-operator should use
						//string(clusterProfile): "true",
						string(clusterProfile): "false-except-for-the-config-operator",
					},
				},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: featureSetName,
					},
				},
				Status: configv1.FeatureGateStatus{
					FeatureGates: []configv1.FeatureGateDetails{
						*currentDetails,
					},
				},
			}

			featureGateOutBytes := writeFeatureGateV1OrDie(featureGateInstance)
			featureSetFileName := fmt.Sprintf("featureGate-%s-%s.yaml", clusterProfileToShortName[clusterProfile], featureSetName)
			if len(featureSetName) == 0 {
				featureSetFileName = fmt.Sprintf("featureGate-%s-%s.yaml", clusterProfileToShortName[clusterProfile], "Default")
			}

			destFile := filepath.Join(o.AssetOutputDir, featureSetFileName)
			if err := os.WriteFile(destFile, []byte(featureGateOutBytes), 0644); err != nil {
				return fmt.Errorf("error writing FeatureGate manifest: %w", err)
			}
		}
	}

	return nil
}
