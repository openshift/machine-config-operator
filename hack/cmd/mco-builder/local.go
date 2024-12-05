package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/openshift/machine-config-operator/hack/cmd/mco-builder/internal/builders"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/containers"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/rollout"
	"github.com/openshift/machine-config-operator/hack/internal/pkg/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	aggerrs "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

type localBuildOpts struct {
	builderKind              string // builders.BuilderType
	finalImagePushSecretPath string
	finalImagePullspec       string
	directPush               bool
	repoRoot                 string
	skipRollout              bool
}

func (l *localBuildOpts) getBuilderType() builders.BuilderType {
	return builders.BuilderType(l.builderKind)
}

func (l *localBuildOpts) validate() error {
	if l.repoRoot == "" {
		return fmt.Errorf("--repo-root must be provided")
	}

	if err := utils.CheckForBinaries([]string{"oc"}); err != nil {
		return err
	}

	if _, err := os.Stat(l.repoRoot); err != nil {
		return err
	}

	localBuilderTypes := builders.GetLocalBuilderTypes()
	if !localBuilderTypes.Has(l.getBuilderType()) {
		return fmt.Errorf("invalid builder type %s, valid builder types: %v", l.getBuilderType(), sets.List(localBuilderTypes))
	}

	if _, err := exec.LookPath(l.builderKind); err != nil {
		return err
	}

	if l.directPush {
		if l.finalImagePushSecretPath != "" {
			return fmt.Errorf("--push-secret may not be used in direct mode")
		}

		if l.finalImagePullspec != "" {
			return fmt.Errorf("--final-image-pullspec may not be used in direct mode")
		}

		return utils.CheckForBinaries([]string{"skopeo"})
	}

	if l.finalImagePushSecretPath == "" {
		return fmt.Errorf("--push-secret must be provided when not using direct mode")
	}

	if _, err := os.Stat(l.finalImagePushSecretPath); err != nil {
		return err
	}

	if l.finalImagePullspec == "" {
		return fmt.Errorf("--final-image-pullspec must be provided when not using direct mode")
	}

	parsedPullspec, err := containers.AddLatestTagIfMissing(l.finalImagePullspec)
	if err != nil {
		return fmt.Errorf("could not parse final image pullspec %q: %w", l.finalImagePullspec, err)
	}

	l.finalImagePullspec = parsedPullspec

	return nil
}

func init() {

	opts := localBuildOpts{}

	localCmd := &cobra.Command{
		Use:   "local",
		Short: "Builds an MCO image locally and deploys it to your sandbox cluster.",
		Long:  "Builds the MCO image locally using the specified builder and options. Can either push to a remote image registry (such as Quay.io) or can expose a route to enable pushing directly into ones sandbox cluster.",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := opts.validate(); err != nil {
				return err
			}
			return runLocalCmd(opts)
		},
	}

	localCmd.PersistentFlags().BoolVar(&opts.directPush, "direct", false, "Exposes a route and pushes the image directly into ones cluster")
	localCmd.PersistentFlags().StringVar(&opts.repoRoot, "repo-root", "", "Path to the local MCO Git repo")
	localCmd.PersistentFlags().StringVar(&opts.finalImagePushSecretPath, "push-secret", "", "Path to the push secret path needed to push to the provided pullspec (not needed in direct mode)")
	localCmd.PersistentFlags().StringVar(&opts.finalImagePullspec, "final-image-pullspec", "", "Where to push the final image (not needed in direct mode)")
	localCmd.PersistentFlags().StringVar(&opts.builderKind, "builder", string(builders.GetDefaultBuilderTypeForPlatform()), fmt.Sprintf("What image builder to use: %v", sets.List(builders.GetLocalBuilderTypes())))
	localCmd.PersistentFlags().BoolVar(&opts.skipRollout, "skip-rollout", false, "Builds and pushes the image, but does not update the MCO deployment / daemonset objects")

	rootCmd.AddCommand(localCmd)
}

func runLocalCmd(opts localBuildOpts) error {

	cs := framework.NewClientSet("")

	if err := validateLocalAndClusterArches(cs); err != nil {
		if !errors.Is(err, errInvalidArch) {
			return err
		}

		// TODO: Return the error here instead. Need to validate GOARCH against Linux ARM64 and Darwin ARM64.
		klog.Warning(err)
	}

	if opts.directPush {
		return buildLocallyAndPushIntoCluster(cs, opts)
	}

	return buildLocallyAndDeploy(cs, opts)
}

func buildLocallyAndDeploy(cs *framework.ClientSet, buildOpts localBuildOpts) error {
	// TODO: Return these out of this function.
	deferredErrs := []error{}
	defer func() {
		if err := aggerrs.NewAggregate(deferredErrs); err != nil {
			klog.Fatalf("teardown encountered error(s): %s", err)
		}
	}()

	opts := builders.Opts{
		RepoRoot:       buildOpts.repoRoot,
		FinalPullspec:  buildOpts.finalImagePullspec,
		PushSecretPath: buildOpts.finalImagePushSecretPath,
		DockerfileName: filepath.Join(buildOpts.repoRoot, "Dockerfile"),
	}

	builder := builders.NewLocalBuilder(opts)

	if err := builder.Build(); err != nil {
		return err
	}

	if err := builder.Push(); err != nil {
		return err
	}

	digestedPullspec, err := containers.ResolveToDigestedPullspec(buildOpts.finalImagePullspec, buildOpts.finalImagePushSecretPath)
	if err != nil {
		return fmt.Errorf("could not resolve %s to digested image pullspec: %w", buildOpts.finalImagePullspec, err)
	}

	klog.Infof("Pushed image has digested pullspec %s", digestedPullspec)

	if buildOpts.skipRollout {
		klog.Infof("Skipping rollout since --skip-rollout was used")
		return nil
	}

	if err := rollout.ReplaceMCOImage(cs, buildOpts.finalImagePullspec, false); err != nil {
		return err
	}

	klog.Infof("New MCO rollout complete!")
	return nil
}

func buildLocallyAndPushIntoCluster(cs *framework.ClientSet, buildOpts localBuildOpts) error {
	// TODO: Return these out of this function.
	deferredErrs := []error{}
	defer func() {
		if err := aggerrs.NewAggregate(deferredErrs); err != nil {
			klog.Fatalf("encountered error(s) during teardown: %s", err)
		}
	}()

	extHostname, err := rollout.ExposeClusterImageRegistry(cs)
	if err != nil {
		return err
	}

	klog.Infof("Cluster is set up for direct pushes")

	secretPath, err := writeBuilderSecretToTempDir(cs, extHostname)
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(filepath.Dir(secretPath)); err != nil {
			deferredErrs = append(deferredErrs, err)
		}
	}()

	extPullspec := fmt.Sprintf("%s/%s/machine-config-operator:latest", extHostname, ctrlcommon.MCONamespace)

	opts := builders.Opts{
		RepoRoot:       buildOpts.repoRoot,
		FinalPullspec:  extPullspec,
		PushSecretPath: secretPath,
		DockerfileName: filepath.Join(buildOpts.repoRoot, "Dockerfile"),
	}

	builder := builders.NewLocalBuilder(opts)

	if err := builder.Build(); err != nil {
		return err
	}

	if err := builder.Push(); err != nil {
		return err
	}

	digestedPullspec, err := containers.ResolveToDigestedPullspec(extPullspec, secretPath)
	if err != nil {
		return fmt.Errorf("could not resolve %s to digested image pullspec: %w", extPullspec, err)
	}

	klog.Infof("Pushed image has digested pullspec %s", digestedPullspec)

	if buildOpts.skipRollout {
		klog.Infof("Skipping rollout since --skip-rollout was used")
		return nil
	}

	if err := rollout.ReplaceMCOImage(cs, imagestreamPullspec, false); err != nil {
		return err
	}

	klog.Infof("New MCO rollout complete!")
	return nil
}

var errInvalidArch = fmt.Errorf("local and cluster arch differ")

func validateLocalAndClusterArches(cs *framework.ClientSet) error {
	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	// TODO: Handle multiarch cases.
	clusterArch := nodes.Items[0].Status.NodeInfo.Architecture

	if clusterArch != runtime.GOARCH {
		return fmt.Errorf("local (%s) / cluster (%s): %w", runtime.GOARCH, clusterArch, errInvalidArch)
	}

	klog.Infof("Local (%s) arch matches cluster (%s)", runtime.GOARCH, clusterArch)

	return nil
}
