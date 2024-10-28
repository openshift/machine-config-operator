package util

import (
	"fmt"
	"os"
	"os/exec"

	g "github.com/onsi/ginkgo/v2"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// IsROSA Determine whether it is a ROSA env, now only support prow
func IsROSA() bool {
	_, err := os.Stat(os.Getenv("SHARED_DIR") + "/cluster-name")
	if err != nil {
		if !os.IsExist(err) {
			return false
		}
	}
	return len(os.Getenv("TEST_ROSA_TOKEN")) > 0
}

// ROSALogin rosa login, If the login fails then skip
func ROSALogin() {
	e2e.Logf("ROSA login")
	if len(os.Getenv("TEST_ROSA_TOKEN")) == 0 {
		g.Skip("env TEST_ROSA_LOGIN_ENV not set")
	}
	cmd := fmt.Sprintf(`rosa login --env "%s" --token "%s"`, os.Getenv("TEST_ROSA_LOGIN_ENV"), os.Getenv("TEST_ROSA_TOKEN"))
	_, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		e2e.Failf("rosa cli login error" + err.Error())
	}
}

// Get cluster ID for ROSA created cluster
func GetROSAClusterID() string {
	return os.Getenv("CLUSTER_ID")
}
