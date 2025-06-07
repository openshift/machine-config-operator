package main

import (
	"fmt"
	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd/cmdimages"
	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd/cmdinfo"
	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd/cmdlist"
	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd/cmdrun"
	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd/cmdupdate"
	exutil "github.com/openshift/machine-config-operator/test/extended/util"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	"os"

	"github.com/spf13/cobra"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	// If using ginkgo, import your tests here
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

	specs.AddBeforeAll(func() {

	})

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "OpenShift Tests Extension - MCO",
	}

	exutil.InitStandardFlags()

	root.AddCommand(
		cmdlist.NewListCommand(registry),
		cmdinfo.NewInfoCommand(registry),
		cmdupdate.NewUpdateCommand(registry),
		cmdimages.NewImagesCommand(registry),
		injectFrameworkInit(cmdrun.NewRunSuiteCommand(registry)),
		injectFrameworkInit(cmdrun.NewRunTestCommand(registry)),
	)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}

func injectFrameworkInit(command *cobra.Command) *cobra.Command {
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if err := exutil.InitTest(false); err != nil {
			return err
		}
		e2e.AfterReadingAllFlags(exutil.TestContext)
		e2e.TestContext.DumpLogsOnFailure = true
		exutil.TestContext.DumpLogsOnFailure = true
		return nil
	}
	return command
}
