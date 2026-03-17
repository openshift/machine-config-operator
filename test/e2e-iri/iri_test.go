package e2e_iri_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestIRIResource_Available(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Check that the initial InternalReleaseImage resource has been installed.
	_, err := cs.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)

	// Verify that the expected MachineConfigs have been created.
	_, err = cs.MachineConfigs().Get(ctx, "02-master-internalreleaseimage", v1.GetOptions{})
	require.NoError(t, err)
	_, err = cs.MachineConfigs().Get(ctx, "02-worker-internalreleaseimage", v1.GetOptions{})
	require.NoError(t, err)
}

func TestMachineConfigNodesStatus(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	cv, err := cs.ClusterVersions().Get(ctx, "version", v1.GetOptions{})
	require.NoError(t, err)

	mcnList, err := cs.MachineConfigNodes().List(ctx, v1.ListOptions{})
	require.NoError(t, err)

	for _, mcn := range mcnList.Items {
		requireCondition(t, mcn.Status.Conditions, string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded), v1.ConditionFalse)

		require.Len(t, mcn.Status.InternalReleaseImage.Releases, 1)
		r := mcn.Status.InternalReleaseImage.Releases[0]

		expectedVersion := "ocp-release-bundle-" + cv.Status.Desired.Version
		// MCN IRI Name field max len is 64 chars
		if len(expectedVersion) > 64 {
			expectedVersion = expectedVersion[:64]
		}
		require.Equal(t, expectedVersion, r.Name)
		require.NotEmpty(t, r.Image, "OCP release pullspec cannot be empty")

		requireCondition(t, r.Conditions, string(mcfgv1alpha1.InternalReleaseImageConditionTypeAvailable), v1.ConditionTrue)
		requireCondition(t, r.Conditions, string(mcfgv1alpha1.InternalReleaseImageConditionTypeDegraded), v1.ConditionFalse)
	}
}

func requireCondition(t *testing.T, conditions []v1.Condition, condType string, condStatus v1.ConditionStatus) {
	t.Helper()
	for _, c := range conditions {
		if c.Type == condType && c.Status == condStatus {
			return
		}
	}
	t.Fatalf("expected condition %q with status %q not found", condType, condStatus)
}

func TestIRIController_VerifyIRIRegistryOnAllTheMasterNodes_NoCert(t *testing.T) {
	skipIfOpenShiftCI(t)
	skipIfNoBaremetal(t)

	masterNodes, err := framework.NewClientSet("").CoreV1Interface.Nodes().List(context.TODO(), v1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
	require.NoError(t, err)

	// For every control plane node, ping the IRI registry at port 22625,
	// using directly the node IP.
	for _, node := range masterNodes.Items {
		nodeAddr := ""
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				nodeAddr = addr.Address
				break
			}
		}
		require.NotEmpty(t, nodeAddr)

		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			Timeout: 30 * time.Second,
		}
		pingIRIRegistry(t, client, nodeAddr)
	}
}

func skipIfNoBaremetal(t *testing.T) {
	infra, err := framework.NewClientSet("").Infrastructures().Get(context.Background(), "cluster", v1.GetOptions{})
	require.NoError(t, err)
	if infra.Status.PlatformStatus.Type != configv1.BareMetalPlatformType {
		t.Skip("Skipping non-baremetal platforms")
	}
}

// Currently some tests are not supported in the OpenShift CI
// environment (due the proxy settings)
func skipIfOpenShiftCI(t *testing.T) {
	items := []string{
		// Specific to OpenShift CI.
		"OPENSHIFT_CI",
		// Common to all CI systems.
		"CI",
	}

	for _, item := range items {
		if _, ok := os.LookupEnv(item); ok {
			t.Skip("Skipping OpenShift CI environment")
		}
	}
}

func TestIRIController_VerifyIRIRegistryOnApiInt_WithCert(t *testing.T) {
	skipIfOpenShiftCI(t)
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Retrieve the root CA (same of MCS).
	cm, err := cs.ConfigMaps("openshift-machine-config-operator").Get(ctx, "machine-config-server-ca", v1.GetOptions{})
	require.NoError(t, err)
	rootCA := []byte(cm.Data["ca-bundle.crt"])
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(rootCA))

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: roots,
			},
		},
		Timeout: 30 * time.Second,
	}

	// The api-int DNS entry may be not resolvable in the current environment.
	// Let's fetch directly the related IP address.
	infra, err := cs.Infrastructures().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs)

	pingIRIRegistry(t, client, infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs[0])
}

func pingIRIRegistry(t *testing.T, client *http.Client, ipAddr string) {
	iriRegistryUrl := fmt.Sprintf("https://%s:%d/v2/", ipAddr, 22625)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, iriRegistryUrl, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, resp.StatusCode, http.StatusOK)
	apiVersion := resp.Header.Get("Docker-Distribution-Api-Version")
	require.Equal(t, apiVersion, "registry/2.0")
}

// TestIRIController_VerifyTLSProfileEnforced verifies that the IRI registry
// enforces the TLS minimum version derived from the cluster's APIServer TLS profile.
// It connects with the expected minimum version (should succeed) and one version
// below it (should be rejected). Uses ExecCmdOnNode to run curl on the node
// directly, since port 22625 is not accessible from the CI test runner.
func TestIRIController_VerifyTLSProfileEnforced(t *testing.T) {
	cs := framework.NewClientSet("")

	// Read the cluster's APIServer TLS profile to determine the expected minimum version.
	apiServerCfg, err := cs.ConfigV1Interface.APIServers().Get(context.Background(), "cluster", v1.GetOptions{})
	require.NoError(t, err)

	profileName, expectedMinVersion, rejectedVersion := tlsVersionsFromProfile(t, apiServerCfg.Spec.TLSSecurityProfile)
	t.Logf("Cluster TLS profile: %s, expected minimum TLS version: %s", profileName, expectedMinVersion)

	// Find a master node to exec commands on.
	masterNodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), v1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
	require.NoError(t, err)
	require.NotEmpty(t, masterNodes.Items)
	node := masterNodes.Items[0]

	// Verify the minimum TLS version succeeds.
	t.Run(fmt.Sprintf("minimum version succeeds with %s profile", profileName), func(t *testing.T) {
		out := helpers.ExecCmdOnNode(t, cs, node, "curl", "-s", "-k", "-o", "/dev/null", "-w", "%{http_code}",
			"--tlsv"+expectedMinVersion, "--tls-max", expectedMinVersion,
			"https://localhost:22625/v2/")
		require.Equal(t, "200", out, "expected TLS %s to succeed", expectedMinVersion)
	})

	// Verify that one version below the minimum is rejected at the TLS layer.
	// curl exits with status 35 (CURLE_SSL_CONNECT_ERROR) when the server
	// rejects the TLS version during the handshake.
	t.Run(fmt.Sprintf("below minimum is rejected with %s profile", profileName), func(t *testing.T) {
		_, err := helpers.ExecCmdOnNodeWithError(cs, node, "curl", "-s", "-k", "-o", "/dev/null", "-w", "%{http_code}",
			"--tlsv"+rejectedVersion, "--tls-max", rejectedVersion,
			"https://localhost:22625/v2/")
		require.Error(t, err, "TLS %s should be rejected with %s profile", rejectedVersion, profileName)
		require.Contains(t, err.Error(), "exit status 35", "expected TLS handshake failure (exit status 35), got: %v", err)
	})
}

// tlsVersionsFromProfile returns the profile name, expected minimum TLS version,
// and the rejected TLS version (one below the minimum) for the given profile.
// The versions are returned in curl format (e.g. "1.2", "1.3").
func tlsVersionsFromProfile(t *testing.T, profile *configv1.TLSSecurityProfile) (profileName, expectedMinVersion, rejectedVersion string) {
	t.Helper()

	// Map from OpenShift TLS version strings to curl-compatible versions
	// and the version below them for rejection testing.
	tlsVersionMap := map[configv1.TLSProtocolVersion]struct{ curlVersion, belowVersion string }{
		configv1.VersionTLS10: {"1.0", ""},
		configv1.VersionTLS11: {"1.1", "1.0"},
		configv1.VersionTLS12: {"1.2", "1.1"},
		configv1.VersionTLS13: {"1.3", "1.2"},
	}

	// Determine the profile spec and name.
	var minTLSVersion configv1.TLSProtocolVersion
	if profile == nil {
		profileName = "Intermediate (default)"
		minTLSVersion = configv1.TLSProfiles[configv1.TLSProfileIntermediateType].MinTLSVersion
	} else {
		profileName = string(profile.Type)
		switch profile.Type {
		case configv1.TLSProfileCustomType:
			if profile.Custom != nil {
				minTLSVersion = profile.Custom.MinTLSVersion
			} else {
				// Custom profile with no spec, fall back to Intermediate.
				minTLSVersion = configv1.TLSProfiles[configv1.TLSProfileIntermediateType].MinTLSVersion
			}
		default:
			spec, ok := configv1.TLSProfiles[profile.Type]
			if !ok {
				t.Fatalf("Unknown TLS profile type: %s", profile.Type)
			}
			minTLSVersion = spec.MinTLSVersion
		}
	}

	versions, ok := tlsVersionMap[minTLSVersion]
	if !ok {
		t.Fatalf("Unknown TLS version: %s", minTLSVersion)
	}
	expectedMinVersion = versions.curlVersion
	rejectedVersion = versions.belowVersion

	if rejectedVersion == "" {
		t.Skipf("Skipping rejection test: no TLS version below %s to test against", expectedMinVersion)
	}

	return profileName, expectedMinVersion, rejectedVersion
}

func TestIRIController_ShouldPreventDeletionWhenInUse(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Get the InternalReleaseImage resource
	iri, err := cs.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)

	// Verify the IRI has releases in its status
	require.NotEmpty(t, iri.Status.Releases, "IRI should have releases in status")

	// Get the ClusterVersion to know what release the cluster is using
	cv, err := cs.ClusterVersions().Get(ctx, "version", v1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, cv.Status.Desired.Image, "ClusterVersion should have a desired image")

	// Verify that at least one release in IRI matches the current cluster version
	matchFound := false
	for _, release := range iri.Status.Releases {
		if release.Image == cv.Status.Desired.Image {
			matchFound = true
			break
		}
	}
	require.True(t, matchFound, "IRI should contain a release matching the current cluster version")

	// Attempt to delete the InternalReleaseImage - this should fail
	err = cs.InternalReleaseImages().Delete(ctx, "cluster", v1.DeleteOptions{})
	require.Error(t, err, "Deleting IRI while in use should fail")

	// Verify the error is an Invalid error from the ValidatingAdmissionPolicy
	// ValidatingAdmissionPolicy returns Invalid (422) errors, not Forbidden (403)
	require.True(t, k8serrors.IsInvalid(err), "Error should be an Invalid error from admission policy, got: %v", err)
	require.Contains(t, err.Error(), "Cannot delete InternalReleaseImage while the cluster is using a release bundle from this resource",
		"Error message should indicate the IRI is in use")

	// Verify the IRI still exists
	iri, err = cs.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err, "IRI should still exist after failed deletion attempt")
	require.NotNil(t, iri, "IRI should not be nil")
}

func TestIRIController_ShouldRestoreMachineConfigsWhenModified(t *testing.T) {
	skipIfNoBaremetal(t)

	cases := []struct {
		name       string
		userAction func(t *testing.T, ctx context.Context, cs *framework.ClientSet, configs []*mcfgv1.MachineConfig)
	}{
		{
			name: "user deletes all the IRI machine configs",
			userAction: func(t *testing.T, ctx context.Context, cs *framework.ClientSet, configs []*mcfgv1.MachineConfig) {
				// Delete all the IRI MachineConfigs found.
				for _, mc := range configs {
					err := cs.MachineConfigs().Delete(ctx, mc.Name, v1.DeleteOptions{})
					require.NoError(t, err)
				}
			},
		},
		{
			name: "user patches all the IRI machine configs",
			userAction: func(t *testing.T, ctx context.Context, cs *framework.ClientSet, configs []*mcfgv1.MachineConfig) {
				// Update all the IRI MachineConfigs found.
				for _, mc := range configs {
					updatedMC := mc.DeepCopy()
					updatedMC.Spec.OSImageURL = "some-unused-value"
					_, err := cs.MachineConfigs().Update(ctx, updatedMC, v1.UpdateOptions{})
					require.NoError(t, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := framework.NewClientSet("")
			ctx := context.Background()

			// Check that initially IRI MachineConfigs are defined.
			machineConfigs, err := cs.MachineConfigs().List(ctx, v1.ListOptions{})
			require.NoError(t, err)
			iriMachineConfigs := []*mcfgv1.MachineConfig{}
			for _, mc := range machineConfigs.Items {
				if len(mc.OwnerReferences) != 0 && mc.OwnerReferences[0].Kind == "InternalReleaseImage" && mc.OwnerReferences[0].Name == "cluster" {
					iriMachineConfigs = append(iriMachineConfigs, mc.DeepCopy())
				}
			}
			require.NotEmpty(t, iriMachineConfigs)

			// Apply the user action.
			tc.userAction(t, ctx, cs, iriMachineConfigs)

			// Wait until all of them will be restored.
			err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 1*time.Minute, true, func(ctx context.Context) (done bool, err error) {
				for _, oldMC := range iriMachineConfigs {
					newMC, err := cs.MachineConfigs().Get(ctx, oldMC.Name, v1.GetOptions{})
					if k8serrors.IsNotFound(err) {
						return false, nil
					}
					if err != nil {
						return true, err
					}
					// Check that the ignition is really the same.
					oldIgn, err := ctrlcommon.ParseAndConvertConfig(oldMC.Spec.Config.Raw)
					require.NoError(t, err)
					newIgn, err := ctrlcommon.ParseAndConvertConfig(newMC.Spec.Config.Raw)
					require.NoError(t, err)
					if !reflect.DeepEqual(oldIgn, newIgn) {
						return true, fmt.Errorf("newer version of MachineConfig %s is different from the original one.", oldMC.Name)
					}
				}
				return true, nil
			})
			require.NoError(t, err)
		})
	}
}
