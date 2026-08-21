package extended

import (
	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

// OCBTestEnv manages common OCB test resources (MCP + MOSC) lifecycle.
// Use NewOCBTestEnvWithCustomMCP or NewOCBTestEnvWithCompactPool to create one,
// then defer env.Cleanup() or call env.ValidateAndCleanup().
type OCBTestEnv struct {
	OC          *exutil.CLI
	MCP         *MachineConfigPool
	MOSC        *MachineOSConfig
	isCustomMCP bool
}

type ocbSetupConfig struct {
	numWorkers int
	moscOpts   []MOSCCreateOption
	skipMOSC   bool
}

// OCBSetupOption configures optional behavior for OCBTestEnv setup.
type OCBSetupOption func(*ocbSetupConfig)

// WithOCBWorkers sets the number of worker nodes to add to the custom MCP.
func WithOCBWorkers(n int) OCBSetupOption {
	return func(c *ocbSetupConfig) { c.numWorkers = n }
}

// WithOCBMOSCOptions passes MOSCCreateOption values through to CreateMOSC.
func WithOCBMOSCOptions(opts ...MOSCCreateOption) OCBSetupOption {
	return func(c *ocbSetupConfig) { c.moscOpts = append(c.moscOpts, opts...) }
}

// WithOCBSkipMOSC skips creating a MOSC, only creates the MCP.
func WithOCBSkipMOSC() OCBSetupOption {
	return func(c *ocbSetupConfig) { c.skipMOSC = true }
}

func applyOCBOptions(opts []OCBSetupOption) ocbSetupConfig {
	cfg := ocbSetupConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// NewOCBTestEnvWithCustomMCP creates a custom MCP and MOSC.
// The caller must defer env.Cleanup() to ensure proper teardown.
func NewOCBTestEnvWithCustomMCP(oc *exutil.CLI, mcpName string, opts ...OCBSetupOption) *OCBTestEnv {
	cfg := applyOCBOptions(opts)

	exutil.By("Create custom " + mcpName + " MCP")
	mcp, err := CreateCustomMCP(oc.AsAdmin(), mcpName, cfg.numWorkers)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating a new custom pool: %s", mcpName)
	logger.Infof("OK!\n")

	env := &OCBTestEnv{
		OC:          oc,
		MCP:         mcp,
		isCustomMCP: true,
	}

	if cfg.skipMOSC {
		return env
	}

	exutil.By("Configure OCB functionality for the " + mcpName + " MCP")
	mosc, err := CreateMOSC(oc.AsAdmin(), mcpName, mcpName, cfg.moscOpts...)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
	logger.Infof("OK!\n")

	env.MOSC = mosc
	return env
}

// NewOCBTestEnvWithCompactPool uses the compact-compatible pool and creates a MOSC.
// The caller must defer env.Cleanup() to ensure proper teardown.
func NewOCBTestEnvWithCompactPool(oc *exutil.CLI, opts ...OCBSetupOption) *OCBTestEnv {
	cfg := applyOCBOptions(opts)
	mcp := GetCompactCompatiblePool(oc.AsAdmin())

	env := &OCBTestEnv{
		OC:  oc,
		MCP: mcp,
	}

	if cfg.skipMOSC {
		return env
	}

	exutil.By("Configure OCB functionality for the " + mcp.GetName() + " MCP")
	mosc, err := CreateMOSC(oc.AsAdmin(), mcp.GetName(), mcp.GetName(), cfg.moscOpts...)
	o.Expect(err).NotTo(o.HaveOccurred(), "Error creating the MachineOSConfig resource")
	logger.Infof("OK!\n")

	env.MOSC = mosc
	return env
}

// Cleanup tears down the MOSC (via DisableOCL) and deletes the custom MCP if applicable.
func (env *OCBTestEnv) Cleanup() {
	if env.MOSC != nil {
		DisableOCL(env.MOSC)
	}
	if env.isCustomMCP && env.MCP != nil {
		env.MCP.delete()
	}
}

// CleanupMOSCOnly removes only the MOSC without deleting the custom MCP.
func (env *OCBTestEnv) CleanupMOSCOnly() {
	if env.MOSC != nil {
		DisableOCL(env.MOSC)
	}
}

// CleanupMCPOnly deletes the custom MCP.
func (env *OCBTestEnv) CleanupMCPOnly() {
	if env.isCustomMCP && env.MCP != nil {
		env.MCP.delete()
	}
}

// ValidateAndCleanup validates the MOSC, cleans it up, and checks garbage collection.
func (env *OCBTestEnv) ValidateAndCleanup(checkers []Checker) {
	ValidateSuccessfulMOSC(env.MOSC, checkers)

	exutil.By("Remove the MachineOSConfig resource")
	o.Expect(env.MOSC.CleanupAndDelete()).To(o.Succeed(), "Error cleaning up %s", env.MOSC)
	logger.Infof("OK!\n")

	ValidateMOSCIsGarbageCollected(env.MOSC, env.MCP)

	exutil.AssertAllPodsToBeReady(env.OC.AsAdmin(), MachineConfigNamespace)
	logger.Infof("OK!\n")
}
