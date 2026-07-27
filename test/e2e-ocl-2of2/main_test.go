package e2e_ocl_2of2_test

import (
	"flag"
	"os"
	"testing"

	"k8s.io/klog/v2"
)

func TestMain(m *testing.M) {
	flag.Parse()
	klog.Infof("-skip-cleanup: %v", skipCleanupAlways)
	klog.Infof("-skip-cleanup-on-failure: %v", skipCleanupOnlyAfterFailure)
	os.Exit(m.Run())
}
