package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/openshift/machine-config-operator/pkg/controller/build"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/version"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	createDigestConfigMapCmd = &cobra.Command{
		Use:   "create-digest-configmap",
		Short: "Create the digest ConfigMap",
		Long:  "",
		RunE:  runCreateDigestConfigMapCmd,
	}

	createOpts struct {
		configMapName string
		digestFile    string
		labels        string
		namespace     string
	}
)

func init() {
	rootCmd.AddCommand(createDigestConfigMapCmd)
	createDigestConfigMapCmd.PersistentFlags().StringVar(&createOpts.configMapName, "configmap-name", "", "The name of the digest ConfigMap to create.")
	createDigestConfigMapCmd.PersistentFlags().StringVar(&createOpts.digestFile, "digestfile", "", "Path to the digest file.")
	createDigestConfigMapCmd.PersistentFlags().StringVar(&createOpts.namespace, "namespace", ctrlcommon.MCONamespace, "The namespace to create the digest ConfigMap in.")
	createDigestConfigMapCmd.PersistentFlags().StringVar(&createOpts.labels, "labels", "", "Labels to apply to the digest ConfigMap.")
}

func runCreateDigestConfigMapCmd(_ *cobra.Command, _ []string) error {
	flag.Set("v", "4")
	flag.Set("logtostderr", "true")
	flag.Parse()

	klog.V(2).Infof("Options parsed: %+v", createOpts)

	// To help debugging, immediately log version
	klog.Infof("Version: %+v (%s)", version.Raw, version.Hash)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cb, err := clients.NewBuilder("")
	if err != nil {
		return err
	}

	opts := build.DigestConfigMapOpts{
		ConfigMapName: createOpts.configMapName,
		Namespace:     createOpts.namespace,
		DigestFile:    createOpts.digestFile,
		Labels:        createOpts.labels,
	}

	if err := build.ApplyDigestConfigMapFromFile(ctx, cb.KubeClientOrDie(""), opts); err != nil {
		return fmt.Errorf("apply configmap: %w", err)
	}

	klog.Infof("Applied digest configmap %q in namespace %q", createOpts.configMapName, createOpts.namespace)
	return nil
}
