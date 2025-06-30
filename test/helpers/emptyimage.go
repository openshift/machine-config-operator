package helpers

import (
	"archive/tar"
	"compress/gzip"

	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ImageTarballFilename string = "scratch.tar.gz"
)

func CreateScratchImageTarball(imageTarballDir string) error {
	layoutDir, err := os.MkdirTemp("", "create-scratch-image-tarball")
	if err != nil {
		return err
	}

	defer os.RemoveAll(layoutDir)

	layoutDir = filepath.Join(layoutDir, "oci-image")

	if err := os.MkdirAll(layoutDir, 0o755); err != nil {
		return fmt.Errorf("could not create layout dir %s: %w", layoutDir, err)
	}

	layout := v1.ImageLayout{
		Version: v1.ImageLayoutVersion,
	}

	layoutJSON, err := json.Marshal(layout)
	if err != nil {
		return fmt.Errorf("could not marshal layout JSON: %w", err)
	}

	layoutFile := filepath.Join(layoutDir, "oci-layout")
	if err := os.WriteFile(layoutFile, layoutJSON, 0o644); err != nil {
		return fmt.Errorf("could not write layout JSON to %s: %w", layoutFile, err)
	}

	manifest := v1.Manifest{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: v1.MediaTypeImageManifest,
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    "", // Will be set later
			Size:      0,  // Will be set later
		},
		Layers: []v1.Descriptor{}, // "FROM scratch" has no layers
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("could not marshal manifest JSON: %w", err)
	}

	manifestDigest := digest.FromBytes(manifestJSON)
	manifestFilename := filepath.Join(layoutDir, "blobs", manifestDigest.Algorithm().String(), manifestDigest.Hex())
	manifestDir := filepath.Dir(manifestFilename)
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return fmt.Errorf("could not create manifest dir %s: %w", manifestDir, err)
	}

	if err := os.WriteFile(manifestFilename, manifestJSON, 0o644); err != nil {
		return fmt.Errorf("could not write manifest JSON to %s: %w", manifestFilename, err)
	}

	config := v1.Image{
		Created: &time.Time{},
		Platform: v1.Platform{
			Architecture: "amd64",
			OS:           "linux",
		},
		Config: v1.ImageConfig{},
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{},
		},
		History: []v1.History{},
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("could not marshal config JSON: %w", err)
	}

	configDigest := digest.FromBytes(configJSON)
	configFilename := filepath.Join(layoutDir, "blobs", configDigest.Algorithm().String(), configDigest.Hex())
	configDir := filepath.Dir(configFilename)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return fmt.Errorf("could not create config dir %s: %w", configDir, err)
	}

	if err := os.WriteFile(configFilename, configJSON, 0o644); err != nil {
		return fmt.Errorf("could not write config JSON to %s: %w", configFilename, err)
	}

	manifest.Config.Digest = configDigest
	manifest.Config.Size = int64(len(configJSON))
	manifestJSONUpdated, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("could not marshal updated manifest JSON: %w", err)
	}

	manifestDigestUpdated := digest.FromBytes(manifestJSONUpdated)
	manifestFilenameUpdated := filepath.Join(layoutDir, "blobs", manifestDigestUpdated.Algorithm().String(), manifestDigestUpdated.Hex())
	if manifestDigest != manifestDigestUpdated {
		if err := os.WriteFile(manifestFilenameUpdated, manifestJSONUpdated, 0o644); err != nil {
			return fmt.Errorf("could not write updated manifest JSON to %s: %w", manifestFilenameUpdated, err)
		}
	}

	index := v1.Index{
		Versioned: specs.Versioned{
			SchemaVersion: 2,
		},
		MediaType: v1.MediaTypeImageIndex,
		Manifests: []v1.Descriptor{
			{
				MediaType: v1.MediaTypeImageManifest,
				Digest:    manifestDigestUpdated,
				Size:      int64(len(manifestJSONUpdated)),
				Platform: &v1.Platform{
					Architecture: "amd64",
					OS:           "linux",
				},
			},
		},
	}

	indexJSON, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("could not marshal index JSON: %w", err)
	}

	indexJSONFilename := filepath.Join(layoutDir, "index.json")
	if err := os.WriteFile(indexJSONFilename, indexJSON, 0o644); err != nil {
		return fmt.Errorf("could not write index JSON file to %s: %w", indexJSONFilename, err)
	}

	archiveFilename := filepath.Join(imageTarballDir, ImageTarballFilename)

	archiveFile, err := os.Create(archiveFilename)
	if err != nil {
		return fmt.Errorf("could not create archive at %s: %w", archiveFilename, err)
	}

	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	err = filepath.Walk(layoutDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("error from filepath.Walk: %w", err)
		}

		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(layoutDir, path)
		if err != nil {
			return fmt.Errorf("could not relative path for %s and %s: %w", layoutDir, path, err)
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("could not get tarball header for file %s: %w", path, err)
		}

		header.Name = relPath
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("could not write tarball header for %s: %w", relPath, err)
		}

		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("could not open path %s: %w", path, err)
		}

		defer file.Close()

		_, err = io.Copy(tarWriter, file)
		if err != nil {
			return fmt.Errorf("could not copy from file %s to tarwriter: %w", path, err)
		}

		return nil
	})

	if err != nil {
		return fmt.Errorf("could not walk directory %s: %w", layoutDir, err)
	}

	return nil
}
