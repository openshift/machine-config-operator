package mco

import (
	"fmt"

	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
	"github.com/tidwall/gjson"

	gomegamatchers "github.com/onsi/gomega/matchers"
	"github.com/onsi/gomega/types"
)

// struct implementing gomaega matcher interface
type gjsonStringMatcher struct {
	path            string
	strData         string
	expected        interface{}
	expectedMatcher types.GomegaMatcher
}

// Match checks it the condition matches the given json path. The json information matched is always treated as a string.
func (matcher *gjsonStringMatcher) Match(actual interface{}) (success bool, err error) {

	// Check that the checked value is a string
	strJSON, ok := actual.(string)
	logger.Debugf("Matched JSON: %s", strJSON)
	if !ok {
		return false, fmt.Errorf(`Wrong type. Matcher expects a type "string"`)
	}

	if !gjson.Valid(strJSON) {
		return false, fmt.Errorf(`Wrong format. The string is not a valid JSON`)
	}
	data := gjson.Get(strJSON, matcher.path)
	if !data.Exists() {
		return false, fmt.Errorf(`The matched path %s does not exist in the provided JSON`, matcher.path)
	}
	matcher.strData = data.String()

	// Guess if we provided a value or another matcher in order to check the condition
	var isMatcher bool
	matcher.expectedMatcher, isMatcher = matcher.expected.(types.GomegaMatcher)
	if !isMatcher {
		matcher.expectedMatcher = &gomegamatchers.EqualMatcher{Expected: matcher.expected}
	}

	return matcher.expectedMatcher.Match(matcher.strData)
}

// FailureMessage returns the message when testing `Should` case and `Match` returned false
func (matcher *gjsonStringMatcher) FailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	strJSON, _ := actual.(string)
	message = fmt.Sprintf("%s\n, the matcher was not satisfied by the path %s in json %s",
		matcher.expectedMatcher.FailureMessage(matcher.strData), matcher.path, strJSON)
	return message
}

// FailureMessage returns the message when testing `ShouldNot` case and `Match` returned true
func (matcher *gjsonStringMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	strJSON, _ := actual.(string)
	message = fmt.Sprintf("%s\n, the matcher was satisfied (but it should NOT)  by the path %s in json %s",
		matcher.expectedMatcher.NegatedFailureMessage(matcher.strData), matcher.path, strJSON)

	return message
}

// HavePathWithCalue returns the gomega matcher to check if a path in a json data has matches the given condition
func HavePathWithValue(path string, expected interface{}) types.GomegaMatcher {
	return &gjsonStringMatcher{path: path, expected: expected}
}
