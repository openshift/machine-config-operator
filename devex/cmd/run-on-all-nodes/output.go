package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

type output struct {
	RemoteCommand string `json:"remoteCommand"`
	LocalCommand  string `json:"localCommand"`
	node          *corev1.Node
	stdout        *bytes.Buffer
	stderr        *bytes.Buffer
	err           error
}

func (o output) String() string {
	out := &strings.Builder{}
	fmt.Fprintf(out, "[%s - %v]:\n", o.node.Name, getNodeRoles(o.node))
	fmt.Fprintf(out, "$ %s\n", o.RemoteCommand)
	fmt.Fprintln(out, o.stdout.String())
	fmt.Fprintln(out, o.stderr.String())

	if o.err != nil {
		fmt.Fprintln(out, "Full invocation:", o.LocalCommand)
		fmt.Fprintln(out, "Error:", o.err)
	}

	return out.String()
}

func (o output) MarshalJSON() ([]byte, error) {
	type out struct {
		LocalCommand  string   `json:"localCommand"`
		RemoteCommand string   `json:"remoteCommand"`
		NodeRoles     []string `json:"nodeRoles"`
		Node          string   `json:"node"`
		Stdout        string   `json:"stdout"`
		Stderr        string   `json:"stderr"`
		Error         string   `json:"error,omitempty"`
	}

	jsonOut := out{
		LocalCommand:  o.LocalCommand,
		RemoteCommand: o.RemoteCommand,
		Node:          o.node.Name,
		Stdout:        o.stdout.String(),
		Stderr:        o.stderr.String(),
		NodeRoles:     getNodeRoles(o.node),
	}

	if o.err != nil {
		jsonOut.Error = o.err.Error()
	}

	return json.Marshal(jsonOut)
}

func (o output) ToFile() error {
	writeLog := func(node *corev1.Node, streamName string, buf *bytes.Buffer) error {
		logFileName := fmt.Sprintf("%s-%s.log", node.Name, streamName)
		klog.Infof("Writing output to %s", logFileName)
		return os.WriteFile(logFileName, buf.Bytes(), 0o644)
	}

	eg := errgroup.Group{}

	eg.Go(func() error {
		return writeLog(o.node, "stdout", o.stdout)
	})

	eg.Go(func() error {
		return writeLog(o.node, "stderr", o.stderr)
	})

	return eg.Wait()
}

func (o output) ToJSONFile() error {
	outBytes, err := json.Marshal(o)
	if err != nil {
		return err
	}

	filename := fmt.Sprintf("%s-results.json", o.node.Name)
	klog.Infof("Writing output in JSON format to %s", filename)
	return os.WriteFile(filename, outBytes, 0o644)
}
