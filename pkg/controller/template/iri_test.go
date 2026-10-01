package template

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/clarketm/json"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestMergesIRIRegistryCredentialsIntoPullSecret verifies that the template
// controller merges IRI registry credentials into the rendered pull secret so
// nodes can authenticate to the IRI registry without writing to the user-controlled
// global pull secret.
func TestMergesIRIRegistryCredentialsIntoPullSecret(t *testing.T) {
	f := newFixture(t)

	cc := newControllerConfig(ctrlcommon.ControllerConfigName)
	cc.Spec.DNS = &configv1.DNS{
		Spec: configv1.DNSSpec{BaseDomain: "example.com"},
	}

	pullSecretJSON := []byte(`{"auths":{"quay.io":{"auth":"dGVzdDp0ZXN0"}}}`)
	ps := newPullSecret("coreos-pull-secret", pullSecretJSON)

	iriRegistryCredentialsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ctrlcommon.InternalReleaseImageAuthSecretName,
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: map[string][]byte{
			"password": []byte("testpassword"),
		},
	}

	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	f.kubeobjects = append(f.kubeobjects, ps, iriRegistryCredentialsSecret)
	f.iriObjects = append(f.iriObjects, &mcfgv1.InternalReleaseImage{
		ObjectMeta: metav1.ObjectMeta{Name: ctrlcommon.InternalReleaseImageInstanceName},
	})

	ctrl := f.newController()
	if err := ctrl.syncHandler(ctrlcommon.ControllerConfigName); err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}

	// Find the 00-master MachineConfig from what was created.
	mcs, err := f.client.MachineconfigurationV1().MachineConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to list MachineConfigs: %v", err)
	}
	var masterMC *mcfgv1.MachineConfig
	for i := range mcs.Items {
		if mcs.Items[i].Name == "00-master" {
			masterMC = &mcs.Items[i]
			break
		}
	}
	if masterMC == nil {
		t.Fatal("00-master MachineConfig not found after sync")
	}

	// Parse the ignition config and extract /var/lib/kubelet/config.json.
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(masterMC.Spec.Config.Raw)
	if err != nil {
		t.Fatalf("failed to parse ignition config: %v", err)
	}
	pullSecretData, err := ctrlcommon.GetIgnitionFileDataByPath(&ignCfg, "/var/lib/kubelet/config.json")
	if err != nil {
		t.Fatalf("failed to get pull secret file from ignition: %v", err)
	}

	var dockerConfig map[string]interface{}
	if err := json.Unmarshal(pullSecretData, &dockerConfig); err != nil {
		t.Fatalf("failed to parse pull secret JSON: %v", err)
	}
	auths, ok := dockerConfig["auths"].(map[string]interface{})
	if !ok {
		t.Fatal("pull secret missing 'auths' field")
	}

	// Verify IRI entries are present for both api-int and localhost with correct credentials.
	expectedAuth := base64.StdEncoding.EncodeToString([]byte("openshift:testpassword"))
	for _, host := range []string{"api-int.example.com:22625", "localhost:22625"} {
		entry, found := auths[host].(map[string]interface{})
		if !found {
			t.Errorf("IRI auth entry missing for %s in rendered pull secret", host)
			continue
		}
		if entry["auth"] != expectedAuth {
			t.Errorf("IRI auth value for %s = %q, want %q", host, entry["auth"], expectedAuth)
		}
	}

	// Verify the original quay.io entry is preserved.
	if _, found := auths["quay.io"]; !found {
		t.Error("original quay.io auth entry was dropped from pull secret")
	}
}
