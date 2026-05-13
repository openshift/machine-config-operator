package e2e_ocl_shared_test

import (
	_ "embed"
	"testing"

	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"

	corev1 "k8s.io/api/core/v1"
)

var (
	// Provides a Containerfile that installs cowsayusing the Centos Stream 9
	// EPEL repository to do so without requiring any entitlements.
	//go:embed Containerfile.cowsay-rhel9
	CowsayDockerfileRHEL9 string

	// Provides a Containerfile that installs cowsayusing the Centos Stream 10
	// EPEL repository to do so without requiring any entitlements.
	//go:embed Containerfile.cowsay-rhel10
	CowsayDockerfileRHEL10 string

	// Provides a Containerfile that installs Buildah from the default RHCOS RPM
	// repositories. If the installation succeeds, the entitlement certificate is
	// working.
	//go:embed Containerfile.entitled
	EntitledDockerfile string

	// Provides a Containerfile that works similarly to the cowsay Dockerfile
	// with the exception that the /etc/yum.repos.d and /etc/pki/rpm-gpg key
	// content is mounted into the build context by the BuildController.
	//go:embed Containerfile.yum-repos-d
	YumReposDockerfile string

	//go:embed Containerfile.okd-fcos
	OkdFcosDockerfile string

	//go:embed Containerfile.simple
	SimpleDockerfile string
)

func GetCowsayDockerfileForNode(t *testing.T, cs *framework.ClientSet, node corev1.Node) string {
	nodeOS := helpers.GetOSReleaseForNode(t, cs, node)
	if nodeOS.OS.IsEL9() {
		return CowsayDockerfileRHEL9
	}

	return CowsayDockerfileRHEL10
}
