package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchive_Success(t *testing.T) {
	// Create a temporary directory with test files
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))

	// Create a temporary output file
	outputFile := filepath.Join(t.TempDir(), "output.tar.gz.gpg")

	// Test the Archive function
	err := Archive(tempDir, outputFile)

	assert.NoError(t, err)
	assert.FileExists(t, outputFile)

	// Verify the output file is not empty
	stat, err := os.Stat(outputFile)
	require.NoError(t, err)
	assert.Greater(t, stat.Size(), int64(0))

	// Verify the file is GPG encrypted by checking for GPG binary markers
	fileContent, err := os.ReadFile(outputFile)
	require.NoError(t, err)

	// GPG encrypted files start with packet type markers (high bit set, indicating packet type)
	assert.True(t, len(fileContent) > 0 && fileContent[0] >= 0x80,
		"File should be GPG encrypted (expected GPG packet marker >= 0x80, got 0x%02x)", fileContent[0])
}

func TestArchive_NonExistentSource(t *testing.T) {
	nonExistentDir := "/non/existent/directory"
	outputFile := filepath.Join(t.TempDir(), "output.tar.gz.gpg")

	err := Archive(nonExistentDir, outputFile)

	assert.Error(t, err)
}

func TestArchive_InvalidTarget(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))

	// Try to write to a directory that doesn't exist
	invalidTarget := "/non/existent/directory/output.tar.gz.gpg"

	err := Archive(tempDir, invalidTarget)

	assert.Error(t, err)
}

func TestArchive_EmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()
	outputFile := filepath.Join(t.TempDir(), "output.tar.gz.gpg")

	err := Archive(tempDir, outputFile)

	assert.NoError(t, err)
	assert.FileExists(t, outputFile)
}
