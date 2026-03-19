package extended

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
)

func prepareDockerfileDirectory(baseDir, dockerFileContent string) (string, error) {
	layout := "2006_01_02T15-04-05Z"

	directory := filepath.Join(baseDir, fmt.Sprintf("containerbuild-%s", time.Now().Format(layout)))
	if err := os.Mkdir(directory, os.ModePerm); err != nil {
		return "", err
	}

	dockerFile := filepath.Join(directory, "Dockerfile")
	if err := os.WriteFile(dockerFile, []byte(dockerFileContent), 0o644); err != nil {
		return "", err
	}

	return directory, nil
}

func getImageFromReleaseInfo(oc *exutil.CLI, imageName, dockerConfigFile string) (string, error) {
	stdout, stderr, err := oc.Run("adm").Args("release", "info", "--insecure", "--image-for", imageName,
		"--registry-config", dockerConfigFile).Outputs()
	if err != nil {
		logger.Errorf("STDOUT: %s", stdout)
		logger.Errorf("STDERR: %s", stderr)
		return stdout + stderr, err
	}

	return stdout, nil
}

// GetLayeringBaseImageByStream returns the full base image for layering resolved from release info for the given stream.
func GetLayeringBaseImageByStream(oc *exutil.CLI, stream, dockerConfigFile string) (string, error) {
	releaseInfoName := LayeringBaseImageReleaseInfo
	if stream == OSImageStreamRHEL10 {
		releaseInfoName = LayeringBaseImageReleaseInfoRhel10
	}
	return getImageFromReleaseInfo(oc.AsAdmin(), releaseInfoName, dockerConfigFile)
}

func getLayeringTestImageRepository(defaultTag string) string {
	layeringImageRepo, exists := os.LookupEnv(EnvVarLayeringTestImageRepository)

	if !exists {
		layeringImageRepo = DefaultLayeringQuayRepository
	}

	// If no tag is provided for the image, we add one
	if !strings.Contains(layeringImageRepo, ":") && defaultTag != "" {
		layeringImageRepo = layeringImageRepo + ":" + defaultTag
	}

	return layeringImageRepo
}

// generateUniqueTag creates a string that can be used as an unique tag for an image
func generateUniqueTag(oc *exutil.CLI) (string, error) {
	const maxDockerTagLength = 128

	clusterName, err := exutil.GetInfraID(oc)
	if err != nil {
		return "", err
	}

	testCaseID := GetCurrentTestPolarionIDNumber()
	if testCaseID == "" {
		testCaseID = "unknown"
	}

	uniqueID := strings.ReplaceAll(uuid.NewString(), "-", "")
	uniqueTag := fmt.Sprintf("%s-%s-%s", uniqueID, testCaseID, clusterName)

	// Sanitize the full tag to keep only valid Docker tag characters [a-zA-Z0-9_.-]
	var sanitized strings.Builder
	for _, r := range uniqueTag {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			sanitized.WriteRune(r)
		}
	}
	uniqueTag = sanitized.String()

	if len(uniqueTag) > maxDockerTagLength {
		uniqueTag = uniqueTag[:maxDockerTagLength]
	}

	logger.Infof("Using unique tag %s", uniqueTag)
	return uniqueTag, nil
}
