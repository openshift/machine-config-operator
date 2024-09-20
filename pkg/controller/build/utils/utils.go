package utils

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetRequiredLabelValueFromObject(obj metav1.Object, key string) (string, error) {
	val, err := getRequiredKeyFromMap(obj.GetLabels(), key)
	if err != nil {
		return "", fmt.Errorf("could not get label for object %s: %w", obj.GetName(), err)
	}

	return val, nil
}

func GetRequiredAnnotationValueFromObject(obj metav1.Object, key string) (string, error) {
	val, err := getRequiredKeyFromMap(obj.GetAnnotations(), key)
	if err != nil {
		return "", fmt.Errorf("could not get annotation for object %s: %w", obj.GetName(), err)
	}

	return val, nil
}

func getRequiredKeyFromMap(in map[string]string, key string) (string, error) {
	if in == nil {
		return "", fmt.Errorf("uninitialized map")
	}

	val, ok := in[key]
	if !ok || val == "" {
		return "", fmt.Errorf("required key %q missing", key)
	}

	return val, nil
}
