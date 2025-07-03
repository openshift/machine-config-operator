package common

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const featureGatesConnectionTimeout = 1 * time.Minute

// FeatureGatesHandler Represents a common easy to use entity to interact with FeatureGates
type FeatureGatesHandler interface {
	// Connect Reached out the FeatureGates backed to fetch them.
	Connect(ctx context.Context) error

	// Enabled Checks if the given FeatureGate is enabled or not.
	// If the feature does not exist it always returns false.
	Enabled(configv1.FeatureGateName) bool

	// Exists Checks if the given FeatureGate is known or not.
	Exists(configv1.FeatureGateName) bool

	// KnownFeatures Fetches a list of the known features.
	KnownFeatures() []configv1.FeatureGateName
}

// FeatureGatesHandlerImpl Main implementation of the FeatureGatesHandler interface
type FeatureGatesHandlerImpl struct {
	features        map[configv1.FeatureGateName]bool
	fgAccess        featuregates.FeatureGateAccess
	connectTimeout  time.Duration
	connectionMutex sync.Mutex
	connectionDone  bool
}

// NewFeatureGatesAccessHandler Creates a FeatureGatesHandlerImpl from [featuregates.FeatureGateAccess]
// The given [featuregates.FeatureGateAccess] should already have a Go routine for its Run method.
func NewFeatureGatesAccessHandler(featureGateAccess featuregates.FeatureGateAccess) *FeatureGatesHandlerImpl {
	fgHandler := &FeatureGatesHandlerImpl{
		fgAccess:        featureGateAccess,
		features:        make(map[configv1.FeatureGateName]bool),
		connectTimeout:  featureGatesConnectionTimeout,
		connectionMutex: sync.Mutex{},
		connectionDone:  false,
	}
	return fgHandler
}

// NewFeatureGatesHardcodedHandler Creates a FeatureGatesHandlerImpl from known enabled and
// disable [configv1.FeatureGateName].
func NewFeatureGatesHardcodedHandler(enabled, disabled []configv1.FeatureGateName) *FeatureGatesHandlerImpl {
	fgHandler := NewFeatureGatesAccessHandler(featuregates.NewHardcodedFeatureGateAccess(enabled, disabled))
	if err := fgHandler.Connect(context.Background()); err != nil {
		panic(fmt.Errorf("hardcoded feature gate impossible failure: %v", err))
	}
	return fgHandler
}

// NewFeatureGatesCRHandlerImpl Creates a FeatureGatesHandlerImpl from a [configv1.FeatureGate] and a version.
// The function may return an error if the given version is not found.
func NewFeatureGatesCRHandlerImpl(featureGate *configv1.FeatureGate, desiredVersion string) (*FeatureGatesHandlerImpl, error) {
	fgAccess, err := featuregates.NewHardcodedFeatureGateAccessFromFeatureGate(featureGate, desiredVersion)
	if err != nil {
		return nil, err
	}
	fgHandler := NewFeatureGatesAccessHandler(fgAccess)
	if err = fgHandler.Connect(context.Background()); err != nil {
		panic(fmt.Errorf("hardcoded feature gate impossible failure: %v", err))
	}
	return fgHandler, nil
}

func (h *FeatureGatesHandlerImpl) Enabled(featureGate configv1.FeatureGateName) bool {
	enabled, ok := h.features[featureGate]
	if !ok {
		klog.Infof("FeatureGate check request for unknown feature:  %v", featureGate)
		return false
	}
	return enabled
}

func (h *FeatureGatesHandlerImpl) Exists(featureGate configv1.FeatureGateName) bool {
	_, ok := h.features[featureGate]
	return ok
}

func (h *FeatureGatesHandlerImpl) KnownFeatures() []configv1.FeatureGateName {
	return slices.Collect(maps.Keys(h.features))
}

func (h *FeatureGatesHandlerImpl) registerFeatureGates(features featuregates.FeatureGate) {
	var enabled []string
	var disabled []string
	for _, feature := range features.KnownFeatures() {
		if features.Enabled(feature) {
			enabled = append(enabled, string(feature))
			h.features[feature] = true
		} else {
			disabled = append(disabled, string(feature))
			h.features[feature] = false
		}
	}
	klog.Infof("FeatureGates initialized: enabled=%v, disabled=%v", enabled, disabled)
}

func (h *FeatureGatesHandlerImpl) Connect(ctx context.Context) error {
	h.connectionMutex.Lock()
	defer h.connectionMutex.Unlock()
	if h.connectionDone {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(h.connectTimeout):
		return fmt.Errorf("timed out waiting for FeatureGates to be ready")
	case <-h.fgAccess.InitialFeatureGatesObserved():
		fgs, err := h.fgAccess.CurrentFeatureGates()
		if err != nil {
			return fmt.Errorf("unable to get initial features: %v", err)
		}
		h.registerFeatureGates(fgs)
		h.connectionDone = true
		return nil
	}
}

// IsBootImageControllerRequired checks that the currently enabled feature gates and
// the platform of the cluster requires a boot image controller. If any errors are
// encountered, it will log them and return false.
func IsBootImageControllerRequired(ctx *ControllerContext) bool {
	configClient := ctx.ClientBuilder.ConfigClientOrDie("ensure-boot-image-infra-client")
	infra, err := configClient.ConfigV1().Infrastructures().Get(context.TODO(), "cluster", metav1.GetOptions{})
	if err != nil {
		klog.Errorf("unable to get infrastructures for boot image controller startup: %v", err)
		return false
	}
	if infra.Status.PlatformStatus == nil {
		klog.Errorf("unable to get infra.Status.PlatformStatus for boot image controller startup: %v", err)
		return false
	}
	return CheckBootImagePlatform(infra, ctx.FeatureGatesHandler)
}

// Current valid feature gate and platform combinations:
// GCP -> FeatureGateManagedBootImages
// AWS -> FeatureGateManagedBootImagesAWS
// vSphere -> FeatureGateManagedBootImagesvSphere
func CheckBootImagePlatform(infra *configv1.Infrastructure, fgHandler FeatureGatesHandler) bool {
	switch infra.Status.PlatformStatus.Type {
	case configv1.AWSPlatformType:
		return fgHandler.Enabled(features.FeatureGateManagedBootImagesAWS)
	case configv1.GCPPlatformType:
		return fgHandler.Enabled(features.FeatureGateManagedBootImages)
	case configv1.VSpherePlatformType:
		return fgHandler.Enabled(features.FeatureGateManagedBootImagesvSphere)
	}
	return false
}
