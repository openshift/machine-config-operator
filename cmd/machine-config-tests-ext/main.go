package main

import (
	"fmt"
	"os"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	e2e "k8s.io/kubernetes/test/e2e/framework"

	"github.com/spf13/cobra"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	_ "github.com/openshift/machine-config-operator/test/extended"
)

func main() {
	// Extension registry
	registry := e.NewRegistry()

	// You can declare multiple extensions, but most people will probably only need to create one.
	ext := e.NewExtension("openshift", "payload", "mco-tests")

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

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "MCO Extended Test Suite (OTE Based)",
	}

	exutil.InitStandardFlags()
	specs.AddBeforeAll(func() {
		if err := exutil.InitTest(false); err != nil {
			panic(err)
		}
		e2e.AfterReadingAllFlags(exutil.TestContext)
		e2e.TestContext.DumpLogsOnFailure = true
		exutil.TestContext.DumpLogsOnFailure = true
	})

	root.AddCommand(
		cmd.DefaultExtensionCommands(registry)...,
	)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}
