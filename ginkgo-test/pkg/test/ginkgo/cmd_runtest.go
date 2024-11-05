package ginkgo

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

type ExitError struct {
	Code int
}

func (e ExitError) Error() string {
	return fmt.Sprintf("exit with code %d", e.Code)
}

// TestOptions handles running a single test.
type TestOptions struct {
	DryRun bool
	Out    io.Writer
	ErrOut io.Writer
}

var _ ginkgo.GinkgoTestingT = &TestOptions{}

func (opt *TestOptions) Run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("only a single test name may be passed")
	}

	// Ignore the upstream suite behavior within test execution
	ginkgo.GetSuite().ClearBeforeAndAfterSuiteNodes()
	tests, err := testsForSuite()
	if err != nil {
		return err
	}
	var test *testCase
	for _, t := range tests {
		if t.name == args[0] {
			test = t
			break
		}
	}
	if test == nil {
		return fmt.Errorf("no test exists with that name: %s", args[0])
	}

	if opt.DryRun {
		fmt.Fprintf(opt.Out, "Running test (dry-run)\n")
		return nil
	}

	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	suiteConfig.FocusStrings = []string{fmt.Sprintf("^ %s$", regexp.QuoteMeta(test.name))}

	// These settings are matched to upstream's ginkgo configuration. See:
	// https://github.com/kubernetes/kubernetes/blob/v1.25.0/test/e2e/framework/test_context.go#L354-L355
	// Turn on EmitSpecProgress to get spec progress (especially on interrupt)
	suiteConfig.EmitSpecProgress = true //it is removed when origin bump to k1.28, but we want to keep it.
	// Randomize specs as well as suites
	suiteConfig.RandomizeAllSpecs = true
	// turn off stdout/stderr capture see https://github.com/kubernetes/kubernetes/pull/111240
	suiteConfig.OutputInterceptorMode = "none" //it is removed when origin bump to k1.28, but we want to keep it.
	// https://github.com/kubernetes/kubernetes/blob/v1.25.0/hack/ginkgo-e2e.sh#L172-L173
	suiteConfig.Timeout = 24 * time.Hour
	reporterConfig.NoColor = true
	reporterConfig.Succinct = true //simple the information at the beginning SuiteWillBegin.
	// reporterConfig.Verbose = true //for g.By when origin bump to k1.28, we do not use it because we alreay make exutil.By

	ginkgo.SetReporterConfig(reporterConfig)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ginkgo.GetSuite().RunSpec(test.spec, ginkgo.Labels{}, "openshift extended e2e", cwd, ginkgo.GetFailer(), ginkgo.GetWriter(), suiteConfig, reporterConfig)

	var summary types.SpecReport
	for _, report := range ginkgo.GetSuite().GetReport().SpecReports {
		if report.NumAttempts > 0 {
			summary = report
		}
	}

	switch {
	case summary.State == types.SpecStatePassed:
	case summary.State == types.SpecStateSkipped:
		if len(summary.Failure.Message) > 0 {
			fmt.Fprintf(opt.ErrOut, "skip [%s:%d]: %s\n", lastFilenameSegment(summary.Failure.Location.FileName), summary.Failure.Location.LineNumber, summary.Failure.Message)
		}
		if len(summary.Failure.ForwardedPanic) > 0 {
			fmt.Fprintf(opt.ErrOut, "skip [%s:%d]: %s\n", lastFilenameSegment(summary.Failure.Location.FileName), summary.Failure.Location.LineNumber, summary.Failure.ForwardedPanic)
		}
		return ExitError{Code: 3}
	case summary.State == types.SpecStateFailed, summary.State == types.SpecStatePanicked, summary.State == types.SpecStateInterrupted:
		if len(summary.Failure.ForwardedPanic) > 0 {
			if len(summary.Failure.Location.FullStackTrace) > 0 {
				fmt.Fprintf(opt.ErrOut, "\n%s\n", summary.Failure.Location.FullStackTrace)
			}
			fmt.Fprintf(opt.ErrOut, "fail [%s:%d]: Test Panicked: %s\n", lastFilenameSegment(summary.Failure.Location.FileName), summary.Failure.Location.LineNumber, summary.Failure.ForwardedPanic)
			return ExitError{Code: 1}
		}
		fmt.Fprintf(opt.ErrOut, "fail [%s:%d]: %s\n", lastFilenameSegment(summary.Failure.Location.FileName), summary.Failure.Location.LineNumber, summary.Failure.Message)
		return ExitError{Code: 1}
	default:
		return fmt.Errorf("unrecognized test case outcome: %#v", summary)
	}
	return nil
}

func (opt *TestOptions) Fail() {
	// this function allows us to pass TestOptions as the first argument,
	// it's empty becase we have failure check mechanism implemented above.
}

func lastFilenameSegment(filename string) string {
	if parts := strings.Split(filename, "/vendor/"); len(parts) > 1 {
		return parts[len(parts)-1]
	}
	if parts := strings.Split(filename, "/src/"); len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return filename
}
