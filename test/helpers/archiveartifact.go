package helpers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Creates and manages a temporary directory for archiving files to the
// detected build artifact directory.
type ArtifactArchive struct {
	t           *testing.T
	archiveName string
	artifactDir string
	stagingDir  string
}

const (
	artifactDirEnvVar string = "ARTIFACT_DIR"
)

// Creates an ArtifactArchive instance for writing archived files to the build
// artifact directory.
func NewArtifactArchive(t *testing.T, archiveName string) (*ArtifactArchive, error) {
	t.Helper()

	_, err := exec.LookPath("tar")
	if err != nil {
		return nil, err
	}

	artifactDir, err := GetBuildArtifactDir(t)
	if err != nil {
		return nil, err
	}

	a := &ArtifactArchive{
		t:           t,
		archiveName: filepath.Join(artifactDir, archiveName),
		artifactDir: artifactDir,
		stagingDir:  t.TempDir(),
	}

	// Create the root directory for the staging area.
	if err := os.MkdirAll(a.StagingDir(), 0o755); err != nil {
		return nil, err
	}

	t.Logf("Created archive staging dir: %q. All files written there will be archived to: %q", a.stagingDir, a.archiveName)
	return a, nil
}

// Returns the root of the archive. In other words, all files within the
// archive will be in a folder with this name, which will be a sanitized
// representation of the test name.
func (a *ArtifactArchive) ArchiveRoot() string {
	return SanitizeTestName(a.t)
}

// Returns the staging directory where files may be written to. The archive
// root is appended so that all files written to the path returned by this
// function are under the archive root.
func (a *ArtifactArchive) StagingDir() string {
	return filepath.Join(a.stagingDir, a.ArchiveRoot())
}

// Writes the archive to the build artifact directory using the tar program.
func (a *ArtifactArchive) WriteArchive() error {
	cmd := exec.Command("tar", "-cvzf", a.archiveName, "-C", a.stagingDir, ".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		a.t.Logf(string(output))
		return err
	}

	a.t.Logf("Archived files to %q", a.archiveName)

	return nil
}

// Determines where to write files to. ARTIFACT_DIR is a well-known env var
// provided by the OpenShift CI system. Writing to the path in this env var
// will ensure that any files written to that path end up in the OpenShift CI
// GCP bucket for later viewing.
//
// If this env var is not set, these files will be written to the current
// working directory.
func GetBuildArtifactDir(t *testing.T) (string, error) {
	artifactDir, ok := os.LookupEnv(artifactDirEnvVar)
	if ok && artifactDir != "" {
		t.Logf("%s set to %q", artifactDirEnvVar, artifactDir)
		return artifactDir, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	t.Logf("%s not set or empty, using current working directory: %q", artifactDirEnvVar, cwd)

	return cwd, nil
}

// Makes the test name safe for use as a filename by removing all invalid characters.
func SanitizeTestName(t *testing.T) string {
	return sanitizeTestName(t.Name())
}

func sanitizeTestName(name string) string {
	invalidChars := []string{
		"/",
		"#",
	}

	for _, char := range invalidChars {
		name = strings.ReplaceAll(name, char, "_")
	}

	return name
}
