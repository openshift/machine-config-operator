package buildrequest

import (
	"fmt"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type resourceAnnotationKey string

// Resource request and limit annotation keys
const (
	cpuResourceRequestAnnotationKey              resourceAnnotationKey = "machineconfiguration.openshift.io/cpu-request"
	cpuResourceLimitAnnotationKey                resourceAnnotationKey = "machineconfiguration.openshift.io/cpu-limit"
	memoryResourceRequestAnnotationKey           resourceAnnotationKey = "machineconfiguration.openshift.io/memory-request"
	memoryResourceLimitAnnotationKey             resourceAnnotationKey = "machineconfiguration.openshift.io/memory-limit"
	storageResourceRequestAnnotationKey          resourceAnnotationKey = "machineconfiguration.openshift.io/storage-request"
	storageResourceLimitAnnotationKey            resourceAnnotationKey = "machineconfiguration.openshift.io/storage-limit"
	ephemeralStorageResourceRequestAnnotationKey resourceAnnotationKey = "machineconfiguration.openshift.io/ephemeral-storage-request"
	ephemeralStorageResourceLimitAnnotationKey   resourceAnnotationKey = "machineconfiguration.openshift.io/ephemeral-storage-limit"

	// Default CPU request value. Taken from the machine-os-builder deployment
	// spec.
	defaultCPURequest string = "20m"
	// Default memory request value. Taken from the machine-os-builder deployment
	// spec.
	defaultMemoryRequest string = "50Mi"
)

// Holds all of the functions required for getting the resource requests. This
// allows to keep these functions cleanly separated while simultaneously
// allowing a single linear path through them.
type resources struct {
	mosc *mcfgv1.MachineOSConfig
}

// The finalized resource requirements for both the builder container as well
// as the waiting container.
type resourceRequirements struct {
	builder  *corev1.ResourceRequirements
	defaults *corev1.ResourceRequirements
}

// The main entrypoint into getting the resource requirements for the
// BuildRequest.
func getResourceRequirements(mosc *mcfgv1.MachineOSConfig) (*resourceRequirements, error) {
	r := &resources{mosc: mosc}

	builder, err := r.getBuilderResourceRequirements()
	if err != nil {
		return nil, fmt.Errorf("could not get ResourceRequirements for builder: %w", err)
	}

	defaults, err := r.getDefaultResourceRequirements()
	if err != nil {
		return nil, fmt.Errorf("could not get ResourceRequiremnts for defaults: %w", err)
	}

	return &resourceRequirements{
		builder:  builder,
		defaults: defaults,
	}, nil
}

// Gets the resource requirements for the build pod from the MachineOSConfig
// annotations or falls back to the default hard-coded values we specified.
func (r *resources) getBuilderResourceRequirements() (*corev1.ResourceRequirements, error) {
	defaults, err := r.getDefaultResourceRequirements()
	if err != nil {
		return nil, err
	}

	userProvided, err := r.getUserProvidedResourceRequirements()
	if err != nil {
		return nil, fmt.Errorf("could not get user-provided ResourceRequirements: %w", err)
	}

	// If no user-provided values are found, return early.
	if userProvided == nil {
		return defaults, nil
	}

	// User-provided requests override the default requests.
	for key, val := range userProvided.Requests {
		defaults.Requests[key] = val
	}

	// User-provided limits override the default limits.
	for key, val := range userProvided.Limits {
		defaults.Limits[key] = val
	}

	return defaults, nil
}

// Gets the default resource requirements.
func (r *resources) getDefaultResourceRequirements() (*corev1.ResourceRequirements, error) {
	requestList, err := getResourceList(map[corev1.ResourceName]string{
		corev1.ResourceCPU:    defaultCPURequest,
		corev1.ResourceMemory: defaultMemoryRequest,
	})

	if err != nil {
		return nil, fmt.Errorf("cannot get RequestList for default resource requirements: %w", err)
	}

	return &corev1.ResourceRequirements{
		Requests: requestList,
		Limits:   corev1.ResourceList{},
	}, nil
}

// Gets the user-provided resource requirements, if any.
func (r *resources) getUserProvidedResourceRequirements() (*corev1.ResourceRequirements, error) {
	requests, err := r.getResourceListFromAnnotations(map[corev1.ResourceName]resourceAnnotationKey{
		corev1.ResourceCPU:              cpuResourceRequestAnnotationKey,
		corev1.ResourceMemory:           memoryResourceRequestAnnotationKey,
		corev1.ResourceStorage:          storageResourceRequestAnnotationKey,
		corev1.ResourceEphemeralStorage: ephemeralStorageResourceRequestAnnotationKey,
	})

	if err != nil {
		return nil, fmt.Errorf("could not get user-provided resource requests: %w", err)
	}

	limits, err := r.getResourceListFromAnnotations(map[corev1.ResourceName]resourceAnnotationKey{
		corev1.ResourceCPU:              cpuResourceLimitAnnotationKey,
		corev1.ResourceMemory:           memoryResourceLimitAnnotationKey,
		corev1.ResourceStorage:          storageResourceLimitAnnotationKey,
		corev1.ResourceEphemeralStorage: ephemeralStorageResourceLimitAnnotationKey,
	})

	if err != nil {
		return nil, fmt.Errorf("could not get user-provided resource limits: %w", err)
	}

	// If no user-provided resources are found, return nil here since there is
	// nothing further to do.
	if len(requests) == 0 && len(limits) == 0 {
		return nil, nil
	}

	return &corev1.ResourceRequirements{
		Requests: requests,
		Limits:   limits,
	}, nil
}

// Gets the ResourceList from the MachineOSConfig annotation.
func (r *resources) getResourceListFromAnnotations(in map[corev1.ResourceName]resourceAnnotationKey) (corev1.ResourceList, error) {
	out := corev1.ResourceList{}

	for name, annoKey := range in {
		val, ok := r.mosc.Annotations[string(annoKey)]
		if !ok {
			continue
		}

		qty, err := parseResourceQuantity(val)
		if err != nil {
			return nil, fmt.Errorf("could not parse annotation %q value %q: %w", annoKey, val, err)
		}

		out[name] = qty
	}

	return out, nil
}

// Parses the resource string for each resource name into a resource.Quantity
// and inserts it into a ResourceList. This is needed because the
// resource.Quantity values are private.
func getResourceList(in map[corev1.ResourceName]string) (corev1.ResourceList, error) {
	out := corev1.ResourceList{}

	for name, qtyStr := range in {
		qty, err := parseResourceQuantity(qtyStr)
		if err != nil {
			return nil, err
		}

		out[name] = qty
	}

	return out, nil
}

// Parses a string into a resource quantity. This was made into its own
// function for more consistent error wrapping.
func parseResourceQuantity(qtyStr string) (resource.Quantity, error) {
	qty, err := resource.ParseQuantity(qtyStr)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("could not parse %q into a resource.Quantity: %w", qtyStr, err)
	}

	return qty, nil
}
