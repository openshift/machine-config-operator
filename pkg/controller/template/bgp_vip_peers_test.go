package template

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfglistersv1 "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
)

// Golden payloads: legacyFullPeersFile is the exact frr-peers.json content the
// nodes got before passwords moved out of the ControllerConfig; it must still
// be rendered byte-for-byte from fullPeersTransport.
const (
	legacyFullPeersFile = `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"password":"s3cret","port":1790,"bfdEnabled":"true","ebgpMultiHop":"true","holdTime":"90","keepaliveTime":"30"}],"communities":["64512:100","64512:4200000000:300"],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"holdTime":"180"}]}}`
	fullPeersTransport  = `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513,"passwordSecret":{"name":"bgp-peer-pw","namespace":"openshift-config"},"port":1790,"bfdEnabled":"true","ebgpMultiHop":"true","holdTime":"90","keepaliveTime":"30"}],"communities":["64512:100","64512:4200000000:300"],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"],"hostOverrides":{"master-0":[{"peerAddress":"192.168.111.2","peerASN":64514,"holdTime":"180"}]}}`
	noPasswordPeers     = `{"localASN":64512,"defaultPeers":[{"peerAddress":"192.168.111.1","peerASN":64513}],"apiVIPs":["192.168.111.5"],"ingressVIPs":["192.168.111.4"]}`
	frrPeersPath        = "/etc/kubernetes/static-pod-resources/frr-k8s/frr-peers.json"
	bgpLeakMarker       = "SYNTHETIC-LEAK-MARKER"
)

// bgpControllerConfig returns the baremetal test ControllerConfig named like
// the real one, with BGP VIP management on or off and the given transport.
func bgpControllerConfig(t *testing.T, transport string, bgp bool) *mcfgv1.ControllerConfig {
	t.Helper()
	cc, err := controllerConfigFromFile(configs["baremetal"])
	require.NoError(t, err)
	cc.Name = ctrlcommon.ControllerConfigName
	cc.Generation = 1
	bm := cc.Spec.Infra.Status.PlatformStatus.BareMetal
	if bgp {
		bm.VIPManagement = configv1.VIPManagementTypeBGP
	}
	bm.APIServerInternalIPs = []string{"192.168.111.5"}
	bm.IngressIPs = []string{"192.168.111.4"}
	cc.Spec.BGPVIPPeersJSON = transport
	cc.Spec.PullSecret = &corev1.ObjectReference{Namespace: metav1.NamespaceDefault, Name: "coreos-pull-secret"}
	return cc
}

// bgpPasswordSecret builds a basic-auth password Secret.
func bgpPasswordSecret(namespace, name string, password []byte) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeBasicAuth,
		Data:       map[string][]byte{"password": password},
	}
}

// staticSecretGetter serves the given Secrets.
func staticSecretGetter(secrets ...*corev1.Secret) ctrlcommon.BGPVIPSecretGetter {
	return func(namespace, name string) (*corev1.Secret, error) {
		for _, s := range secrets {
			if s.Namespace == namespace && s.Name == name {
				return s, nil
			}
		}
		return nil, assert.AnError
	}
}

// findFile returns the named file of the MachineConfig called mcName.
func findFile(t *testing.T, mcs []*mcfgv1.MachineConfig, mcName, path string) (*ign3types.File, ign3types.Config, bool) {
	t.Helper()
	for _, mc := range mcs {
		if mc.Name != mcName {
			continue
		}
		ign, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
		require.NoError(t, err)
		for i := range ign.Storage.Files {
			if ign.Storage.Files[i].Path == path {
				return &ign.Storage.Files[i], ign, true
			}
		}
		return nil, ign, false
	}
	return nil, ign3types.Config{}, false
}

// fileContents decodes an Ignition file's contents.
func fileContents(t *testing.T, f *ign3types.File) string {
	t.Helper()
	data, err := ctrlcommon.DecodeIgnitionFileContents(f.Contents.Source, f.Contents.Compression)
	require.NoError(t, err)
	return string(data)
}

// TestBGPVIPPeersFileRendering locks the rendered frr-peers.json: byte-identical
// to the pre-change payload, mode 0600, only in 00-master, and rendered without
// touching the ControllerConfig.
func TestBGPVIPPeersFileRendering(t *testing.T) {
	ps := []byte(`{"dummy":"dummy"}`)
	pw := bgpPasswordSecret("openshift-config", "bgp-peer-pw", []byte("s3cret"))
	cases := []struct {
		name      string
		transport string
		bgp       bool
		secrets   []*corev1.Secret
		expected  string
	}{
		{name: "BGP with a passwordSecret reference", transport: fullPeersTransport, bgp: true, secrets: []*corev1.Secret{pw}, expected: legacyFullPeersFile},
		{name: "BGP without passwords", transport: noPasswordPeers, bgp: true, expected: noPasswordPeers},
		{name: "BGP with a transport without references (inline password) is passed through verbatim", transport: legacyFullPeersFile, bgp: true, expected: legacyFullPeersFile},
		{name: "no BGP, no transport: empty file as before", transport: "", bgp: false, expected: ""},
		{name: "no BGP with a stale transport: empty, no Secret needed", transport: fullPeersTransport, bgp: false, expected: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := bgpControllerConfig(t, tc.transport, tc.bgp)
			specBefore := cc.Spec.DeepCopy()

			peersFile, err := bgpVIPPeersFileJSON(cc, staticSecretGetter(tc.secrets...))
			require.NoError(t, err)
			assert.Equal(t, tc.expected, peersFile)
			mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, ps, peersFile, nil, ctrlcommon.TLSGroupsOptions{})
			require.NoError(t, err)

			f, _, found := findFile(t, mcs, "00-master", frrPeersPath)
			require.True(t, found, "00-master must carry frr-peers.json")
			assert.Equal(t, tc.expected, fileContents(t, f))
			require.NotNil(t, f.Mode)
			assert.Equal(t, 0o600, *f.Mode)
			for _, mc := range mcs {
				if mc.Name == "00-master" {
					continue
				}
				_, _, other := findFile(t, mcs, mc.Name, frrPeersPath)
				assert.False(t, other, "only 00-master renders frr-peers.json, found in %s", mc.Name)
			}
			// The resolved password is render-only.
			assert.Equal(t, specBefore, &cc.Spec, "rendering must not modify the ControllerConfig")

			// The bootstrap path renders the same bytes.
			bootstrapMCs, err := RunBootstrap(templateDir, cc, ps, nil, ctrlcommon.TLSGroupsOptions{}, staticSecretGetter(tc.secrets...))
			require.NoError(t, err)
			bf, _, found := findFile(t, bootstrapMCs, "00-master", frrPeersPath)
			require.True(t, found)
			assert.Equal(t, tc.expected, fileContents(t, bf))
		})
	}

	t.Run("bootstrap without a Secret source fails closed", func(t *testing.T) {
		cc := bgpControllerConfig(t, fullPeersTransport, true)
		_, err := RunBootstrap(templateDir, cc, ps, nil, ctrlcommon.TLSGroupsOptions{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no Secret source")
		assert.NotContains(t, err.Error(), "s3cret")
	})
}

// bgpFixture sets up a template controller fixture with a BGP ControllerConfig,
// the pull secret and the given openshift-config Secrets. existing are
// MachineConfigs already in the cluster (the last good render).
func bgpFixture(t *testing.T, cc *mcfgv1.ControllerConfig, secrets []*corev1.Secret, existing []*mcfgv1.MachineConfig) *fixture {
	t.Helper()
	f := newFixture(t)
	f.ccLister = append(f.ccLister, cc)
	f.objects = append(f.objects, cc)
	f.kubeobjects = append(f.kubeobjects, newPullSecret("coreos-pull-secret", []byte(`{"dummy": "dummy"}`)))
	for _, s := range secrets {
		f.kubeobjects = append(f.kubeobjects, s)
	}
	for _, mc := range existing {
		f.objects = append(f.objects, mc)
	}
	return f
}

// writtenMachineConfigs returns the MachineConfigs created or updated by the
// controller, by name.
func writtenMachineConfigs(actions []core.Action) (created, updated map[string]*mcfgv1.MachineConfig) {
	created, updated = map[string]*mcfgv1.MachineConfig{}, map[string]*mcfgv1.MachineConfig{}
	for _, a := range actions {
		if a.GetResource().Resource != "machineconfigs" {
			continue
		}
		// CreateAction and UpdateAction share a method set: tell them apart
		// by verb.
		withObject, ok := a.(core.CreateAction)
		if !ok {
			continue
		}
		mc := withObject.GetObject().(*mcfgv1.MachineConfig)
		switch a.GetVerb() {
		case "create":
			created[mc.Name] = mc
		case "update":
			updated[mc.Name] = mc
		}
	}
	return created, updated
}

// controllerConfigStatusWrites returns the ControllerConfig objects sent to
// the status subresource.
func controllerConfigStatusWrites(actions []core.Action) []*mcfgv1.ControllerConfig {
	var out []*mcfgv1.ControllerConfig
	for _, a := range actions {
		if a.GetResource().Resource == "controllerconfigs" && a.GetSubresource() == "status" && a.GetVerb() == "update" {
			if act, ok := a.(core.UpdateAction); ok {
				out = append(out, act.GetObject().(*mcfgv1.ControllerConfig))
			}
		}
	}
	return out
}

// lastConditionMessage returns the message of the latest true condition of
// the given type among status writes.
func lastConditionMessage(ccs []*mcfgv1.ControllerConfig, condType mcfgv1.ControllerConfigStatusConditionType) string {
	for i := len(ccs) - 1; i >= 0; i-- {
		for _, c := range ccs[i].Status.Conditions {
			if c.Type == condType && c.Status == corev1.ConditionTrue {
				return c.Message
			}
		}
	}
	return ""
}

// renderMCs renders the template MachineConfigs for cc with the given peers
// file.
func renderMCs(t *testing.T, cc *mcfgv1.ControllerConfig, peersFile string) []*mcfgv1.MachineConfig {
	t.Helper()
	mcs, err := getMachineConfigsForControllerConfig(templateDir, cc, []byte(`{"dummy": "dummy"}`), peersFile, nil, ctrlcommon.TLSGroupsOptions{})
	require.NoError(t, err)
	return mcs
}

// TestBGPVIPPasswordSecretResolvedAtRender: the template controller resolves
// the reference from the openshift-config Secret informer cache (no API read),
// renders the pre-change bytes, and keeps the password out of the
// ControllerConfig status.
func TestBGPVIPPasswordSecretResolvedAtRender(t *testing.T) {
	cc := bgpControllerConfig(t, fullPeersTransport, true)
	f := bgpFixture(t, cc, []*corev1.Secret{bgpPasswordSecret("openshift-config", "bgp-peer-pw", []byte("s3cret"))}, nil)
	c := f.newController()
	require.NoError(t, c.syncHandler(getKey(cc, t)))

	created, updated := writtenMachineConfigs(f.client.Actions())
	assert.Empty(t, updated)
	master := created["00-master"]
	require.NotNil(t, master)
	file, _, found := findFile(t, []*mcfgv1.MachineConfig{master}, "00-master", frrPeersPath)
	require.True(t, found)
	assert.Equal(t, legacyFullPeersFile, fileContents(t, file))

	for _, a := range filterInformerActions(f.kubeclient.Actions()) {
		if get, ok := a.(core.GetAction); ok && a.GetResource().Resource == "secrets" {
			assert.Equal(t, "coreos-pull-secret", get.GetName(), "BGP password Secrets are read from the informer cache")
		}
	}
	statuses := controllerConfigStatusWrites(f.client.Actions())
	require.NotEmpty(t, statuses, "the sync must report ControllerConfig status")
	assert.Contains(t, lastConditionMessage(statuses, mcfgv1.TemplateControllerCompleted), "sync completed")
	for _, status := range statuses {
		data, err := json.Marshal(status)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "s3cret")
		assert.Equal(t, fullPeersTransport, status.Spec.BGPVIPPeersJSON, "the ControllerConfig keeps the reference-only transport")
	}
}

// TestBGPVIPPasswordSecretRenderFailure: an already referenced password
// Secret that is missing (deleted), of the wrong type, oversized, or
// referenced outside openshift-config fails the sync closed: no MachineConfig
// is written (the last good render stays) and the reported error carries no
// Secret contents.
func TestBGPVIPPasswordSecretRenderFailure(t *testing.T) {
	lastGood := renderMCs(t, bgpControllerConfig(t, fullPeersTransport, true), legacyFullPeersFile)
	cases := []struct {
		name      string
		transport string
		secrets   []*corev1.Secret
		expect    string
	}{
		{name: "missing Secret", transport: fullPeersTransport, expect: "openshift-config/bgp-peer-pw"},
		{name: "wrong Secret type", transport: fullPeersTransport, secrets: []*corev1.Secret{{
			ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer-pw", Namespace: "openshift-config"},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"password": []byte(bgpLeakMarker)},
		}}, expect: "kubernetes.io/basic-auth"},
		{name: "password over 80 bytes", transport: fullPeersTransport, secrets: []*corev1.Secret{
			bgpPasswordSecret("openshift-config", "bgp-peer-pw", bytes.Repeat([]byte(bgpLeakMarker), 5)),
		}, expect: "80-byte TCP MD5 limit"},
		{name: "reference outside openshift-config", transport: strings.Replace(fullPeersTransport, `"namespace":"openshift-config"`, `"namespace":"kube-system"`, 1), secrets: []*corev1.Secret{
			bgpPasswordSecret("kube-system", "bgp-peer-pw", []byte(bgpLeakMarker)),
		}, expect: "only \"openshift-config\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := bgpControllerConfig(t, tc.transport, true)
			f := bgpFixture(t, cc, tc.secrets, lastGood)
			c := f.newController()
			recorder := record.NewFakeRecorder(100)
			c.eventRecorder = recorder

			err := c.syncHandler(getKey(cc, t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expect)

			created, updated := writtenMachineConfigs(f.client.Actions())
			assert.Empty(t, created, "no MachineConfig may be written on failure")
			assert.Empty(t, updated, "no MachineConfig may be written on failure")
			got, getErr := f.client.MachineconfigurationV1().MachineConfigs().Get(t.Context(), "00-master", metav1.GetOptions{})
			require.NoError(t, getErr)
			file, _, found := findFile(t, []*mcfgv1.MachineConfig{got}, "00-master", frrPeersPath)
			require.True(t, found)
			assert.Equal(t, legacyFullPeersFile, fileContents(t, file), "the last good peers file stays in place")

			statuses := controllerConfigStatusWrites(f.client.Actions())
			msg := lastConditionMessage(statuses, mcfgv1.TemplateControllerFailing)
			assert.Contains(t, msg, tc.expect)
			for _, status := range statuses {
				data, err := json.Marshal(status)
				require.NoError(t, err)
				assert.NotContains(t, string(data), bgpLeakMarker)
				assert.NotContains(t, string(data), "s3cret")
				for _, cond := range status.Status.Conditions {
					assert.NotContains(t, cond.Message, "192.168.111.")
				}
			}
			close(recorder.Events)
			for event := range recorder.Events {
				assert.NotContains(t, event, bgpLeakMarker)
			}
		})
	}
}

// TestBGPVIPPasswordSecretRotation: a rotated Secret re-renders 00-master with
// the new password, and frr-peers.json is the only file that changes, which
// is what keeps the update non-disruptive on the nodes (the file's default
// NodeDisruptionPolicy is None).
func TestBGPVIPPasswordSecretRotation(t *testing.T) {
	cc := bgpControllerConfig(t, fullPeersTransport, true)
	before := renderMCs(t, cc, legacyFullPeersFile)
	f := bgpFixture(t, cc, []*corev1.Secret{bgpPasswordSecret("openshift-config", "bgp-peer-pw", []byte("r0tated"))}, before)
	c := f.newController()
	require.NoError(t, c.syncHandler(getKey(cc, t)))

	created, updated := writtenMachineConfigs(f.client.Actions())
	assert.Empty(t, created)
	require.Len(t, updated, 1, "only 00-master changes on rotation")
	after := updated["00-master"]
	require.NotNil(t, after)

	file, newIgn, found := findFile(t, []*mcfgv1.MachineConfig{after}, "00-master", frrPeersPath)
	require.True(t, found)
	assert.Equal(t, strings.Replace(legacyFullPeersFile, `"password":"s3cret"`, `"password":"r0tated"`, 1), fileContents(t, file))
	_, oldIgn, found := findFile(t, before, "00-master", frrPeersPath)
	require.True(t, found)
	assert.Equal(t, []string{frrPeersPath}, ctrlcommon.CalculateConfigFileDiffs(&oldIgn, &newIgn))
}

// TestFilterSecretBGPVIPPasswordSecret locks which Secret events re-sync the
// ControllerConfig for the BGP VIP peers file.
func TestFilterSecretBGPVIPPasswordSecret(t *testing.T) {
	referenced := bgpPasswordSecret("openshift-config", "bgp-peer-pw", []byte("s3cret"))
	rotated := bgpPasswordSecret("openshift-config", "bgp-peer-pw", []byte("r0tated"))
	cases := []struct {
		name      string
		transport string
		bgp       bool
		event     func(c *Controller)
		expected  int
	}{
		{name: "add of a referenced Secret", transport: fullPeersTransport, bgp: true, event: func(c *Controller) { c.addSecret(referenced) }, expected: 1},
		{name: "data change of a referenced Secret", transport: fullPeersTransport, bgp: true, event: func(c *Controller) { c.updateSecret(referenced, rotated) }, expected: 1},
		{name: "resync of a referenced Secret without data change", transport: fullPeersTransport, bgp: true, event: func(c *Controller) { c.updateSecret(referenced, referenced.DeepCopy()) }, expected: 0},
		{name: "deletion of a referenced Secret", transport: fullPeersTransport, bgp: true, event: func(c *Controller) { c.deleteSecret(referenced) }, expected: 1},
		{name: "tombstone of a referenced Secret", transport: fullPeersTransport, bgp: true, event: func(c *Controller) {
			c.deleteSecret(cache.DeletedFinalStateUnknown{Key: "openshift-config/bgp-peer-pw", Obj: referenced})
		}, expected: 1},
		{name: "unreferenced openshift-config Secret", transport: fullPeersTransport, bgp: true, event: func(c *Controller) {
			c.addSecret(bgpPasswordSecret("openshift-config", "other", []byte("x")))
		}, expected: 0},
		{name: "same name in another namespace", transport: fullPeersTransport, bgp: true, event: func(c *Controller) {
			c.addSecret(bgpPasswordSecret("kube-system", "bgp-peer-pw", []byte("x")))
		}, expected: 0},
		{name: "BGP VIP management not active", transport: fullPeersTransport, bgp: false, event: func(c *Controller) { c.addSecret(referenced) }, expected: 0},
		{name: "malformed transport", transport: `{"localASN":`, bgp: true, event: func(c *Controller) { c.addSecret(referenced) }, expected: 0},
		{name: "pull secret change still re-syncs", transport: fullPeersTransport, bgp: true, event: func(c *Controller) {
			c.addSecret(newPullSecret("coreos-pull-secret", []byte(`{"dummy": "dummy"}`)))
		}, expected: 1},
		// Regression guard only: deleting the pull secret has always just
		// been logged; this change must not alter that, whatever the merits.
		{name: "pull-secret deletion keeps its previous (log-only) behavior", transport: fullPeersTransport, bgp: true, event: func(c *Controller) {
			c.deleteSecret(newPullSecret("pull-secret", []byte(`{}`)))
		}, expected: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A bare controller: no informers run, so the event handlers
			// under test are the only callers of the enqueue hook.
			cc := bgpControllerConfig(t, tc.transport, tc.bgp)
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			require.NoError(t, indexer.Add(cc))
			enqueued := 0
			c := &Controller{ccLister: mcfglistersv1.NewControllerConfigLister(indexer)}
			c.enqueueControllerConfig = func(*mcfgv1.ControllerConfig) { enqueued++ }
			tc.event(c)
			assert.Equal(t, tc.expected, enqueued)
		})
	}
}

// TestBGPVIPUnresolvableSecretWritesNoMachineConfig: when the BGP VIP peer
// password Secret cannot be resolved, the sync fails before any
// MachineConfig write, including the one-time image URL cleanup of existing
// generated MachineConfigs.
func TestBGPVIPUnresolvableSecretWritesNoMachineConfig(t *testing.T) {
	cases := []struct {
		name    string
		secrets []*corev1.Secret
		expect  string
	}{
		{name: "missing Secret", expect: "openshift-config/bgp-peer-pw"},
		{name: "invalid Secret", secrets: []*corev1.Secret{{
			ObjectMeta: metav1.ObjectMeta{Name: "bgp-peer-pw", Namespace: "openshift-config"},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"password": []byte("s3cret")},
		}}, expect: "kubernetes.io/basic-auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc := bgpControllerConfig(t, fullPeersTransport, true)
			existing := renderMCs(t, cc, legacyFullPeersFile)
			// A generated MachineConfig that still carries image URLs: the
			// image URL cleanup would update it.
			require.NotEmpty(t, existing[0].Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey])
			existing[0].Spec.OSImageURL = "registry.example.com/os@sha256:0000000000000000000000000000000000000000000000000000000000000000"
			f := bgpFixture(t, cc, tc.secrets, existing)
			c := f.newController()

			err := c.syncHandler(getKey(cc, t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.expect)
			for _, a := range f.client.Actions() {
				if a.GetResource().Resource != "machineconfigs" {
					continue
				}
				switch a.GetVerb() {
				case "update", "patch", "create", "delete":
					t.Errorf("unexpected MachineConfig %s before the BGP VIP peers file was resolved", a.GetVerb())
				}
			}
		})
	}
}
