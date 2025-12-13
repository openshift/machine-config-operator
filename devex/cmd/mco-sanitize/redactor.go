package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Redactor defines the interface for sanitizing sensitive data in Kubernetes resources.
// Implementations should redact specified fields while preserving the structure and
// metadata necessary for analysis and debugging purposes.
type Redactor interface {
	// Redact sanitizes the given inputData based on the provided Kubernetes resource metadata.
	// The inputData should be the unmarshalled representation of the k8sResources.
	// A content marshaler is required to calculate the length of redacted content.
	// Returns true if any redaction was performed, false otherwise.
	// Returns an error if the redaction process encounters any issues.
	Redact(inputData interface{}, k8sResources []KubernetesMetaResource, marshaler contentMarshaler) (bool, error)
}

// redactTargetConfig holds redaction configuration for a specific Kubernetes resource type
type redactTargetConfig struct {
	namespaces []string
	apiVersion string
	paths      []string
}

// KubernetesRedactor sanitizes Kubernetes resources by redacting sensitive fields configured
// by a given ConfigRedact
type KubernetesRedactor struct {
	targetConfigs map[string]redactTargetConfig
}

// NewKubernetesRedactor creates a new KubernetesRedactor based on a given
// set of ConfigRedact. Returns an error if configs is nil or if any config
// has an empty Kind field.
func NewKubernetesRedactor(configs []ConfigRedact) (*KubernetesRedactor, error) {
	if configs == nil {
		return nil, errors.New("invalid redact configuration")
	}
	targetConfigs := make(map[string]redactTargetConfig)
	for _, cfg := range configs {
		if cfg.Kind == "" {
			return nil, errors.New("invalid redact configuration. Empty kind")
		}
		targetConfigs[cfg.Kind] = redactTargetConfig{
			paths:      cfg.Paths,
			apiVersion: cfg.APIVersion,
			namespaces: cfg.Namespaces,
		}
	}
	return &KubernetesRedactor{targetConfigs: targetConfigs}, nil
}

// Redact sanitizes the given inputData, that corresponds to the unmarshalled representation of the given
// k8sResources.
// To perform the sanitization length calculation, a content marshaller is required. The marshaller should match
// with the unmarshaller used to retrieve the inputData.
// Only kubernetes resources targeted by the redactor configuration will be sanitized, others won't be touched.
// This function returns a true boolean indicating if at least one sanitization was done, otherwise it returns false.
// If any error is encountered while processing the input data, an error will be returned.
func (r *KubernetesRedactor) Redact(inputData interface{}, k8sResources []KubernetesMetaResource, marshaler contentMarshaler) (bool, error) {
	if len(k8sResources) == 0 {
		return false, nil
	}

	switch value := inputData.(type) {
	case map[string]interface{}:
		return r.redactResource(inputData, &k8sResources[0], marshaler)

	case []map[string]interface{}:
		if len(value) != len(k8sResources) {
			return false, fmt.Errorf("mismatch between input data length (%d) and resources length (%d)",
				len(value), len(k8sResources))
		}

		changed := false
		for idx, item := range k8sResources {
			var resourceChanged bool
			resourceChanged, err := r.redactResource(value[idx], &item, marshaler)
			if err != nil {
				return changed, err
			}
			if resourceChanged {
				changed = true
			}
		}
		return changed, nil
	}
	return false, nil
}

// redactResource processes a single Kubernetes resource for redaction based on the redactor's configuration.
// It checks if the resource matches any configured redaction rules and applies redaction to all matching paths.
// Returns true if any changes were made to the input data, false otherwise.
func (r *KubernetesRedactor) redactResource(inputData interface{}, k8sResource *KubernetesMetaResource, marshaler contentMarshaler) (bool, error) {
	targetConfig := r.getRedactConfig(k8sResource)
	if targetConfig == nil {
		return false, nil
	}
	changed := false
	for _, path := range targetConfig.paths {
		iterChanged, err := recursiveWalk(&visitorNode{value: inputData, marshaler: marshaler}, strings.Split(path, "."))
		if err != nil {
			return changed, fmt.Errorf("error visiting resource %#v with path %s: %w", k8sResource, path, err)
		}
		if iterChanged {
			changed = true
		}
	}
	return changed, nil
}

// getRedactConfig retrieves the redaction configuration for a given Kubernetes resource.
// It matches the resource against configured rules based on Kind, APIVersion, and Namespace.
// Returns nil if no matching configuration is found or if the resource doesn't meet the criteria.
func (r *KubernetesRedactor) getRedactConfig(k8sResource *KubernetesMetaResource) *redactTargetConfig {
	targetConfig, ok := r.targetConfigs[k8sResource.Kind]
	if !ok {
		return nil
	}
	if targetConfig.apiVersion != "" && targetConfig.apiVersion != k8sResource.APIVersion {
		return nil
	}
	if targetConfig.namespaces != nil && len(targetConfig.namespaces) > 0 && !slices.Contains(targetConfig.namespaces, k8sResource.Metadata.Namespace) {
		return nil
	}
	return &targetConfig
}

// recursiveWalk traverses the data structure following the specified path and performs redaction.
// It handles three types of nodes: arrays, maps and primitives.
// Returns true if any redaction was performed, false otherwise.
func recursiveWalk(node *visitorNode, path []string) (bool, error) {
	if len(path) == 0 {
		return true, node.redact()
	}
	key := path[0]
	switch value := node.value.(type) {
	case []interface{}:
		// The current node value is an array
		// In arrays we only support * (give me all) or indexing (give me a zero base index)
		if key == "*" {
			// Easy case, sanitize everything inside the array
			changed := false
			for idx := range value {
				iterChanged, err := recursiveWalk(node.newArrayChild(idx), path[1:])
				if err != nil {
					return false, err
				}
				if iterChanged {
					changed = true
				}
			}
			return changed, nil
		}
		// The path provides the index to pick. Transverse only that index
		pathIndex, err := strconv.Atoi(key)
		if err != nil {
			return false, errors.New("redact path uses array indexing at a path level that is not an array")
		}
		newNode := node.newArrayChild(pathIndex)
		if newNode == nil {
			return false, nil
		}
		return recursiveWalk(newNode, path[1:])
	case map[string]interface{}:
		// Map case. newObjectChild returns nil if the key doesn't exist which should make us
		// break the transversing path
		newNode := node.newObjectChild(key)
		if newNode == nil {
			return false, nil
		}
		return recursiveWalk(newNode, path[1:])
	default:
		// It's not possible to reach this path unless the user configures a path
		// that enforces the sanitized to continue transversing through something
		// that is not an array neither a map
		return false, fmt.Errorf("invalid query path. A path cannot traverse a primitive type")
	}
}

// visitorNode represents a node in the object graph traversal during redaction.
type visitorNode struct {
	value     interface{}      // The current value at this node
	key       string           // The key name if this node is a map value
	index     int              // The array index if this node is an array element
	parent    *visitorNode     // Reference to the parent node
	marshaler contentMarshaler // Marshaler function
}

// newArrayChild creates a new visitorNode for an array element at the specified index.
// This method assumes the current node's value is an array and returns a new node
// representing the child element at the given index. Returns nil if the index is
// out of bounds (greater than or equal to the array length).
func (n *visitorNode) newArrayChild(index int) *visitorNode {
	if len(n.value.([]interface{})) <= index {
		// No matching element, skip
		return nil
	}
	return &visitorNode{
		value:     n.value.([]interface{})[index],
		index:     index,
		parent:    n,
		marshaler: n.marshaler,
	}
}

// newObjectChild creates a new visitorNode for a map value with the specified key.
// This method assumes the current node's value is a map and returns a new node
// representing the child value for the given key. Returns nil if the key doesn't
// exist or the value is nil.
func (n *visitorNode) newObjectChild(key string) *visitorNode {
	val, ok := n.value.(map[string]interface{})[key]
	if !ok || val == nil {
		// No matching key, skip
		return nil
	}
	return &visitorNode{
		value:     val,
		key:       key,
		parent:    n,
		marshaler: n.marshaler,
	}
}

// replace replaces the current node's value in its parent data structure.
// If the node has no parent (root level), it updates its own value.
// For array elements, it updates the value at the node's index.
// For map values, it updates the value for the node's key.
func (n *visitorNode) replace(value interface{}) error {
	// If no parent we are replacing at root level
	if n.parent == nil {
		n.value = value
		return nil
	}

	switch val := n.parent.value.(type) {
	case []interface{}:
		val[n.index] = value
		return nil
	case map[string]interface{}:
		val[n.key] = value
		return nil
	default:
		// Unreachable condition: It's not possible to reach this condition
		// cause unmarshalling takes care of making child elements child of
		// a map or slice in yaml/json
		return fmt.Errorf("error redacting kubernetes resource")
	}
}

// redact replaces the current node's value with redaction information.
// For complex types (arrays/maps), it marshals the value to determine its length.
// For strings, it uses the string directly. For other primitives, it converts to string.
// The redacted value is replaced with a map containing a redaction message and the
// original data length for audit purposes.
func (n *visitorNode) redact() error {
	if n.value == nil {
		return nil
	}

	var redactSource string
	switch val := n.value.(type) {
	case []interface{}, map[string]interface{}:
		rawBytes, err := n.marshaler(val)
		if err != nil {
			return err
		}
		redactSource = string(rawBytes)
	case string:
		redactSource = val
	default:
		redactSource = fmt.Sprintf("%v", val)
	}
	redactInfo := map[string]interface{}{
		"_REDACTED": "This field has been redacted",
		"length":    len(redactSource),
	}
	return n.replace(redactInfo)
}
