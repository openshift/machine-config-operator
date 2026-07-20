package internalreleaseimage

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/controller/common"
)

func TestInternalReleaseImageManager(t *testing.T) {
	cases := []struct {
		name string

		iri           *iriBuilder
		nodeName      string
		mcn           *mcnBuilder
		setupRegistry func(r *FakeIRIRegistry)
		verify        func(t *testing.T, actualMCN *mcfgv1.MachineConfigNode, registryDataPath string)

		registryDisabled     bool
		skipRegistryDirSetup bool
	}{
		{
			name:                 "feature not enabled",
			mcn:                  machineConfigNode("master-0"),
			nodeName:             "master-0",
			iri:                  nil,
			registryDisabled:     true,
			skipRegistryDirSetup: true,

			verify: func(t *testing.T, mcn *mcfgv1.MachineConfigNode, registryDataPath string) {
				assert.Empty(t, mcn.Status.InternalReleaseImage)
			},
		},
		{
			name:     "happy path",
			iri:      iri(),
			nodeName: "master-0",
			mcn:      machineConfigNode("master-0"),

			setupRegistry: func(r *FakeIRIRegistry) {
				r.AddResponse("/v2", http.StatusOK, "{}").
					AddResponse("/v2/openshift/release-bundles/tags/list", http.StatusOK, `{"name":"openshift/release-bundles","tags":["ocp-release-bundle-4.22.0-0.ci-2026-04-01-050515"]}`).
					AddResponse("/v2/openshift/release-images/tags/list", http.StatusOK, `{"name":"openshift/release-images","tags":["68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389"]}`).
					AddResponse("/v2/openshift/release-images/manifests/sha256:68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389", http.StatusOK, "{}")
			},

			verify: func(t *testing.T, mcn *mcfgv1.MachineConfigNode, registryDataPath string) {
				verifyCondition(t, mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded), metav1.ConditionFalse)

				assert.Len(t, mcn.Status.InternalReleaseImage.Releases, 1)
				r := mcn.Status.InternalReleaseImage.Releases[0]
				assert.Equal(t, "ocp-release-bundle-4.22.0-0.ci-2026-04-01-050515", r.Name)
				assert.Equal(t, "localhost:22625/openshift/release-images@sha256:68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389", r.Image)
				verifyCondition(t, r.Conditions, string(mcfgv1.InternalReleaseImageConditionTypeAvailable), metav1.ConditionTrue)
				verifyCondition(t, r.Conditions, string(mcfgv1.InternalReleaseImageConditionTypeDegraded), metav1.ConditionFalse)
			},
		},
		{
			name:             "registry down",
			iri:              iri(),
			nodeName:         "master-0",
			mcn:              machineConfigNode("master-0"),
			registryDisabled: true,

			verify: func(t *testing.T, mcn *mcfgv1.MachineConfigNode, registryDataPath string) {
				verifyCondition(t, mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded), metav1.ConditionTrue)

				assert.Len(t, mcn.Status.InternalReleaseImage.Releases, 0)
			},
		},
		{
			name:     "Missing release manifest",
			iri:      iri(),
			nodeName: "master-0",
			mcn:      machineConfigNode("master-0"),

			setupRegistry: func(r *FakeIRIRegistry) {
				r.AddResponse("/v2", http.StatusOK, "{}").
					AddResponse("/v2/openshift/release-bundles/tags/list", http.StatusOK, `{"name":"openshift/release-bundles","tags":["ocp-release-bundle-4.22.0-0.ci-2026-04-01-050515"]}`).
					AddResponse("/v2/openshift/release-images/tags/list", http.StatusOK, `{"name":"openshift/release-images","tags":["68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389"]}`).
					AddResponse("/v2/openshift/release-images/manifests/sha256:68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389", http.StatusNotFound, `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown","detail":{"Tag":"68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0388"}}]}`)
			},

			verify: func(t *testing.T, mcn *mcfgv1.MachineConfigNode, registryDataPath string) {
				verifyCondition(t, mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded), metav1.ConditionTrue)

				assert.Len(t, mcn.Status.InternalReleaseImage.Releases, 1)
				r := mcn.Status.InternalReleaseImage.Releases[0]
				assert.Equal(t, "ocp-release-bundle-4.22.0-0.ci-2026-04-01-050515", r.Name)
				assert.Equal(t, "localhost:22625/openshift/release-images@sha256:68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389", r.Image)
				verifyCondition(t, r.Conditions, string(mcfgv1.InternalReleaseImageConditionTypeAvailable), metav1.ConditionFalse)
				verifyCondition(t, r.Conditions, string(mcfgv1.InternalReleaseImageConditionTypeDegraded), metav1.ConditionTrue)
			},
		},
		{
			name:     "IRI deletion in progress - registry still active",
			mcn:      machineConfigNode("master-0").withIRIBundle("ocp-release-bundle-4.22.0-0.ci-2026-04-01-050515", "localhost:22625/openshift/release-images@sha256:68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389"),
			nodeName: "master-0",
			iri:      iri().withDeletionTimestamp(),
			setupRegistry: func(r *FakeIRIRegistry) {
				r.AddResponse("/v2", http.StatusOK, "{}")
			},

			verify: func(t *testing.T, mcn *mcfgv1.MachineConfigNode, registryDataPath string) {
				// MCN status should NOT be cleaned (registry still active)
				assert.NotEmpty(t, mcn.Status.InternalReleaseImage.Releases, "MCN IRI status should not be cleaned while registry is active")

				// Storage should NOT be reclaimed (registry still active)
				testFile := filepath.Join(registryDataPath, "test-data.txt")
				_, err := os.Stat(testFile)
				assert.NoError(t, err, "Storage should not be reclaimed while registry is active")
			},
		},
		{
			name:             "IRI deleted - registry down",
			mcn:              machineConfigNode("master-0").withIRIBundle("ocp-release-bundle-4.22.0-0.ci-2026-04-01-050515", "localhost:22625/openshift/release-images@sha256:68bdf24405449be5c78a1f27a7b64fc9ee980e4bc3c9b169e8b3da08e50e0389"),
			nodeName:         "master-0",
			iri:              nil,
			registryDisabled: true,

			verify: func(t *testing.T, mcn *mcfgv1.MachineConfigNode, registryDataPath string) {
				// MCN status should be cleaned
				for _, c := range mcn.Status.Conditions {
					assert.NotEqual(t, string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded), c.Type)
				}
				assert.Empty(t, mcn.Status.InternalReleaseImage)

				// Storage should be reclaimed (registry is down)
				entries, err := os.ReadDir(registryDataPath)
				assert.NoError(t, err)
				assert.Empty(t, entries, "Storage should be reclaimed when registry is down")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Use a temp directory for registry data in tests
			tempDir := t.TempDir()

			// Set the actual registry data path
			var registryDataPath string
			if tc.skipRegistryDirSetup {
				// For "feature not enabled" case: point to non-existent directory
				registryDataPath = filepath.Join(tempDir, "nonexistent-registry")
			} else {
				// For normal cases: use tempDir and ensure it exists
				registryDataPath = tempDir
				require.NoError(t, os.MkdirAll(registryDataPath, 0755))

				testFile := filepath.Join(registryDataPath, "test-data.txt")
				require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))
			}

			fakeMCClient := fake.NewClientset(tc.mcn.obj)
			mcInformerFactory := mcfginformers.NewSharedInformerFactory(fakeMCClient, func() time.Duration { return 0 }())
			iriInformer := mcInformerFactory.Machineconfiguration().V1().InternalReleaseImages()
			mcnInformer := mcInformerFactory.Machineconfiguration().V1().MachineConfigNodes()

			mcInformerFactory.Start(ctx.Done())
			mcInformerFactory.WaitForCacheSync(ctx.Done())

			require.NoError(t, mcnInformer.Informer().GetIndexer().Add(tc.mcn.build()))
			if tc.iri != nil {
				require.NoError(t, iriInformer.Informer().GetIndexer().Add(tc.iri.build()))
			}

			if !tc.registryDisabled {
				fakeRegistry := NewFakeIRIRegistry()
				if tc.setupRegistry != nil {
					tc.setupRegistry(fakeRegistry)
				}
				require.NoError(t, fakeRegistry.Start())
				defer fakeRegistry.Close()
			}

			iriManager := New(tc.nodeName, fakeMCClient, iriInformer, mcnInformer)
			iriManager.registryClient = &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true,
					},
				},
			}
			// Provide a dummy auth token so the manager doesn't try to read
			// /var/lib/kubelet/config.json (which doesn't exist in unit tests).
			iriManager.authToken = "dGVzdDp0ZXN0"          // base64("test:test")
			iriManager.registryDataPath = registryDataPath // Use test directory (may not exist)
			require.NoError(t, iriManager.syncHandler(common.InternalReleaseImageInstanceName))

			if tc.mcn != nil {
				mcnUpdated, err := fakeMCClient.MachineconfigurationV1().MachineConfigNodes().Get(context.Background(), tc.mcn.obj.Name, metav1.GetOptions{})
				require.NoError(t, err)
				tc.verify(t, mcnUpdated, registryDataPath)
			}
		})
	}
}

func verifyCondition(t *testing.T, conditions []metav1.Condition, eCondType string, eCondStatus metav1.ConditionStatus) {
	t.Helper()
	for _, c := range conditions {
		if c.Type == eCondType {
			assert.Equal(t, eCondStatus, c.Status)
			return
		}
	}
	assert.Failf(t, "expected condition type %s not found", eCondType)
}
