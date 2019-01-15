package e2e_test

import (
	"os"
	"testing"
)

const (
	namespace = "openshift-machine-config-operator"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
