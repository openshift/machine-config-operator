package main

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"k8s.io/klog/v2"
)

// FileFilter defines an interface for filtering files during filesystem transversing.
type FileFilter interface {
	// Filter returns true if the file at the given path should be processed,
	// false otherwise.
	Filter(path string) bool
}

// DefaultStaticFileFilter is a file filter that accepts files based on their extension.
// It filters for YAML (.yaml, .yml) and JSON (.json) files.
type DefaultStaticFileFilter struct{}

// NewDefaultStaticFileFilter creates a new instance of DefaultStaticFileFilter.
func NewDefaultStaticFileFilter() *DefaultStaticFileFilter {
	return &DefaultStaticFileFilter{}
}

// Filter returns true if the file has a YAML (.yaml, .yml) or JSON (.json) extension.
func (fp *DefaultStaticFileFilter) Filter(path string) bool {
	return strings.HasSuffix(path, ".yaml") ||
		strings.HasSuffix(path, ".yml") ||
		strings.HasSuffix(path, ".json")
}

// FileProcessor defines an interface for processing individual files.
// Implementations handle the actual work to be performed on each file,
// such as sanitization, validation, or transformation.
type FileProcessor interface {
	// Process performs the required operation on the file at the given path.
	// Returns true if the file was modified, false otherwise.
	// Returns an error if the processing operation fails.
	Process(ctx context.Context, path string) (bool, error)
}

// FsWalker provides concurrent filesystem traversal and processing capabilities.
// It walks through a directory tree, applies filtering criteria, and processes
// matching files using a configurable number of worker goroutines.
type FsWalker struct {
	workerCount   int           // Number of concurrent workers for file processing
	inputPath     string        // Root path to start traversal from
	filter        FileFilter    // Filter to determine which files to process
	fileProcessor FileProcessor // Processor to handle individual files
}

// fsWalkerTaskResult represents the result of processing a single file.
// It contains information about any errors encountered, whether the file
// was modified, and the path of the processed file.
type fsWalkerTaskResult struct {
	error   error  // Error encountered during processing, if any
	changed bool   // Whether the file was modified during processing
	path    string // Path of the processed file
}

// NewFsWalker creates a new filesystem walker with the specified configuration.
// The walker will traverse the inputPath directory tree, apply the given filter
// to determine which files to process, and use the specified number of workers
// to process files concurrently.
func NewFsWalker(inputPath string, workers int, filter FileFilter, processor FileProcessor) *FsWalker {
	return &FsWalker{
		workerCount:   workers,
		inputPath:     inputPath,
		filter:        filter,
		fileProcessor: processor,
	}
}

// Walk traverses the filesystem starting from the configured input path,
// processing files that match the filter criteria using concurrent workers.
// Files are processed concurrently using the configured number of goroutines.
// Returns an error if the processing operation fails.
func (fw *FsWalker) Walk(ctx context.Context) error {
	reportChan := make(chan fsWalkerTaskResult)
	workerChan := make(chan string)
	var wg sync.WaitGroup
	var reportWg sync.WaitGroup

	var shouldExit atomic.Bool
	shouldExit.Store(false)

	// Create the worker routines
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

		if fw.filter.Filter(path) {
			select {
			case workerChan <- path:
				// Successfully sent
			case <-ctx.Done():
				// Context cancelled, exit immediately
				return filepath.SkipAll
			}
		}
		return nil
	})

	// Wait for workers and report routines to exit
	close(workerChan)
	wg.Wait()

	close(reportChan)
	reportWg.Wait()
	return err
}
