package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/klog"
	"k8s.io/kubectl/pkg/util/templates"
	e2e "k8s.io/kubernetes/test/e2e/framework"

	"github.com/openshift/machine-config-operator/ginkgo-test/pkg/monitor"
	testginkgo "github.com/openshift/machine-config-operator/ginkgo-test/pkg/test/ginkgo"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	exutilcloud "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/cloud"

	// these are loading important global flags that we need to get and set
	_ "k8s.io/kubernetes/test/e2e"
	_ "k8s.io/kubernetes/test/e2e/lifecycle"
)

// func main() {
// 	logs.InitLogs()
// 	defer logs.FlushLogs()

// 	rand.Seed(time.Now().UTC().UnixNano())

// 	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)
// 	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)

// 	root := &cobra.Command{
// 		Long: templates.LongDesc(`
// 		OpenShift Extended Platform Tests

// 		This command verifies behavior of an OpenShift cluster by running remote tests against
// 		the cluster API that exercise functionality. In general these tests may be disruptive
// 		or require elevated privileges - see the descriptions of each test suite.
// 		`),
// 	}

// 	root.AddCommand(
// 		newRunCommand(),
// 		newRunTestCommand(),
// 		newRunMonitorCommand(),
// 	)

// 	pflag.CommandLine = pflag.NewFlagSet("empty", pflag.ExitOnError)
// 	flag.CommandLine = flag.NewFlagSet("empty", flag.ExitOnError)
// 	exutil.InitStandardFlags()

// 	if err := func() error {
// 		defer serviceability.Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()
// 		return root.Execute()
// 	}(); err != nil {
// 		if ex, ok := err.(testginkgo.ExitError); ok {
// 			os.Exit(ex.Code)
// 		}
// 		fmt.Fprintf(os.Stderr, "error: %v\n", err)
// 		os.Exit(1)
// 	}
// }

func newRunMonitorCommand() *cobra.Command {
	monitorOpt := &monitor.Options{
		Out:    os.Stdout,
		ErrOut: os.Stderr,
	}
	cmd := &cobra.Command{
		Use:   "run-monitor",
		Short: "Continuously verify the cluster is functional",
		Long: templates.LongDesc(`
		Run a continuous verification process

		`),

		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return monitorOpt.Run()
		},
	}
	return cmd
}

func newRunCommand() *cobra.Command {
	opt := &testginkgo.Options{
		Suites: staticSuites,
	}

	cmd := &cobra.Command{
		Use:   "run SUITE",
		Short: "Run a test suite",
		Long: templates.LongDesc(`
		Run a test suite against an OpenShift server

		This command will run one of the following suites against a cluster identified by the current
		KUBECONFIG file. See the suite description for more on what actions the suite will take.

		If you specify the --dry-run argument, the names of each individual test that is part of the
		suite will be printed, one per line. You may filter this list and pass it back to the run
		command with the --file argument. You may also pipe a list of test names, one per line, on
		standard input by passing "-f -".

		`) + testginkgo.SuitesString(opt.Suites, "\n\nAvailable test suites:\n\n"),

		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mirrorToFile(opt, func() error {
				if err := initProvider(opt.Provider, opt.DryRun); err != nil {
					return err
				}

				if !opt.DryRun {
					checkClusterTypeAndSetEnvs()
				}

				e2e.AfterReadingAllFlags(exutil.TestContext)
				e2e.TestContext.DumpLogsOnFailure = true
				exutil.TestContext.DumpLogsOnFailure = true
				return opt.Run(args)
			})
		},
	}
	bindOptions(opt, cmd.Flags())
	return cmd
}

func newRunTestCommand() *cobra.Command {
	testOpt := &testginkgo.TestOptions{
		Out:    os.Stdout,
		ErrOut: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "run-test NAME",
		Short: "Run a single test by name",
		Long: templates.LongDesc(`
		Execute a single test

		This executes a single test by name. It is used by the run command during suite execution but may also
		be used to test in isolation while developing new tests.
		`),

		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := initProvider(os.Getenv("TEST_PROVIDER"), testOpt.DryRun); err != nil {
				return err
			}

			if !testOpt.DryRun {
				readClusterTypeEnvsAndSetFlags()
			}

			e2e.AfterReadingAllFlags(exutil.TestContext)
			e2e.TestContext.DumpLogsOnFailure = true
			exutil.TestContext.DumpLogsOnFailure = true
			return testOpt.Run(args)
		},
	}
	cmd.Flags().BoolVar(&testOpt.DryRun, "dry-run", testOpt.DryRun, "Print the test to run without executing them.")
	return cmd
}

func checkClusterTypeAndSetEnvs() {
	if exutil.PreSetEnvK8s() == "yes" {
		_ = os.Setenv(exutil.EnvIsExternalOIDCCluster, "no")
	} else {
		exutil.PreSetEnvOIDCCluster()
	}
}

func readClusterTypeEnvsAndSetFlags() {
	isK8sEnv := os.Getenv(exutil.EnvIsKubernetesCluster)
	isExtOIDCEnv := os.Getenv(exutil.EnvIsExternalOIDCCluster)
	if len(isK8sEnv) == 0 {
		isK8sEnv = exutil.PreSetEnvK8s()
		if isK8sEnv == "yes" {
			isExtOIDCEnv = "no"
			_ = os.Setenv(exutil.EnvIsExternalOIDCCluster, "no")
		}
	}
	if len(isExtOIDCEnv) == 0 {
		isExtOIDCEnv = exutil.PreSetEnvOIDCCluster()
	}
	exutil.IsExternalOIDCClusterFlag = isExtOIDCEnv
	exutil.IsKubernetesClusterFlag = isK8sEnv
	e2e.Logf("Is kubernetes cluster: %s, is external OIDC cluster: %s", exutil.IsKubernetesClusterFlag, exutil.IsExternalOIDCClusterFlag)
}

// mirrorToFile ensures a copy of all output goes to the provided OutFile, including
// any error returned from fn. The function returns fn() or any error encountered while
// attempting to open the file.
func mirrorToFile(opt *testginkgo.Options, fn func() error) error {
	if len(opt.OutFile) == 0 {
		opt.Out, opt.ErrOut = os.Stdout, os.Stderr
		return fn()
	}

	f, err := os.OpenFile(opt.OutFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	opt.Out = io.MultiWriter(os.Stdout, f)
	opt.ErrOut = io.MultiWriter(os.Stderr, f)
	exitErr := fn()
	if exitErr != nil {
		fmt.Fprintf(f, "error: %s", exitErr)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "error: Unable to close output file\n")
	}
	return exitErr
}

func bindOptions(opt *testginkgo.Options, flags *pflag.FlagSet) {
	flags.BoolVar(&opt.DryRun, "dry-run", opt.DryRun, "Print the tests to run without executing them.")
	flags.BoolVar(&opt.PrintCommands, "print-commands", opt.PrintCommands, "Print the sub-commands that would be executed instead.")
	flags.StringVar(&opt.JUnitDir, "junit-dir", opt.JUnitDir, "The directory to write test reports to.")
	flags.StringVar(&opt.Provider, "provider", opt.Provider, "The cluster infrastructure provider. Will automatically default to the correct value.")
	flags.StringVarP(&opt.TestFile, "file", "f", opt.TestFile, "Create a suite from the newline-delimited test names in this file.")
	flags.StringVar(&opt.Regex, "run", opt.Regex, "Regular expression of tests to run.")
	flags.StringVarP(&opt.OutFile, "output-file", "o", opt.OutFile, "Write all test output to this file.")
	flags.IntVar(&opt.Count, "count", opt.Count, "Run each test a specified number of times. Defaults to 1 or the suite's preferred value.")
	flags.DurationVar(&opt.Timeout, "timeout", opt.Timeout, "Set the maximum time a test can run before being aborted. This is read from the suite by default, but will be 10 minutes otherwise.")
	flags.BoolVar(&opt.IncludeSuccessOutput, "include-success", opt.IncludeSuccessOutput, "Print output from successful tests.")
	flags.IntVar(&opt.Parallelism, "max-parallel-tests", opt.Parallelism, "Maximum number of tests running in parallel. 0 defaults to test suite recommended value, which is different in each suite.")
}

func initProvider(provider string, dryRun bool) error {
	// record the exit error to the output file
	// if err := decodeProviderTo(provider, exutil.TestContext, dryRun); err != nil {
	// 	e2e.Logf("Fail to decode Provider:%s, but continue to run with skeleton mode", provider)
	// }
	exutil.TestContext.AllowedNotReadyNodes = 100
	exutil.TestContext.MaxNodesToGather = 0
	exutil.AnnotateTestSuite()
	err := exutil.InitTest(dryRun)
	gomega.RegisterFailHandler(ginkgo.Fail)

	// TODO: infer SSH keys from the cluster
	return err
}

func decodeProviderTo(provider string, testContext *e2e.TestContextType, dryRun bool) error {
	switch provider {
	case "":
		if _, ok := os.LookupEnv("KUBE_SSH_USER"); ok {
			if _, ok := os.LookupEnv("LOCAL_SSH_KEY"); ok {
				testContext.Provider = "local"
				break
			}
		}
		if dryRun {
			break
		}
		fallthrough

	case "azure", "aws", "gce", "vsphere":
		provider, cfg, err := exutilcloud.LoadConfig()
		if err != nil {
			return err
		}
		if cfg != nil {
			testContext.Provider = provider
			testContext.CloudConfig = *cfg
		}

	default:
		var providerInfo struct{ Type string }
		if err := json.Unmarshal([]byte(provider), &providerInfo); err != nil {
			return fmt.Errorf("provider must be a JSON object with the 'type' key at a minimum: %v", err)
		}
		if len(providerInfo.Type) == 0 {
			return fmt.Errorf("provider must be a JSON object with the 'type' key")
		}
		testContext.Provider = providerInfo.Type
		if err := json.Unmarshal([]byte(provider), &testContext.CloudConfig); err != nil {
			return fmt.Errorf("provider must decode into the cloud config object: %v", err)
		}
	}
	if len(testContext.Provider) == 0 {
		testContext.Provider = "skeleton"
	}
	klog.V(2).Infof("Provider %s: %#v", testContext.Provider, testContext.CloudConfig)
	return nil
}
