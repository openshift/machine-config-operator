package main

import (
	"context"
	"k8s.io/klog/v2"
)

func sanitize(ctx context.Context, inputPath string, workerCount int) error {
	config, err := NewConfigFromEnv()
	if err != nil {
		return err
	}
	if config == nil {
		config, err = BuildDefaultConfig()
	}
	if err != nil {
		return err
	}
	
	if config.Redact == nil {
		klog.Warning("Empty redact list")
		return nil
	}

	redactor, err := NewKubernetesRedactor(config.Redact)
	if err != nil {
		return err
	}
	processor := newFileProcessor(redactor)
	walker := newFsWalker(inputPath, workerCount, processor, processor)
	return walker.Walk(ctx)
}
