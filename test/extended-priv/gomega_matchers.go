package extended

import (
	"encoding/json"
	"fmt"

	g "github.com/onsi/ginkgo/v2"
	gomegamatchers "github.com/onsi/gomega/matchers"
	"github.com/onsi/gomega/types"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// struct implementing gomaega matcher interface
type conditionMatcher struct {
	conditionType string
	field         string
	expected      interface{}

	value            string
	expectedMatcher  types.GomegaMatcher
	currentCondition string // stores the current condition being checked, so that it can be displayed in the error message if the check fails
}

// Match checks it the condition with the given type has the right value in the given field.
func (matcher *conditionMatcher) Match(actual interface{}) (success bool, err error) {

	// Check that the checked valued is a Resource
	resource, ok := actual.(ResourceInterface)
	if !ok {
		logger.Errorf("Wrong type. Matcher expects a type implementing 'ResourceInterface'")
		return false, fmt.Errorf(`Wrong type. Matcher expects a type "ResourceInterface" in test case %v`, g.CurrentSpecReport().FullText())
	}

	// Extract the value of the condition that we want to check
	matcher.currentCondition, err = resource.Get(`{.status.conditions[?(@.type=="` + matcher.conditionType + `")]}`)
	if err != nil {
		return false, err
	}

	if matcher.currentCondition == "" {
		return false, fmt.Errorf(`Condition type "%s" cannot be found in resource %s in test case %v`, matcher.conditionType, resource, g.CurrentSpecReport().FullText())
	}

	var conditionMap map[string]string
	jsonerr := json.Unmarshal([]byte(matcher.currentCondition), &conditionMap)
	if jsonerr != nil {
		return false, jsonerr
	}

	matcher.value, ok = conditionMap[matcher.field]
	if !ok {
		return false, fmt.Errorf(`Condition field "%s" cannot be found in condition %s for resource %s in test case %v`,
			matcher.field, matcher.conditionType, resource, g.CurrentSpecReport().FullText())
	}

	logger.Infof("Value: %s", matcher.value)

	// Guess if we provided a value or another matcher in order to check the condition
	var isMatcher bool
	matcher.expectedMatcher, isMatcher = matcher.expected.(types.GomegaMatcher)
	if !isMatcher {
		matcher.expectedMatcher = &gomegamatchers.EqualMatcher{Expected: matcher.expected}
	}

	return matcher.expectedMatcher.Match(matcher.value)
}

// FailureMessage returns the message in case of successful match
func (matcher *conditionMatcher) FailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	resource, _ := actual.(ResourceInterface)
	message = fmt.Sprintf("In resource %s, the following condition field '%s.%s' failed to satisfy matcher.\n%s\n", resource,
		matcher.conditionType, matcher.field, matcher.expectedMatcher.FailureMessage(matcher.value))
	message += matcher.currentCondition

	return message
}

// FailureMessage returns the message in case of failed match
func (matcher *conditionMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	resource, _ := actual.(ResourceInterface)

	message = fmt.Sprintf("In resource %s, the following condition field '%s.%s' failed satisified matcher, but it shouldn't:\n%s\n", resource,
		matcher.conditionType, matcher.field, matcher.expectedMatcher.NegatedFailureMessage(matcher.value))
	message += matcher.currentCondition

	return message
}

// HaveConditionField returns the gomega matcher to check if a resource's given condition field is matching the expected value
func HaveConditionField(conditionType, conditionField string, expected interface{}) types.GomegaMatcher {
	return &conditionMatcher{conditionType: conditionType, field: conditionField, expected: expected}
}

// HaveNodeDegradedMessage returns the gomega matcher to check if a resource is reporting the given degraded message
func HaveNodeDegradedMessage(expected interface{}) types.GomegaMatcher {
	return &conditionMatcher{conditionType: "NodeDegraded", field: "message", expected: expected}
}

// HaveDegradedMessage returns the gomega matcher to check if a resource is reporting the given node degraded message
func HaveDegradedMessage(expected interface{}) types.GomegaMatcher {
	return &conditionMatcher{conditionType: "Degraded", field: "message", expected: expected}
}

// HaveAvailableMessage returns the gomega matcher to check if a resource is reporting the given node available message
func HaveAvailableMessage(expected interface{}) types.GomegaMatcher {
	return &conditionMatcher{conditionType: "Available", field: "message", expected: expected}
}

// DegradedMatcher struct implementing gomaega matcher interface to check Degraded condition
type DegradedMatcher struct {
	*conditionMatcher
}

// FailureMessage returns the message in case of successful match
func (matcher *DegradedMatcher) FailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	resource, _ := actual.(ResourceInterface)

	message = fmt.Sprintf("Resource %s is NOT Degraded but it should.\n%s condition: %s\n", resource, matcher.conditionType, matcher.currentCondition)
	message += matcher.expectedMatcher.FailureMessage(matcher.value)

	return message
}

// FailureMessage returns the message in case of failed match
func (matcher *DegradedMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	resource, _ := actual.(ResourceInterface)

	message = fmt.Sprintf("Resource %s is Degraded but it should not.\n%s condition: %s", resource, matcher.conditionType, matcher.currentCondition)
	message += matcher.expectedMatcher.NegatedFailureMessage(matcher.value)

	return message
}

// BeDegraded returns the gomega matcher to check if a resource is degraded or not.
func BeDegraded() types.GomegaMatcher {
	return &DegradedMatcher{&conditionMatcher{conditionType: "Degraded", field: "status", expected: "True"}}
}

// AvailableMatcher struct implementing gomaega matcher interface to check "Available" condition
type AvailableMatcher struct {
	*conditionMatcher
}

// FailureMessage returns the message in case of successful match
func (matcher *AvailableMatcher) FailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	resource, _ := actual.(ResourceInterface)

	message = fmt.Sprintf("Resource %s is NOT Available but it should.\n%s condition: %s\n", resource, matcher.conditionType, matcher.currentCondition)
	message += matcher.expectedMatcher.FailureMessage(matcher.value)

	return message
}

// FailureMessage returns the message in case of failed match
func (matcher *AvailableMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	resource, _ := actual.(ResourceInterface)

	message = fmt.Sprintf("Resource %s is Available but it should not.\n%s condition: %s", resource, matcher.conditionType, matcher.currentCondition)
	message += matcher.expectedMatcher.NegatedFailureMessage(matcher.value)

	return message
}

// BeAvailable returns the gomega matcher to check if a resource is available or not.
func BeAvailable() types.GomegaMatcher {
	return &DegradedMatcher{&conditionMatcher{conditionType: "Available", field: "status", expected: "True"}}
}

// BeUpgradeable returns the gomega matcher to check if a resource is upgradeable or not.
func BeUpgradeable() types.GomegaMatcher {
	return &conditionMatcher{conditionType: "Upgradeable", field: "status", expected: "True"}
}
