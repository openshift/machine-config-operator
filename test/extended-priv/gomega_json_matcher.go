package extended

import (
	"fmt"

	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"

	gomegamatchers "github.com/onsi/gomega/matchers"
	"github.com/onsi/gomega/types"
)

// How to use this matcher
//
// 		json := `
// {
//   "name": "Alice",
//   "age": 30,
//   "height": 1.68,
//   "skills": ["Go", "Python", "JavaScript"],
//   "scores": [95, 88, 76, 100],
//   "contact": {
//     "email": "alice@example.com",
//     "phone": "+1-202-555-0184"
//   },
//   "projects": [
//     {
//       "title": "Inventory App",
//       "lines_of_code": 2500,
//       "technologies": ["Go", "React"]
//     },
//     {
//       "title": "Data Pipeline",
//       "lines_of_code": 4300,
//       "technologies": ["Python", "Airflow"]
//     }
//   ],
//   "settings": {
//     "notifications": true,
//     "theme": "dark",
//     "preferred_numbers": [3, 7, 42]
//   },
//   "extra": []
// }
//
// `
//
//		o.Expect(json).To(HavePathWithValue("age", float64(30)))
//		o.Expect(json).To(HavePathWithValue("age", o.BeEquivalentTo(30)))
//		o.Expect(json).To(HavePathWithValue("age", o.BeNumerically("==", 30) ))
//		o.Expect(json).To(HavePathWithValue("name", "Alice"))
//		o.Expect(json).To(HavePathWithValue("height", float64(1.68)))
//		o.Expect(json).To(HavePathWithValue("height", o.BeNumerically(">", 1) ))
//		o.Expect(json).To(HavePathWithValue("skills", []interface{}{"Go", "Python", "JavaScript"}))
//		o.Expect(json).To(HavePathWithValue("skills", o.ConsistOf("Go", "Python", "JavaScript")))
//		o.Expect(json).To(HavePathWithValue("scores", []interface{}{float64(95), 88.0, 76.0, 100.0}))
//		o.Expect(json).To(HavePathWithValue("scores", o.ConsistOf(95.0, float64(88), o.BeEquivalentTo(76), o.BeEquivalentTo(100))))
//		o.Expect(json).To(HavePathWithValue("skills", o.HaveLen(3)))
//		o.Expect(json).To(HavePathWithValue("scores", o.ContainElement(float64(88))))
//		o.Expect(json).To(HavePathWithValue("scores", o.ContainElements(88.0, 100.0)))
//		o.Expect(json).To(HavePathWithValue("extra", o.HaveLen(0)))
//		o.Expect(json).To(HavePathWithValue("settings.notifications", o.BeTrue()))

// struct implementing gomaega matcher interface
type gjsonMatcher struct {
	path            string
	data            interface{}
	expected        interface{}
	expectedMatcher types.GomegaMatcher
}

// Match checks if the condition matches the given json path. The json information matched is always treated as a string.
func (matcher *gjsonMatcher) Match(actual interface{}) (success bool, err error) {

	// Check that the checked value is a string
	strJSON, ok := actual.(string)
	logger.Debugf("Matched JSON: %s", strJSON)
	if !ok {
		return false, fmt.Errorf(`Wrong type. Matcher expects a type "string": %s`, actual)
	}

	if !gjson.Valid(strJSON) {
		return false, fmt.Errorf(`Wrong format. The string is not a valid JSON: %s`, strJSON)
	}
	data := gjson.Get(strJSON, matcher.path)
	if !data.Exists() {
		return false, fmt.Errorf(`The matched path %s does not exist in the provided JSON: %s`, matcher.path, strJSON)
	}
	matcher.data = data.Value()

	// Guess if we provided a value or another matcher in order to check the condition
	var isMatcher bool
	matcher.expectedMatcher, isMatcher = matcher.expected.(types.GomegaMatcher)
	if !isMatcher {
		matcher.expectedMatcher = &gomegamatchers.EqualMatcher{Expected: matcher.expected}
	}

	return matcher.expectedMatcher.Match(matcher.data)
}

// FailureMessage returns the message when testing `Should` case and `Match` returned false
func (matcher *gjsonMatcher) FailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	strJSON, _ := actual.(string)
	message = fmt.Sprintf("%s\n, the matcher was not satisfied by the path %s in json %s",
		matcher.expectedMatcher.FailureMessage(matcher.data), matcher.path, strJSON)
	return message
}

// FailureMessage returns the message when testing `ShouldNot` case and `Match` returned true
func (matcher *gjsonMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	strJSON, _ := actual.(string)
	message = fmt.Sprintf("%s\n, the matcher was satisfied (but it should NOT)  by the path %s in json %s",
		matcher.expectedMatcher.NegatedFailureMessage(matcher.data), matcher.path, strJSON)

	return message
}

// HavePathWithValue returns the gomega matcher to check if a path in a json data matches the given condition
func HavePathWithValue(path string, expected interface{}) types.GomegaMatcher {
	return &gjsonMatcher{path: path, expected: expected}
}
