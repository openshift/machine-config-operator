package helpers

import "os"

// Ensures that a given cleanup function only runs once; even if called
// multiple times.
func MakeIdempotent(f func()) func() {
	hasRun := false

	return func() {
		if !hasRun {
			f()
			hasRun = true
		}
	}
}

// Ensures that a given cleanup function only runs once; even if called multiple times.
// When the provided shouldRun function returns false, executing the cleanup will be
// skipped. The shouldRun will also only be executed once.
func MakeIdempotentSkippable(shouldRun func() bool, f func()) func() {
	return MakeIdempotent(func() {
		if shouldRun() {
			f()
		}
	})
}

// Makes a given function idempotent and ensures that it is called at least
// once during the test run.
func MakeIdempotentAndRegister(t TestingT, f func()) func() {
	out := MakeIdempotent(f)
	t.Cleanup(out)
	return out
}

// Makes a given function idempotent and ensures that it is called at least
// once during the test run, but only if the provided shouldRun function
// returns true. The shouldRun function will also only be evaluated a single
// time.
func MakeIdempotentSkippableAndRegister(t TestingT, shouldRun func() bool, f func()) func() {
	out := MakeIdempotentSkippable(shouldRun, f)
	t.Cleanup(out)
	return out
}

// Provides simple configuration for potentially skipping a cleanup in the
// event of test failure.
type IdempotentConfig struct {
	// Always skip running this function.
	SkipAlways bool
	// Only skip running this function when the test has failed.
	SkipOnlyOnFailure bool
	// Whether the test failed or not. Private because we set it from testing.T.
	testFailed bool
	// Whether we're running in CI. Private because we set it using the inCI()
	// helper.
	inCI bool
}

func (i *IdempotentConfig) shouldRun() bool {
	// If skipCleanupAlways is set, then we should not run cleanups regardless of
	// outcome. This takes precedence over the inCI() check.
	if i.SkipAlways {
		return false
	}

	// If the test failed and skipCleanupOnlyAfterFailure is set, we should skip
	// cleanup. This takes precedence over the inCI() check.
	if i.testFailed && i.SkipOnlyOnFailure {
		return false
	}

	// If the test failed, the skip cleanup after failures flag is not set, and
	// we're in CI, skip running cleanups since the CI system will capture the
	// current cluster state, which is useful for debugging the test failure.
	if i.testFailed && i.inCI {
		return false
	}

	// At this point, we know the test passed or there is nothing precluding us
	// from running the cleanups.
	return true
}

// Makes a function idempotent modulo the provided config struct which
// determines if the function should always be run, only run on failure, etc.
// In CI, we generally don't want to skip cleanups in the event of failure.
func MakeConfigurableIdempotentAndRegister(t TestingT, shouldRunCfg IdempotentConfig, f func()) func() {
	shouldRunCfg.testFailed = t.Failed()
	shouldRunCfg.inCI = inCI()
	return MakeIdempotentSkippableAndRegister(t, shouldRunCfg.shouldRun, f)
}

// Determines if we're running in a CI system based upon the presence (or lack
// thereof) of certain environment variables.
func inCI() bool {
	items := []string{
		// Specific to OpenShift CI.
		"OPENSHIFT_CI",
		// Common to all CI systems.
		"CI",
	}

	for _, item := range items {
		if _, ok := os.LookupEnv(item); ok {
			return true
		}
	}

	return false
}
