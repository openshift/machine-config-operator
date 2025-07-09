package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	v "github.com/openshift-eng/openshift-tests-extension/pkg/version"
	"github.com/openshift/machine-config-operator/pkg/version"
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
	setVersion()

	// You can declare multiple extensions, but most people will probably only need to create one.
	ext := e.NewExtension("openshift", "payload", "machine-config-operator")

	// all test cases
	defaultTimeout := 120 * time.Minute
	ext.AddSuite(e.Suite{
		Name: "openshift/machine-config-operator/disruptive",
		Parents: []string{
			"openshift/disruptive",
		},
		Qualifiers: []string{
			`name.contains("[Suite:openshift/machine-config-operator/disruptive")`,
		},
		ClusterStability: e.ClusterStabilityDisruptive,
		TestTimeout:      &defaultTimeout,
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

func setVersion() {
	v.CommitFromGit = version.Hash
	v.BuildDate = version.Date
	const dirtyTreeState = "dirty"
	if !strings.Contains(strings.ToLower(version.Raw), dirtyTreeState) {
		v.GitTreeState = "clean"
	} else {
		v.GitTreeState = dirtyTreeState
	}
}
