package ginkgo

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/onsi/ginkgo/v2/types"
)

type testCase struct {
	name      string
	spec      types.TestSpec
	locations []types.CodeLocation

	// identifies which tests can be run in parallel (ginkgo runs suites linearly)
	testExclusion string

	start    time.Time
	end      time.Time
	duration time.Duration
	out      []byte
	success  bool
	failed   bool
	skipped  bool

	previous *testCase
}

func newTestCaseFromGinkgoSpec(spec types.TestSpec) (*testCase, error) {
	name := spec.Text()
	if strings.Contains(name, "[Exclusive]") {
		name = strings.ReplaceAll(name, "[Exclusive]", "[Exclusive] [Serial]")
	}
	if strings.Contains(name, "[Disruptive]") {
		name = strings.ReplaceAll(name, "[Disruptive]", "[Disruptive] [Serial]")
	}
	tc := &testCase{
		name:      name,
		locations: spec.CodeLocations(),
		spec:      spec,
	}

	return tc, nil
}

func (t *testCase) Retry() *testCase {
	copied := &testCase{
		name:          t.name,
		spec:          t.spec,
		locations:     t.locations,
		testExclusion: t.testExclusion,

		previous: t,
	}
	return copied
}

type TestSuite struct {
	Name        string
	Description string

	Matches func(name string) bool

	// Init should be run once before a test in this suite is run. Not called by
	// methods in this package.
	Init func(map[string]string) error

	// The number of times to execute each test in this suite.
	Count int
	// The maximum parallelism of this suite.
	Parallelism int
	// The number of flakes that may occur before this test is marked as a failure.
	MaximumAllowedFlakes int

	TestTimeout time.Duration
}

func (s *TestSuite) Filter(tests []*testCase) []*testCase {
	matches := make([]*testCase, 0, len(tests))
	for _, test := range tests {
		if !s.Matches(test.name) {
			continue
		}
		matches = append(matches, test)
	}
	return matches
}

func newSuiteFromFile(name string, contents []byte) (*TestSuite, error) {
	suite := &TestSuite{
		Name: name,
	}
	tests := make(map[string]int)
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "\"") {
			var err error
			line, err = strconv.Unquote(line)
			if err != nil {
				return nil, err
			}
			tests[line]++
		}
	}
	suite.Matches = func(name string) bool {
		_, ok := tests[name]
		return ok
	}
	return suite, nil
}

func filterWithRegex(suite *TestSuite, regex string) error {
	re, err := regexp.Compile(regex)
	if err != nil {
		return err
	}
	origMatches := suite.Matches
	suite.Matches = func(name string) bool {
		if match := origMatches(name); !match {
			return false
		}

		return re.MatchString(name)
	}
	return nil
}

func testNames(tests []*testCase) []string {
	var names []string
	for _, t := range tests {
		names = append(names, t.name)
	}
	return names
}

// SuitesString returns a string with the provided suites formatted. Prefix is
// printed at the beginning of the output.
func SuitesString(suites []*TestSuite, prefix string) string {
	buf := &bytes.Buffer{}
	fmt.Fprintf(buf, prefix)
	for _, suite := range suites {
		fmt.Fprintf(buf, "%s\n  %s\n\n", suite.Name, suite.Description)
	}
	return buf.String()
}

func runWithTimeout(ctx context.Context, c *exec.Cmd, timeout time.Duration) ([]byte, error) {
	if timeout > 0 {
		go func() {
			select {
			// interrupt tests after timeout, and abort if they don't complete quick enough
			case <-time.After(timeout):
				if c.Process != nil {
					c.Process.Signal(syscall.SIGINT)
				}
				// if the process appears to be hung a significant amount of time after the timeout
				// send an ABRT so we get a stack dump
				select {
				case <-time.After(time.Minute):
					if c.Process != nil {
						c.Process.Signal(syscall.SIGABRT)
					}
				}
			case <-ctx.Done():
				if c.Process != nil {
					c.Process.Signal(syscall.SIGINT)
				}
			}

		}()
	}
	return c.CombinedOutput()
}
