package utils

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Gets the label value from a given object or returns an error if it is not found.
func GetRequiredLabelValueFromObject(obj metav1.Object, key string) (string, error) {
	val, err := getRequiredKeyFromMap(obj.GetLabels(), key)
	if err != nil {
		return "", fmt.Errorf("could not get label for object %s: %w", obj.GetName(), err)
	}

	return val, nil
}

// Gets the annotation value from a given object or returns an error if it is not found.
func GetRequiredAnnotationValueFromObject(obj metav1.Object, key string) (string, error) {
	val, err := getRequiredKeyFromMap(obj.GetAnnotations(), key)
	if err != nil {
		return "", fmt.Errorf("could not get annotation for object %s: %w", obj.GetName(), err)
	}

	return val, nil
}

// Performs the map lookup and returns the required key or an error if the map
// is uninitialized or the key is not found.
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
