package helpers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArtifactArchive(t *testing.T) {
	artifactDir := t.TempDir()
	t.Setenv(artifactDirEnvVar, artifactDir)

	archiveName := "test-archive.tar.gz"

	archive, err := NewArtifactArchive(t, archiveName)
	assert.NoError(t, err)

	contents := []byte("hello world")

	for i := 1; i <= 10; i++ {
		filename := fmt.Sprintf("file-%d.txt", i)
		require.NoError(t, os.WriteFile(filepath.Join(archive.StagingDir(), filename), contents, 0o755))
	}

	assert.NoError(t, archive.WriteArchive())
	assert.FileExists(t, filepath.Join(artifactDir, archiveName))

	extractDir := t.TempDir()
	cmd := exec.Command("tar", "-xzvf", filepath.Join(artifactDir, archiveName), "-C", extractDir)
	require.NoError(t, cmd.Run())

	extractDir = filepath.Join(extractDir, archive.ArchiveRoot())

	for i := 1; i <= 10; i++ {
		filename := fmt.Sprintf("file-%d.txt", i)
		assert.FileExists(t, filepath.Join(extractDir, filename))
	}
}

func TestGetBuildArtifactDir(t *testing.T) {
	assertFromEnvVar := func() {
		val := os.Getenv(artifactDirEnvVar)
		result, err := GetBuildArtifactDir(t)
		require.NoError(t, err)
		assert.Equal(t, val, result)
	}

	assertFromCwd := func() {
		cwd, err := os.Getwd()
		require.NoError(t, err)

		result, err := GetBuildArtifactDir(t)
		require.NoError(t, err)
		assert.Equal(t, cwd, result)
	}

	val, ok := os.LookupEnv(artifactDirEnvVar)
	if ok && val != "" {
		assertFromEnvVar()
		os.Unsetenv(artifactDirEnvVar)
	}

	assertFromCwd()
	t.Setenv(artifactDirEnvVar, "/artifacts")
	assertFromEnvVar()
	t.Setenv(artifactDirEnvVar, "")
	assertFromCwd()
}

func TestSanitizeTestName(t *testing.T) {
	tests := []struct {
		name     string
		testName string
		expected string
	}{
		{
			name:     "Simple test name",
			testName: "TestSomething",
			expected: "TestSomething",
		},
		{
			name:     "With subtests",
			testName: "TestSomething/WithSubtest",
			expected: "TestSomething_WithSubtest",
		},
		{
			name:     "With unique suffix",
			testName: "TestSomething#01",
			expected: "TestSomething_01",
		},
		{
			name:     "With unique suffix and subtest",
			testName: "TestSomething/WithSubtest#01",
			expected: "TestSomething_WithSubtest_01",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, sanitizeTestName(test.testName))
		})
	}

	assert.Equal(t, "TestSanitizeTestName", SanitizeTestName(t))
}
