package helpers

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateScratchImageTarball(t *testing.T) {
	tmpDir := t.TempDir()

	imageTarballPath := filepath.Join(tmpDir, ImageTarballFilename)

	assert.NoError(t, CreateScratchImageTarball(tmpDir))
	assert.FileExists(t, imageTarballPath)
	assert.NoDirExists(t, filepath.Join(tmpDir, "oci-image"))
}
