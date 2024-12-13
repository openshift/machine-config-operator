package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/cli"
	"k8s.io/klog/v2"

	"golang.org/x/sync/errgroup"

	"github.com/openshift/machine-config-operator/hack/internal/pkg/utils"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/spf13/cobra"
	aggerrs "k8s.io/apimachinery/pkg/util/errors"
)

type runOpts struct {
	command       string
	kubeconfig    string
	labelSelector string
	keepGoing     bool
	writeLogs     bool
	writeJSONLogs bool
	json          bool
	exitZero      bool
}

func main() {
	opts := runOpts{}

	rootCmd := &cobra.Command{
		Use:   "run-on-all-nodes [flags] [command]",
		Short: "Automates running a command on all nodes in a given OpenShift cluster",
		Long:  "",
		RunE: func(_ *cobra.Command, args []string) error {
			if args[0] == "" {
				return fmt.Errorf("no command provided")
			}

			opts.command = args[0]

			return runOnAllNodes(opts)
		},
		Args: cobra.ExactArgs(1),
	}

	rootCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	rootCmd.PersistentFlags().StringVar(&opts.labelSelector, "label-selector", "", "Label selector for nodes.")
	rootCmd.PersistentFlags().BoolVar(&opts.keepGoing, "keep-going", false, "Do not stop on first command error")
	rootCmd.PersistentFlags().BoolVar(&opts.writeLogs, "write-logs", false, "Write command logs to disk under $PWD/<nodename>-{stdout,stderr}.log")
	rootCmd.PersistentFlags().BoolVar(&opts.writeJSONLogs, "write-json-logs", false, "Write logs in JSON format to disk under $PWD/<nodename>-results.json")
	rootCmd.PersistentFlags().BoolVar(&opts.json, "json", false, "Write output in JSON format")
	rootCmd.PersistentFlags().BoolVar(&opts.exitZero, "exit-zero", false, "Return zero even if a command fails")

	os.Exit(cli.Run(rootCmd))
}

func getNodeRoles(node *corev1.Node) []string {
	roles := []string{}

	for label := range node.Labels {
		if strings.Contains(label, "node-role.kubernetes.io") {
			roles = append(roles, label)
		}
	}

	return roles
}

func getNodeNames(nodes *corev1.NodeList) []string {
	names := []string{}

	for _, node := range nodes.Items {
		names = append(names, node.Name)
	}

	return names
}

func runCommand(outChan chan output, node *corev1.Node, opts runOpts) error {
	cmd := exec.Command("oc", "debug", fmt.Sprintf("node/%s", node.Name), "--", "chroot", "/host", "/bin/bash", "-c", opts.command)

	stdout := bytes.NewBuffer([]byte{})
	stderr := bytes.NewBuffer([]byte{})
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = utils.ToEnvVars(map[string]string{
		"KUBECONFIG": opts.kubeconfig,
	})

	runErr := cmd.Run()

	out := output{
		RemoteCommand: opts.command,
		LocalCommand:  cmd.String(),
		node:          node,
		stdout:        stdout,
		stderr:        stderr,
		err:           runErr,
	}

	outChan <- out

	// If we're not supposed to keep going and we encounter an error, stop here.
	if !opts.keepGoing && runErr != nil {
		return fmt.Errorf("could not run command %s, stdout %q, stderr %q: %w", cmd, stdout.String(), stderr.String(), runErr)
	}

	if opts.writeLogs {
		return aggerrs.NewAggregate([]error{out.ToFile(), runErr})
	}

	if opts.writeJSONLogs {
		return aggerrs.NewAggregate([]error{out.ToJSONFile(), runErr})
	}

	return runErr
}

func writeToLogs(out output) error {
	writeLog := func(node *corev1.Node, streamName string, buf *bytes.Buffer) error {
		logFileName := fmt.Sprintf("%s-%s.log", node.Name, streamName)
		klog.Infof("Writing output to %s", logFileName)
		return os.WriteFile(logFileName, buf.Bytes(), 0o644)
	}

	eg := errgroup.Group{}

	eg.Go(func() error {
		return writeLog(out.node, "stdout", out.stdout)
	})

	eg.Go(func() error {
		return writeLog(out.node, "stderr", out.stderr)
	})

	return eg.Wait()
}

func runCommandOnAllNodes(nodes *corev1.NodeList, opts runOpts) error {
	eg := new(errgroup.Group)

	// Spwan a separate error-collection Goroutine so that we can collect all errors
	// received to determine the exit code.
	errChan := make(chan error)

	errs := []error{}
	go func() {
		for err := range errChan {
			errs = append(errs, err)
		}
	}()

	outChan := make(chan output)

	// Spawn a separate logging Goroutine so that outputs are not interweaved.
	go func() {
		for msg := range outChan {
			if !opts.json {
				klog.Info(msg)
				continue
			}

			out, err := json.Marshal(msg)
			if err != nil {
				// Send the error to the error channel for later handling / processing.
				errChan <- err
				klog.Errorf("could not write output from node %s: %s", msg.node.Name, err)
			}

			klog.Info(string(out))
		}
	}()

	for _, node := range nodes.Items {
		node := node
		// For each node, spawn an oc command and run the provided command on the node.
		eg.Go(func() error {
			err := runCommand(outChan, &node, opts)
			// If we should keep going, collect the error via the error channel for
			// future processing.
			if opts.keepGoing {
				errChan <- err
				return nil
			}

			// If we should not keep going, return the error value directly. This
			// will stop all of the other goroutines in this errorgroup.
			return err
		})
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	close(outChan)
	close(errChan)

	return aggerrs.NewAggregate(errs)
}

func runOnAllNodes(opts runOpts) error {
	if err := utils.CheckForBinaries([]string{"oc"}); err != nil {
		return err
	}

	if opts.writeLogs && opts.writeJSONLogs {
		return fmt.Errorf("--write-logs and --write-json-logs cannot be combined")
	}

	cs := framework.NewClientSet("")

	kubeconfig, err := cs.GetKubeconfig()
	if err != nil {
		return err
	}

	opts.kubeconfig = kubeconfig

	listOpts := metav1.ListOptions{}

	if opts.labelSelector != "" {
		listOpts.LabelSelector = opts.labelSelector
		klog.Info("Using label selector:", opts.labelSelector)
	}

	nodes, err := cs.CoreV1Interface.Nodes().List(context.TODO(), listOpts)
	if err != nil {
		return err
	}

	klog.Info("Running on nodes:", getNodeNames(nodes))
	klog.Info("")

	err = runCommandOnAllNodes(nodes, opts)
	if opts.exitZero {
		klog.Info("--exit-zero set, will return zero even though the following error(s) occurred")
		klog.Error(err)
		return nil
	}

	return err
}
