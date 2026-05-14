package e2e_iri_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
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
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	iripkg "github.com/openshift/machine-config-operator/pkg/controller/internalreleaseimage"
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
		pingIRIRegistry(t, client, nodeAddr, ctrlcommon.IRIRegistryUsername, password)
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
	pingIRIRegistry(t, client, infra.Status.PlatformStatus.BareMetal.APIServerInternalIPs[0], ctrlcommon.IRIRegistryUsername, string(authSecret.Data["password"]))
}

func pingIRIRegistry(t *testing.T, client *http.Client, ipAddr, username, password string) {
	iriRegistryUrl := fmt.Sprintf("https://%s:%d/v2/", ipAddr, ctrlcommon.IRIRegistryPort)

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
	url := fmt.Sprintf("https://api-int.%s:%d/v2/", baseDomain, ctrlcommon.IRIRegistryPort)
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
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(ctrlcommon.IRIRegistryUsername+":"+password))

	statusCode := curlIRIRegistry(t, cs, node, baseDomain, "-H", "Authorization: "+authHeader)
	require.Equal(t, "200", statusCode, "authenticated request should succeed")
}

// getIRIReleasePullSpec queries the IRI registry's release-images tags list and
// returns a pullspec of the form api-int.<baseDomain>:<port>/openshift/release-images:<tag>.
// It retries until tags are available to handle brief registry stabilization delays
// after credential restores or pool rollouts.
func getIRIReleasePullSpec(t *testing.T, cs *framework.ClientSet, node corev1.Node, baseDomain, password string) string {
	t.Helper()
	const iriRootCAPath = "/rootfs/etc/pki/ca-trust/source/anchors/iri-root-ca.crt"
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(ctrlcommon.IRIRegistryUsername+":"+password))
	url := fmt.Sprintf("https://api-int.%s:%d/v2/openshift/release-images/tags/list", baseDomain, ctrlcommon.IRIRegistryPort)

	var tag string
	err := wait.PollUntilContextTimeout(context.Background(), 10*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		body := strings.TrimSpace(helpers.ExecCmdOnNode(t, cs, node,
			"curl", "-s", "--cacert", iriRootCAPath, "-H", "Authorization: "+authHeader, url))
		var tagsResp struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(body), &tagsResp); err != nil || len(tagsResp.Tags) == 0 {
			return false, nil
		}
		tag = tagsResp.Tags[0]
		return true, nil
	})
	require.NoError(t, err, "timed out waiting for IRI release-images tags to be available")

	return fmt.Sprintf("api-int.%s:%d/openshift/release-images:%s", baseDomain, ctrlcommon.IRIRegistryPort, tag)
}

// verifyCanPullFromIRI creates a pod with imagePullPolicy:Always using the
// given IRI registry image and verifies the kubelet can authenticate and pull
// it. Returns nil when the image is pulled successfully (pod reaches Running,
// Succeeded, or Failed phase — all indicate auth succeeded). If the pod hits
// ImagePullBackOff (CRI-O may cache auth failures briefly), it is deleted and
// recreated to force a fresh authentication attempt.
func verifyCanPullFromIRI(t *testing.T, cs *framework.ClientSet, ctx context.Context, imageRef, podName string) error {
	t.Helper()
	newPod := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: v1.ObjectMeta{
				Name:      name,
				Namespace: ctrlcommon.MCONamespace,
			},
			Spec: corev1.PodSpec{
				RestartPolicy: corev1.RestartPolicyNever,
				Containers: []corev1.Container{
					{
						Name:            "iri-pull-test",
						Image:           imageRef,
						ImagePullPolicy: corev1.PullAlways,
						// The command may fail if the image has no shell; that is
						// acceptable — we only care that the image was pulled.
						Command: []string{"sh", "-c", "exit 0"},
					},
				},
			},
		}
	}

	attempt := 0
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		name := fmt.Sprintf("%s-%d", podName, attempt)
		attempt++
		if _, err := cs.Pods(ctrlcommon.MCONamespace).Create(ctx, newPod(name), v1.CreateOptions{}); err != nil {
			return false, fmt.Errorf("failed to create pull-test pod: %w", err)
		}
		defer cs.Pods(ctrlcommon.MCONamespace).Delete(context.Background(), name, v1.DeleteOptions{})

		err := waitForPodImagePull(ctx, cs, name)
		if errors.Is(err, errImagePullBackOff) {
			// CRI-O cached the auth failure — outer loop retries with a new pod.
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	})
}

// errImagePullBackOff is returned by waitForPodImagePull when the pod hits
// ImagePullBackOff or ErrImagePull, indicating a cached auth failure.
var errImagePullBackOff = errors.New("ImagePullBackOff")

// waitForPodImagePull polls the given pod until its image is pulled (pod
// reaches Running/Succeeded/Failed) or returns errImagePullBackOff if CRI-O
// rejects the pull due to a cached auth failure.
func waitForPodImagePull(ctx context.Context, cs *framework.ClientSet, podName string) error {
	return wait.PollUntilContextTimeout(ctx, 3*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		p, err := cs.Pods(ctrlcommon.MCONamespace).Get(ctx, podName, v1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, s := range p.Status.ContainerStatuses {
			if s.State.Waiting == nil {
				continue
			}
			switch s.State.Waiting.Reason {
			case "ImagePullBackOff", "ErrImagePull":
				return false, errImagePullBackOff
			}
		}
		switch p.Status.Phase {
		case corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
			return true, nil
		}
		return false, nil
	})
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

	// Get the IRI release image pullspec for pod-based pull verification.
	// The pullspec is constructed from the registry's tags list so it references
	// the local IRI registry, not the original quay.io source.
	node := helpers.GetRandomNode(t, cs, "master")
	iriImageRef := getIRIReleasePullSpec(t, cs, node, baseDomain, originalPassword)

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

	// Verify kubelet can pull from the IRI registry with the current credentials.
	t.Logf("Verifying kubelet can pull from IRI registry before rotation...")
	require.NoError(t, verifyCanPullFromIRI(t, cs, ctx, iriImageRef, "iri-pull-pre-rotation"),
		"kubelet should be able to pull from IRI registry before credential rotation")
	t.Logf("Pre-rotation pull verified")

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
		return iripkg.HtpasswdMatchesPassword(string(secret.Data["htpasswd"]), ctrlcommon.IRIRegistryUsername, newPassword), nil
	})
	require.NoError(t, err, "timed out waiting for htpasswd regeneration")
	t.Logf("Controller regenerated htpasswd")

	// Poll until the new credentials are accepted. The registry only accepts
	// them once MCD has written the new htpasswd file to the node, so this
	// also serves as the rollout completion check.
	newAuthHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(ctrlcommon.IRIRegistryUsername+":"+newPassword))
	oldAuthHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(ctrlcommon.IRIRegistryUsername+":"+originalPassword))

	t.Logf("Waiting for new credentials to be accepted on node...")
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		return curlIRIRegistry(t, cs, node, baseDomain, "-H", "Authorization: "+newAuthHeader) == "200", nil
	})
	require.NoError(t, err, "timed out waiting for new credentials to be accepted after rotation")
	t.Logf("New credentials accepted")

	// Wait for the full master pool rollout to complete before asserting old
	// credentials are rejected. The curlIRIRegistry check above only proves one
	// backend accepted the new htpasswd; without waiting for pool completion the
	// old-credential probe could land on an unrotated master via the api-int VIP.
	t.Logf("Waiting for master pool rollout to complete...")
	require.NoError(t, helpers.WaitForPoolCompleteAny(t, cs, "master"), "master pool rollout did not complete after credential rotation")
	t.Logf("Master pool rollout complete")

	statusCode := curlIRIRegistry(t, cs, node, baseDomain, "-H", "Authorization: "+oldAuthHeader)
	require.Equal(t, "401", statusCode, "old credentials should be rejected after rotation")
	t.Logf("Old credentials correctly rejected with %s", statusCode)

	// Wait for the template controller to re-render 00-master with the new pull
	// secret credentials and for MCD to apply it. Rotation triggers two sequential
	// MC rollouts: first 02-master (htpasswd), then 00-master (pull secret).
	// WaitForPoolCompleteAny returns after the first; we need the second to complete
	// before the kubelet pull check so /var/lib/kubelet/config.json is updated.
	t.Logf("Waiting for pull secret credentials to be updated on node...")
	iriHost := fmt.Sprintf("api-int.%s:%d", baseDomain, ctrlcommon.IRIRegistryPort)
	newAuthB64 := base64.StdEncoding.EncodeToString([]byte(ctrlcommon.IRIRegistryUsername + ":" + newPassword))
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		out := strings.TrimSpace(helpers.ExecCmdOnNode(t, cs, node,
			"cat", "/rootfs/var/lib/kubelet/config.json"))
		var cfg map[string]interface{}
		if jsonErr := json.Unmarshal([]byte(out), &cfg); jsonErr != nil {
			return false, nil
		}
		auths, _ := cfg["auths"].(map[string]interface{})
		entry, _ := auths[iriHost].(map[string]interface{})
		return entry["auth"] == newAuthB64, nil
	})
	require.NoError(t, err, "timed out waiting for kubelet config.json to be updated with new IRI credentials")
	t.Logf("Kubelet config.json updated with new credentials")

	// Verify kubelet can still pull from the IRI registry with the new credentials.
	// This exercises the full kubelet pull path (/var/lib/kubelet/config.json) rather
	// than just raw HTTP auth.
	t.Logf("Verifying kubelet can pull from IRI registry after rotation...")
	require.NoError(t, verifyCanPullFromIRI(t, cs, ctx, iriImageRef, "iri-pull-post-rotation"),
		"kubelet should be able to pull from IRI registry after credential rotation")
	t.Logf("Post-rotation pull verified")

	t.Logf("Credential rotation completed successfully")
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
