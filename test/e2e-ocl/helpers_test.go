package e2e_ocl_test

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

	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/types"
	ign3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/distribution/reference"
	imagev1 "github.com/openshift/api/image/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	aggerrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"
)

const (
	clonedSecretLabelKey string = "machineconfiguration.openshift.io/cloned-secret"
)

func applyMC(t *testing.T, cs *framework.ClientSet, mc *mcfgv1.MachineConfig) func() {
	cleanupFunc := helpers.ApplyMC(t, cs, mc)
	t.Logf("Created new MachineConfig %q", mc.Name)

	return makeIdempotentAndRegister(t, func() {
		cleanupFunc()
		t.Logf("Deleted MachineConfig %q", mc.Name)
	})
}

func createMachineOSConfig(t *testing.T, cs *framework.ClientSet, mosc *mcfgv1alpha1.MachineOSConfig) func() {
	helpers.SetMetadataOnObject(t, mosc)

	_, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Create(context.TODO(), mosc, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created MachineOSConfig %q", mosc.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Delete(context.TODO(), mosc.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted MachineOSConfig %q", mosc.Name)
	})
}

// Sets up the ImageStream in the desired namesspace. If in a different
// namespace than the MCO, it will create the namespace and clone the pull
// secret into the MCO namespace. Returns the name of the push secret used, the
// image pullspec, and an idempotent cleanup function.
func setupImageStream(t *testing.T, cs *framework.ClientSet, objMeta metav1.ObjectMeta) (string, string, func()) {
	t.Helper()

	cleanups := helpers.NewCleanupFuncs()

	pushSecretName := "builder-push-secret-name"

	// If no namespace is provided, default to the MCO namespace.
	if objMeta.Namespace == "" {
		objMeta.Namespace = ctrlcommon.MCONamespace
	}

	builderSAObjMeta := metav1.ObjectMeta{
		Namespace: objMeta.Namespace,
		Name:      "builder",
	}

	// If we're told to use a different namespace than the MCO namespace, we need
	// to do some additional steps.
	if objMeta.Namespace != ctrlcommon.MCONamespace {
		// Create the namespace.
		cleanups.Add(createNamespace(t, cs, objMeta))

		// Wait for the builder service account to exist within the new namespace.
		require.NoError(t, waitForServiceAccountToExist(cs, builderSAObjMeta))
	}

	// Create the Imagestream.
	pullspec, isCleanupFunc := createImagestream(t, cs, objMeta)
	cleanups.Add(isCleanupFunc)

	// Create a long-lived image registry pull secret so that it will not get
	// automatically rotated while the test is running.
	opts := helpers.LongLivedSecretOpts{
		DeleteIfExists: true,
		ServiceAccount: builderSAObjMeta,
		Lifetime:       "24h",
		Secret: metav1.ObjectMeta{
			Namespace: ctrlcommon.MCONamespace,
			Name:      pushSecretName,
		},
	}

	cleanups.Add(helpers.CreateLongLivedPullSecretForTest(context.TODO(), t, cs, opts))

	return pushSecretName, pullspec, makeIdempotentAndRegister(t, cleanups.Run)
}

// Creates a namespace. Returns an idempotent cleanup function.
func createNamespace(t *testing.T, cs *framework.ClientSet, objMeta metav1.ObjectMeta) func() {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: objMeta.Namespace,
		},
	}

	helpers.SetMetadataOnObject(t, ns)

	_, err := cs.CoreV1Interface.Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Created namespace %q", ns.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.CoreV1Interface.Namespaces().Delete(context.TODO(), ns.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted namespace %q", ns.Name)
	})
}

// There may be a delay between the time a new namespace is created and its
// service accounts to be created. This will wait up to one minute for the
// specified service account to be created.
func waitForServiceAccountToExist(cs *framework.ClientSet, objMeta metav1.ObjectMeta) error {
	return wait.PollImmediate(1*time.Second, 1*time.Minute, func() (bool, error) {
		builderSA, err := cs.CoreV1Interface.ServiceAccounts(objMeta.Namespace).Get(context.TODO(), objMeta.Name, metav1.GetOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			return false, err
		}

		return builderSA != nil, nil
	})
}

// Creates an OpenShift ImageStream in the MCO namespace for the test and
// registers a cleanup function. Returns the pullspec with the latest tag for
// the newly-created ImageStream.
func createImagestream(t *testing.T, cs *framework.ClientSet, objMeta metav1.ObjectMeta) (string, func()) {
	is := &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objMeta.Name,
			Namespace: objMeta.Namespace,
		},
	}

	helpers.SetMetadataOnObject(t, is)

	created, err := cs.ImageV1Interface.ImageStreams(is.Namespace).Create(context.TODO(), is, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, created.Status.DockerImageRepository)

	pullspec := fmt.Sprintf("%s:latest", created.Status.DockerImageRepository)
	t.Logf("Created ImageStream \"%s/%s\", has pullspec %q", is.Namespace, is.Name, pullspec)

	return pullspec, makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.ImageV1Interface.ImageStreams(is.Namespace).Delete(context.TODO(), is.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted ImageStream \"%s/%s\"", is.Namespace, is.Name)
	})
}

// Creates a given ConfigMap and registers a cleanup function to delete it.
func createConfigMap(t *testing.T, cs *framework.ClientSet, cm *corev1.ConfigMap) func() {
	helpers.SetMetadataOnObject(t, cm)

	_, err := cs.CoreV1Interface.ConfigMaps(cm.Namespace).Create(context.TODO(), cm, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created ConfigMap \"%s/%s\"", cm.Namespace, cm.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.CoreV1Interface.ConfigMaps(cm.Namespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted ConfigMap \"%s/%s\"", cm.Namespace, cm.Name)
	})
}

// Creates a given Secret and registers a cleanup function to delete it.
func createSecret(t *testing.T, cs *framework.ClientSet, secret *corev1.Secret) func() {
	helpers.SetMetadataOnObject(t, secret)

	_, err := cs.CoreV1Interface.Secrets(secret.Namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Created secret \"%s/%s\"", secret.Namespace, secret.Name)

	return makeIdempotentAndRegister(t, func() {
		require.NoError(t, cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).Delete(context.TODO(), secret.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted secret \"%s/%s\"", secret.Namespace, secret.Name)
	})
}

// Copies the global pull secret from openshift-config/pull-secret into the MCO
// namespace so that it can be used by the build processes.
func copyGlobalPullSecret(t *testing.T, cs *framework.ClientSet) func() {
	src := metav1.ObjectMeta{
		Name:      "pull-secret",
		Namespace: "openshift-config",
	}

	dst := metav1.ObjectMeta{
		Name:      globalPullSecretCloneName,
		Namespace: ctrlcommon.MCONamespace,
	}

	return cloneSecret(t, cs, src, dst)
}

// Computes the name of the currently-running MachineOSBuild given a MachineConfigPool and MachineOSConfig.
func getMachineOSBuildNameForPool(cs *framework.ClientSet, poolName, moscName string) (string, error) {
	mcp, err := cs.MachineconfigurationV1Interface.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	mosc, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().Get(context.TODO(), moscName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	mosbs, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().List(context.TODO(), metav1.ListOptions{
		LabelSelector: utils.MachineOSBuildSelector(mosc, mcp).String(),
	})

	if err != nil {
		return "", err
	}

	if len(mosbs.Items) == 1 {
		return mosbs.Items[0].Name, nil
	}

	if len(mosbs.Items) == 0 {
		return "", fmt.Errorf("no MachineOSBuild found for MachineOSConfig %s, MachineConfigPool %s, rendered MachineConfig %s", mosc.Name, mcp.Name, mcp.Spec.Configuration.Name)
	}

	return "", fmt.Errorf("found multiple MachineOSBuilds for MachineOSConfig %s, MachineConfigPool %s, rendered MachineConfig %s", mosc.Name, mcp.Name, mcp.Spec.Configuration.Name)
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

// Registers a cleanup function, making it idempotent, and wiring up the skip
// cleanup checks which will cause cleanup to be skipped under certain
// conditions.
func makeIdempotentAndRegister(t *testing.T, cleanupFunc func()) func() {
	cfg := helpers.IdempotentConfig{
		SkipAlways:        skipCleanupAlways,
		SkipOnlyOnFailure: skipCleanupOnlyAfterFailure,
	}

	return helpers.MakeConfigurableIdempotentAndRegister(t, cfg, cleanupFunc)
}

// Registers a cleanup function, making it idempotent and ensures that it will
// always be run, regardless of skip cleanup opts or whether we're in CI.
//
// Note: Use this wrapper only in cases where you want to ensure that a
// function is only called once despite there being multiple calls to the
// returned function. If there is only one call to the returned function
// anyway, use t.Cleanup() instead for clarity.
func makeIdempotentAndRegisterAlwaysRun(t *testing.T, cleanupFunc func()) func() {
	return helpers.MakeIdempotentAndRegister(t, cleanupFunc)
}

// TOOD: Refactor into smaller functions.
func cleanupEphemeralBuildObjects(t *testing.T, cs *framework.ClientSet) {
	labelSelector := utils.OSBuildSelector().String()

	// Any secrets that get created by BuildController should have different
	// label selectors since they're produced differently.
	secretList, err := cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: utils.CanonicalizedSecretSelector().String(),
	})

	require.NoError(t, err)

	cmList, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})

	require.NoError(t, err)

	jobList, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).List(context.TODO(), metav1.ListOptions{
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

	if len(secretList.Items) == 0 {
		t.Logf("No build-time secrets to clean up")
	}

	if len(cmList.Items) == 0 {
		t.Logf("No ephemeral ConfigMaps to clean up")
	}

	if len(jobList.Items) == 0 {
		t.Logf("No ephemeral Jobs to clean up")
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

	for _, item := range secretList.Items {
		t.Logf("Cleaning up build-time Secret %s", item.Name)
		require.NoError(t, deleteObject(context.TODO(), t, &item, cs.CoreV1Interface.Secrets(ctrlcommon.MCONamespace)))
	}

	for _, item := range cmList.Items {
		t.Logf("Cleaning up ephemeral ConfigMap %q", item.Name)
		require.NoError(t, deleteObject(context.TODO(), t, &item, cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace)))
	}

	for _, item := range jobList.Items {
		t.Logf("Cleaning up build job %q", item.Name)
		bgDeletion := metav1.DeletePropagationBackground
		require.NoError(t, deleteObjectWithOpts(context.TODO(), t, &item, cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace), metav1.DeleteOptions{
			PropagationPolicy: &bgDeletion,
		}))
	}

	for _, item := range podList.Items {
		t.Logf("Cleaning up build pod %q", item.Name)
		require.NoError(t, deleteObject(context.TODO(), t, &item, cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace)))
	}

	for _, item := range moscList.Items {
		t.Logf("Cleaning up MachineOSConfig %q", item.Name)
		require.NoError(t, deleteObject(context.TODO(), t, &item, cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs()))
	}

	for _, item := range mosbList.Items {
		t.Logf("Cleaning up MachineOSBuild %q", item.Name)
		require.NoError(t, deleteObject(context.TODO(), t, &item, cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds()))
	}
}

type deleter interface {
	Delete(context.Context, string, metav1.DeleteOptions) error
}

type kubeObject interface {
	runtime.Object
	GetName() string
}

func deleteObject(ctx context.Context, t *testing.T, obj kubeObject, deleter deleter) error {
	return deleteObjectWithOpts(ctx, t, obj, deleter, metav1.DeleteOptions{})
}

func deleteObjectWithOpts(ctx context.Context, t *testing.T, obj kubeObject, deleter deleter, opts metav1.DeleteOptions) error {
	kind, err := utils.GetKindForObject(obj)
	if err != nil && kind == "" {
		kind = "<unknown>"
	}

	err = deleter.Delete(ctx, obj.GetName(), opts)

	if err == nil {
		t.Logf("Cleaned up %s %q", kind, obj.GetName())
		return nil
	}

	if k8serrors.IsNotFound(err) {
		t.Logf("%s %q already cleaned up", kind, obj.GetName())
		return nil
	}

	return err
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

// Writes any ephemeral build objects to disk as YAML files.
func writeBuildArtifactsToFiles(t *testing.T, cs *framework.ClientSet, poolName string) {
	lo := metav1.ListOptions{
		LabelSelector: utils.OSBuildSelector().String(),
	}

	archiveName := fmt.Sprintf("%s-build-artifacts.tar.gz", helpers.SanitizeTestName(t))

	archive, err := helpers.NewArtifactArchive(t, archiveName)
	require.NoError(t, err)

	err = aggerrs.NewAggregate([]error{
		writeConfigMapsToFile(t, cs, lo, archive.StagingDir()),
		writeBuildPodsToFile(t, cs, lo, archive.StagingDir()),
		writeMachineOSBuildsToFile(t, cs, archive.StagingDir()),
		writeMachineOSConfigsToFile(t, cs, archive.StagingDir()),
	})

	require.NoError(t, err, "could not write build artifacts to files, got: %s", err)

	require.NoError(t, archive.WriteArchive(), "could not write archive")
}

// Writes all MachineOSBuilds to a file.
func writeMachineOSBuildsToFile(t *testing.T, cs *framework.ClientSet, archiveDir string) error {
	mosbList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSBuilds().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	if len(mosbList.Items) == 0 {
		t.Logf("No MachineOSBuilds to write")
		return nil
	}

	return dumpObjectToYAMLFile(t, mosbList, filepath.Join(archiveDir, "machineosbuilds.yaml"))
}

// Writes all MachineOSConfigs to a file.
func writeMachineOSConfigsToFile(t *testing.T, cs *framework.ClientSet, archiveDir string) error {
	moscList, err := cs.MachineconfigurationV1alpha1Interface.MachineOSConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	if len(moscList.Items) == 0 {
		t.Logf("No MachineOSConfigs to write")
		return nil
	}

	return dumpObjectToYAMLFile(t, moscList, filepath.Join(archiveDir, "machineosconfigs.yaml"))
}

// Writes all ConfigMaps that match the OS Build labels to files.
func writeConfigMapsToFile(t *testing.T, cs *framework.ClientSet, lo metav1.ListOptions, archiveDir string) error {
	cmList, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), lo)

	if err != nil {
		return err
	}

	if len(cmList.Items) == 0 {
		t.Logf("No ConfigMaps matching label selector %q found", lo.LabelSelector)
		return nil
	}

	return dumpObjectToYAMLFile(t, cmList, filepath.Join(archiveDir, "configmaps.yaml"))
}

// Wrttes all pod specs that match the OS Build labels to files.
func writeBuildPodsToFile(t *testing.T, cs *framework.ClientSet, lo metav1.ListOptions, archiveDir string) error {
	podList, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), lo)
	if err != nil {
		return err
	}

	if len(podList.Items) == 0 {
		t.Logf("No pods matching label selector %q found", lo.LabelSelector)
		return nil
	}

	return dumpObjectToYAMLFile(t, podList, filepath.Join(archiveDir, "pods.yaml"))
}

// Dumps a struct to the provided filename in YAML format, creating any
// parent directories as needed.
func dumpObjectToYAMLFile(t *testing.T, obj interface{}, filename string) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
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
func streamMachineOSBuilderPodLogsToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet, dirPath string) error {
	pods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=machine-os-builder",
	})

	require.NoError(t, err)

	mobPod := &pods.Items[0]
	return streamPodContainerLogsToFile(ctx, t, cs, mobPod, dirPath)
}

// Streams the logs for all of the containers running in the build pod. The pod
// logs can provide a valuable window into how / why a given build failed.
func streamBuildPodLogsToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet, mosb *mcfgv1alpha1.MachineOSBuild, dirPath string) error {
	jobName := mosb.Status.BuilderReference.PodImageBuilder.Name

	pod, err := getPodFromJob(ctx, cs, jobName)
	if err != nil {
		return err
	}

	return streamPodContainerLogsToFile(ctx, t, cs, pod, dirPath)
}

func getPodFromJob(ctx context.Context, cs *framework.ClientSet, jobName string) (*corev1.Pod, error) {
	job, err := cs.BatchV1Interface.Jobs(ctrlcommon.MCONamespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get job %s: %w", job, err)
	}

	podList, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("job-name=%s", jobName)})
	if err != nil {
		return nil, fmt.Errorf("could not get pods with job label %s: %w", jobName, err)
	}

	if podList != nil {
		if len(podList.Items) == 1 {
			return &podList.Items[0], nil
		}

		// this is needed when we test the case for a new pod being created after deleting the existing one
		// as sometimes it takes time for the old pod to be completely deleted
		for _, pod := range podList.Items {
			if pod.Status.Phase == corev1.PodRunning {
				return &pod, nil
			}
		}
	}
	return nil, fmt.Errorf("no pod found for job %s", jobName)
}

// Attaches a follower to each of the containers within a given pod in order to
// stream their logs to disk for future debugging.
func streamPodContainerLogsToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet, pod *corev1.Pod, dirPath string) error {
	errGroup, egCtx := errgroup.WithContext(ctx)

	for _, container := range pod.Spec.Containers {
		container := container
		pod := pod.DeepCopy()

		// Because we follow the logs for each container in a build pod, this
		// blocks the current Goroutine. So we run each log stream operation in a
		// separate Goroutine to avoid blocking the main Goroutine.
		errGroup.Go(func() error {
			return streamContainerLogToFile(egCtx, t, cs, pod, container, dirPath)
		})
	}

	// Only propagate errors that are not a context cancellation.
	if err := errGroup.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

// Streams the logs for a given container to a file.
func streamContainerLogToFile(ctx context.Context, t *testing.T, cs *framework.ClientSet, pod *corev1.Pod, container corev1.Container, dirPath string) error {
	logger, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: container.Name,
		Follow:    true,
	}).Stream(ctx)

	if err != nil {
		return fmt.Errorf("could not get logs for container %s in pod %s: %w", container.Name, pod.Name, err)
	}
	defer logger.Close()

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
	src := metav1.ObjectMeta{
		Name:      "etc-pki-entitlement",
		Namespace: "openshift-config-managed",
	}

	dst := metav1.ObjectMeta{
		Name:      src.Name,
		Namespace: ctrlcommon.MCONamespace,
	}

	_, err := cs.CoreV1Interface.Secrets(src.Namespace).Get(context.TODO(), src.Name, metav1.GetOptions{})
	if err == nil {
		return cloneSecret(t, cs, src, dst)
	}

	if k8serrors.IsNotFound(err) {
		t.Logf("Secret %q not found in %q, skipping test", src.Name, src.Namespace)
		t.Skip()
		return func() {}
	}

	t.Fatalf("could not get %q from %q: %s", src.Name, src.Namespace, err)
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
func cloneSecret(t *testing.T, cs *framework.ClientSet, src, dst metav1.ObjectMeta) func() {
	secret, err := cs.CoreV1Interface.Secrets(src.Namespace).Get(context.TODO(), src.Name, metav1.GetOptions{})
	require.NoError(t, err)

	secretCopy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dst.Name,
			Namespace: dst.Namespace,
			Labels: map[string]string{
				clonedSecretLabelKey: "",
			},
		},
		Data: secret.Data,
		Type: secret.Type,
	}

	cleanup := createSecret(t, cs, secretCopy)
	t.Logf("Cloned \"%s/%s\" to \"%s/%s\"", src.Namespace, src.Name, dst.Namespace, dst.Name)

	return makeIdempotentAndRegister(t, func() {
		cleanup()
		t.Logf("Deleted cloned secret \"%s/%s\"", dst.Namespace, dst.Name)
	})
}

func newMachineConfig(name, pool string) *mcfgv1.MachineConfig {
	mode := 420
	testfiledata := fmt.Sprintf("data:,%s-%s", name, pool)
	path := fmt.Sprintf("/etc/%s-%s", name, pool)
	file := ign3types.File{
		Node: ign3types.Node{
			Path: path,
		},
		FileEmbedded1: ign3types.FileEmbedded1{
			Contents: ign3types.Resource{
				Source: &testfiledata,
			},
			Mode: &mode,
		},
	}

	return helpers.NewMachineConfig(name, helpers.MCLabelForRole(pool), "", []ign3types.File{file})
}

// Gets an override image pullspec for TestGracefulBuildFailureRecovery. We
// override this option to produce a faster test failure since the image we
// select will both be smaller than the OS image as well as not contain the
// required binaries such as Ignition, rpm-ostree, etc.
//
// This will use the MCO's image from the machine-config-operator-images
// ConfigMap if it is found to be a digested image. Otherwise, it will get the
// digested image pullspec for registry.fedoraproject.org/fedora:latest. The
// reason why we look up that image is because it does not require a pull
// secret in order to get its digest, which the BaseOSImagePullspec field
// requires.
func getImagePullspecForFailureTest(ctx context.Context, cs *framework.ClientSet) (string, error) {
	images, err := ctrlcommon.GetImagesConfig(ctx, cs.GetKubeclient())
	if err != nil {
		return "", err
	}

	parsed, err := docker.ParseReference("//" + images.MachineConfigOperator)
	if err != nil {
		return "", err
	}

	switch parsed.DockerReference().(type) {
	case reference.Digested:
		return images.MachineConfigOperator, nil
	case reference.Tagged:
		return resolveTaggedPullspecToDigestedPullspec(ctx, "registry.fedoraproject.org/fedora:latest")
	default:
		return "", fmt.Errorf("unknown image reference spec %q", images.MachineConfigOperator)
	}
}

// Talks to an image registry to get the digested image pullspec for the
// supplied image pullspec. Note: Only supports public image registries. This
// is the same as doing:
// $ skopeo inspect docker://image-pullspec | jq '.Digest'
func resolveTaggedPullspecToDigestedPullspec(ctx context.Context, pullspec string) (string, error) {
	sysCtx := &types.SystemContext{}

	tagged, err := docker.ParseReference("//" + pullspec)
	if err != nil {
		return "", err
	}

	digest, err := docker.GetDigest(ctx, sysCtx, tagged)
	if err != nil {
		return "", err
	}

	canonical, err := reference.WithDigest(reference.TrimNamed(tagged.DockerReference()), digest)
	if err != nil {
		return "", err
	}

	return canonical.String(), nil
}
