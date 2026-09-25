//go:build !iri_delete

package e2e_iri_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

const iriRootCAPath = "/rootfs" + constants.IRIRootCAPath

func TestIRIResource_Available(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Check that the initial InternalReleaseImage resource has been installed.
	_, err := cs.MachineconfigurationV1Interface.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
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

		requireCondition(t, r.Conditions, string(mcfgv1.InternalReleaseImageConditionTypeAvailable), v1.ConditionTrue)
		requireCondition(t, r.Conditions, string(mcfgv1.InternalReleaseImageConditionTypeDegraded), v1.ConditionFalse)
	}
}

func TestInternalReleaseImageAggregatedStatusHappyPath(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	iri, err := cs.MachineconfigurationV1Interface.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)

	require.NotEmpty(t, iri.Status.Releases, "Cluster-level IRI should have aggregated releases")
	baseDomain := getBaseDomain(t, cs)

	// Verify each release in the aggregated status
	for _, release := range iri.Status.Releases {
		// Release should use api-int URL format, not localhost
		require.Contains(t, release.Image, "api-int."+baseDomain, "Aggregated release should use api-int URL")
		require.NotContains(t, release.Image, "localhost", "Aggregated release should not use localhost")

		require.NotEmpty(t, release.Conditions, "Release should have conditions")
	}

	requireCondition(t, iri.Status.Conditions, string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded), v1.ConditionFalse)

	// The reason should be AllReleasesAvailable in a healthy cluster
	for _, cond := range iri.Status.Conditions {
		if cond.Type == string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded) {
			require.Equal(t, "AllReleasesAvailable", cond.Reason, "In a healthy cluster, reason should be AllReleasesAvailable")
		}
	}

	require.Len(t, iri.Status.Releases, len(iri.Spec.Releases), "Status releases should match spec releases count")
	for i, specRelease := range iri.Spec.Releases {
		require.Equal(t, specRelease.Name, iri.Status.Releases[i].Name, "Release names should match between spec and status")
	}
}

func TestInternalReleaseImageAggregatesFromMCNs(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Get the cluster-level IRI
	iri, err := cs.MachineconfigurationV1Interface.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, iri.Status.Releases, "Cluster IRI should have releases")

	// Get all MachineConfigNodes
	mcnList, err := cs.MachineConfigNodes().List(ctx, v1.ListOptions{})
	require.NoError(t, err)

	// Filter to control plane nodes (IRI only runs on control plane)
	masterNodes, err := cs.CoreV1Interface.Nodes().List(ctx, v1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/master=",
	})
	require.NoError(t, err)
	require.NotEmpty(t, masterNodes.Items, "Should have control plane nodes")

	controlPlaneMCNs := 0
	for _, mcn := range mcnList.Items {
		// Check if this MCN corresponds to a control plane node
		isMaster := false
		for _, node := range masterNodes.Items {
			if node.Name == mcn.Name {
				isMaster = true
				break
			}
		}

		if !isMaster {
			continue
		}

		controlPlaneMCNs++

		// Verify each control plane MCN has IRI status
		require.NotEmpty(t, mcn.Status.InternalReleaseImage.Releases,
			"Control plane MCN %s should have IRI releases", mcn.Name)

		// Verify MCN releases match the cluster IRI spec
		require.Len(t, mcn.Status.InternalReleaseImage.Releases, len(iri.Spec.Releases),
			"MCN %s should have same number of releases as IRI spec", mcn.Name)

		// Verify MCN has the InternalReleaseImageDegraded condition
		hasIRIDegradedCondition := false
		for _, cond := range mcn.Status.Conditions {
			if cond.Type == string(mcfgv1.MachineConfigNodeInternalReleaseImageDegraded) {
				hasIRIDegradedCondition = true
				// In a healthy cluster, this should be False
				require.Equal(t, v1.ConditionFalse, cond.Status,
					"MCN %s should not be degraded in healthy cluster", mcn.Name)
				break
			}
		}
		require.True(t, hasIRIDegradedCondition,
			"MCN %s should have InternalReleaseImageDegraded condition", mcn.Name)
	}

	require.Greater(t, controlPlaneMCNs, 0, "Should have at least one control plane MCN")

	// Verify cluster IRI aggregates release names from MCNs
	for _, specRelease := range iri.Spec.Releases {
		found := false
		for _, statusRelease := range iri.Status.Releases {
			if statusRelease.Name == specRelease.Name {
				found = true
				break
			}
		}
		require.True(t, found, "Cluster IRI should aggregate release %s from MCNs", specRelease.Name)
	}
}

func TestInternalReleaseImageStatusConditions(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	iri, err := cs.MachineconfigurationV1Interface.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)

	// Verify cluster-level IRI has Degraded condition
	require.NotEmpty(t, iri.Status.Conditions, "IRI should have status conditions")

	foundDegraded := false
	for _, cond := range iri.Status.Conditions {
		if cond.Type == string(mcfgv1.InternalReleaseImageStatusConditionTypeDegraded) {
			foundDegraded = true

			// Verify condition has reason and message
			require.NotEmpty(t, cond.Reason, "Degraded condition should have a reason")
			require.NotEmpty(t, cond.Message, "Degraded condition should have a message")

			// Verify LastTransitionTime is set
			require.False(t, cond.LastTransitionTime.IsZero(),
				"Degraded condition should have LastTransitionTime set")

			// In a healthy cluster, should be False with AllReleasesAvailable
			require.Equal(t, v1.ConditionFalse, cond.Status,
				"Degraded condition should be False in healthy cluster")
			require.Equal(t, "AllReleasesAvailable", cond.Reason,
				"Degraded condition reason should be AllReleasesAvailable in healthy cluster")
		}
	}
	require.True(t, foundDegraded, "IRI should have Degraded condition")

	// Verify each release has proper conditions
	require.NotEmpty(t, iri.Status.Releases, "IRI should have releases")
	for _, release := range iri.Status.Releases {
		require.NotEmpty(t, release.Conditions, "Release %s should have conditions", release.Name)

		// Check for Available condition
		foundAvailable := false
		foundReleaseDegraded := false

		for _, cond := range release.Conditions {
			require.NotEmpty(t, cond.Reason, "Condition in release %s should have a reason", release.Name)
			require.NotEmpty(t, cond.Message, "Condition in release %s should have a message", release.Name)
			require.False(t, cond.LastTransitionTime.IsZero(),
				"Condition in release %s should have LastTransitionTime", release.Name)

			switch cond.Type {
			case string(mcfgv1.InternalReleaseImageConditionTypeAvailable):
				foundAvailable = true
				// In healthy cluster, Available should be True
				require.Equal(t, v1.ConditionTrue, cond.Status,
					"Available condition should be True for release %s in healthy cluster", release.Name)
			case string(mcfgv1.InternalReleaseImageConditionTypeDegraded):
				foundReleaseDegraded = true
				// In healthy cluster, Degraded should be False
				require.Equal(t, v1.ConditionFalse, cond.Status,
					"Degraded condition should be False for release %s in healthy cluster", release.Name)
			}
		}

		require.True(t, foundAvailable, "Release %s should have Available condition", release.Name)
		require.True(t, foundReleaseDegraded, "Release %s should have Degraded condition", release.Name)
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

	cs := framework.NewClientSet("")
	masterNodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), v1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
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
	iriRegistryUrl := fmt.Sprintf("https://%s:%d/v2/", ipAddr, ctrlcommon.IRIRegistryPort)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, iriRegistryUrl, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	apiVersion := resp.Header.Get("Docker-Distribution-Api-Version")
	require.Equal(t, "registry/2.0", apiVersion)
}

// getBaseDomain retrieves the cluster's base domain from the ControllerConfig.
func getBaseDomain(t *testing.T, cs *framework.ClientSet) string {
	t.Helper()
	cconfig, err := cs.ControllerConfigs().Get(context.Background(), ctrlcommon.ControllerConfigName, v1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, cconfig.Spec.DNS)
	require.NotEmpty(t, cconfig.Spec.DNS.Spec.BaseDomain)
	return cconfig.Spec.DNS.Spec.BaseDomain
}

func TestIRIRegistry_UnauthenticatedReadSucceeds(t *testing.T) {
	cs := framework.NewClientSet("")

	baseDomain := getBaseDomain(t, cs)
	node := helpers.GetRandomNode(t, cs, "master")

	url := fmt.Sprintf("https://api-int.%s:%d/v2/", baseDomain, ctrlcommon.IRIRegistryPort)
	args := []string{"curl", "-s", "--cacert", iriRootCAPath, "-o", "/dev/null", "-w", "%{http_code}", url}
	statusCode := strings.TrimSpace(helpers.ExecCmdOnNode(t, cs, node, args...))
	require.Equal(t, "200", statusCode, "unauthenticated read request should succeed")
}

// TestIRIController_VerifyTLSProfileEnforced verifies that changing the cluster's
// APIServer TLS profile actually changes what the IRI registry will negotiate.
//
// The test deliberately changes the profile rather than reading whichever one the
// cluster happens to have. A cluster on the default Intermediate profile resolves to
// tls1.2, which is also the registry's own built-in default, so a test that only
// observes the default passes identically with this feature reverted.
//
// It asserts TLS 1.2 before the change and TLS 1.3 after it. Both are versions the
// RHCOS system crypto policy allows the client to offer, so a handshake failure is
// attributable to the registry rejecting the version rather than to curl declining to
// offer it -- which would not be true of a TLS 1.0 or 1.1 probe.
//
// Uses ExecCmdOnNode to run curl on the node directly, since port 22625 is not
// accessible from the CI test runner.
func TestIRIController_VerifyTLSProfileEnforced(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Fetch authentication credentials for the IRI registry.
	authSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	require.NoError(t, err)
	password := string(authSecret.Data["password"])
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(ctrlcommon.IRIRegistryUsername+":"+password))

	// Find a master node to exec commands on.
	masterNodes, err := cs.CoreV1Interface.Nodes().List(ctx, v1.ListOptions{LabelSelector: "node-role.kubernetes.io/master="})
	require.NoError(t, err)
	require.NotEmpty(t, masterNodes.Items)
	node := masterNodes.Items[0]

	apiServerCfg, err := cs.ConfigV1Interface.APIServers().Get(ctx, ctrlcommon.APIServerInstanceName, v1.GetOptions{})
	require.NoError(t, err)
	originalProfile := apiServerCfg.Spec.TLSSecurityProfile

	// Registered before the first change so the cluster is restored even if a wait
	// below times out partway through a control plane roll.
	t.Cleanup(func() { setIRITLSProfile(t, cs, node, authHeader, originalProfile) })

	// Normalize to Intermediate rather than assuming the cluster starts on the default
	// profile: a cluster that already sets Modern would otherwise fail the TLS 1.2
	// baseline for a reason that has nothing to do with the registry. On the usual
	// unset-profile cluster this resolves to what is already rendered, so the IRI
	// controller produces an identical MachineConfig and nothing rolls.
	//
	// The matching sub-struct has to be set alongside Type: the APIServer CRD rejects
	// a profile that names a type without it.
	setIRITLSProfile(t, cs, node, authHeader, &configv1.TLSSecurityProfile{
		Type:         configv1.TLSProfileIntermediateType,
		Intermediate: &configv1.IntermediateTLSProfile{},
	})

	// Baseline: Intermediate resolves to tls1.2, and the client is able to negotiate
	// it. Without this the post-change assertion cannot distinguish "the registry now
	// refuses TLS 1.2" from "this client never offered TLS 1.2".
	t.Run("TLS 1.2 is accepted before the profile changes", func(t *testing.T) {
		require.True(t, iriRegistryAcceptsTLS(t, cs, node, authHeader, "1.2"))
	})

	setIRITLSProfile(t, cs, node, authHeader, &configv1.TLSSecurityProfile{
		Type:   configv1.TLSProfileModernType,
		Modern: &configv1.ModernTLSProfile{},
	})

	t.Run("TLS 1.3 is accepted with the Modern profile", func(t *testing.T) {
		require.True(t, iriRegistryAcceptsTLS(t, cs, node, authHeader, "1.3"),
			"registry should accept TLS 1.3 under the Modern profile")
	})

	t.Run("TLS 1.2 is rejected with the Modern profile", func(t *testing.T) {
		require.False(t, iriRegistryAcceptsTLS(t, cs, node, authHeader, "1.2"),
			"registry should reject TLS 1.2 under the Modern profile, which sets a minimum of TLS 1.3")
	})
}

// setIRITLSProfile sets the cluster APIServer TLS profile and blocks until the IRI
// registry on the given node is serving the resulting configuration.
//
// It waits on the registry's observable behaviour rather than on MachineConfigPool
// status: the profile change has to travel through the IRI controller, a re-rendered
// MachineConfig, and an MCD-driven daemon-reload and service restart, and only the
// last of those is what the assertions care about.
func setIRITLSProfile(t *testing.T, cs *framework.ClientSet, node corev1.Node, authHeader string, profile *configv1.TLSSecurityProfile) {
	t.Helper()
	ctx := context.Background()

	// The condition has to describe the whole end state, not just "the new minimum
	// version is accepted". A registry on Intermediate already accepts TLS 1.3, since
	// tls1.2 is a floor rather than a pin, so waiting only for TLS 1.3 would return on
	// the first poll -- before the Modern config had been applied -- and hand the
	// caller a registry that still accepts TLS 1.2.
	settled := func() bool { return iriRegistryAcceptsTLS(t, cs, node, authHeader, "1.2") }
	wanted := "accept TLS 1.2"
	if profile != nil && profile.Type == configv1.TLSProfileModernType {
		settled = func() bool {
			return iriRegistryAcceptsTLS(t, cs, node, authHeader, "1.3") &&
				!iriRegistryAcceptsTLS(t, cs, node, authHeader, "1.2")
		}
		wanted = "accept TLS 1.3 and refuse TLS 1.2"
	}
	t.Logf("Setting APIServer TLS profile to %v and waiting for the IRI registry to %s", profile, wanted)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		apiServerCfg, err := cs.ConfigV1Interface.APIServers().Get(ctx, ctrlcommon.APIServerInstanceName, v1.GetOptions{})
		if err != nil {
			return err
		}
		apiServerCfg.Spec.TLSSecurityProfile = profile
		_, err = cs.ConfigV1Interface.APIServers().Update(ctx, apiServerCfg, v1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)

	// The control plane rolls to pick up the new profile, so this is slow.
	err = wait.PollUntilContextTimeout(ctx, 30*time.Second, 30*time.Minute, true, func(_ context.Context) (bool, error) {
		return settled(), nil
	})
	require.NoError(t, err, "IRI registry did not %s after the profile change", wanted)
}

// iriRegistryAcceptsTLS reports whether the IRI registry on the node completes a
// request pinned to exactly the given TLS version.
func iriRegistryAcceptsTLS(t *testing.T, cs *framework.ClientSet, node corev1.Node, authHeader, version string) bool {
	t.Helper()

	out, err := helpers.ExecCmdOnNodeWithError(cs, node, "curl", "-s", "-k", "-o", "/dev/null", "-w", "%{http_code}",
		"-H", "Authorization: "+authHeader,
		"--tlsv"+version, "--tls-max", version,
		"https://localhost:22625/v2/")
	if err != nil {
		t.Logf("TLS %s request to the IRI registry failed: %v", version, err)
		return false
	}
	return strings.TrimSpace(out) == "200"
}

func TestIRIController_ShouldPreventDeletionWhenInUse(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Get the InternalReleaseImage resource
	iri, err := cs.MachineconfigurationV1Interface.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)

	// Verify the IRI has releases in its status
	require.NotEmpty(t, iri.Status.Releases, "IRI should have releases in status")

	// Get the ClusterVersion to know what release the cluster is using
	cv, err := cs.ClusterVersions().Get(ctx, "version", v1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, cv.Status.Desired.Image, "ClusterVersion should have a desired image")

	// Verify that at least one release in IRI matches the current cluster version
	// Note: Compare SHA256 digests, not full URLs, because IRI uses api-int registry
	// while ClusterVersion uses the external registry
	cvDigest := extractSHA256Digest(cv.Status.Desired.Image)
	require.NotEmpty(t, cvDigest, "ClusterVersion image should have a SHA256 digest")

	matchFound := false
	for _, release := range iri.Status.Releases {
		releaseDigest := extractSHA256Digest(release.Image)
		if releaseDigest == cvDigest {
			matchFound = true
			break
		}
	}
	require.True(t, matchFound, "IRI should contain a release matching the current cluster version (digest: %s)", cvDigest)

	// Attempt to delete the InternalReleaseImage - this should fail
	err = cs.MachineconfigurationV1Interface.InternalReleaseImages().Delete(ctx, "cluster", v1.DeleteOptions{})
	require.Error(t, err, "Deleting IRI while in use should fail")

	// Verify the error is an Invalid error from the ValidatingAdmissionPolicy
	// ValidatingAdmissionPolicy returns Invalid (422) errors, not Forbidden (403)
	require.True(t, k8serrors.IsInvalid(err), "Error should be an Invalid error from admission policy, got: %v", err)
	require.Contains(t, err.Error(), "Cannot delete InternalReleaseImage while the cluster is using a release bundle from this resource",
		"Error message should indicate the IRI is in use")

	// Verify the IRI still exists
	iri, err = cs.MachineconfigurationV1Interface.InternalReleaseImages().Get(ctx, "cluster", v1.GetOptions{})
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
				if strings.HasSuffix(mc.Name, "-internalreleaseimage") {
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

// TestCertRotation_IRICertIsRegeneratedOnCARotation verifies that when the MCS CA
// rotates, the cert rotation controller regenerates the IRI TLS certificate and
// signs it with the new CA.
func TestCertRotation_IRICertIsRegeneratedOnCARotation(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Record the current IRI TLS cert so we can detect when it changes.
	iriSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageTLSSecretName, v1.GetOptions{})
	require.NoError(t, err)
	originalCert := iriSecret.Data[corev1.TLSCertKey]
	require.NotEmpty(t, originalCert, "IRI TLS cert should be present before rotation")

	// Trigger MCS CA rotation by deleting the CA secret. library-go's
	// CertRotationController recreates the secret with a new keypair and then
	// reconciles the machine-config-server-ca ConfigMap (the CA bundle). The
	// configmap update event fires updateConfigMap(), which calls
	// reconcileIRICertificate() to regenerate the IRI TLS cert.
	err = cs.Secrets(ctrlcommon.MCONamespace).Delete(ctx, ctrlcommon.MachineConfigServerCAName, v1.DeleteOptions{})
	require.NoError(t, err)

	// Wait for the IRI TLS cert to be replaced.
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		updated, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageTLSSecretName, v1.GetOptions{})
		if err != nil {
			return false, err
		}
		return !bytes.Equal(updated.Data[corev1.TLSCertKey], originalCert), nil
	})
	require.NoError(t, err, "IRI TLS cert should have been rotated after MCS CA rotation")

	// Verify the new cert is signed by the new MCS CA bundle.
	cm, err := cs.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.MachineConfigServerCAName, v1.GetOptions{})
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(cm.Data["ca-bundle.crt"])))

	iriSecret, err = cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageTLSSecretName, v1.GetOptions{})
	require.NoError(t, err)

	block, _ := pem.Decode(iriSecret.Data[corev1.TLSCertKey])
	require.NotNil(t, block, "Should be able to decode rotated IRI TLS cert PEM")
	iriCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	_, err = iriCert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	require.NoError(t, err, "Rotated IRI TLS cert should be signed by the new MCS CA")
}

// TestIRIController_VerifyMLKEMSupport verifies that the IRI registry supports
// ML-KEM (post-quantum) key exchange as required for OpenShift 4.22+.
func TestIRIController_VerifyMLKEMSupport(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")

	baseDomain := getBaseDomain(t, cs)
	node := helpers.GetRandomNode(t, cs, "master")

	target := fmt.Sprintf("api-int.%s:%d", baseDomain, ctrlcommon.IRIRegistryPort)

	output := helpers.ExecCmdOnNode(t, cs, node,
		"bash", "-c",
		fmt.Sprintf("echo | openssl s_client -connect %s -CAfile %s -groups X25519MLKEM768 -tls1_3 -verify_return_error 2>&1 | grep -E 'Cipher|TLSv1.3|group'",
			target, iriRootCAPath))

	t.Logf("openssl s_client output:\n%s", output)

	require.Contains(t, output, "TLSv1.3", "IRI registry should support TLS 1.3")
	require.True(t,
		strings.Contains(output, "X25519MLKEM768") || strings.Contains(output, "x25519_mlkem768"),
		"IRI registry should support X25519MLKEM768 (ML-KEM) key exchange. Output: %s", output)
}

// extractSHA256Digest extracts the SHA256 digest from an OCI image reference.
// Example: "registry.example.com/repo/image@sha256:abc123..." → "sha256:abc123..."
// Returns empty string if no digest is found.
func extractSHA256Digest(imageRef string) string {
	// Split on @ to get the digest part
	parts := strings.SplitN(imageRef, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	digest := parts[1]
	// Verify it's a sha256 digest
	if strings.HasPrefix(digest, "sha256:") {
		return digest
	}
	return ""
}
