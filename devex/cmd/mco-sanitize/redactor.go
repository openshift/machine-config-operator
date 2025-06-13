package main

import (
	"errors"
	"fmt"
	"slices"
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
		var changed bool
		var err error
		for idx, item := range k8sResources {
			var resourceChanged bool
			resourceChanged, err = r.redactResource(value[idx], &item, marshaler)
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
	visitor := NewVisitor(inputData)
	for _, path := range targetConfig.paths {
		if err := visitor.Visit(path, redactFn, marshaler); err != nil {
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

func redactFn(node *structVisitorNode, arg interface{}) error {
	if node.value == nil {
		return nil
	}

	var redactSource string
	switch node.value.(type) {
	case []interface{}, map[string]interface{}:
		rawBytes, err := arg.(contentMarshaler)(node.value)
		if err != nil {
			return err
		}
		redactSource = string(rawBytes)
	case string:
		redactSource = node.value.(string)
	default:
		redactSource = fmt.Sprintf("%v", node.value)
	}
	redactInfo := map[string]interface{}{
		"_REDACTED": "This field has been redacted",
		"length":    len(redactSource),
	}
	return node.replace(redactInfo)
}

type structVisitorFn func(node *structVisitorNode, arg interface{}) error

type StructVisitor struct {
	root *structVisitorNode
}

func NewVisitor(content interface{}) *StructVisitor {
	return &StructVisitor{root: &structVisitorNode{value: content}}
}

func (r *StructVisitor) Visit(path string, fn structVisitorFn, arg interface{}) error {
	return recursiveWalk(r.root, strings.Split(path, "."), fn, arg)
}
func (r *StructVisitor) Interface() interface{} {
	return r.root.value
}

type structVisitorNode struct {
	value  interface{}
	key    string
	index  int
	parent *structVisitorNode
}

func recursiveWalk(node *structVisitorNode, path []string, fn structVisitorFn, arg interface{}) error {
	if len(path) == 0 {
		return fn(node, arg)
	}
	key := path[0]
	switch node.value.(type) {
	case []interface{}:
		for idx, _ := range node.value.([]interface{}) {
			newNode := &structVisitorNode{
				value:  node.value.([]interface{})[idx],
				index:  idx,
				parent: node,
			}
			if err := recursiveWalk(newNode, path, fn, arg); err != nil {
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
			value:  val,
			key:    key,
			parent: node,
		}
		return recursiveWalk(&newNode, path[1:], fn, arg)
	default:
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
