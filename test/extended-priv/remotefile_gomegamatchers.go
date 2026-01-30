package extended

import (
	"fmt"
	"reflect"

	o "github.com/onsi/gomega"
	gomegamatchers "github.com/onsi/gomega/matchers"
	"github.com/onsi/gomega/types"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// struct implementing gomega matcher interface for RemoteFile struct
type remoteFileMethodMatcher struct {
	method          string
	methodDescMsg   string
	retValue        interface{}
	expected        interface{}
	expectedMatcher types.GomegaMatcher
}

// Match checks it the condition matches the given json path. The json information matched is always treated as a string.
func (matcher *remoteFileMethodMatcher) Match(actual interface{}) (success bool, err error) {

	// Check that the checked value is a string
	remoteFilePtr, ok := actual.(*RemoteFile)
	if !ok {
		remoteFileObj, ok := actual.(RemoteFile)
		if !ok {
			return false, fmt.Errorf(`Wrong type. Matcher expects a type "RemoteFile" or "*RemoteFile"`)
		}
		remoteFilePtr = &remoteFileObj
	}

	// If the info was not already gathered, we gather it
	if !remoteFilePtr.HasInfo() {
		err = remoteFilePtr.Fetch()
		logger.Infof("Gathering info for %s", remoteFilePtr)
		if err != nil {
			return false, err
		}
	}

	// Get a reflect.Value representing the instance
	value := reflect.ValueOf(remoteFilePtr)

	// Get a reflect.Method representing the desired method
	method := value.MethodByName(matcher.method)
	if !method.IsValid() {
		msg := fmt.Sprintf("RemoteFile gomega matcher. ERROR! NOT VALID METHOD: [%s]", matcher.method)
		logger.Errorf(msg)
		return false, o.StopTrying(msg)
	}

	// Call the method on the instance
	retValues := method.Call(nil) // We call the method without parameters
	numRetValues := len(retValues)
	if numRetValues == 0 {
		msg := fmt.Sprintf("RemoteFile gomega matcher. ERROR! METHOD DOES NOT RETURN ANY VALUE: [%s]", matcher.method)
		logger.Errorf(msg)
		return false, o.StopTrying(msg)
	}

	if numRetValues != 1 {
		logger.Warnf("The method %s is returning %d values. The matcher is ignoring all return values but the first one",
			matcher.method, numRetValues)
	}

	matcher.retValue = method.Call(nil)[0].Interface() // We call the method without parameters

	// Guess if we provided a value or another matcher in order to check the condition
	var isMatcher bool
	matcher.expectedMatcher, isMatcher = matcher.expected.(types.GomegaMatcher)
	if !isMatcher {
		matcher.expectedMatcher = &gomegamatchers.EqualMatcher{Expected: matcher.expected}
	}

	return matcher.expectedMatcher.Match(matcher.retValue)
}

// FailureMessage returns the message when testing `Should` case and `Match` returned false
func (matcher *remoteFileMethodMatcher) FailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	remoteFile, ok := actual.(RemoteFile)
	if !ok {
		remoteFilePtr, _ := actual.(*RemoteFile)
		remoteFile = *remoteFilePtr
	}

	methodDesc := matcher.method
	if matcher.methodDescMsg != "" {
		methodDesc = matcher.methodDescMsg
	}

	message = fmt.Sprintf("%s\n. File %s in node %s. The matcher was not satisfied by %s.",
		matcher.expectedMatcher.FailureMessage(matcher.retValue), remoteFile.GetFullPath(), remoteFile.node.GetName(), methodDesc)
	return message
}

// FailureMessage returns the message when testing `ShouldNot` case and `Match` returned true
func (matcher *remoteFileMethodMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	// The type was already validated in Match, we can safely ignore the error
	remoteFile, ok := actual.(RemoteFile)
	if !ok {
		remoteFilePtr, _ := actual.(*RemoteFile)
		remoteFile = *remoteFilePtr
	}

	methodDesc := matcher.method
	if matcher.methodDescMsg != "" {
		methodDesc = matcher.methodDescMsg
	}

	message = fmt.Sprintf("%s\n. File %s in node %s. The matcher was satisfied by %s, but it should NOT be satisfied",
		matcher.expectedMatcher.NegatedFailureMessage(matcher.retValue), remoteFile.GetFullPath(), remoteFile.node.GetName(), methodDesc)

	return message
}

// HaveContent returns the gomega matcher to check a RemoteFile's content
func HaveContent(expected interface{}) types.GomegaMatcher {
	return &remoteFileMethodMatcher{expected: expected, method: "GetTextContent", methodDescMsg: "the file content"}
}

// HaveOctalPermissions returns the gomega matcher to check a RemoteFile's permissions. Permissions should be provided as a string representing octal permissions, like '"0644"')
func HaveOctalPermissions(expected interface{}) types.GomegaMatcher {
	return &remoteFileMethodMatcher{expected: expected, method: "GetOctalPermissions", methodDescMsg: "the file permissions"}
}

// HaveOwner returns the gomega matcher to check a RemoteFile's owner.
func HaveOwner(expected interface{}) types.GomegaMatcher {
	return &remoteFileMethodMatcher{expected: expected, method: "GetUIDName", methodDescMsg: "the file owner"}
}

// HaveOwnerID returns the gomega matcher to check a RemoteFile's owner ID. The expected owner's id shoul be a string representing the owner's id, like '"0"' (for root)
func HaveOwnerID(expected interface{}) types.GomegaMatcher {
	return &remoteFileMethodMatcher{expected: expected, method: "GetUIDNumber", methodDescMsg: "the file owner"}
}

// HaveGroup returns the gomega matcher to check a RemoteFile's group.
func HaveGroup(expected interface{}) types.GomegaMatcher {
	return &remoteFileMethodMatcher{expected: expected, method: "GetGIDName", methodDescMsg: "the file group"}
}

// HaveGroupID returns the gomega matcher to check a RemoteFile's group ID. The expected group's id shoul be a string representing the groups's id, like '"0"' (for root)
func HaveGroupID(expected interface{}) types.GomegaMatcher {
	return &remoteFileMethodMatcher{expected: expected, method: "GetGIDNumber", methodDescMsg: "the file group"}
}
