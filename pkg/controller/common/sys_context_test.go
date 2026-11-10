package common

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	operatorlistersv1alpha1 "github.com/openshift/client-go/operator/listers/operator/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func newGlobalPullSecret(t *testing.T, auths map[string]string) *corev1.Secret {
	t.Helper()
	entries := map[string]interface{}{}
	for host, auth := range auths {
		entries[host] = map[string]interface{}{"auth": auth}
	}
	raw, err := json.Marshal(map[string]interface{}{"auths": entries})
	require.NoError(t, err)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      GlobalPullSecretName,
			Namespace: OpenshiftConfigNamespace,
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: raw},
	}
}

func newImageLister(imgs ...*configv1.Image) configlistersv1.ImageLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, i := range imgs {
		indexer.Add(i)
	}
	return configlistersv1.NewImageLister(indexer)
}

func newICSPLister(rules ...*operatorv1alpha1.ImageContentSourcePolicy) operatorlistersv1alpha1.ImageContentSourcePolicyLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, r := range rules {
		indexer.Add(r)
	}
	return operatorlistersv1alpha1.NewImageContentSourcePolicyLister(indexer)
}

func newIDMSLister(rules ...*configv1.ImageDigestMirrorSet) configlistersv1.ImageDigestMirrorSetLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, r := range rules {
		indexer.Add(r)
	}
	return configlistersv1.NewImageDigestMirrorSetLister(indexer)
}

func newITMSLister(rules ...*configv1.ImageTagMirrorSet) configlistersv1.ImageTagMirrorSetLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, r := range rules {
		indexer.Add(r)
	}
	return configlistersv1.NewImageTagMirrorSetLister(indexer)
}

// authHostsFromSysContext reads back the authfile the builder wrote and returns
// its auths map. This is the file containers/image actually consults, so it is
// the only faithful assertion target: asserting on the input secret would not
// prove the credentials reached the inspection path.
func authHostsFromSysContext(t *testing.T, authFilePath string) map[string]string {
	t.Helper()
	require.NotEmpty(t, authFilePath, "no authfile was written")
	raw, err := os.ReadFile(authFilePath)
	require.NoError(t, err)

	var parsed struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))

	out := map[string]string{}
	for host, e := range parsed.Auths {
		out[host] = e.Auth
	}
	return out
}

// TestSysContextFactoryMergesIRICredentials is the regression test for the
// agent-installer iso-no-registry failure: machine-config-controller could not
// inspect release images because the sysContext authfile was built from the
// global pull secret alone, which by design never carries IRI credentials.
// The IRI registry answered 401 ("error authenticating user \"\""), OSImageStream
// discovery never completed, EnsureOSImageStream never returned, the template
// controller never started, and ControllerConfig.status stayed at generation 0.
func TestSysContextFactoryMergesIRICredentials(t *testing.T) {
	const (
		baseDomain = "example.com"
		password   = "testpassword"
	)
	wantAuth := base64.StdEncoding.EncodeToString([]byte(IRIRegistryUsername + ":" + password))

	tests := []struct {
		name string
		// IRI present in the cluster at all
		iris []*mcfgv1.InternalReleaseImage
		// the IRI auth secret, which can legitimately lag the IRI resource
		iriSecrets  []*corev1.Secret
		wantIRIAuth bool
	}{
		{
			name:        "IRI in use: credentials reach the authfile",
			iris:        []*mcfgv1.InternalReleaseImage{newIRIObject()},
			iriSecrets:  []*corev1.Secret{newIRIRegistryCredentialsSecret(password)},
			wantIRIAuth: true,
		},
		{
			name:        "no IRI: authfile is untouched",
			iris:        nil,
			iriSecrets:  nil,
			wantIRIAuth: false,
		},
		{
			name:        "IRI present but auth secret not created yet: skip, do not fail",
			iris:        []*mcfgv1.InternalReleaseImage{newIRIObject()},
			iriSecrets:  nil,
			wantIRIAuth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ccLister := newCCLister(cconfigWithDNS(baseDomain))
			factory := NewSysContextFactory(
				ccLister,
				newSecretLister(newGlobalPullSecret(t, map[string]string{
					"quay.io": base64.StdEncoding.EncodeToString([]byte("user:pass")),
				})),
				NewIRISecretMerger(newSecretLister(tt.iriSecrets...), ccLister, newIRILister(tt.iris...)),
				newImageLister(),
				newICSPLister(),
				newIDMSLister(),
				newITMSLister(),
			)

			sysCtx, err := factory()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sysCtx.Cleanup() })

			hosts := authHostsFromSysContext(t, sysCtx.SysContext.AuthFilePath)

			// The pre-existing entry must always survive the merge.
			assert.Contains(t, hosts, "quay.io", "merge must not drop existing auths")

			apiInt := fmt.Sprintf("api-int.%s:%d", baseDomain, IRIRegistryPort)
			localhost := fmt.Sprintf("localhost:%d", IRIRegistryPort)

			if !tt.wantIRIAuth {
				assert.NotContains(t, hosts, apiInt)
				assert.NotContains(t, hosts, localhost)
				return
			}

			// api-int is the host the MCC pod actually reaches; localhost is the
			// first mirror in the IDMS rules and is unusable from a pod netns,
			// but both are merged so the authfile matches the node's.
			assert.Equal(t, wantAuth, hosts[apiInt], "missing/incorrect IRI auth for %s", apiInt)
			assert.Equal(t, wantAuth, hosts[localhost], "missing/incorrect IRI auth for %s", localhost)
		})
	}
}
