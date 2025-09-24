package main

import (
	"context"

	"k8s.io/klog/v2"
)

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
