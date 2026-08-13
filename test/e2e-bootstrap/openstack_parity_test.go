package e2e_bootstrap_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	configv1 "github.com/openshift/api/config/v1"
	configv1alpha1 "github.com/openshift/api/config/v1alpha1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	apioperatorsv1alpha1 "github.com/openshift/api/operator/v1alpha1"

	"github.com/openshift/machine-config-operator/pkg/controller/bootstrap"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

// Debug test for openshift/machine-config-operator#6326 e2e-openstack:
// the bootstrap-rendered MC and the in-cluster rendered MC must match on
// an on-prem (OpenStack) platform, where the on-prem templates
// (keepalived, frr-k8s, kube-vip) render real content.
func TestE2EBootstrapOpenStackParity(t *testing.T) {
	ctx := context.Background()

	testEnv := framework.NewTestEnv(t)

	configv1.Install(scheme.Scheme)
	configv1alpha1.Install(scheme.Scheme)
	mcfgv1.Install(scheme.Scheme)
	mcfgv1alpha1.Install(scheme.Scheme)
	apioperatorsv1alpha1.Install(scheme.Scheme)

	baseTestManifests := loadBaseTestManifests(t)

	// Mutate the ControllerConfig's infra to OpenStack with VIPs, mirroring
	// the CI cluster where the mismatch was observed.
	found := false
	for _, obj := range baseTestManifests {
		if cc, ok := obj.(*mcfgv1.ControllerConfig); ok {
			cc.Spec.Infra.Status.PlatformStatus = &configv1.PlatformStatus{
				Type: configv1.OpenStackPlatformType,
				OpenStack: &configv1.OpenStackPlatformStatus{
					APIServerInternalIPs: []string{"10.0.0.5"},
					IngressIPs:           []string{"10.0.0.7"},
					APIServerInternalIP:  "10.0.0.5",
					IngressIP:            "10.0.0.7",
				},
			}
			cc.Spec.DNS = &configv1.DNS{
				TypeMeta:   metav1.TypeMeta{APIVersion: "config.openshift.io/v1", Kind: "DNS"},
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec:       configv1.DNSSpec{BaseDomain: "domain.example.com"},
			}
			found = true
		}
	}
	require.True(t, found, "no ControllerConfig in base manifests")

	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, testEnv.Stop())
	}()

	clientSet := framework.NewClientSetFromConfig(cfg)

	for _, ns := range []string{framework.OpenshiftConfigNamespace, bootstrapTestName} {
		_, err = clientSet.Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	objs := append([]runtime.Object{}, baseTestManifests...)
	objs = append(objs, loadRawManifests(t, [][]byte{
		[]byte(`apiVersion: config.openshift.io/v1
kind: Node
metadata:
  name: cluster`),
	})...)

	fixture := newTestFixture(t, cfg, objs)
	defer framework.CleanEnvironment(t, clientSet)
	defer fixture.stop()

	controllerRenderedMasterConfigName, err := helpers.WaitForRenderedConfigs(t, clientSet, "master", "99-master-ssh", "99-master-generated-registries")
	require.NoError(t, err)
	t.Logf("Controller rendered master config as %q", controllerRenderedMasterConfigName)

	destDir, err := os.MkdirTemp("", "controller-bootstrap")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	srcDir, err := os.MkdirTemp("", "controller-bootstrap-source")
	require.NoError(t, err)
	defer os.RemoveAll(srcDir)

	for id, obj := range objs {
		manifest, err := yaml.Marshal(obj)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, fmt.Sprintf("manifest-%d.yaml", id)), manifest, 0o644))
	}

	bootstrapper := bootstrap.New(templatesDir, srcDir, filepath.Join(bootstrapTestDataDir, "/machineconfigcontroller-pull-secret"), nil)
	require.NoError(t, bootstrapper.Run(destDir))

	compareRenderedConfigPool(t, clientSet, destDir, "master", controllerRenderedMasterConfigName)

	// Sanity: the bootstrap-rendered MC must contain the on-prem static pod
	// files (path-gated to disabled-manifests on non-BGP clusters) and the
	// ssh passwd section - exactly what the CI bootstrap MC was missing.
	paths, err := filepath.Glob(filepath.Join(destDir, "machine-configs", "rendered-master-*.yaml"))
	require.NoError(t, err)
	require.Len(t, paths, 1)
	mcBytes, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	assert.Contains(t, string(mcBytes), "disabled-manifests/0000-frr-k8s.yaml")
	assert.Contains(t, string(mcBytes), "sshAuthorizedKeys")
}
