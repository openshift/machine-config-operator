package main

import (
	"context"
	"io/fs"
	"k8s.io/klog/v2"
	"path/filepath"
	"sync"
	"sync/atomic"
)

type FileFilter interface {
	Filter(path string) (bool, error)
}

type FileProcessor interface {
	Process(ctx context.Context, path string) (bool, error)
}

type fsWalker struct {
	workerCount   int
	inputPath     string
	filter        FileFilter
	fileProcessor FileProcessor
}

type fsWalkerTaskResult struct {
	error   error
	changed bool
	path    string
}

func newFsWalker(inputPath string, workers int, filter FileFilter, processor FileProcessor) *fsWalker {
	return &fsWalker{
		workerCount:   workers,
		inputPath:     inputPath,
		filter:        filter,
		fileProcessor: processor,
	}
}

func (fw *fsWalker) Walk(ctx context.Context) error {
	reportChan := make(chan fsWalkerTaskResult)
	workerChan := make(chan string)
	var wg sync.WaitGroup
	var reportWg sync.WaitGroup

	var shouldExit atomic.Bool
	shouldExit.Store(false)

	for range fw.workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case path, ok := <-workerChan:
					if !ok {
						return
					}
					changed, err := fw.fileProcessor.Process(ctx, path)
					if err != nil || changed {
						// Ignore tasks without errors or unchanged
						reportChan <- fsWalkerTaskResult{
							error:   err,
							changed: changed,
							path:    path,
						}
					}
				case <-ctx.Done():
					shouldExit.Store(true)
					return
				}
			}

		}()
	}

	// Create the reporting routine
	reportWg.Add(1)
	go func() {
		defer reportWg.Done()
		for report := range reportChan {
			if report.error != nil {
				klog.Errorf("error while processing file %q: %v", report.path, report.error)
			} else if report.changed && report.path != "" {
				path, err := filepath.Rel(fw.inputPath, report.path)
				if err != nil {
					path = report.path
				}
				klog.Infof("file %s redacted", path)
			}
		}
	}()
	err := filepath.WalkDir(fw.inputPath, func(path string, dirEntry fs.DirEntry, err error) error {
		if shouldExit.Load() {
			return filepath.SkipAll
		}
		if err != nil {
			return err
		}
		if dirEntry.IsDir() {
			return nil
		}

		shouldRedact, err := fw.filter.Filter(path)
		if err != nil {
			return err
		}

		if shouldRedact {
			workerChan <- path
		}
		return nil
	})

	close(workerChan)
	wg.Wait()

	close(reportChan)
	reportWg.Wait()
	return err
}
