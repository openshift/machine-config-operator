// Assisted-by: Claude
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Mock implementations for testing
type mockFileFilter struct {
	filterFunc func(path string) bool
}

func (m *mockFileFilter) Filter(path string) bool {
	if m.filterFunc != nil {
		return m.filterFunc(path)
	}
	return true
}

type mockFileProcessor struct {
	processFunc    func(ctx context.Context, path string) (bool, error)
	processedFiles []string
	mu             sync.Mutex
}

func (m *mockFileProcessor) Process(ctx context.Context, path string) (bool, error) {
	m.mu.Lock()
	m.processedFiles = append(m.processedFiles, path)
	m.mu.Unlock()

	if m.processFunc != nil {
		return m.processFunc(ctx, path)
	}
	return false, nil
}

func (m *mockFileProcessor) getProcessedFiles() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.processedFiles))
	copy(result, m.processedFiles)
	return result
}

func TestDefaultStaticFileFilter_Filter(t *testing.T) {
	filter := NewDefaultStaticFileFilter()

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"YAML file", "/path/to/file.yaml", true},
		{"YML file", "/path/to/file.yml", true},
		{"JSON file", "/path/to/file.json", true},
		{"Go file", "/path/to/file.go", false},
		{"Text file", "/path/to/file.txt", false},
		{"No extension", "/path/to/file", false},
		{"Hidden YAML", "/path/to/.hidden.yaml", true},
		{"Nested YAML", "/very/deep/path/config.yml", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filter.Filter(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNewFsWalker(t *testing.T) {
	filter := &mockFileFilter{}
	processor := &mockFileProcessor{}

	walker := NewFsWalker("/test/path", 5, filter, processor)

	assert.NotNil(t, walker)
	assert.Equal(t, 5, walker.workerCount)
	assert.Equal(t, "/test/path", walker.inputPath)
	assert.Equal(t, filter, walker.filter)
	assert.Equal(t, processor, walker.fileProcessor)
}

func TestFsWalker_Walk_EmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()

	filter := &mockFileFilter{}
	processor := &mockFileProcessor{}
	walker := NewFsWalker(tempDir, 2, filter, processor)

	ctx := context.Background()
	err := walker.Walk(ctx)

	assert.NoError(t, err)
	assert.Empty(t, processor.getProcessedFiles())
}

func TestFsWalker_Walk_SingleFile(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.txt")
	err := os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	filter := &mockFileFilter{}
	processor := &mockFileProcessor{}
	walker := NewFsWalker(tempDir, 1, filter, processor)

	ctx := context.Background()
	err = walker.Walk(ctx)

	assert.NoError(t, err)
	processedFiles := processor.getProcessedFiles()
	assert.Len(t, processedFiles, 1)
	assert.Equal(t, testFile, processedFiles[0])
}

func TestFsWalker_Walk_MultipleFiles(t *testing.T) {
	tempDir := t.TempDir()

	files := []string{"file1.txt", "file2.yaml", "file3.json"}
	for _, filename := range files {
		testFile := filepath.Join(tempDir, filename)
		err := os.WriteFile(testFile, []byte("test content"), 0644)
		require.NoError(t, err)
	}

	filter := &mockFileFilter{}
	processor := &mockFileProcessor{}
	walker := NewFsWalker(tempDir, 2, filter, processor)

	ctx := context.Background()
	err := walker.Walk(ctx)

	assert.NoError(t, err)
	processedFiles := processor.getProcessedFiles()
	assert.Len(t, processedFiles, 3)

	// Check all files were processed
	processedMap := make(map[string]bool)
	for _, file := range processedFiles {
		processedMap[filepath.Base(file)] = true
	}
	for _, filename := range files {
		assert.True(t, processedMap[filename], "File %s should have been processed", filename)
	}
}

func TestFsWalker_Walk_WithSubdirectories(t *testing.T) {
	tempDir := t.TempDir()

	// Create subdirectory
	subDir := filepath.Join(tempDir, "subdir")
	err := os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	// Create files in root and subdirectory
	rootFile := filepath.Join(tempDir, "root.txt")
	subFile := filepath.Join(subDir, "sub.txt")

	err = os.WriteFile(rootFile, []byte("root content"), 0644)
	require.NoError(t, err)
	err = os.WriteFile(subFile, []byte("sub content"), 0644)
	require.NoError(t, err)

	filter := &mockFileFilter{}
	processor := &mockFileProcessor{}
	walker := NewFsWalker(tempDir, 1, filter, processor)

	ctx := context.Background()
	err = walker.Walk(ctx)

	assert.NoError(t, err)
	processedFiles := processor.getProcessedFiles()
	assert.Len(t, processedFiles, 2)

	processedPaths := make(map[string]bool)
	for _, file := range processedFiles {
		processedPaths[file] = true
	}
	assert.True(t, processedPaths[rootFile])
	assert.True(t, processedPaths[subFile])
}

func TestFsWalker_Walk_FilterFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Create different file types
	yamlFile := filepath.Join(tempDir, "config.yaml")
	txtFile := filepath.Join(tempDir, "readme.txt")
	jsonFile := filepath.Join(tempDir, "data.json")

	for _, file := range []string{yamlFile, txtFile, jsonFile} {
		err := os.WriteFile(file, []byte("content"), 0644)
		require.NoError(t, err)
	}

	// Filter only YAML files
	filter := &mockFileFilter{
		filterFunc: func(path string) bool {
			return filepath.Ext(path) == ".yaml"
		},
	}
	processor := &mockFileProcessor{}
	walker := NewFsWalker(tempDir, 1, filter, processor)

	ctx := context.Background()
	err := walker.Walk(ctx)

	assert.NoError(t, err)
	processedFiles := processor.getProcessedFiles()
	assert.Len(t, processedFiles, 1)
	assert.Equal(t, yamlFile, processedFiles[0])
}

func TestFsWalker_Walk_ProcessorError(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.txt")
	err := os.WriteFile(testFile, []byte("content"), 0644)
	require.NoError(t, err)

	filter := &mockFileFilter{}
	processor := &mockFileProcessor{
		processFunc: func(ctx context.Context, path string) (bool, error) {
			return false, errors.New("processor error")
		},
	}
	walker := NewFsWalker(tempDir, 1, filter, processor)

	ctx := context.Background()
	err = walker.Walk(ctx)

	// The walker should not return processor errors, they should be logged
	assert.NoError(t, err)
	processedFiles := processor.getProcessedFiles()
	assert.Len(t, processedFiles, 1)
}

func TestFsWalker_Walk_ContextCancellation(t *testing.T) {
	tempDir := t.TempDir()

	// Create several files
	for i := 0; i < 5; i++ {
		testFile := filepath.Join(tempDir, "test"+string(rune('0'+i))+".txt")
		err := os.WriteFile(testFile, []byte("content"), 0644)
		require.NoError(t, err)
	}

	var processStarted atomic.Bool
	filter := &mockFileFilter{}
	processor := &mockFileProcessor{
		processFunc: func(ctx context.Context, path string) (bool, error) {
			processStarted.Store(true)
			// Simulate slow processing
			time.Sleep(100 * time.Millisecond)
			return false, nil
		},
	}
	walker := NewFsWalker(tempDir, 1, filter, processor)

	ctx, cancel := context.WithCancel(context.Background())

	// Start walking in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- walker.Walk(ctx)
	}()

	// Wait for processing to start then cancel
	for !processStarted.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	// Wait for completion with timeout
	select {
	case err := <-errChan:
		// Should complete without error (cancellation is handled gracefully)
		assert.NoError(t, err)
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out - walker didn't respond to cancellation within 1 second")
	}
}

func TestFsWalker_Walk_MultipleWorkers(t *testing.T) {
	tempDir := t.TempDir()

	fileCount := 10
	for i := 0; i < fileCount; i++ {
		testFile := filepath.Join(tempDir, "file"+string(rune('0'+i))+".txt")
		err := os.WriteFile(testFile, []byte("content"), 0644)
		require.NoError(t, err)
	}

	filter := &mockFileFilter{}
	processor := &mockFileProcessor{}
	walker := NewFsWalker(tempDir, 3, filter, processor) // 3 workers

	ctx := context.Background()
	err := walker.Walk(ctx)

	assert.NoError(t, err)
	processedFiles := processor.getProcessedFiles()
	assert.Len(t, processedFiles, fileCount)
}

func TestFsWalker_Walk_ProcessorReturnsChanged(t *testing.T) {
	tempDir := t.TempDir()

	testFile := filepath.Join(tempDir, "test.txt")
	err := os.WriteFile(testFile, []byte("content"), 0644)
	require.NoError(t, err)

	filter := &mockFileFilter{}
	processor := &mockFileProcessor{
		processFunc: func(ctx context.Context, path string) (bool, error) {
			return true, nil // Return changed=true
		},
	}
	walker := NewFsWalker(tempDir, 1, filter, processor)

	ctx := context.Background()
	err = walker.Walk(ctx)

	assert.NoError(t, err)
	processedFiles := processor.getProcessedFiles()
	assert.Len(t, processedFiles, 1)
}

func TestFsWalker_Walk_NonexistentDirectory(t *testing.T) {
	filter := &mockFileFilter{}
	processor := &mockFileProcessor{}
	walker := NewFsWalker("/nonexistent/path", 1, filter, processor)

	ctx := context.Background()
	err := walker.Walk(ctx)

	assert.Error(t, err)
}
