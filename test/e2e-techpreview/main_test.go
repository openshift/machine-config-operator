package e2e_techpreview_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// TODO: When adding tests to this suite, be sure to revert the "disable e2e-gcp-op-techpreview"
// commit from https://github.com/openshift/release/pull/67617
