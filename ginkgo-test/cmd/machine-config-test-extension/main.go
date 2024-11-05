package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	e2e "k8s.io/kubernetes/test/e2e/framework"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"

	// If using ginkgo, import your tests here

	"github.com/openshift/machine-config-operator/ginkgo-test/pkg/version"
	_ "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/mco"
	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
)

func main() {
	// Extension registry
	registry := e.NewRegistry()

	// You can declare multiple extensions, but most people will probably only need to create one.
	ext := e.NewExtension("openshift", "payload", "mco-tests")
	// set source code info, will be replaced by ldflags
	ext.Source.BuildDate = version.BuildDate
	ext.Source.Commit = version.CommitFromGit
	ext.Source.GitTreeState = version.GitTreeState

	// add smoke test suite to do health check
	ext.AddSuite(e.Suite{
		Name: "mco/smoke-test",
		Qualifiers: []string{
			`name.contains("health check")`,
		},
	})

	// all level0 test cases
	ext.AddSuite(e.Suite{
		Name: "mco/level0",
		Parents: []string{
			"openshift/disruptive",
		},
		Qualifiers: []string{
			`name.contains("LEVEL0")`,
		},
	})

	// critical test cases
	ext.AddSuite(e.Suite{
		Name: "mco/critical",
		Parents: []string{
			"openshift/disruptive",
		},
		Qualifiers: []string{
			`name.contains("-Critical-")`,
		},
	})

	// all test cases
	ext.AddSuite(e.Suite{
		Name: "mco/full",
		Parents: []string{
			"openshift/disruptive",
		},
		Qualifiers: []string{
			`name.contains("sig-mco")`,
		},
	})

	// If using Ginkgo, build test specs automatically
	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(fmt.Sprintf("couldn't build extension test specs from ginkgo: %+v", err.Error()))
	}

	specs.AddBeforeAll(func() {
		// initialize test framework
		// dryRun is false by default
		err := initProvider(os.Getenv("TEST_PROVIDER"), false)
		if err != nil {
			panic(fmt.Sprintf("initialize test framework failed: %+v", err.Error()))
		}
		readClusterTypeEnvsAndSetFlags()
		e2e.AfterReadingAllFlags(exutil.TestContext)
		e2e.TestContext.DumpLogsOnFailure = true
		exutil.TestContext.DumpLogsOnFailure = true
	})

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "OpenShift Tests Extension - MCO",
	}

	root.AddCommand(cmd.DefaultExtensionCommands(registry)...)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}
