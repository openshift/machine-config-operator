package main

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/ghodss/yaml"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/version"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	apierrs "k8s.io/apimachinery/pkg/api/errors"

	"github.com/openshift/machine-config-operator/pkg/controller/build"
)

const (
	createdByOnClusterBuildsHelper string = "machineconfiguration.openshift.io/createdByOnClusterBuildsHelper"
	globalPullSecretCloneName      string = "global-pull-secret-copy"
)

func hasOurLabel(labels map[string]string) bool {
	if labels == nil {
		return false
	}

	_, ok := labels[createdByOnClusterBuildsHelper]
	return ok
}

// Compresses and base-64 encodes a given byte array. Ideal for loading an
// arbitrary byte array into a ConfigMap or Secret.
func compressAndEncode(payload []byte) (*bytes.Buffer, error) {
	out := bytes.NewBuffer(nil)

	if len(payload) == 0 {
		return out, nil
	}

	// We need to base64-encode our gzipped data so we can marshal it in and out
	// of a string since ConfigMaps and Secrets expect a textual representation.
	base64Enc := base64.NewEncoder(base64.StdEncoding, out)
	defer base64Enc.Close()

	err := compress(bytes.NewBuffer(payload), base64Enc)
	if err != nil {
		return nil, fmt.Errorf("could not compress and encode payload: %w", err)
	}

	err = base64Enc.Close()
	if err != nil {
		return nil, fmt.Errorf("could not close base64 encoder: %w", err)
	}

	return out, err
}

// Compresses a given io.Reader to a given io.Writer
func compress(r io.Reader, w io.Writer) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("could not initialize gzip writer: %w", err)
	}

	defer gz.Close()

	if _, err := io.Copy(gz, r); err != nil {
		return fmt.Errorf("could not compress payload: %w", err)
	}

	if err := gz.Close(); err != nil {
		return fmt.Errorf("could not close gzipwriter: %w", err)
	}

	return nil
}

func storeMachineConfigOnDisk(cs *framework.ClientSet, pool *mcfgv1.MachineConfigPool, dir string) error {
	mc, err := cs.MachineConfigs().Get(context.TODO(), pool.Spec.Configuration.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	out, err := json.Marshal(mc)
	if err != nil {
		return err
	}

	if err := os.Mkdir(filepath.Join(dir, "machineconfig"), 0o755); err != nil {
		return err
	}

	compressed, err := compressAndEncode(out)
	if err != nil {
		return err
	}

	mcPath := filepath.Join(dir, "machineconfig", "machineconfig.json.gz")

	if err := os.WriteFile(mcPath, compressed.Bytes(), 0o755); err != nil {
		return err
	}

	klog.Infof("Stored MachineConfig %s on disk at %s", mc.Name, mcPath)
	return nil
}

// TODO: Dedupe this with the code from the buildcontroller package.
func getImageBuildRequest(cs *framework.ClientSet, targetPool string) (*build.ImageBuildRequest, error) {
	osImageURLConfigMap, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "machine-config-osimageurl", metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	customDockerfile, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), "on-cluster-build-custom-dockerfile", metav1.GetOptions{})
	if err != nil && !apierrs.IsNotFound(err) {
		return nil, err
	}

	var customDockerfileContents string
	if customDockerfile != nil {
		customDockerfileContents = customDockerfile.Data[targetPool]
	}

	mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	buildReq := build.ImageBuildRequest{
		Pool: mcp,
		BaseImage: build.ImageInfo{
			Pullspec: osImageURLConfigMap.Data["baseOSContainerImage"],
		},
		ExtensionsImage: build.ImageInfo{
			Pullspec: osImageURLConfigMap.Data["baseOSExtensionsContainerImage"],
		},
		ReleaseVersion:   osImageURLConfigMap.Data["releaseVersion"],
		CustomDockerfile: customDockerfileContents,
	}

	return &buildReq, nil
}

func renderDockerfile(ibr *build.ImageBuildRequest, out io.Writer, copyToStdout bool) error {
	// TODO: Export the template from the assets package.
	dockerfileTemplate, err := os.ReadFile("/Users/zzlotnik/go/src/github.com/openshift/machine-config-operator/pkg/controller/build/assets/Dockerfile.on-cluster-build-template")
	if err != nil {
		return err
	}

	tmpl, err := template.New("dockerfile").Parse(string(dockerfileTemplate))
	if err != nil {
		return err
	}

	if copyToStdout {
		out = io.MultiWriter(out, os.Stdout)
	}

	return tmpl.Execute(out, ibr)
}

func renderDockerfileToDisk(cs *framework.ClientSet, targetPool, dir string) error {
	ibr, err := getImageBuildRequest(cs, targetPool)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	dockerfilePath := filepath.Join(dir, "Dockerfile")

	dockerfile, err := os.Create(dockerfilePath)
	defer func() {
		failOnError(dockerfile.Close())
	}()
	if err != nil {
		return err
	}

	klog.Infof("Rendered Dockerfile to disk at %s", dockerfilePath)
	return renderDockerfile(ibr, dockerfile, false)
}

func createPool(cs *framework.ClientSet, poolName string) (*mcfgv1.MachineConfigPool, error) {
	pool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: poolName,
			Labels: map[string]string{
				createdByOnClusterBuildsHelper: "",
			},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			MachineConfigSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      mcfgv1.MachineConfigRoleLabelKey,
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"worker", poolName},
					},
				},
			},
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"node-role.kubernetes.io/" + poolName: "",
				},
			},
		},
	}

	klog.Infof("Creating MachineConfigPool %q", pool.Name)

	_, err := cs.MachineConfigPools().Create(context.TODO(), pool, metav1.CreateOptions{})
	switch {
	case apierrs.IsAlreadyExists(err):
		klog.Infof("Pool %q already exists, will reuse", poolName)
	case err != nil && !apierrs.IsAlreadyExists(err):
		return nil, err
	}

	klog.Infof("Waiting for pool %s to get a rendered MachineConfig", poolName)

	if _, err := waitForRenderedConfigs(cs, poolName, "99-worker-ssh"); err != nil {
		return nil, err
	}

	return cs.MachineConfigPools().Get(context.TODO(), poolName, metav1.GetOptions{})
}

func optInPool(cs *framework.ClientSet, targetPool string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if mcp.Labels == nil {
			mcp.Labels = map[string]string{}
		}

		mcp.Labels[ctrlcommon.LayeringEnabledPoolLabel] = ""

		klog.Infof("Opted MachineConfigPool %q into layering", mcp.Name)
		_, err = cs.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
		return err
	})
}

func addImageToLayeredPool(cs *framework.ClientSet, pullspec, targetPool string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		if mcp.Labels == nil {
			if err := optInPool(cs, targetPool); err != nil {
				return err
			}
		}

		if mcp.Annotations == nil {
			mcp.Annotations = map[string]string{}
		}

		mcp.Annotations[ctrlcommon.ExperimentalNewestLayeredImageEquivalentConfigAnnotationKey] = pullspec
		mcp, err = cs.MachineConfigPools().Update(context.TODO(), mcp, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Applied image %q to MachineConfigPool %s", pullspec, mcp.Name)
		return clearThenSetStatusOnPool(cs, targetPool, mcfgv1.MachineConfigPoolBuildSuccess, corev1.ConditionTrue)
	})
}

func teardownPool(cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) error {
	err := cs.MachineConfigPools().Delete(context.TODO(), mcp.Name, metav1.DeleteOptions{})
	if apierrs.IsNotFound(err) {
		klog.Infof("Pool %s not found", mcp.Name)
		return nil
	}

	if err != nil && !apierrs.IsNotFound(err) {
		return err
	}

	klog.Infof("Deleted pool %s", mcp.Name)
	return nil
}

func deleteAllNonStandardPools(cs *framework.ClientSet) error {
	pools, err := cs.MachineConfigPools().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, pool := range pools.Items {
		if pool.Name != "master" && pool.Name != "worker" {
			if err := teardownPool(cs, &pool); err != nil {
				return err
			}
		}
	}

	return nil
}

func resetAllNodeAnnotations(cs *framework.ClientSet) error {
	workerPool, err := cs.MachineConfigPools().Get(context.TODO(), "worker", metav1.GetOptions{})
	if err != nil {
		return err
	}

	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, node := range nodes.Items {
		if err := resetNodeAnnotationsAndLabels(cs, workerPool, &node); err != nil {
			return err
		}
	}

	return nil
}

func resetNodeAnnotationsAndLabels(cs *framework.ClientSet, originalPool *mcfgv1.MachineConfigPool, node *corev1.Node) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		node, err := cs.CoreV1Interface.Nodes().Get(context.TODO(), node.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		expectedNodeRoles := map[string]struct{}{
			"node-role.kubernetes.io/worker":        {},
			"node-role.kubernetes.io/master":        {},
			"node-role.kubernetes.io/control-plane": {},
		}

		for label := range node.Labels {
			_, isExpectedNodeRole := expectedNodeRoles[label]
			if strings.HasPrefix(label, "node-role.kubernetes.io") && !isExpectedNodeRole {
				delete(node.Labels, label)
			}
		}

		if _, ok := node.Labels[helpers.MCPNameToRole(originalPool.Name)]; ok {
			node.Annotations[constants.CurrentMachineConfigAnnotationKey] = originalPool.Spec.Configuration.Name
			node.Annotations[constants.DesiredMachineConfigAnnotationKey] = originalPool.Spec.Configuration.Name
			delete(node.Annotations, constants.CurrentImageAnnotationKey)
			delete(node.Annotations, constants.DesiredImageAnnotationKey)
		}

		_, err = cs.CoreV1Interface.Nodes().Update(context.TODO(), node, metav1.UpdateOptions{})
		return err
	})
}

func deleteAllMachineConfigsForPool(cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) error {
	machineConfigs, err := cs.MachineConfigs().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, mc := range machineConfigs.Items {
		if _, ok := mc.Annotations[helpers.MCPNameToRole(mcp.Name)]; ok && !strings.HasPrefix(mc.Name, "rendered-") {
			if err := cs.MachineConfigs().Delete(context.TODO(), mc.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}
		}
	}

	return nil
}

func deleteBuildObjects(cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool) error {
	selector, err := getSelectorForMCP(mcp)
	if err != nil {
		return err
	}

	return deleteBuildObjectsForSelector(cs, selector)
}

func getSelectorForMCP(mcp *mcfgv1.MachineConfigPool) (labels.Selector, error) {
	key := "machineconfiguration.openshift.io/desiredConfig"
	selector := labels.NewSelector()
	if mcp == nil {
		req, err := labels.NewRequirement(key, selection.Exists, []string{})
		if err != nil {
			return labels.NewSelector(), err
		}

		return selector.Add(*req), nil
	}

	req, err := labels.NewRequirement(key, selection.Equals, []string{mcp.Spec.Configuration.Name})
	if err != nil {
		return labels.NewSelector(), err
	}

	return selector.Add(*req), nil
}

func deleteBuildObjectsForSelector(cs *framework.ClientSet, selector labels.Selector) error {
	listOpts := metav1.ListOptions{
		LabelSelector: selector.String(),
	}

	configMaps, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).List(context.TODO(), listOpts)

	if err != nil {
		return err
	}

	for _, configMap := range configMaps.Items {
		if err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Delete(context.TODO(), configMap.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
		klog.Infof("Deleted ConfigMap %s", configMap.Name)
	}

	pods, err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).List(context.TODO(), listOpts)
	if err != nil {
		return err
	}

	for _, pod := range pods.Items {
		if err := cs.CoreV1Interface.Pods(ctrlcommon.MCONamespace).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
		klog.Infof("Deleted Pod %s", pod.Name)
	}

	builds, err := cs.BuildV1Interface.Builds(ctrlcommon.MCONamespace).List(context.TODO(), listOpts)
	if err != nil {
		return err
	}

	for _, build := range builds.Items {
		if err := cs.BuildV1Interface.Builds(ctrlcommon.MCONamespace).Delete(context.TODO(), build.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}
		klog.Infof("Deleted Build %s", build.Name)
	}

	klog.Infof("Cleaned up all build objects for selector %s", selector.String())

	return nil
}

func waitForRenderedConfigs(cs *framework.ClientSet, pool string, mcNames ...string) (string, error) {
	var renderedConfig string
	startTime := time.Now()
	found := make(map[string]bool)

	ctx := context.Background()

	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		// Set up the list
		for _, name := range mcNames {
			found[name] = false
		}

		// Update found based on the MCP
		mcp, err := cs.MachineConfigPools().Get(ctx, pool, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, mc := range mcp.Spec.Configuration.Source {
			if _, ok := found[mc.Name]; ok {
				found[mc.Name] = true
			}
		}

		// If any are still false, then they weren't included in the MCP
		for _, nameFound := range found {
			if !nameFound {
				return false, nil
			}
		}

		// All the required names were found
		renderedConfig = mcp.Spec.Configuration.Name
		return true, nil
	}); err != nil {
		return "", fmt.Errorf("machine configs %v hasn't been picked by pool %s (waited %s): %w", notFoundNames(found), pool, time.Since(startTime), err)
	}
	klog.Infof("Pool %s has rendered configs %v with %s (waited %v)", pool, mcNames, renderedConfig, time.Since(startTime))
	return renderedConfig, nil
}

func notFoundNames(foundNames map[string]bool) []string {
	out := []string{}
	for name, found := range foundNames {
		if !found {
			out = append(out, name)
		}
	}
	return out
}

func clearBuildStatusesOnPool(cs *framework.ClientSet, targetPool string) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		buildConditions := map[mcfgv1.MachineConfigPoolConditionType]struct{}{
			mcfgv1.MachineConfigPoolBuildSuccess: {},
			mcfgv1.MachineConfigPoolBuildFailed:  {},
			mcfgv1.MachineConfigPoolBuildPending: {},
			mcfgv1.MachineConfigPoolBuilding:     {},
		}

		filtered := []mcfgv1.MachineConfigPoolCondition{}
		for _, cond := range mcp.Status.Conditions {
			if _, ok := buildConditions[cond.Type]; !ok {
				filtered = append(filtered, cond)
			}
		}

		mcp.Status.Conditions = filtered
		_, err = cs.MachineConfigPools().UpdateStatus(context.TODO(), mcp, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Cleared build statuses on MachineConfigPool %s", targetPool)
		return nil
	})
}

func setStatusOnPool(cs *framework.ClientSet, targetPool string, condType mcfgv1.MachineConfigPoolConditionType, status corev1.ConditionStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
		if err != nil {
			return err
		}

		newCond := mcfgv1.NewMachineConfigPoolCondition(condType, status, "", "")
		mcfgv1.SetMachineConfigPoolCondition(&mcp.Status, *newCond)

		_, err = cs.MachineConfigPools().UpdateStatus(context.TODO(), mcp, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		klog.Infof("Set %s / %s on %s", condType, status, targetPool)

		return nil
	})
}

func clearThenSetStatusOnPool(cs *framework.ClientSet, targetPool string, condType mcfgv1.MachineConfigPoolConditionType, status corev1.ConditionStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := clearBuildStatusesOnPool(cs, targetPool); err != nil {
			return err
		}

		return setStatusOnPool(cs, targetPool, condType, status)
	})
}

func extractBuildObjectsForTargetPool(cs *framework.ClientSet, targetPool, targetDir string) error {
	mcp, err := cs.MachineConfigPools().Get(context.TODO(), targetPool, metav1.GetOptions{})
	if err != nil {
		return err
	}

	return extractBuildObjects(cs, mcp, targetDir)
}

func extractBuildObjects(cs *framework.ClientSet, mcp *mcfgv1.MachineConfigPool, targetDir string) error {
	return extractBuildObjectsForRenderedMC(cs, mcp.Spec.Configuration.Name, targetDir)
}

func extractBuildObjectsForRenderedMC(cs *framework.ClientSet, mcName, targetDir string) error {
	ctx := context.Background()

	dockerfileCM, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "dockerfile-"+mcName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	mcCM, err := cs.CoreV1Interface.ConfigMaps(ctrlcommon.MCONamespace).Get(ctx, "mc-"+mcName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	klog.Infof("Extracted Dockerfile from %q", dockerfileCM.Name)
	klog.Infof("Extracted MachineConfig %s from %q", mcName, mcCM.Name)

	return storeBuildObjectsOnDisk(dockerfileCM.Data["Dockerfile"], mcCM.Data["machineconfig.json.gz"], filepath.Join(targetDir, "build-objects-"+mcName))
}

func storeBuildObjectsOnDisk(dockerfile, machineConfig, targetDir string) error {
	mcDirName := filepath.Join(targetDir, "machineconfig")
	dockerfileName := filepath.Join(targetDir, "Dockerfile")
	mcFilename := filepath.Join(targetDir, "machineconfig.json.gz")

	if err := os.MkdirAll(mcDirName, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(dockerfileName, []byte(dockerfile), 0o755); err != nil {
		return err
	}

	klog.Infof("Wrote Dockerfile to %s", dockerfileName)

	if err := os.WriteFile(mcFilename, []byte(machineConfig), 0o755); err != nil {
		return err
	}

	klog.Infof("Wrote MachineConfig to %s", mcFilename)

	return nil
}

func getDir(target string) string {
	if target != "" {
		return target
	}

	cwd, err := os.Getwd()
	failOnError(err)
	return cwd
}

func failOnError(err error) {
	if err != nil {
		klog.Fatalln(err)
	}
}

func common(opts interface{}) {
	flag.Set("v", "4")
	flag.Set("logtostderr", "true")
	flag.Parse()

	klog.Infof("Options parsed: %+v", opts)

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)
}

func failIfNotSet(in, name string) {
	if isEmpty(in) {
		if !strings.HasPrefix(name, "--") {
			name = "--" + name
		}
		klog.Fatalf("Required flag %s not set", name)
	}
}

func isNoneSet(in1, in2 string) bool {
	return isEmpty(in1) && isEmpty(in2)
}

func isOnlyOneSet(in1, in2 string) bool {
	if !isEmpty(in1) && !isEmpty(in2) {
		return false
	}

	return true
}

func isEmpty(in string) bool {
	return in == ""
}

func dumpYAMLToStdout(in interface{}) error {
	out, err := yaml.Marshal(in)
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(out)
	return err
}

func getListOptsForOurLabel() metav1.ListOptions {
	req, err := labels.NewRequirement(createdByOnClusterBuildsHelper, selection.Exists, []string{})
	if err != nil {
		klog.Fatalln(err)
	}

	return metav1.ListOptions{
		LabelSelector: req.String(),
	}
}

func ignoreIsNotFound(err error) error {
	if err == nil {
		return nil
	}

	if apierrs.IsNotFound(err) {
		return nil
	}

	return err
}
