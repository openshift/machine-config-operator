package e2e_iri_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	iri "github.com/openshift/machine-config-operator/pkg/controller/internalreleaseimage"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestIRIResource_Available(t *testing.T) {
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

func TestIRIController_VerifyIRIRegistryOnAllTheMasterNodes_NoCert(t *testing.T) {
	skipIfOpenShiftCI(t)

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

func TestIRIController_ShouldPreventDeletionWhenInUse(t *testing.T) {
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

// getBaseDomain retrieves the cluster's base domain from the ControllerConfig.
func getBaseDomain(t *testing.T, cs *framework.ClientSet) string {
	ctx := context.Background()
	cconfig, err := cs.ControllerConfigs().Get(ctx, ctrlcommon.ControllerConfigName, v1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, cconfig.Spec.DNS)
	require.NotEmpty(t, cconfig.Spec.DNS.Spec.BaseDomain)
	return cconfig.Spec.DNS.Spec.BaseDomain
}

// getIRIRegistryClient returns an HTTP client configured with the cluster's
// root CA for making requests to the IRI registry.
func getIRIRegistryClient(t *testing.T, cs *framework.ClientSet) *http.Client {
	ctx := context.Background()
	cm, err := cs.ConfigMaps("openshift-machine-config-operator").Get(ctx, "machine-config-server-ca", v1.GetOptions{})
	require.NoError(t, err)
	rootCA := []byte(cm.Data["ca-bundle.crt"])
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(rootCA))

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: roots,
			},
		},
		Timeout: 30 * time.Second,
	}
}

// getAPIIntIP returns the api-int IP address from the infrastructure resource.
func getAPIIntIP(t *testing.T, cs *framework.ClientSet) string {
	ctx := context.Background()
	infra, err := cs.Infrastructures().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs)
	return infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs[0]
}

func TestIRIAuth_UnauthenticatedRequestReturns401(t *testing.T) {
	skipIfOpenShiftCI(t)
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")

	// Verify the auth secret exists (auth is enabled).
	ctx := context.Background()
	_, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		t.Skip("IRI auth secret not found, authentication is not enabled")
	}
	require.NoError(t, err)

	client := getIRIRegistryClient(t, cs)
	apiIntIP := getAPIIntIP(t, cs)
	url := fmt.Sprintf("https://%s:%d/v2/", apiIntIP, 22625)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"unauthenticated request should return 401 when auth is enabled")
}

func TestIRIAuth_AuthenticatedRequestSucceeds(t *testing.T) {
	skipIfOpenShiftCI(t)
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Get the auth secret to read credentials.
	authSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		t.Skip("IRI auth secret not found, authentication is not enabled")
	}
	require.NoError(t, err)

	password := string(authSecret.Data["password"])
	require.NotEmpty(t, password)

	// Extract the current username from the pull secret.
	baseDomain := getBaseDomain(t, cs)
	pullSecret, err := cs.Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(ctx, ctrlcommon.GlobalPullSecretName, v1.GetOptions{})
	require.NoError(t, err)
	username, _ := iri.ExtractIRICredentialsFromPullSecret(pullSecret.Data[corev1.DockerConfigJsonKey], baseDomain)
	if username == "" {
		username = iri.IRIBaseUsername
	}

	client := getIRIRegistryClient(t, cs)
	apiIntIP := getAPIIntIP(t, cs)
	url := fmt.Sprintf("https://%s:%d/v2/", apiIntIP, 22625)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"authenticated request should succeed")
	require.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-Api-Version"))
}

func TestIRIAuth_CredentialRotation(t *testing.T) {
	skipIfOpenShiftCI(t)
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	// Get the auth secret.
	authSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		t.Skip("IRI auth secret not found, authentication is not enabled")
	}
	require.NoError(t, err)

	originalPassword := string(authSecret.Data["password"])
	require.NotEmpty(t, originalPassword)

	baseDomain := getBaseDomain(t, cs)

	// Record the original pull secret credentials.
	pullSecret, err := cs.Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(ctx, ctrlcommon.GlobalPullSecretName, v1.GetOptions{})
	require.NoError(t, err)
	originalUsername, originalPullSecretPassword := iri.ExtractIRICredentialsFromPullSecret(pullSecret.Data[corev1.DockerConfigJsonKey], baseDomain)
	require.NotEmpty(t, originalPullSecretPassword, "pull secret should have IRI credentials before rotation")

	// Record the current rendered MC names for master and worker pools so we
	// can detect when the MCP rollout completes.
	masterMCP, err := cs.MachineConfigPools().Get(ctx, "master", v1.GetOptions{})
	require.NoError(t, err)
	workerMCP, err := cs.MachineConfigPools().Get(ctx, "worker", v1.GetOptions{})
	require.NoError(t, err)

	t.Logf("Before rotation: username=%s, master rendered=%s, worker rendered=%s",
		originalUsername, masterMCP.Status.Configuration.Name, workerMCP.Status.Configuration.Name)

	// Trigger rotation by updating the password in the auth secret.
	newPassword := fmt.Sprintf("rotated-%d", time.Now().UnixNano())
	authSecret.Data["password"] = []byte(newPassword)
	_, err = cs.Secrets(ctrlcommon.MCONamespace).Update(ctx, authSecret, v1.UpdateOptions{})
	require.NoError(t, err)
	t.Logf("Updated auth secret password to trigger rotation")

	// Phase 1: Wait for dual htpasswd to be written to the auth secret.
	t.Logf("Waiting for phase 1: dual htpasswd deployment...")
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		secret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
		if err != nil {
			return false, err
		}
		htpasswd := string(secret.Data["htpasswd"])
		// Dual htpasswd has the new password under a new username.
		newUsername := iri.NextIRIUsername(originalUsername)
		return iri.HtpasswdHasValidEntry(htpasswd, newUsername, newPassword), nil
	})
	require.NoError(t, err, "timed out waiting for dual htpasswd (phase 1)")
	t.Logf("Phase 1 complete: dual htpasswd written")

	// Phase 2: Wait for MCP rollout to complete and pull secret to be updated.
	// The controller waits for all MCPs to be fully updated before writing the
	// pull secret, so we poll the pull secret for the new credentials.
	t.Logf("Waiting for phase 2: pull secret update...")
	expectedUsername := iri.NextIRIUsername(originalUsername)
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 30*time.Minute, true, func(ctx context.Context) (bool, error) {
		ps, err := cs.Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(ctx, ctrlcommon.GlobalPullSecretName, v1.GetOptions{})
		if err != nil {
			return false, err
		}
		u, p := iri.ExtractIRICredentialsFromPullSecret(ps.Data[corev1.DockerConfigJsonKey], baseDomain)
		return u == expectedUsername && p == newPassword, nil
	})
	require.NoError(t, err, "timed out waiting for pull secret update (phase 2)")
	t.Logf("Phase 2 complete: pull secret updated with username=%s", expectedUsername)

	// Wait for both pools to finish rolling out the updated pull secret.
	t.Logf("Waiting for MCP rollout after pull secret update...")
	helpers.WaitForPoolCompleteAny(t, cs, "master")
	helpers.WaitForPoolCompleteAny(t, cs, "worker")

	// Phase 3: Wait for dual htpasswd to be cleaned up to a single entry.
	t.Logf("Waiting for phase 3: htpasswd cleanup...")
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 30*time.Minute, true, func(ctx context.Context) (bool, error) {
		secret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
		if err != nil {
			return false, err
		}
		htpasswd := string(secret.Data["htpasswd"])
		// After cleanup, the htpasswd should only have the new entry.
		hasNew := iri.HtpasswdHasValidEntry(htpasswd, expectedUsername, newPassword)
		hasOld := iri.HtpasswdHasValidEntry(htpasswd, originalUsername, originalPullSecretPassword)
		return hasNew && !hasOld, nil
	})
	require.NoError(t, err, "timed out waiting for htpasswd cleanup (phase 3)")
	t.Logf("Phase 3 complete: htpasswd cleaned up to single entry")

	// Verify the registry accepts the new credentials.
	client := getIRIRegistryClient(t, cs)
	apiIntIP := getAPIIntIP(t, cs)
	url := fmt.Sprintf("https://%s:%d/v2/", apiIntIP, 22625)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(expectedUsername+":"+newPassword)))

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"registry should accept the new rotated credentials")
	t.Logf("Credential rotation completed successfully")
}

func TestIRIController_ShouldRestoreMachineConfigsWhenModified(t *testing.T) {
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
