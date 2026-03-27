package e2e_iri_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
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

	cs := framework.NewClientSet("")
	authSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(context.TODO(), ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	require.NoError(t, err)
	password := string(authSecret.Data["password"])

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
		pingIRIRegistry(t, client, nodeAddr, "openshift", password)
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

	authSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	require.NoError(t, err)
	password := string(authSecret.Data["password"])

	pingIRIRegistry(t, client, infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs[0], "openshift", password)
}

func pingIRIRegistry(t *testing.T, client *http.Client, ipAddr, username, password string) {
	iriRegistryUrl := fmt.Sprintf("https://%s:%d/v2/", ipAddr, 22625)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, iriRegistryUrl, nil)
	require.NoError(t, err)
	req.SetBasicAuth(username, password)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	apiVersion := resp.Header.Get("Docker-Distribution-Api-Version")
	require.Equal(t, "registry/2.0", apiVersion)
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
	t.Helper()
	cconfig, err := cs.ControllerConfigs().Get(context.Background(), ctrlcommon.ControllerConfigName, v1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, cconfig.Spec.DNS)
	require.NotEmpty(t, cconfig.Spec.DNS.Spec.BaseDomain)
	return cconfig.Spec.DNS.Spec.BaseDomain
}

// curlIRIRegistry runs curl against the IRI registry /v2/ endpoint via
// ExecCmdOnNode, which executes inside the MCD pod on the given master node.
// The MCD pod runs on the host network and can reach api-int:22625, making
// this approach work in CI where the port is not reachable from the test runner.
// Returns the HTTP status code string (e.g. "200", "401").
func curlIRIRegistry(t *testing.T, cs *framework.ClientSet, node corev1.Node, baseDomain string, extraArgs ...string) string {
	t.Helper()
	const iriRootCAPath = "/rootfs/etc/pki/ca-trust/source/anchors/iri-root-ca.crt"
	url := fmt.Sprintf("https://api-int.%s:22625/v2/", baseDomain)
	args := []string{"curl", "-s", "--cacert", iriRootCAPath, "-o", "/dev/null", "-w", "%{http_code}"}
	args = append(args, extraArgs...)
	args = append(args, url)
	return strings.TrimSpace(helpers.ExecCmdOnNode(t, cs, node, args...))
}

func TestIRIAuth_UnauthenticatedRequestReturns401(t *testing.T) {
	cs := framework.NewClientSet("")
	ctx := context.Background()

	_, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		t.Skip("IRI auth secret not found, authentication is not enabled")
	}
	require.NoError(t, err)

	baseDomain := getBaseDomain(t, cs)
	node := helpers.GetRandomNode(t, cs, "master")

	statusCode := curlIRIRegistry(t, cs, node, baseDomain)
	require.Equal(t, "401", statusCode, "unauthenticated request should return 401")
}

func TestIRIAuth_AuthenticatedRequestSucceeds(t *testing.T) {
	cs := framework.NewClientSet("")
	ctx := context.Background()

	authSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		t.Skip("IRI auth secret not found, authentication is not enabled")
	}
	require.NoError(t, err)

	password := string(authSecret.Data["password"])
	require.NotEmpty(t, password)

	baseDomain := getBaseDomain(t, cs)
	node := helpers.GetRandomNode(t, cs, "master")
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(iri.IRIRegistryUsername+":"+password))

	statusCode := curlIRIRegistry(t, cs, node, baseDomain, "-H", "Authorization: "+authHeader)
	require.Equal(t, "200", statusCode, "authenticated request should succeed")
}

func TestIRIAuth_CredentialRotation(t *testing.T) {
	cs := framework.NewClientSet("")
	ctx := context.Background()

	authSecret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		t.Skip("IRI auth secret not found, authentication is not enabled")
	}
	require.NoError(t, err)

	originalPassword := string(authSecret.Data["password"])
	originalHtpasswd := string(authSecret.Data["htpasswd"])
	require.NotEmpty(t, originalPassword)

	baseDomain := getBaseDomain(t, cs)

	// Restore credentials on test completion.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		secret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(cleanupCtx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
		if err != nil {
			t.Errorf("cleanup: failed to get auth secret: %v", err)
			return
		}
		secret.Data["password"] = []byte(originalPassword)
		secret.Data["htpasswd"] = []byte(originalHtpasswd)
		if _, err := cs.Secrets(ctrlcommon.MCONamespace).Update(cleanupCtx, secret, v1.UpdateOptions{}); err != nil {
			t.Errorf("cleanup: failed to restore auth secret: %v", err)
			return
		}
		t.Logf("Cleanup: restored auth secret, waiting for MCP rollout...")
		if err := helpers.WaitForPoolCompleteAny(t, cs, "master"); err != nil {
			t.Errorf("cleanup: MCP rollout did not complete: %v", err)
		}
		t.Logf("Cleanup: credential restoration complete")
	})

	// Trigger rotation by writing a new password.
	newPassword := fmt.Sprintf("rotated-%d", time.Now().UnixNano())
	authSecret.Data["password"] = []byte(newPassword)
	delete(authSecret.Data, "htpasswd") // controller will regenerate
	_, err = cs.Secrets(ctrlcommon.MCONamespace).Update(ctx, authSecret, v1.UpdateOptions{})
	require.NoError(t, err)
	t.Logf("Updated auth secret password to trigger rotation")

	// Wait for the controller to regenerate htpasswd.
	t.Logf("Waiting for controller to regenerate htpasswd...")
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		secret, err := cs.Secrets(ctrlcommon.MCONamespace).Get(ctx, ctrlcommon.InternalReleaseImageAuthSecretName, v1.GetOptions{})
		if err != nil {
			return false, err
		}
		return iri.HtpasswdMatchesPassword(string(secret.Data["htpasswd"]), iri.IRIRegistryUsername, newPassword), nil
	})
	require.NoError(t, err, "timed out waiting for htpasswd regeneration")
	t.Logf("Controller regenerated htpasswd")

	// Wait for the pull secret to be updated with the new password.
	t.Logf("Waiting for pull secret update...")
	iriHost := fmt.Sprintf("api-int.%s:22625", baseDomain)
	expectedAuth := base64.StdEncoding.EncodeToString([]byte(iri.IRIRegistryUsername + ":" + newPassword))
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		ps, err := cs.Secrets(ctrlcommon.OpenshiftConfigNamespace).Get(ctx, ctrlcommon.GlobalPullSecretName, v1.GetOptions{})
		if err != nil {
			return false, err
		}
		var dockerConfig map[string]interface{}
		if err := json.Unmarshal(ps.Data[corev1.DockerConfigJsonKey], &dockerConfig); err != nil {
			return false, err
		}
		auths, _ := dockerConfig["auths"].(map[string]interface{})
		entry, _ := auths[iriHost].(map[string]interface{})
		return entry["auth"] == expectedAuth, nil
	})
	require.NoError(t, err, "timed out waiting for pull secret update")
	t.Logf("Pull secret updated with new credentials")

	// Poll until the new credentials are accepted. The registry only accepts
	// them once MCD has written the new htpasswd file to the node, so this
	// also serves as the rollout completion check.
	node := helpers.GetRandomNode(t, cs, "master")
	newAuthHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(iri.IRIRegistryUsername+":"+newPassword))
	oldAuthHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(iri.IRIRegistryUsername+":"+originalPassword))

	t.Logf("Waiting for new credentials to be accepted on node...")
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		return curlIRIRegistry(t, cs, node, baseDomain, "-H", "Authorization: "+newAuthHeader) == "200", nil
	})
	require.NoError(t, err, "timed out waiting for new credentials to be accepted after rotation")
	t.Logf("New credentials accepted")

	statusCode := curlIRIRegistry(t, cs, node, baseDomain, "-H", "Authorization: "+oldAuthHeader)
	require.Equal(t, "401", statusCode, "old credentials should be rejected after rotation")
	t.Logf("Old credentials correctly rejected with %s", statusCode)

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
