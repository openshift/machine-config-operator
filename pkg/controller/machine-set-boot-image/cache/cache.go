package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/coreos/stream-metadata-go/stream"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

const (
	// ImageBasedApplicationName is to use as application name used by image-based.
	ImageBasedApplicationName = "imagebased"
	// ImageDataType is used by installer.
	ImageDataType = "image"
)

func getFileHashBuffered(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("couldn't open file %v", err)
	}
	defer file.Close()

	hasher := sha256.New()
	buf := make([]byte, 1024*1024) // Buffer size 1MB

	for {
		n, err := file.Read(buf)
		if n > 0 {
			hasher.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("error reading file: %v", err)
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// GetFileFromCache returns path of the cached file if found, otherwise returns an empty string
// or error.
func getFileFromCache(fileName, cacheDir string) (string, string, error) {
	filePath := filepath.Join(cacheDir, fileName)

	// If the file has already been cached, return its path
	_, err := os.Stat(filePath)
	if err == nil {
		klog.Infof("The file was found in cache: %v. Reusing...", filePath)
		hash, err := getFileHashBuffered(filePath)
		if err != nil {
			return "", "", fmt.Errorf("error calculating SHA256 hash of filepath %v: %v", filePath, err)
		}
		return filePath, hash, nil
	}
	if !os.IsNotExist(err) {
		return "", "", err
	}

	return "", "", nil
}

// GetCacheDir returns a local path of the cache, where the installer should put the data:
// /tmp/<applicationName>/<dataType>_cache
// If the directory doesn't exist, it will be automatically created.
func getCacheDir(dataType, applicationName string) (string, error) {
	if dataType == "" {
		return "", errors.Errorf("data type can't be an empty string")
	}

	cacheDir := filepath.Join("/tmp", applicationName, dataType+"_cache")

	_, err := os.Stat(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(cacheDir, 0o755)
			if err != nil {
				return "", err
			}
		} else {
			return "", err
		}
	}

	return cacheDir, nil
}

// cacheFile puts data in the cache.
func cacheFile(ova *stream.Artifact, filePath, cacheDir string) (string, error) {

	flockPath := fmt.Sprintf("%s.lock", filePath)
	flock, err := os.Create(flockPath)
	if err != nil {
		return "", err
	}
	defer flock.Close()
	defer func() {
		err2 := os.Remove(flockPath)
		if err == nil {
			err = err2
		}
	}()

	err = unix.Flock(int(flock.Fd()), unix.LOCK_EX)
	if err != nil {
		return "", err
	}
	defer func() {
		err2 := unix.Flock(int(flock.Fd()), unix.LOCK_UN)
		if err == nil {
			err = err2
		}
	}()

	_, err = os.Stat(filePath)
	if err != nil && !os.IsNotExist(err) {
		return "", nil // another cacheFile beat us to it
	}

	klog.Infof("Downloading bootimage to %s", filePath)
	return ova.Download(cacheDir)
}

// download obtains a file from a given URL, puts it in the cache folder, defined by dataType parameter,
// and returns the local file path.
func DownloadOva(ova *stream.Artifact) (string, error) {

	fileName, err := ova.Name()
	if err != nil {
		return "", err
	}

	cacheDir, err := getCacheDir(ImageDataType, ImageBasedApplicationName)
	if err != nil {
		return "", err
	}

	filePath, hash, err := getFileFromCache(fileName, cacheDir)
	if err != nil {
		return "", err
	}
	if filePath != "" {
		// Found cached file
		if hash == ova.Sha256 {
			return filePath, nil
		}
		klog.Infof("Cache for %v is corrupted", filePath)
	}

	filePath = filepath.Join(cacheDir, fileName)
	return cacheFile(ova, filePath, cacheDir)
}
