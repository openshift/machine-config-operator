package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/cluster"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/diff"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/mustgather"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/node"
	"github.com/openshift/machine-config-operator/devex/cmd/mcdiff/internal/report"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
)

type fileOptions struct {
	pool        string
	showContent bool
	output      string
	fromFile    string
	node        string
	mustGather  string
	configFlags *genericclioptions.ConfigFlags
	// getter, if set, is used instead of building a kube client. Tests inject this.
	getter cluster.Getter
	// nodeReader, if set, is used instead of a live MCD exec reader. Tests inject this.
	nodeReader node.Reader
	out        io.Writer
}

func newFileCommand() *cobra.Command {
	o := &fileOptions{
		configFlags: genericclioptions.NewConfigFlags(true),
		out:         os.Stdout,
	}
	cmd := &cobra.Command{
		Use:   "file PATH",
		Short: "Show the rendered MachineConfig for a file path in a pool",
		Long: `Inspect a file path against the rendered MachineConfig for a MachineConfigPool.

Without --from-file or --node, this reports what the pool's rendered MachineConfig
says the file should contain, and which source MachineConfig last wrote it.
Ignition data URLs are decoded automatically (base64 and percent-encoded), so you
do not need the jq / urllib / base64 steps from KCS articles.

With --from-file, compare a local file against that expected content.
With --node, compare the on-disk file on a live node (via the machine-config-daemon
pod host rootfs, equivalent to oc debug node/<node> -- cat /host/<path>).
A missing host file is reported as MISSING ON NODE (exit 0). Mode drift
(0644 vs 0755) is reported in addition to content and size deltas.
With --must-gather, read MachineConfigs (and optional node snapshots) from an
unpacked oc adm must-gather directory instead of a live cluster. No kubeconfig
is required.

Common MachineConfigDaemon degraded cases this replaces a debug-node walkthrough
for: /etc/chrony.conf, /etc/resolv.conf, /usr/local/bin/configure-ovs.sh,
and /etc/kubernetes/kubelet-ca.crt (including could-not-stat missing files).

--from-file cannot be combined with --node or --must-gather.
--must-gather and --node may be combined to diff against host files in the archive.

Exit 0 means the inspection succeeded, including MATCH, CONTENT MISMATCH,
MODE MISMATCH, unmanaged paths, and files MISSING ON NODE. Non-zero means the
tool could not inspect the pool, could not read inputs, or could not reach the node.

Expected file contents are omitted unless --show-content is set. Unified diffs
are printed because they are the comparison result.`,
		Example: `  # Inspect expected content and last writer
  mcdiff file /etc/ssh/sshd_config --pool worker

  # Compare against the file on a live node (replaces oc debug + jq/base64/urldecode)
  mcdiff file /etc/chrony.conf --pool worker --node worker-0

  # Compare against a local copy
  mcdiff file /etc/ssh/sshd_config --pool worker --from-file ./sshd_config

  # Offline analysis from a must-gather
  mcdiff file /etc/ssh/sshd_config --pool worker --must-gather ./must-gather.local

  # Offline node diff from a must-gather snapshot
  mcdiff file /etc/kubernetes/kubelet-ca.crt --pool master --node master-0 --must-gather ./must-gather.local

  # JSON output
  mcdiff file /etc/resolv.conf --pool worker -o json`,
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
	cmd.Flags().StringVar(&o.pool, "pool", "", "MachineConfigPool name (required)")
	_ = cmd.MarkFlagRequired("pool")
	cmd.Flags().BoolVar(&o.showContent, "show-content", false, "Print expected file contents from the rendered MachineConfig")
	cmd.Flags().StringVar(&o.fromFile, "from-file", "", "Compare a local file against the rendered MachineConfig")
	cmd.Flags().StringVar(&o.node, "node", "", "Compare the on-disk file on a live node (or must-gather snapshot) against the rendered MachineConfig")
	cmd.Flags().StringVar(&o.mustGather, "must-gather", "", "Unpacked must-gather directory (offline; skips kubeconfig)")
	cmd.Flags().StringVarP(&o.output, "output", "o", "text", "Output format: text or json")
	_ = cmd.MarkFlagDirname("must-gather")
	_ = cmd.MarkFlagFilename("from-file")
	o.configFlags.AddFlags(cmd.Flags())
	return cmd
}

func validateFileFlags(nodeName, fromFile, mustGather string) error {
	if nodeName != "" && fromFile != "" {
		return fmt.Errorf("cannot use --from-file and --node together")
	}
	if mustGather != "" && fromFile != "" {
		return fmt.Errorf("cannot use --must-gather and --from-file together")
	}
	return nil
}

func (o *fileOptions) run(ctx context.Context, path string) error {
	if err := validateFileFlags(o.node, o.fromFile, o.mustGather); err != nil {
		return err
	}

	g := o.getter
	nr := o.nodeReader
	if o.mustGather != "" {
		mg, err := mustgather.Open(o.mustGather)
		if err != nil {
			return err
		}
		if g == nil {
			g = mg.Getter()
		}
		if o.node != "" && nr == nil {
			nr = mg.NodeReader()
		}
	} else {
		if g == nil {
			var err error
			g, err = getterFromFlags(o.configFlags)
			if err != nil {
				return err
			}
		}
		if o.node != "" && nr == nil {
			var err error
			nr, err = nodeReaderFromFlags(o.configFlags)
			if err != nil {
				return err
			}
		}
	}
	return runFile(ctx, g, inspectArgs{
		path:        path,
		pool:        o.pool,
		output:      o.output,
		showContent: o.showContent,
		fromFile:    o.fromFile,
		node:        o.node,
		nodeReader:  nr,
		mustGather:  o.mustGather,
	}, o.out)
}

func getterFromFlags(flags *genericclioptions.ConfigFlags) (cluster.Getter, error) {
	restConfig, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	client, err := mcfgclientset.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create machineconfiguration client: %w", err)
	}
	return cluster.NewKubeGetter(client), nil
}

func nodeReaderFromFlags(flags *genericclioptions.ConfigFlags) (node.Reader, error) {
	restConfig, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	kube, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	return node.NewKubeReader(kube, restConfig), nil
}

type inspectArgs struct {
	path        string
	pool        string
	output      string
	showContent bool
	fromFile    string
	node        string
	nodeReader  node.Reader
	mustGather  string
}

func runFile(ctx context.Context, g cluster.Getter, args inspectArgs, w io.Writer) error {
	if err := validateFileFlags(args.node, args.fromFile, args.mustGather); err != nil {
		return err
	}

	pf, err := cluster.LoadPoolFile(ctx, g, args.pool, args.path)
	if err != nil {
		return err
	}

	opts := report.Options{
		ShowContent: args.showContent,
		Format:      args.output,
		MustGather:  args.mustGather,
	}
	switch {
	case args.fromFile != "":
		actual, err := os.ReadFile(args.fromFile)
		if err != nil {
			return fmt.Errorf("failed to read --from-file %q: %w", args.fromFile, err)
		}
		opts.FromFile = args.fromFile
		opts.Actual = actual
		if pf.Found {
			cmp := diff.Compare(pf.Expected, actual, args.path, args.fromFile)
			opts.Diff = &cmp
		}
	case args.node != "":
		if args.nodeReader == nil {
			return fmt.Errorf("node reader is not configured")
		}
		actual, actualMode, err := args.nodeReader.ReadFile(ctx, args.node, args.path)
		opts.Node = args.node
		if err != nil {
			if errors.Is(err, node.ErrFileNotFound) {
				opts.ActualMissing = true
				break
			}
			return fmt.Errorf("failed to read %q from node %q: %w", args.path, args.node, err)
		}
		opts.Actual = actual
		if pf.Found {
			cmp := diff.WithModes(diff.Compare(pf.Expected, actual, args.path, "node:"+args.node), pf.Mode, actualMode)
			opts.Diff = &cmp
		}
	}
	return report.Write(w, pf, opts)
}

func init() {
	rootCmd.AddCommand(newFileCommand())
}
