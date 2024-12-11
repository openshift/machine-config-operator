package main

import (
	"os"

	"github.com/openshift/machine-config-operator/hack/cmd/onclustertesting/internal/legacycmds"
	"github.com/spf13/cobra"
)

func init() {
	cmds := map[string]func() *cobra.Command{
		"ENABLE_SET_IMAGE_COMMAND":     legacycmds.SetImageCommand,
		"ENABLE_SET_STATUS_COMMAND":    legacycmds.SetStatusCommand,
		"ENABLE_EXTRACT_COMMAND":       legacycmds.ExtractCommand,
		"ENABLE_CLEAR_STATUS_COMMAND":  legacycmds.ClearStatusCommand,
		"ENABLE_MACHINECONFIG_COMMAND": legacycmds.MachineConfigCommand,
	}

	for envVarName, cmd := range cmds {
		if _, ok := os.LookupEnv(envVarName); ok {
			rootCmd.AddCommand(cmd())
		}
	}
}
