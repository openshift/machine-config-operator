package main

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

var (
	rootCmd = &cobra.Command{
		Use:   "mcdiff",
		Short: "Explains MachineConfig files: expected content, last writer, and diffs",
		Long: `mcdiff inspects files managed by the Machine Config Operator.

The file subcommand answers what the rendered MachineConfig says a path should
contain, which MachineConfig last wrote it, and optionally how that differs
from a local file, a live node, or a must-gather archive.

The node subcommand scans every file in a node's rendered MachineConfig against
the host filesystem, for the case where a node is degraded and the drifted
path is unknown.

The diff subcommand diffs two MachineConfig objects with dyff.

Shell completions:
  source <(mcdiff completion bash)
  source <(mcdiff completion zsh)
  mcdiff completion fish | source`,
	}
)

func init() {
	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
}

func main() {
	rootCmd.InitDefaultCompletionCmd()
	os.Exit(cli.Run(rootCmd))
}
