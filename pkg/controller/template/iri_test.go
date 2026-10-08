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
	"k8s.io/client-go/tools/cache"
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

// TestDeleteIRISecretEnqueues pins the delete half of the IRI auth secret
// watch. Without it a deleted secret leaves the already-merged credentials in
// the rendered pull secret until something unrelated triggers a render, so the
// handler has to reach filterSecret for both a live object and a tombstone.
func TestDeleteIRISecretEnqueues(t *testing.T) {
	iriSecret := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ctrlcommon.InternalReleaseImageAuthSecretName,
				Namespace: ctrlcommon.MCONamespace,
			},
		}
	}

	tests := []struct {
		name        string
		obj         interface{}
		wantEnqueue bool
	}{
		{
			name:        "deleted IRI auth secret",
			obj:         iriSecret(),
			wantEnqueue: true,
		},
		{
			// The informer delivers a tombstone when it missed the delete watch
			// event; unwrapping it is the difference between re-rendering and
			// silently keeping stale credentials.
			name:        "tombstone wrapping the IRI auth secret",
			obj:         cache.DeletedFinalStateUnknown{Key: ctrlcommon.MCONamespace + "/" + ctrlcommon.InternalReleaseImageAuthSecretName, Obj: iriSecret()},
			wantEnqueue: true,
		},
		{
			name: "unrelated secret in the MCO namespace",
			obj: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:      "some-other-secret",
				Namespace: ctrlcommon.MCONamespace,
			}},
			wantEnqueue: false,
		},
		{
			name:        "tombstone holding something that is not a Secret",
			obj:         cache.DeletedFinalStateUnknown{Key: "bogus", Obj: &corev1.ConfigMap{}},
			wantEnqueue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			// Only the lister is populated: leaving f.objects empty keeps the
			// fixture's informers from delivering events of their own, so the
			// queue reflects nothing but the call under test.
			f.ccLister = append(f.ccLister, newControllerConfig(ctrlcommon.ControllerConfigName))

			c := f.newController()
			defer c.queue.ShutDown()

			// Watching the real workqueue rather than stubbing
			// enqueueControllerConfig keeps this off the controller's fields,
			// which the fixture's already-running informer goroutines read.
			before := c.queue.Len()
			c.deleteIRISecret(tt.obj)
			got := c.queue.Len() - before

			want := 0
			if tt.wantEnqueue {
				want = 1
			}
			if got != want {
				t.Errorf("deleteIRISecret queued %d items, want %d", got, want)
			}
		})
	}
}
