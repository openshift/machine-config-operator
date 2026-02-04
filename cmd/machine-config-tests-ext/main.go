package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	et "github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	v "github.com/openshift-eng/openshift-tests-extension/pkg/version"
	"github.com/openshift/machine-config-operator/pkg/version"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	e2e "k8s.io/kubernetes/test/e2e/framework"

	"github.com/spf13/cobra"

	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	_ "github.com/openshift/machine-config-operator/test/extended"
	_ "github.com/openshift/machine-config-operator/test/extended-priv"
)

func applyLabelFilters(specs et.ExtensionTestSpecs) {
	// Apply Platform label filters: tests with Platform:platformname only run on that platform
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		for label := range spec.Labels {
			if strings.HasPrefix(label, "Platform:") {
				platformName := strings.TrimPrefix(label, "Platform:")
				spec.Include(et.PlatformEquals(platformName))
			}
		}
	})

	// Apply NoPlatform label filters: tests with NoPlatform:platformname excluded from that platform
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		for label := range spec.Labels {
			if strings.HasPrefix(label, "NoPlatform:") {
				platformName := strings.TrimPrefix(label, "NoPlatform:")
				spec.Exclude(et.PlatformEquals(platformName))
			}
		}
	})

	// Exclude tests with "Exclude:REASON" labels
	specs.Walk(func(spec *et.ExtensionTestSpec) {
		for label := range spec.Labels {
			if strings.HasPrefix(label, "Exclude:") {
				spec.Exclude("true")
			}
		}
	})
}

func main() {
	// Extension registry
	registry := e.NewRegistry()
	setVersion()

	// You can declare multiple extensions, but most people will probably only need to create one.
	ext := e.NewExtension("openshift", "payload", "machine-config-operator")

	// all test cases
	defaultTimeout := 120 * time.Minute
	ext.AddGlobalSuite(e.Suite{
		Name: "openshift/machine-config-operator/disruptive",
		Parents: []string{
			"openshift/disruptive",
		},
		Qualifiers: []string{
			`name.contains("[Suite:openshift/machine-config-operator/disruptive")`,
		},
		ClusterStability: e.ClusterStabilityDisruptive,
		TestTimeout:      &defaultTimeout,
		Description:      "Suite that daily sends signals to component readiness",
	})

	// This suite will include all tests not included in the previous suite inheriting from openshift/disruptive
	ext.AddGlobalSuite(e.Suite{
		Name: "openshift/machine-config-operator/longduration",
		Qualifiers: []string{
			`name.contains("[Suite:openshift/machine-config-operator/longduration]")`,
		},
		ClusterStability: e.ClusterStabilityDisruptive,
		TestTimeout:      &defaultTimeout,
		Description:      "A long-running, resource-intensive test suite executed on a scheduled basis to provide deep validation beyond the standard executions",
	})

	// This suite includes tests for internalreleaseimage resource for NoRegistryClusterInstall feature for agent-based installer
	ext.AddGlobalSuite(e.Suite{
		Name: "openshift/abi/noregistry",
		Qualifiers: []string{
			`name.contains("InternalReleaseImage")`,
		},
		ClusterStability: e.ClusterStabilityDisruptive,
		TestTimeout:      &defaultTimeout,
		Description:      `InternalReleaseImage tests verify NoRegistryCluster installation feature`,
	})

	// If using Ginkgo, build test specs automatically
	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(fmt.Sprintf("couldn't build extension test specs from ginkgo: %+v", err.Error()))
	}

	applyLabelFilters(specs)

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
