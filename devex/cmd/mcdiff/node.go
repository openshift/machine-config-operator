package main

import (
	"context"
	"fmt"
	"io"
	"os"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/mustgather"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/node"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/report"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/scanner"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
)

type nodeOptions struct {
	pool        string
	showDiffs   bool
	output      string
	mustGather  string
	configFlags *genericclioptions.ConfigFlags
	// getter, if set, is used instead of building a kube client. Tests inject this.
	getter cluster.Getter
	// nodeReader, if set, is used instead of a live MCD exec reader. Tests inject this.
	nodeReader node.Reader
	// nodeGetter, if set, is used instead of a live Node Get. Tests inject this.
	nodeGetter node.NodeGetter
	out        io.Writer
}

func newNodeCommand() *cobra.Command {
	o := &nodeOptions{
		configFlags: genericclioptions.NewConfigFlags(true),
		out:         os.Stdout,
	}
	cmd := &cobra.Command{
		Use:   "node NODE",
		Short: "Scan every file in a node's rendered MachineConfig against the host filesystem",
		Long: `Scan all Ignition files managed by a node's rendered MachineConfig.

This answers which files drifted when a node is degraded and the mismatched
path is unknown. Each managed path is compared against the on-disk copy on the
node (via the machine-config-daemon pod host rootfs, equivalent to
oc debug node/<node> -- cat /host/<path>).

The node's MachineConfigPool is detected from node labels the same way the
Machine Config Operator assigns pools. Pass --pool when the node is unassigned
or matches more than one custom pool.

With --must-gather, read MachineConfigs and optional node snapshots from an
unpacked oc adm must-gather directory instead of a live cluster. No kubeconfig
is required. Standard must-gather archives do not snapshot the entire host
/etc tree; files without a snapshot are reported as missing.

A missing host file is reported as MISSING ON NODE and does not fail the scan
(the same "could not stat file" case from MachineConfigDaemon degraded events).
Mode drift is reported alongside content and size deltas.

By default the report is a summary with size deltas. Pass --show-diffs to
include a unified diff for every mismatched file.

Exit 0 means the scan completed, including CLEAN, DRIFT DETECTED, MISSING ON NODE,
and unreadable files. Non-zero means the tool could not resolve the pool, could
not read the rendered MachineConfig, or could not reach the node.`,
		Example: `  # Scan a live node (pool detected from node labels)
  mcdiff node worker-0

  # Override pool detection
  mcdiff node worker-0 --pool worker

  # Include unified diffs for mismatched files
  mcdiff node worker-0 --show-diffs

  # Offline whole-node scan from a must-gather
  mcdiff node worker-0 --must-gather ./must-gather.local --pool worker

  # JSON summary
  mcdiff node worker-0 -o json`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.out == nil || o.out == os.Stdout {
				o.out = cmd.OutOrStdout()
			}
			return o.run(cmd.Context(), args[0])
		},
	}
	cmd.Flags().StringVar(&o.pool, "pool", "", "MachineConfigPool name (detected from node labels when omitted)")
	cmd.Flags().BoolVar(&o.showDiffs, "show-diffs", false, "Include unified diffs for every mismatched file")
	cmd.Flags().StringVar(&o.mustGather, "must-gather", "", "Unpacked must-gather directory (offline; skips kubeconfig)")
	cmd.Flags().StringVarP(&o.output, "output", "o", "text", "Output format: text or json")
	_ = cmd.MarkFlagDirname("must-gather")
	o.configFlags.AddFlags(cmd.Flags())
	return cmd
}

func (o *nodeOptions) run(ctx context.Context, nodeName string) error {
	g := o.getter
	nr := o.nodeReader
	ng := o.nodeGetter
	if o.mustGather != "" {
		mg, err := mustgather.Open(o.mustGather)
		if err != nil {
			return err
		}
		if g == nil {
			g = mg.Getter()
		}
		if nr == nil {
			nr = mg.NodeReader()
		}
		if ng == nil {
			ng = mg
		}
	} else if g == nil || nr == nil || (o.pool == "" && ng == nil) {
		clients, err := liveClientsFromFlags(o.configFlags)
		if err != nil {
			return err
		}
		if g == nil {
			g = clients.getter
		}
		if nr == nil {
			nr = clients.reader
		}
		if ng == nil {
			ng = clients.nodes
		}
	}
	return runNode(ctx, g, ng, nr, nodeScanArgs{
		node:       nodeName,
		pool:       o.pool,
		output:     o.output,
		showDiffs:  o.showDiffs,
		mustGather: o.mustGather,
	}, o.out)
}

type liveClients struct {
	getter cluster.Getter
	reader node.Reader
	nodes  node.NodeGetter
}

func liveClientsFromFlags(flags *genericclioptions.ConfigFlags) (*liveClients, error) {
	restConfig, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	mcfg, err := mcfgclientset.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create machineconfiguration client: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return &liveClients{
		getter: cluster.NewKubeGetter(mcfg),
		reader: node.NewKubeReader(kube, restConfig),
		nodes:  node.NewKubeNodeGetter(kube),
	}, nil
}

type nodeScanArgs struct {
	node       string
	pool       string
	output     string
	showDiffs  bool
	mustGather string
}

func runNode(ctx context.Context, g cluster.Getter, nodes node.NodeGetter, reader node.Reader, args nodeScanArgs, w io.Writer) error {
	result, err := scanner.Scan(ctx, g, nodes, reader, args.node, scanner.Options{Pool: args.pool})
	if err != nil {
		return err
	}
	return report.WriteScan(w, result, report.ScanOptions{
		Format:     args.output,
		ShowDiffs:  args.showDiffs,
		MustGather: args.mustGather,
	})
}

func init() {
	rootCmd.AddCommand(newNodeCommand())
}
