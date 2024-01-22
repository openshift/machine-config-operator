package featuregates

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/status"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

const FeatureVersionName = "feature-gates"

// FeatureGateController is responsible for setting usable FeatureGates on features.config.openshift.io/cluster
type FeatureGateController struct {
	processVersion       string
	featureGatesClient   configv1client.FeatureGatesGetter
	featureGatesLister   configlistersv1.FeatureGateLister
	clusterVersionLister configlistersv1.ClusterVersionLister
	// for unit testing
	featureSetMap map[configv1.FeatureSet]*configv1.FeatureGateEnabledDisabled

	versionRecorder status.VersionGetter
	eventRecorder   events.Recorder
}

// NewController returns a new FeatureGateController.
func NewFeatureGateController(
	featureGateDetails map[configv1.FeatureSet]*configv1.FeatureGateEnabledDisabled,
	operatorClient operatorv1helpers.OperatorClient,
	processVersion string,
	featureGatesClient configv1client.FeatureGatesGetter, featureGatesInformer v1.FeatureGateInformer,
	clusterVersionInformer v1.ClusterVersionInformer,
	versionRecorder status.VersionGetter,
	eventRecorder events.Recorder) factory.Controller {
	c := &FeatureGateController{
		processVersion:       processVersion,
		featureGatesClient:   featureGatesClient,
		featureGatesLister:   featureGatesInformer.Lister(),
		clusterVersionLister: clusterVersionInformer.Lister(),
		featureSetMap:        featureGateDetails,
		versionRecorder:      versionRecorder,
		eventRecorder:        eventRecorder,
	}

	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			featureGatesInformer.Informer(),
			clusterVersionInformer.Informer(),
		).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("FeatureGateController", eventRecorder)
}

func (c FeatureGateController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	featureGates, err := c.featureGatesLister.Get("cluster")
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to get FeatureGate: %w", err)
	}

	clusterVersion, err := c.clusterVersionLister.Get("version")
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to get ClusterVersion: %w", err)
	}

	knownVersions := sets.NewString(c.processVersion)
	for _, cvoVersion := range clusterVersion.Status.History {
		knownVersions.Insert(cvoVersion.Version)
	}

	currentDetails, err := FeaturesGateDetailsFromFeatureSets(c.featureSetMap, featureGates, c.processVersion)
	if err != nil {
		return fmt.Errorf("unable to determine FeatureGateDetails from FeatureSets: %w", err)
	}
	// desiredFeatureGates will include first, the current version's feature gates
	// then all the historical featuregates in order, removing those for versions not in the CVO history.
	desiredFeatureGates := []configv1.FeatureGateDetails{*currentDetails}

	for i := range featureGates.Status.FeatureGates {
		featureGateValues := featureGates.Status.FeatureGates[i]
		if featureGateValues.Version == c.processVersion {
			// we already added our processVersion
			continue
		}
		if !knownVersions.Has(featureGateValues.Version) {
			continue
		}
		desiredFeatureGates = append(desiredFeatureGates, featureGateValues)
	}

	if reflect.DeepEqual(desiredFeatureGates, featureGates.Status.FeatureGates) {
		// no update, confirm in the clusteroperator that the version has been achieved.
		c.versionRecorder.SetVersion(
			FeatureVersionName,
			c.processVersion,
		)

		return nil
	}

	// TODO, this looks ripe for SSA.
	toWrite := featureGates.DeepCopy()
	toWrite.Status.FeatureGates = desiredFeatureGates
	if _, err := c.featureGatesClient.FeatureGates().UpdateStatus(ctx, toWrite, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("unable to update FeatureGate status: %w", err)
	}

	enabled, disabled := []string{}, []string{}
	for _, curr := range currentDetails.Enabled {
		enabled = append(enabled, string(curr.Name))
	}
	for _, curr := range currentDetails.Disabled {
		disabled = append(disabled, string(curr.Name))
	}
	c.eventRecorder.Eventf(
		"FeatureGateUpdate", "FeatureSet=%q, Version=%q, Enabled=%q, Disabled=%q",
		toWrite.Spec.FeatureSet, c.processVersion, strings.Join(enabled, ","), strings.Join(disabled, ","))
	// on successful write, we're at the correct level
	c.versionRecorder.SetVersion(
		FeatureVersionName,
		c.processVersion,
	)

	return nil
}

func featuresGatesFromFeatureSets(knownFeatureSets map[configv1.FeatureSet]*configv1.FeatureGateEnabledDisabled, featureGates *configv1.FeatureGate) ([]configv1.FeatureGateName, []configv1.FeatureGateName, error) {
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

func toFeatureGateNames(in []configv1.FeatureGateDescription) []configv1.FeatureGateName {
	out := []configv1.FeatureGateName{}
	for _, curr := range in {
		out = append(out, curr.FeatureGateAttributes.Name)
	}

	return out
}

// completeFeatureGates identifies every known feature and ensures that is explicitly on or explicitly off
func completeFeatureGates(knownFeatureSets map[configv1.FeatureSet]*configv1.FeatureGateEnabledDisabled, enabled, disabled []configv1.FeatureGateName) ([]configv1.FeatureGateName, []configv1.FeatureGateName) {
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

func FeaturesGateDetailsFromFeatureSets(featureSetMap map[configv1.FeatureSet]*configv1.FeatureGateEnabledDisabled, featureGates *configv1.FeatureGate, currentVersion string) (*configv1.FeatureGateDetails, error) {
	enabled, disabled, err := featuresGatesFromFeatureSets(featureSetMap, featureGates)
	if err != nil {
		return nil, err
	}
	currentDetails := configv1.FeatureGateDetails{
		Version: currentVersion,
	}
	for _, gateName := range enabled {
		currentDetails.Enabled = append(currentDetails.Enabled, configv1.FeatureGateAttributes{
			Name: gateName,
		})
	}
	for _, gateName := range disabled {
		currentDetails.Disabled = append(currentDetails.Disabled, configv1.FeatureGateAttributes{
			Name: gateName,
		})
	}

	// sort for stability
	sort.Sort(byName(currentDetails.Enabled))
	sort.Sort(byName(currentDetails.Disabled))

	return &currentDetails, nil
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
