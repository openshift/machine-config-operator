package main

import (
	"context"

	"k8s.io/klog/v2"
)

// sanitize walks the filesystem starting from inputPath and redacts sensitive data
// from Kubernetes resource files based on the provided configuration.
func sanitize(ctx context.Context, inputPath string, workerCount int, config *Config) error {
	if config == nil || config.Redact == nil {
		klog.Warning("Empty redact list")
		return nil
	}

	redactor, err := NewKubernetesRedactor(config.Redact)
	if err != nil {
		return err
	}

	return NewFsWalker(
		inputPath,
		workerCount,
		NewDefaultStaticFileFilter(),
		NewFileProcessor(redactor),
	).Walk(ctx)
}
