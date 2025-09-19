package main

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type Redactor interface {
	Redact(inputData interface{}, k8sResources []KubernetesMetaResource, marshaler contentMarshaler) (bool, error)
}

type redactTargetConfig struct {
	namespaces []string
	apiVersion string
	paths      []string
}

type KubernetesRedactor struct {
	targetConfigs map[string]redactTargetConfig
}

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
			apiVersion: cfg.ApiVersion,
			namespaces: cfg.Namespaces,
		}
	}
	return &KubernetesRedactor{targetConfigs: targetConfigs}, nil
}

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

		var changed bool
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

func (r *KubernetesRedactor) redactResource(inputData interface{}, k8sResource *KubernetesMetaResource, marshaler contentMarshaler) (bool, error) {
	targetConfig := r.getRedactConfig(k8sResource)
	if targetConfig == nil {
		return false, nil
	}
	for _, path := range targetConfig.paths {
		if err := recursiveWalk(&structVisitorNode{value: inputData, marshaler: marshaler}, strings.Split(path, ".")); err != nil {
			return true, fmt.Errorf("error visiting resource %#v with path %s: %w", k8sResource, path, err)
		}
	}
	return true, nil
}

func (r *KubernetesRedactor) getRedactConfig(k8sResource *KubernetesMetaResource) *redactTargetConfig {
	targetConfig, ok := r.targetConfigs[k8sResource.Kind]
	if !ok {
		return nil
	}
	if targetConfig.apiVersion != "" && targetConfig.apiVersion != k8sResource.ApiVersion {
		return nil
	}
	if targetConfig.namespaces != nil && len(targetConfig.namespaces) > 0 && !slices.Contains(targetConfig.namespaces, k8sResource.Metadata.Namespace) {
		return nil
	}
	return &targetConfig
}

type structVisitorNode struct {
	value     interface{}
	key       string
	index     int
	parent    *structVisitorNode
	marshaler contentMarshaler
}

func recursiveWalk(node *structVisitorNode, path []string) error {
	if len(path) == 0 {
		return node.redact()
	}
	key := path[0]
	switch node.value.(type) {
	case []interface{}:
		for idx, _ := range node.value.([]interface{}) {
			newNode := &structVisitorNode{
				value:     node.value.([]interface{})[idx],
				index:     idx,
				parent:    node,
				marshaler: node.marshaler,
			}
			if err := recursiveWalk(newNode, path); err != nil {
				return err
			}
		}
		return nil
	case map[string]interface{}:
		val := node.value.(map[string]interface{})[key]
		if val == nil {
			// No matching key, skip
			return nil
		}
		newNode := structVisitorNode{
			value:     val,
			key:       key,
			parent:    node,
			marshaler: node.marshaler,
		}
		return recursiveWalk(&newNode, path[1:])
	default:
		// The path is pointing to an array value to sanitize that is a primitive
		if len(path) == 1 && node.key == "" {
			pathIndex, err := strconv.Atoi(key)
			if err == nil && pathIndex == node.index {
				return node.redact()
			} else if err == nil {
				// The node is not the element pointed by the path
				return nil
			}
		}
		return fmt.Errorf("invalid query path. A path cannot traverse a primitive type")
	}
}

func (n *structVisitorNode) replace(value interface{}) error {
	// If no parent we are replacing at root level
	if n.parent == nil {
		n.value = value
		return nil
	}

	switch n.parent.value.(type) {
	case []interface{}:
		n.parent.value.([]interface{})[n.index] = value
		return nil
	case map[string]interface{}:
		n.parent.value.(map[string]interface{})[n.key] = value
		return nil
	default:
		// Unreachable condition: It's not possible to reach this condition
		// cause unmarshalling takes care of making child elements child of
		// a map or slice in yaml/json
		return fmt.Errorf("error redacting kubernetes resource")
	}
}

func (n *structVisitorNode) redact() error {
	if n.value == nil {
		return nil
	}

	var redactSource string
	switch n.value.(type) {
	case []interface{}, map[string]interface{}:
		rawBytes, err := n.marshaler(n.value)
		if err != nil {
			return err
		}
		redactSource = string(rawBytes)
	case string:
		redactSource = n.value.(string)
	default:
		redactSource = fmt.Sprintf("%v", n.value)
	}
	redactInfo := map[string]interface{}{
		"_REDACTED": "This field has been redacted",
		"length":    len(redactSource),
	}
	return n.replace(redactInfo)
}
