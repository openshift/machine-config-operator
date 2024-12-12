package main

import (
	"context"
	"fmt"
	"time"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"
)

func init() {
	setupOpts := opts{}

	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "Sets up pool for on-cluster build testing",
		Long:  "",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSetupCmd(setupOpts)
		},
	}

	inClusterRegistryCmd := &cobra.Command{
		Use:   "in-cluster-registry",
		Short: "Sets up pool for on-cluster build testing using an ImageStream",
		Long:  "",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInClusterRegistrySetupCmd(setupOpts)
		},
	}

	ciSetupCmd := &cobra.Command{
		Use:   "ci",
		Short: "Sets up a cluster for on-cluster builds in a CI context.",
		Long:  "",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCiSetupCmd(setupOpts)
		},
	}

	setupCmd.AddCommand(inClusterRegistryCmd)
	setupCmd.AddCommand(ciSetupCmd)
	setupCmd.PersistentFlags().StringVar(&setupOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to setup")
	setupCmd.PersistentFlags().BoolVar(&setupOpts.waitForBuildInfo, "wait-for-build", false, "Wait for build info")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pullSecretName, "pull-secret-name", "", "The name of a preexisting secret to use as the pull secret. If absent, will clone global pull secret.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pushSecretName, "push-secret-name", "", "The name of a preexisting secret to use as the push secret.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pullSecretPath, "pull-secret-path", "", "Path to a pull secret K8s YAML to use. If absent, will clone global pull secret.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pushSecretPath, "push-secret-path", "", "Path to a push secret K8s YAML to use.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.finalImagePullspec, "final-pullspec", "", "The final image pushspec to use for testing")
	setupCmd.PersistentFlags().StringVar(&setupOpts.containerfilePath, "containerfile-path", "", "Optional Containerfile to inject for the build.")
	setupCmd.PersistentFlags().BoolVar(&setupOpts.enableFeatureGate, "enable-feature-gate", false, "Enables the required featuregates if not already enabled.")
	setupCmd.PersistentFlags().BoolVar(&setupOpts.injectYumRepos, "inject-yum-repos", false, fmt.Sprintf("Injects contents from the /etc/yum.repos.d and /etc/pki/rpm-gpg directories found in %s into the %s namespace.", yumReposContainerImagePullspec, ctrlcommon.MCONamespace))
	setupCmd.PersistentFlags().BoolVar(&setupOpts.copyEtcPkiEntitlementSecret, "copy-etc-pki-entitlement-secret", false, fmt.Sprintf("Copies etc-pki-entitlement into the %s namespace, assuming it exists.", ctrlcommon.MCONamespace))

	rootCmd.AddCommand(setupCmd)
}

func runSetupCmd(setupOpts opts) error {
	utils.ParseFlags()

	// TODO: Figure out how to use cobra flags for validation directly.
	if err := errIfNotSet(setupOpts.poolName, "pool"); err != nil {
		return err
	}

	if err := errIfNotSet(setupOpts.finalImagePullspec, "final-pullspec"); err != nil {
		return err
	}

	if isNoneSet(setupOpts.pushSecretPath, setupOpts.pushSecretName) {
		return fmt.Errorf("either --push-secret-name or --push-secret-path must be provided")
	}

	if !isOnlyOneSet(setupOpts.pushSecretPath, setupOpts.pushSecretName) {
		return fmt.Errorf("flags --pull-secret-name and --pull-secret-path cannot be combined")
	}

	if !isOnlyOneSet(setupOpts.pullSecretPath, setupOpts.pullSecretName) {
		return fmt.Errorf("flags --push-secret-name and --push-secret-path cannot be combined")
	}

	if setupOpts.injectYumRepos && setupOpts.copyEtcPkiEntitlementSecret {
		return fmt.Errorf("flags --inject-yum-repos and --copy-etc-pki-entitlement cannot be combined")
	}

	if err := utils.CheckForBinaries([]string{"oc"}); err != nil {
		return err
	}

	cs := framework.NewClientSet("")

	if err := checkForRequiredFeatureGates(cs, setupOpts); err != nil {
		return err
	}

	return mobSetup(cs, opts{
		pushSecretName:              setupOpts.pushSecretName,
		pullSecretName:              setupOpts.pullSecretName,
		pushSecretPath:              setupOpts.pushSecretPath,
		pullSecretPath:              setupOpts.pullSecretPath,
		finalImagePullspec:          setupOpts.finalImagePullspec,
		containerfilePath:           setupOpts.containerfilePath,
		poolName:                    setupOpts.poolName,
		injectYumRepos:              setupOpts.injectYumRepos,
		copyEtcPkiEntitlementSecret: setupOpts.copyEtcPkiEntitlementSecret,
	})
}

func runInClusterRegistrySetupCmd(setupOpts opts) error {
	utils.ParseFlags()

	if err := errIfNotSet(setupOpts.poolName, "pool"); err != nil {
		return err
	}

	if setupOpts.injectYumRepos && setupOpts.copyEtcPkiEntitlementSecret {
		return fmt.Errorf("flags --inject-yum-repos and --copy-etc-pki-entitlement cannot be combined")
	}

	cs := framework.NewClientSet("")

	if err := checkForRequiredFeatureGates(cs, setupOpts); err != nil {
		return err
	}

	// TODO: Validate that pulls work with the pull image secret.
	pushSecretName, err := createLongLivedImagePushSecretForPool(context.TODO(), cs, setupOpts.poolName)
	if err != nil {
		return fmt.Errorf("could not create long-lived push and pull secrets: %w", err)
	}

	imagestreamName := "os-image"
	if err := createImagestream(cs, imagestreamName); err != nil {
		return err
	}

	pullspec, err := getImagestreamPullspec(cs, imagestreamName)
	if err != nil {
		return err
	}

	setupOpts.pullSecretName = globalPullSecretCloneName
	setupOpts.finalImagePullSecretName = pushSecretName
	setupOpts.pushSecretName = pushSecretName
	setupOpts.finalImagePullspec = pullspec

	return mobSetup(cs, setupOpts)
}

func mobSetup(cs *framework.ClientSet, setupOpts opts) error {
	eg := errgroup.Group{}

	eg.Go(func() error {
		_, err := createPool(cs, setupOpts.poolName)
		return err
	})

	eg.Go(func() error {
		return createSecrets(cs, setupOpts)
	})

	if err := eg.Wait(); err != nil {
		return err
	}

	mosc, err := setupOpts.toMachineOSConfig()
	if err != nil {
		return err
	}

	if err := createMachineOSConfig(cs, mosc); err != nil {
		return err
	}

	if !setupOpts.waitForBuildInfo {
		return nil
	}

	return waitForBuildInfo(cs, setupOpts.poolName)
}

func createSecrets(cs *framework.ClientSet, opts opts) error {
	start := time.Now()

	eg := errgroup.Group{}

	eg.Go(func() error {
		if opts.shouldCloneGlobalPullSecret() {
			if err := copyGlobalPullSecret(cs); err != nil {
				// Not sure why this snarfs any errors from this process.
				return nil
			}
		}

		return nil
	})

	if opts.pushSecretPath != "" {
		pushSecretPath := opts.pushSecretPath
		eg.Go(func() error {
			return createSecretFromFile(cs, pushSecretPath)
		})
	}

	if opts.pullSecretPath != "" {
		pullSecretPath := opts.pullSecretPath
		eg.Go(func() error {
			return createSecretFromFile(cs, pullSecretPath)
		})

	}

	if opts.copyEtcPkiEntitlementSecret {
		eg.Go(func() error {
			return copyEtcPkiEntitlementSecret(cs)
		})
	}

	if opts.injectYumRepos {
		eg.Go(func() error {
			return extractAndInjectYumEpelRepos(cs)
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	secretNames := opts.getSecretNameParams()
	if err := validateSecretsExist(cs, secretNames); err != nil {
		return err
	}

	klog.Infof("All secrets set up after %s", time.Since(start))

	return nil
}

func waitForBuildInfo(_ *framework.ClientSet, _ string) error {
	klog.Infof("no-op for now")
	return nil
}
