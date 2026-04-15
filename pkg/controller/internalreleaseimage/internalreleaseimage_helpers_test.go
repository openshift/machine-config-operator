package internalreleaseimage

// Test builders and helper methods.

import (
	"fmt"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	templatectrl "github.com/openshift/machine-config-operator/pkg/controller/template"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func verifyInternalReleaseMasterMachineConfig(t *testing.T, mc *mcfgv1.MachineConfig) {
	assert.Equal(t, masterName(), mc.Name)
	assert.Equal(t, ctrlcommon.MachineConfigPoolMaster, mc.Labels[mcfgv1.MachineConfigRoleLabelKey])
	assert.Equal(t, controllerKind.Kind, mc.OwnerReferences[0].Kind)

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	assert.NoError(t, err, mc.Name)

	assert.Len(t, ignCfg.Systemd.Units, 1)
	assert.Contains(t, *ignCfg.Systemd.Units[0].Contents, "docker-registry-image-pullspec")
	assert.Contains(t, *ignCfg.Systemd.Units[0].Contents, "REGISTRY_AUTH_HTPASSWD_REALM")
	assert.Contains(t, *ignCfg.Systemd.Units[0].Contents, "REGISTRY_AUTH_HTPASSWD_PATH")

	assert.Len(t, ignCfg.Storage.Files, 5, "Found an unexpected file")
	verifyIgnitionFile(t, &ignCfg, "/etc/pki/ca-trust/source/anchors/iri-root-ca.crt", "iri-root-ca-data")
	verifyIgnitionFile(t, &ignCfg, "/etc/iri-registry/certs/tls.key", "iri-tls-key")
	verifyIgnitionFile(t, &ignCfg, "/etc/iri-registry/certs/tls.crt", "iri-tls-crt")
	verifyIgnitionFileMatches(t, &ignCfg, "/etc/iri-registry/auth/htpasswd", IRIRegistryUsername, "testpassword")
	verifyIgnitionFileContains(t, &ignCfg, "/usr/local/bin/load-registry-image.sh", "docker-registry-image-pullspec")
}

func verifyInternalReleaseWorkerMachineConfig(t *testing.T, mc *mcfgv1.MachineConfig) {
	assert.Equal(t, workerName(), mc.Name)
	assert.Equal(t, ctrlcommon.MachineConfigPoolWorker, mc.Labels[mcfgv1.MachineConfigRoleLabelKey])
	assert.Equal(t, controllerKind.Kind, mc.OwnerReferences[0].Kind)

	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	assert.NoError(t, err)

	assert.Len(t, ignCfg.Systemd.Units, 0)
	assert.Len(t, ignCfg.Storage.Files, 1)
	verifyIgnitionFile(t, &ignCfg, "/etc/pki/ca-trust/source/anchors/iri-root-ca.crt", "iri-root-ca-data")
}

func verifyIgnitionFile(t *testing.T, ignCfg *ign3types.Config, path string, expectedContent string) {
	data, err := ctrlcommon.GetIgnitionFileDataByPath(ignCfg, path)
	assert.NoError(t, err)
	assert.Equal(t, expectedContent, string(data), path)
}

func verifyIgnitionFileContains(t *testing.T, ignCfg *ign3types.Config, path string, expectedContent string) {
	data, err := ctrlcommon.GetIgnitionFileDataByPath(ignCfg, path)
	assert.NoError(t, err)
	assert.Contains(t, string(data), expectedContent, path)
}

// verifyIgnitionFileMatches verifies that the ignition file at path contains a
// valid htpasswd entry matching the given username and password.
func verifyIgnitionFileMatches(t *testing.T, ignCfg *ign3types.Config, path, username, password string) {
	t.Helper()
	data, err := ctrlcommon.GetIgnitionFileDataByPath(ignCfg, path)
	assert.NoError(t, err)
	assert.True(t, HtpasswdMatchesPassword(string(data), username, password),
		"htpasswd at %s should match %s:<password>", path, username)
}

// objs is an helper func to improve the test readability.
func objs(builders ...objBuilder) func() []runtime.Object {
	return func() []runtime.Object {
		objects := []runtime.Object{}
		for _, b := range builders {
			objects = append(objects, b.build())
		}
		return objects
	}
}

type objBuilder interface {
	build() runtime.Object
}

// iriBuilder simplifies the creation of an InternalReleaseImage resource in the test.
type iriBuilder struct {
	obj *mcfgv1alpha1.InternalReleaseImage
}

func iri() *iriBuilder {
	return &iriBuilder{
		obj: &mcfgv1alpha1.InternalReleaseImage{
			ObjectMeta: v1.ObjectMeta{
				Name: ctrlcommon.InternalReleaseImageInstanceName,
			},
			Spec: mcfgv1alpha1.InternalReleaseImageSpec{
				Releases: []mcfgv1alpha1.InternalReleaseImageRef{
					{
						Name: "ocp-release-bundle-4.21.5-x86_64",
					},
				},
			},
		},
	}
}

func (ib *iriBuilder) finalizer(f ...string) *iriBuilder {
	ib.obj.SetFinalizers(f)
	return ib
}

func (ib *iriBuilder) setDeletionTimestamp() *iriBuilder {
	now := v1.Now()
	ib.obj.SetDeletionTimestamp(&now)
	return ib
}

func (ib *iriBuilder) build() runtime.Object {
	return ib.obj
}

// controllerConfigBuilder simplifies the creation of a ControllerConfig resource in the test.
type controllerConfigBuilder struct {
	obj *mcfgv1.ControllerConfig
}

func cconfig() *controllerConfigBuilder {
	return &controllerConfigBuilder{
		obj: &mcfgv1.ControllerConfig{
			ObjectMeta: v1.ObjectMeta{
				Name: ctrlcommon.ControllerConfigName,
			},
			Spec: mcfgv1.ControllerConfigSpec{
				Images: map[string]string{
					templatectrl.DockerRegistryKey: "docker-registry-image-pullspec",
				},
				RootCAData: []byte("iri-root-ca-data"),
			},
		},
	}
}

func (ccb *controllerConfigBuilder) withDNS(baseDomain string) *controllerConfigBuilder {
	ccb.obj.Spec.DNS = &configv1.DNS{
		Spec: configv1.DNSSpec{
			BaseDomain: baseDomain,
		},
	}
	return ccb
}

func (ccb *controllerConfigBuilder) dockerRegistryImage(image string) *controllerConfigBuilder {
	ccb.obj.Spec.Images[templatectrl.DockerRegistryKey] = image
	return ccb
}

func (ccb *controllerConfigBuilder) build() runtime.Object {
	return ccb.obj
}

// machineConfigBuilder simplifies the creation of a MachineConfig resource in the test.
type machineConfigBuilder struct {
	obj *mcfgv1.MachineConfig
}

func machineconfigmaster() *machineConfigBuilder {
	return machineconfig("master")
}

func machineconfigworker() *machineConfigBuilder {
	return machineconfig("worker")
}

func masterName() string {
	return fmt.Sprintf(machineConfigNameFmt, "master")
}

func workerName() string {
	return fmt.Sprintf(machineConfigNameFmt, "worker")
}

func machineconfig(role string) *machineConfigBuilder {
	return &machineConfigBuilder{
		obj: &mcfgv1.MachineConfig{
			ObjectMeta: v1.ObjectMeta{
				Name: fmt.Sprintf(machineConfigNameFmt, role),
				Labels: map[string]string{
					mcfgv1.MachineConfigRoleLabelKey: role,
				},
				OwnerReferences: []v1.OwnerReference{
					{
						Kind: "InternalReleaseImage",
					},
				},
			},
			Spec: mcfgv1.MachineConfigSpec{
				Config: runtime.RawExtension{},
			},
		},
	}
}

func (mcb *machineConfigBuilder) ignition(ign string) *machineConfigBuilder {
	mcb.obj.Spec.Config.Raw = []byte(ign)
	return mcb
}

func (mcb *machineConfigBuilder) build() runtime.Object {
	return mcb.obj
}

// secretBuilder simplifies the creation of a Secret resource in the test.
type secretBuilder struct {
	obj *corev1.Secret
}

func iriCertSecret() *secretBuilder {
	return &secretBuilder{
		obj: &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: ctrlcommon.MCONamespace,
				Name:      ctrlcommon.InternalReleaseImageTLSSecretName,
			},
			Data: map[string][]byte{
				"tls.key": []byte("iri-tls-key"),
				"tls.crt": []byte("iri-tls-crt"),
			},
		},
	}
}

func (sb *secretBuilder) build() runtime.Object {
	return sb.obj
}

func pullSecret() *secretBuilder {
	return &secretBuilder{
		obj: &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: ctrlcommon.OpenshiftConfigNamespace,
				Name:      ctrlcommon.GlobalPullSecretName,
			},
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(`{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`),
			},
		},
	}
}

func iriAuthSecret() *secretBuilder {
	htpasswd, err := GenerateHtpasswdEntry(IRIRegistryUsername, "testpassword")
	if err != nil {
		panic(err)
	}
	return &secretBuilder{
		obj: &corev1.Secret{
			ObjectMeta: v1.ObjectMeta{
				Namespace: ctrlcommon.MCONamespace,
				Name:      ctrlcommon.InternalReleaseImageAuthSecretName,
			},
			Data: map[string][]byte{
				"password": []byte("testpassword"),
				"htpasswd": []byte(htpasswd),
			},
		},
	}
}

// clusterVersionBuilder simplifies the creation of a Secret resource in the test.
type clusterVersionBuilder struct {
	obj *configv1.ClusterVersion
}

func clusterVersion() *clusterVersionBuilder {
	return &clusterVersionBuilder{
		obj: &configv1.ClusterVersion{
			ObjectMeta: v1.ObjectMeta{
				Name: "version",
			},
			Status: configv1.ClusterVersionStatus{
				Desired: configv1.Release{
					Image: "ocp-4.21-release-pullspec",
				},
			},
		},
	}
}

func (cvb *clusterVersionBuilder) build() runtime.Object {
	return cvb.obj
}
