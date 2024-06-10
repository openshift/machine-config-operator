package e2e_techpreview_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	imagev1 "github.com/openshift/api/image/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	aggerrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

func createMachineOSConfig(t *testing.T, cs *framework.ClientSet, mosc *mcfgv1alpha1.MachineOSConfig) func() {
	_, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Create(context.TODO(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created MachineOSConfig %q", mosc.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Delete(context.TODO(), mosc.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted MachineOSConfig %q", mosc.Name)
	})
}

// Identifies a secret in the MCO namespace that has permissions to push to the ImageStream used for the test.
func getBuilderPushSecretName(cs *framework.ClientSet) (string, error) {
	secrets, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	for _, secret := range secrets.Items {
		if strings.HasPrefix(secret.Name, "builder-dockercfg") {
			return secret.Name, nil
		}
	}

	return "", fmt.Errorf("could not find matching secret name in namespace %s", ctrlcommon.MCONamespace)
}

// Gets the ImageStream pullspec for the ImageStream used for the test.
func getImagestreamPullspec(cs *framework.ClientSet, name string) (string, error) {
	is, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s:latest", is.Status.DockerImageRepository), nil
}

// Creates an OpenShift ImageStream in the MCO namespace for the test and
// registers a cleanup function.
func createImagestream(t *testing.T, cs *framework.ClientSet, name string) func() {
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MCONamespace,
		},
	}

	_, err := cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Create(context.TODO(), is, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created ImageStream %q", name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.ImageV1Interface.ImageStreams(ctrlcommon.MCONamespace).Delete(context.TODO(), name, metav1.DeleteOptions{}))
		t.Logf("Deleted ImageStream %q", name)
	})
}

// Creates the on-cluster-build-custom-dockerfile ConfigMap and registers a cleanup function.
func createCustomDockerfileConfigMap(t *testing.T, cs *framework.ClientSet, customDockerfiles map[string]string) func() {
	return createConfigMap(t, cs, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "on-cluster-build-custom-dockerfile",
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: customDockerfiles,
	})
}

// Creates a given ConfigMap and registers a cleanup function to delete it.
func createConfigMap(t *testing.T, cs *framework.ClientSet, cm *corev1.ConfigMap) func() {
	_, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Create(context.TODO(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created ConfigMap %q", cm.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{}))
		klog.Infof("Deleted ConfigMap %q", cm.Name)
	})
}

// Creates a given Secret and registers a cleanup function to delete it.
func createSecret(t *testing.T, cs *framework.ClientSet, secret *corev1.Secret) func() {
	_, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created secret %q", secret.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secret.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted secret %q", secret.Name)
	})
}

// Copies the global pull secret from openshift-config/pull-secret into the MCO
// namespace so that it can be used by the build processes.
func copyGlobalPullSecret(t *testing.T, cs *framework.ClientSet) func() {
	return cloneSecret(t, cs, "pull-secret", "openshift-config", globalPullSecretCloneName, ctrlcommon.MCONamespace)
}

func waitForMachineOSBuildToReachState(t *testing.T, cs *framework.ClientSet, poolName string, condFunc func(*mcfgv1alpha1.MachineOSBuild, error) (bool, error)) {
	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
	require.NoError(t, err)

	mosbName := fmt.Sprintf("%s-%s-builder", poolName, mcp.Spec.Configuration.Name)

	err = wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		mosb, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Get(context.TODO(), mosbName, metav1.GetOptions{})

		return condFunc(mosb, err)
	})

	require.NoError(t, err, "MachineOSBuild %q did not reach desired state", mosbName)
}

// Waits for the target MachineConfigPool to reach a state defined in a supplied function.
func waitForPoolToReachState(t *testing.T, cs *framework.ClientSet, poolName string, condFunc func(*mcfgv1.MachineConfigPool) bool) {
	err := wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		return condFunc(mcp), nil
	})

	require.NoError(t, err, "MachineConfigPool %q did not reach desired state", poolName)
}

// Registers a cleanup function, making it idempotent, and wiring up the
// skip-cleanup flag to it which will cause cleanup to be skipped, if set.
func makeIdempotentAndRegister(t *testing.T, cleanupFunc func()) func() {
	out := helpers.MakeIdempotent(func() {
		if !skipCleanup {
			cleanupFunc()
		}
	})
	t.Cleanup(out)
	return out
}

// TOOD: Refactor into smaller functions.
func cleanupEphemeralBuildObjects(t *testing.T, cs *framework.ClientSet) {
	// TODO: Instantiate this by using the label selector library.
	labelSelector := "machineconfiguration.openshift.io/desiredConfig,machineconfiguration.openshift.io/buildPod,machineconfiguration.openshift.io/targetMachineConfigPool"

	cmList, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	require.NoError(t, err)

	podList, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	require.NoError(t, err)

	mosbList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	moscList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)

	if len(cmList.Items) == 0 {
		t.Logf("No ephemeral ConfigMaps to clean up")
	}

	if len(podList.Items) == 0 {
		t.Logf("No build pods to clean up")
	}

	if len(mosbList.Items) == 0 {
		t.Logf("No MachineOSBuilds to clean up")
	}

	if len(moscList.Items) == 0 {
		t.Logf("No MachineOSConfigs to clean up")
	}

	for _, item := range cmList.Items {
		t.Logf("Cleaning up ephemeral ConfigMap %q", item.Name)
		require.NoError(t, cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}

	for _, item := range podList.Items {
		t.Logf("Cleaning up build pod %q", item.Name)
		require.NoError(t, cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}

	for _, item := range moscList.Items {
		t.Logf("Cleaning up MachineOSConfig %q", item.Name)
		require.NoError(t, cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}

	for _, item := range mosbList.Items {
		t.Logf("Cleaning up MachineOSBuild %q", item.Name)
		require.NoError(t, cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().Delete(context.TODO(), item.Name, metav1.DeleteOptions{}))
	}
}

// Determines where to write the build logs in the event of a failure.
// ARTIFACT_DIR is a well-known env var provided by the OpenShift CI system.
// Writing to the path in this env var will ensure that any files written to
// that path end up in the OpenShift CI GCP bucket for later viewing.
//
// If this env var is not set, these files will be written to the current
// working directory.
func getBuildArtifactDir(t *testing.T) string {
	artifactDir := os.Getenv("ARTIFACT_DIR")
	if artifactDir != "" {
		return artifactDir
	}

	cwd, err := os.Getwd()
	require.NoError(t, err)
	return cwd
}

// Writes any ephemeral ConfigMaps that got created as part of the build
// process to a file. Also writes the build pod spec.
func writeBuildArtifactsToFiles(t *testing.T, cs *framework.ClientSet, poolName string) {
	pool, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
	require.NoError(t, err)

	err = aggerrs.NewAggregate([]error{
		writeConfigMapsToFile(t, cs, pool),
		writePodSpecToFile(t, cs, pool),
		writeMachineOSBuildsToFile(t, cs),
		writeMachineOSConfigsToFile(t, cs),
	})

	require.NoError(t, err, "could not write build artifacts to files, got: %s", err)
}

// Writes all MachineOSBuilds to a file.
func writeMachineOSBuildsToFile(t *testing.T, cs *framework.ClientSet) error {
	mosbList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	if len(mosbList.Items) == 0 {
		t.Logf("No MachineOSBuilds to write")
		return nil
	}

	for _, mosb := range mosbList.Items {
		mosb := mosb
		filename := fmt.Sprintf("%s-%s-MachineOSBuild.yaml", t.Name(), mosb.Name)
		t.Logf("Writing MachineOSBuild %s to %s", mosb.Name, filename)
		if err := dumpObjectToYAMLFile(t, &mosb, filename); err != nil {
			return err
		}
	}

	return nil
}

// Writes all MachineOSConfigs to a file.
func writeMachineOSConfigsToFile(t *testing.T, cs *framework.ClientSet) error {
	moscList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	if len(moscList.Items) == 0 {
		t.Logf("No MachineOSConfigs to write")
		return nil
	}

	for _, mosc := range moscList.Items {
		mosc := mosc
		filename := fmt.Sprintf("%s-%s-MachineOSConfig.yaml", t.Name(), mosc.Name)
		t.Logf("Writing MachineOSConfig %s to %s", mosc.Name, filename)
		if err := dumpObjectToYAMLFile(t, &mosc, filename); err != nil {
			return err
		}
	}

	return nil
}

// Writes the ephemeral ConfigMaps to a file, if found.
func writeConfigMapsToFile(t *testing.T, cs *framework.ClientSet, pool *mcfgv1.MachineConfigPool) error {
	configmaps := []string{
		fmt.Sprintf("dockerfile-%s", pool.Spec.Configuration.Name),
		fmt.Sprintf("mc-%s", pool.Spec.Configuration.Name),
		fmt.Sprintf("digest-%s", pool.Spec.Configuration.Name),
	}

	dirPath := getBuildArtifactDir(t)

	for _, configmap := range configmaps {
		if err := writeConfigMapToFile(t, cs, configmap, dirPath); err != nil {
			return err
		}
	}

	return nil
}

// Writes a given ConfigMap to a file.
func writeConfigMapToFile(t *testing.T, cs *framework.ClientSet, configmapName, dirPath string) error {
	cm, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), configmapName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		t.Logf("ConfigMap %q not found, skipping retrieval", configmapName)
		return nil
	}

	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("could not get configmap %s: %w", configmapName, err)
	}

	filename := filepath.Join(dirPath, fmt.Sprintf("%s-%s-configmap.yaml", t.Name(), configmapName))
	t.Logf("Writing configmap (%s) contents to %s", configmapName, filename)
	return dumpObjectToYAMLFile(t, cm, filename)
}

// Wrttes a pod spec to a file.
func writePodSpecToFile(t *testing.T, cs *framework.ClientSet, pool *mcfgv1.MachineConfigPool) error {
	dirPath := getBuildArtifactDir(t)

	podName := fmt.Sprintf("build-%s", pool.Spec.Configuration.Name)

	pod, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err == nil {
		podFilename := filepath.Join(dirPath, fmt.Sprintf("%s-%s-pod.yaml", t.Name(), pod.Name))
		t.Logf("Writing spec for pod %s to %s", pod.Name, podFilename)
		return dumpObjectToYAMLFile(t, pod, podFilename)
	}

	if k8serrors.IsNotFound(err) {
		t.Logf("Pod spec for %s not found, skipping", pod.Name)
		return nil
	}

	return err
}

// Dumps a struct to the provided filename in YAML format, while ensuring that
// the filename contains both the name of the current-running test as well as
// the destination directory path.
func dumpObjectToYAMLFile(t *testing.T, obj interface{ GetName() string }, filename string) error {
	dirPath := getBuildArtifactDir(t)

	// If we don't have the name of the test embedded in the filename, add it.
	if !strings.Contains(filename, t.Name()) {
		filename = fmt.Sprintf("%s-%s", t.Name(), filename)
	}

	// If we don't have the destination directory in the filename, add it.
	if !strings.Contains(filename, dirPath) {
		filename = filepath.Join(dirPath, filename)
	}

	out, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}

	return os.WriteFile(filename, out, 0o755)
}

// Streams the logs from the Machine OS Builder pod containers to a set of
// files. This can provide a valuable window into how / why the e2e test suite
// failed.
func streamMachineOSBuilderPodLogsToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet) error {
	pods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=machine-os-builder",
	})

	require.NoError(t, err)

	mobPod := &pods.Items[0]
	return streamPodContainerLogsToFile(ctx, t, cs, mobPod)
}

// Streams the logs for all of the containers running in the build pod. The pod
// logs can provide a valuable window into how / why a given build failed.
func streamBuildPodLogsToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet, pool *mcfgv1.MachineConfigPool) error {
	podName := fmt.Sprintf("build-%s", pool.Spec.Configuration.Name)

	pod, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("could not get pod %s: %w", podName, err)
	}

	return streamPodContainerLogsToFile(ctx, t, cs, pod)
}

// Attaches a follower to each of the containers within a given pod in order to
// stream their logs to disk for future debugging.
func streamPodContainerLogsToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet, pod *corev1.Pod) error {
	errGroup, egCtx := errgroup.WithContext(ctx)

	for _, container := range pod.Spec.Containers {
		container := container
		pod := pod.DeepCopy()

		// Because we follow the logs for each container in a build pod, this
		// blocks the current Goroutine. So we run each log stream operation in a
		// separate Goroutine to avoid blocking the main Goroutine.
		errGroup.Go(func() error {
			return streamContainerLogToFile(egCtx, t, cs, pod, container)
		})
	}

	// Only propagate errors that are not a context cancellation.
	if err := errGroup.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

// Streams the logs for a given container to a file.
func streamContainerLogToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet, pod *corev1.Pod, container corev1.Container) error {
	dirPath := getBuildArtifactDir(t)

	logger, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: container.Name,
		Follow:    true,
	}).Stream(ctx)

	defer logger.Close()

	if err != nil {
		return fmt.Errorf("could not get logs for container %s in pod %s: %w", container.Name, pod.Name, err)
	}

	filename := filepath.Join(dirPath, fmt.Sprintf("%s-%s-%s.log", t.Name(), pod.Name, container.Name))
	file, err := os.Create(filename)
	if err != nil {
		return err
	}

	defer file.Close()

	t.Logf("Streaming pod (%s) container (%s) logs to %s", pod.Name, container.Name, filename)
	if _, err := io.Copy(file, logger); err != nil {
		return fmt.Errorf("could not write pod logs to %s: %w", filename, err)
	}

	return nil
}

// Skips a given test if it is detected that the cluster is running OKD. We
// skip these tests because they're either irrelevant for OKD or would fail.
func skipOnOKD(t *testing.T) {
	cs := framework.NewClientSet("")

	isOKD, err := helpers.IsOKDCluster(cs)
	require.NoError(t, err)

	if isOKD {
		t.Logf("OKD detected, skipping test %s", t.Name())
		t.Skip()
	}
}

func skipOnOCP(t *testing.T) {
	cs := framework.NewClientSet("")
	isOKD, err := helpers.IsOKDCluster(cs)
	require.NoError(t, err)

	if !isOKD {
		t.Logf("OCP detected, skipping test %s", t.Name())
		t.Skip()
	}
}

// Extracts the contents of a directory within a given container to a temporary
// directory. Next, it loads them into a bytes map keyed by filename. It does
// not handle nested directories, so use with caution.
func convertFilesFromContainerImageToBytesMap(t *testing.T, pullspec, containerFilepath string) map[string][]byte {
	tempDir := t.TempDir()

	path := fmt.Sprintf("%s:%s", containerFilepath, tempDir)
	cmd := exec.Command("oc", "image", "extract", pullspec, "--path", path)
	t.Logf("Extracting files under %q from %q to %q; running %s", containerFilepath, pullspec, tempDir, cmd.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Run())

	out := map[string][]byte{}

	isCentosImage := strings.Contains(pullspec, "centos")

	err := filepath.Walk(tempDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		contents, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}

		if isCentosImage {
			contents = bytes.ReplaceAll(contents, []byte("$stream"), []byte("9-stream"))
		}

		// Replace $stream with 9-stream in any of the Centos repo content we pulled.
		out[filepath.Base(path)] = contents
		return nil
	})

	require.NoError(t, err)

	return out
}

// Copy the entitlement certificates into the MCO namespace. If the secrets
// cannot be found, calls t.Skip() to skip the test.
//
// Registers and returns a cleanup function to remove the certificate(s) after test completion.
func copyEntitlementCerts(t *testing.T, cs *framework.ClientSet) func() {
	namespace := "openshift-config-managed"
	name := "etc-pki-entitlement"

	_, err := cs.CoreV1Interface.Secrets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err == nil {
		return cloneSecret(t, cs, name, namespace, name, ctrlcommon.MCONamespace)
	}

	if k8serrors.IsNotFound(err) {
		t.Logf("Secret %q not found in %q, skipping test", name, namespace)
		t.Skip()
		return func() {}
	}

	t.Fatalf("could not get %q from %q: %s", name, namespace, err)
	return func() {}
}

// Uses the centos stream 9 container and extracts the contents of both the
// /etc/yum.repos.d and /etc/pki/rpm-gpg directories and injects those into a
// ConfigMap and Secret, respectively. This is so that the build process will
// consume those objects as part of the build process, injecting them into the
// build context.
func injectYumRepos(t *testing.T, cs *framework.ClientSet) func() {
	tempDir := t.TempDir()

	yumReposPath := filepath.Join(tempDir, "yum-repos-d")
	require.NoError(t, os.MkdirAll(yumReposPath, 0o755))

	centosPullspec := "quay.io/centos/centos:stream9"
	yumReposContents := convertFilesFromContainerImageToBytesMap(t, centosPullspec, "/etc/yum.repos.d/")
	rpmGpgContents := convertFilesFromContainerImageToBytesMap(t, centosPullspec, "/etc/pki/rpm-gpg/")

	configMapCleanupFunc := createConfigMap(t, cs, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "etc-yum-repos-d",
			Namespace: ctrlcommon.MCONamespace,
		},
		// Note: Even though the BuildController retrieves this ConfigMap, it only
		// does so to determine whether or not it is present. It does not look at
		// its contents. For that reason, we can use the BinaryData field here
		// because the Build Pod will use its contents the same regardless of
		// whether its string data or binary data.
		BinaryData: yumReposContents,
	})

	secretCleanupFunc := createSecret(t, cs, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "etc-pki-rpm-gpg",
			Namespace: ctrlcommon.MCONamespace,
		},
		Data: rpmGpgContents,
	})

	return makeIdempotentAndRegister(t, func() {
		configMapCleanupFunc()
		secretCleanupFunc()
	})
}

// Clones a given secret from a given namespace into the MCO namespace.
// Registers and returns a cleanup function to delete the secret upon test
// completion.
func cloneSecret(t *testing.T, cs *framework.ClientSet, srcName, srcNamespace, dstName, dstNamespace string) func() {
	secret, err := cs.CoreV1Interface.Secrets(srcNamespace).Get(context.TODO(), srcName, metav1.GetOptions{})
	require.NoError(t, err)

	secretCopy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dstName,
			Namespace: dstNamespace,
		},
		Data: secret.Data,
		Type: secret.Type,
	}

	cleanup := createSecret(t, cs, secretCopy)
	t.Logf("Cloned \"%s/%s\" to \"%s/%s\"", srcNamespace, srcName, dstNamespace, dstName)

	return makeIdempotentAndRegister(t, func() {
		cleanup()
		t.Logf("Deleted cloned secret \"%s/%s\"", dstNamespace, dstName)
	})
}

// Extracts the internal registry pull secret from the ControllerConfig and
// writes it to the designated place on the nodes' filesystem. This is
// needed to work around https://issues.redhat.com/browse/OCPBUGS-33803
// until this bug can be resolved.
func writeInternalRegistryPullSecretToNode(t *testing.T, cs *framework.ClientSet, node corev1.Node) func() {
	cc, err := cs.ControllerConfigs().Get(context.TODO(), "machine-config-controller", metav1.GetOptions{})
	require.NoError(t, err)

	path := "/etc/mco/internal-registry-pull-secret.json"

	return helpers.WriteFileToNode(t, cs, node, path, string(cc.Spec.InternalRegistryPullSecret))
}
