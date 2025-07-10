package util

import (
	"strings"

	o "github.com/onsi/gomega"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// DeleteLabelsFromSpecificResource deletes the custom labels from the specific resource
func DeleteLabelsFromSpecificResource(oc *CLI, resourceKindAndName, resourceNamespace string, labelNames ...string) (string, error) {
	var cargs []string
	if resourceNamespace != "" {
		cargs = append(cargs, "-n", resourceNamespace)
	}
	cargs = append(cargs, resourceKindAndName)
	cargs = append(cargs, StringsSliceElementsAddSuffix(labelNames, "-")...)
	return oc.AsAdmin().WithoutNamespace().Run("label").Args(cargs...).Output()
}

// AddLabelsToSpecificResource adds the custom labels to the specific resource
func AddLabelsToSpecificResource(oc *CLI, resourceKindAndName, resourceNamespace string, labels ...string) (string, error) {
	var cargs []string
	if resourceNamespace != "" {
		cargs = append(cargs, "-n", resourceNamespace)
	}
	cargs = append(cargs, resourceKindAndName)
	cargs = append(cargs, labels...)
	cargs = append(cargs, "--overwrite")
	return oc.AsAdmin().WithoutNamespace().Run("label").Args(cargs...).Output()
}

// GetResourceSpecificLabelValue gets the specified label value from the resource and label name
func GetResourceSpecificLabelValue(oc *CLI, resourceKindAndName, resourceNamespace, labelName string) (string, error) {
	var cargs []string
	if resourceNamespace != "" {
		cargs = append(cargs, "-n", resourceNamespace)
	}
	cargs = append(cargs, resourceKindAndName, "-o=jsonpath={.metadata.labels."+labelName+"}")
	return oc.AsAdmin().WithoutNamespace().Run("get").Args(cargs...).Output()
}

// AddAnnotationsToSpecificResource adds the custom annotations to the specific resource
func AddAnnotationsToSpecificResource(oc *CLI, resourceKindAndName, resourceNamespace string, annotations ...string) (string, error) {
	var cargs []string
	if resourceNamespace != "" {
		cargs = append(cargs, "-n", resourceNamespace)
	}
	cargs = append(cargs, resourceKindAndName)
	cargs = append(cargs, annotations...)
	cargs = append(cargs, "--overwrite")
	return oc.AsAdmin().WithoutNamespace().Run("annotate").Args(cargs...).Output()
}

// RemoveAnnotationFromSpecificResource removes the specified annotation from the resource
func RemoveAnnotationFromSpecificResource(oc *CLI, resourceKindAndName, resourceNamespace, annotationName string) (string, error) {
	var cargs []string
	if resourceNamespace != "" {
		cargs = append(cargs, "-n", resourceNamespace)
	}
	cargs = append(cargs, resourceKindAndName, annotationName+"-")
	return oc.AsAdmin().WithoutNamespace().Run("annotate").Args(cargs...).Output()
}

// GetAnnotationsFromSpecificResource gets the annotations from the specific resource
func GetAnnotationsFromSpecificResource(oc *CLI, resourceKindAndName, resourceNamespace string) ([]string, error) {
	var cargs []string
	if resourceNamespace != "" {
		cargs = append(cargs, "-n", resourceNamespace)
	}
	cargs = append(cargs, resourceKindAndName, "--list")
	annotationsStr, getAnnotationsErr := oc.AsAdmin().WithoutNamespace().Run("annotate").Args(cargs...).Output()
	if getAnnotationsErr != nil {
		e2e.Logf(`Failed to get annotations from /%s in namespace %s: "%v"`, resourceKindAndName, resourceNamespace, getAnnotationsErr)
	}
	return strings.Fields(annotationsStr), getAnnotationsErr
}

// IsSpecifiedAnnotationKeyExist judges whether the specified annotationKey exist on the resource
func IsSpecifiedAnnotationKeyExist(oc *CLI, resourceKindAndName, resourceNamespace, annotationKey string) bool {
	resourceAnnotations, getResourceAnnotationsErr := GetAnnotationsFromSpecificResource(oc, resourceKindAndName, resourceNamespace)
	o.Expect(getResourceAnnotationsErr).NotTo(o.HaveOccurred())
	isAnnotationKeyExist, _ := StringsSliceElementsHasPrefix(resourceAnnotations, annotationKey+"=", true)
	return isAnnotationKeyExist
}

// StringsSliceContains judges whether the strings Slice contains specific element, return bool and the first matched index
// If no matched return (false, 0)
func StringsSliceContains(stringsSlice []string, element string) (bool, int) {
	for index, strElement := range stringsSlice {
		if strElement == element {
			return true, index
		}
	}
	return false, 0
}

// StringsSliceElementsHasPrefix judges whether the strings Slice contains an element which has the specific prefix
// returns bool and the first matched index
// sequential order: -> sequentialFlag: "true"
// reverse order:    -> sequentialFlag: "false"
// If no matched return (false, 0)
func StringsSliceElementsHasPrefix(stringsSlice []string, elementPrefix string, sequentialFlag bool) (bool, int) {
	if len(stringsSlice) == 0 {
		return false, 0
	}
	if sequentialFlag {
		for index, strElement := range stringsSlice {
			if strings.HasPrefix(strElement, elementPrefix) {
				return true, index
			}
		}
	} else {
		for i := len(stringsSlice) - 1; i >= 0; i-- {
			if strings.HasPrefix(stringsSlice[i], elementPrefix) {
				return true, i
			}
		}
	}
	return false, 0
}

// StringsSliceElementsAddSuffix returns a new string slice all elements with the specific suffix added
func StringsSliceElementsAddSuffix(stringsSlice []string, suffix string) []string {
	if len(stringsSlice) == 0 {
		return []string{}
	}
	var newStringsSlice = make([]string, 0, 10)
	for _, element := range stringsSlice {
		newStringsSlice = append(newStringsSlice, element+suffix)
	}
	return newStringsSlice
}
