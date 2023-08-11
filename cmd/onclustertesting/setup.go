package main

import (
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	setupCmd = &cobra.Command{
		Use:   "setup",
		Short: "Sets up pool for on-cluster build testing",
		Long:  "",
		Run:   runSetupCmd,
	}

	inClusterRegistryCmd = &cobra.Command{
		Use:   "in-cluster-registry",
		Short: "Sets up pool for on-cluster build testing using an ImageStream",
		Long:  "",
		Run:   runInClusterRegistrySetupCmd,
	}

	setupOpts struct {
		pullSecretPath     string
		pushSecretPath     string
		pullSecretName     string
		pushSecretName     string
		finalImagePullspec string
		poolName           string
		waitForBuildInfo   bool
	}
)

func init() {
	rootCmd.AddCommand(setupCmd)
	setupCmd.AddCommand(inClusterRegistryCmd)
	setupCmd.PersistentFlags().StringVar(&setupOpts.poolName, "pool", defaultLayeredPoolName, "Pool name to setup")
	setupCmd.PersistentFlags().BoolVar(&setupOpts.waitForBuildInfo, "wait-for-build", false, "Wait for build info")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pullSecretName, "pull-secret-name", "", "The name of a preexisting secret to use as the pull secret. If absent, will clone global pull secret.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pushSecretName, "push-secret-name", "", "The name of a preexisting secret to use as the push secret.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pullSecretPath, "pull-secret-path", "", "Path to a pull secret YAML to use. If absent, will clone global pull secret.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.pushSecretPath, "push-secret-path", "", "Path to a pull secret YAML to use.")
	setupCmd.PersistentFlags().StringVar(&setupOpts.finalImagePullspec, "final-pullspec", "", "The final image pushspec to use for testing")
}

func runSetupCmd(_ *cobra.Command, _ []string) {
	common(setupOpts)

	failIfNotSet(setupOpts.poolName, "pool")
	failIfNotSet(setupOpts.finalImagePullspec, "final-pullspec")

	if isNoneSet(setupOpts.pushSecretPath, setupOpts.pushSecretName) {
		klog.Fatalln("Either --push-secret-name or --push-secret-path must be provided!")
	}

	if !isOnlyOneSet(setupOpts.pushSecretPath, setupOpts.pushSecretName) {
		klog.Fatalln("--pull-secret-name and --pull-secret-path cannot be combined!")
	}

	if !isOnlyOneSet(setupOpts.pullSecretPath, setupOpts.pullSecretName) {
		klog.Fatalln("--push-secret-name and --push-secret-path cannot be combined!")
	}

	if err := mobSetup(framework.NewClientSet(""), setupOpts.poolName, setupOpts.waitForBuildInfo); err != nil {
		klog.Fatal(err)
	}
}

func runInClusterRegistrySetupCmd(_ *cobra.Command, _ []string) {
	common(setupOpts)

	failIfNotSet(setupOpts.poolName, "pool")

	cs := framework.NewClientSet("")

	failOnError(inClusterMobSetup(cs, setupOpts.poolName, setupOpts.waitForBuildInfo))
}

func inClusterMobSetup(cs *framework.ClientSet, targetPool string, getBuildInfo bool) error {
	pushSecretName, err := getBuilderPushSecretName(cs)
	if err != nil {
		return err
	}

	imagestreamName := "os-image"
	if err := createImagestream(cs, imagestreamName); err != nil {
		return err
	}

	pullspec, err := getImagestreamPullspec(cs, imagestreamName)
	if err != nil {
		return err
	}

	if _, err := createPool(cs, targetPool); err != nil {
		return err
	}

	opts := onClusterBuildConfigMapOpts{
		pushSecretName:     pushSecretName,
		finalImagePullspec: pullspec,
	}

	if err := createConfigMapsAndSecrets(cs, opts); err != nil {
		return err
	}

	if err := optInPool(cs, targetPool); err != nil {
		return err
	}

	if !getBuildInfo {
		return nil
	}

	return waitForBuildInfo(cs, targetPool)
}

func mobSetup(cs *framework.ClientSet, targetPool string, getBuildInfo bool) error {
	if _, err := createPool(cs, targetPool); err != nil {
		return err
	}

	opts := onClusterBuildConfigMapOpts{
		pushSecretName:     setupOpts.pushSecretName,
		pullSecretName:     setupOpts.pullSecretName,
		pushSecretPath:     setupOpts.pushSecretPath,
		pullSecretPath:     setupOpts.pullSecretPath,
		finalImagePullspec: setupOpts.finalImagePullspec,
	}

	if err := createConfigMapsAndSecrets(cs, opts); err != nil {
		return err
	}

	if err := optInPool(cs, targetPool); err != nil {
		return err
	}

	if !getBuildInfo {
		return nil
	}

	return waitForBuildInfo(cs, targetPool)
}

func createConfigMapsAndSecrets(cs *framework.ClientSet, opts onClusterBuildConfigMapOpts) error {
	if opts.shouldCloneGlobalPullSecret() {
		if err := copyGlobalPullSecret(cs); err != nil {
			return nil
		}
	}

	if opts.pushSecretPath != "" {
		if err := createSecretFromFile(cs, opts.pushSecretPath); err != nil {
			return err
		}
	}

	if opts.pullSecretPath != "" {
		if err := createSecretFromFile(cs, opts.pullSecretPath); err != nil {
			return err
		}
	}

	secretNames := opts.getSecretNameParams()
	if err := validateSecretsExist(cs, secretNames); err != nil {
		return err
	}

	if err := createOnClusterBuildConfigMap(cs, opts); err != nil {
		return err
	}

	return createCustomDockerfileConfigMap(cs)
}

func waitForBuildInfo(_ *framework.ClientSet, _ string) error {
	klog.Infof("no-op for now")
	return nil
}
