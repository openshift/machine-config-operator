package mco

import (
	"encoding/base32"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/architecture"

	"github.com/google/uuid"

	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	container "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/container"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// OsImageBuilder encapsulates the functionality to build custom osImage in the machine running the testcase
type OsImageBuilder struct {
	oc           *exutil.CLI
	architecture architecture.Architecture
	baseImage,
	osImage,
	dockerFileCommands, // Full docker file but the "FROM basOsImage..." that will be calculated
	dockerConfig,
	tmpDir string
	cleanupRegistryRoute,
	UseInternalRegistry bool
}

func (b *OsImageBuilder) prepareEnvironment() error {
	var err error

	if b.dockerConfig == "" {
		logger.Infof("No docker config file was provided to the osImage builder. Generating a new docker config file")
		exutil.By("Extract pull-secret")
		pullSecret := GetPullSecret(b.oc.AsAdmin())
		tokenDir, err := pullSecret.Extract()
		if err != nil {
			return fmt.Errorf("Error extracting pull-secret. Error: %s", err)
		}
		logger.Infof("Pull secret has been extracted to: %s\n", tokenDir)
		b.dockerConfig = filepath.Join(tokenDir, ".dockerconfigjson")
	}
	logger.Infof("Using docker config file: %s\n", b.dockerConfig)

	b.architecture = architecture.ClusterArchitecture(b.oc)
	logger.Infof("Building using architecture: %s", b.architecture)

	b.baseImage, err = getImageFromReleaseInfo(b.oc.AsAdmin(), LayeringBaseImageReleaseInfo, b.dockerConfig)
	if err != nil {
		return fmt.Errorf("Error getting the base image to build new osImages. Error: %s", err)
	}

	if b.UseInternalRegistry {
		if err := b.preparePushToInternalRegistry(); err != nil {
			return err
		}
	} else if b.osImage == "" {
		uniqueTag, err := generateUniqueTag(b.oc.AsAdmin(), b.baseImage)
		if err != nil {
			return err
		}
		b.osImage = getLayeringTestImageRepository(uniqueTag)
	}
	logger.Infof("Building image: %s", b.osImage)

	if b.tmpDir == "" {
		b.tmpDir = e2e.TestContext.OutputDir
	}

	return nil
}

func (b *OsImageBuilder) preparePushToInternalRegistry() error {

	exposed, expErr := b.oc.Run("get").Args("configs.imageregistry.operator.openshift.io", "cluster", `-ojsonpath={.spec.defaultRoute}`).Output()
	if expErr != nil {
		return fmt.Errorf("Error getting internal registry configuration. Error: %s", expErr)
	}

	if !IsTrue(exposed) {
		b.cleanupRegistryRoute = true
		logger.Infof("The internal registry service is not exposed. Exposing internal registry service...")

		expErr := b.oc.Run("patch").Args("configs.imageregistry.operator.openshift.io/cluster", "--patch", `{"spec":{"defaultRoute":true}}`, "--type=merge").Execute()
		if expErr != nil {
			return fmt.Errorf("Error exposing internal registry. Error: %s", expErr)
		}
	}

	logger.Infof("Create namespace to store the service account to access the internal registry")
	nsExistsErr := b.oc.Run("get").Args("namespace", layeringTestsTmpNamespace).Execute()
	if nsExistsErr != nil {
		err := b.oc.Run("create").Args("namespace", layeringTestsTmpNamespace).Execute()
		if err != nil {
			return fmt.Errorf("Error creating namespace %s to store the layering imagestreams. Error: %s",
				layeringTestsTmpNamespace, err)
		}
	} else {
		logger.Infof("Namespace %s already exists. Skip namespace creation", layeringTestsTmpNamespace)
	}

	logger.Infof("Create service account with registry admin permissions to store the imagestream")
	saExistsErr := b.oc.Run("get").Args("-n", layeringTestsTmpNamespace, "serviceaccount", layeringRegistryAdminSAName).Execute()
	if saExistsErr != nil {
		cErr := b.oc.Run("create").Args("-n", layeringTestsTmpNamespace, "serviceaccount", layeringRegistryAdminSAName).Execute()
		if cErr != nil {
			return fmt.Errorf("Error creating ServiceAccount %s/%s: %s", layeringTestsTmpNamespace, layeringRegistryAdminSAName, cErr)
		}
	} else {
		logger.Infof("SA %s/%s already exists. Skip SA creation", layeringTestsTmpNamespace, layeringRegistryAdminSAName)
	}

	admErr := b.oc.Run("adm").Args("-n", layeringTestsTmpNamespace, "policy", "add-cluster-role-to-user", "registry-admin", "-z", layeringRegistryAdminSAName).Execute()
	if admErr != nil {
		return fmt.Errorf("Error creating ServiceAccount %s: %s", layeringRegistryAdminSAName, admErr)
	}

	logger.Infof("Get SA token")
	saToken, err := b.oc.Run("create").Args("-n", layeringTestsTmpNamespace, "token", layeringRegistryAdminSAName).Output()
	if err != nil {
		logger.Errorf("Error getting token for SA %s", layeringRegistryAdminSAName)
		return err
	}
	logger.Debugf("SA TOKEN: %s", saToken)
	logger.Infof("OK!\n")

	logger.Infof("Get current internal registry route")
	internalRegistryURL, routeErr := b.oc.Run("get").Args("route", "default-route", "-n", "openshift-image-registry", "--template", `{{ .spec.host }}`).Output()
	if routeErr != nil {
		return fmt.Errorf("Error getting internal registry's route. Ourput: %s\nError: %s", internalRegistryURL, routeErr)
	}
	logger.Infof("Current internal registry route: %s", internalRegistryURL)

	uniqueTag, err := generateUniqueTag(b.oc.AsAdmin(), b.baseImage)
	if err != nil {
		return err
	}

	b.osImage = fmt.Sprintf("%s/%s/%s:%s", internalRegistryURL, MachineConfigNamespace, "layering", uniqueTag)
	logger.Infof("Using image: %s", b.osImage)

	logger.Infof("Loging as registry admin to internal registry")
	podmanCLI := container.NewPodmanCLI()
	loginOut, loginErr := podmanCLI.Run("login").Args(internalRegistryURL, "-u", layeringRegistryAdminSAName, "-p", saToken, "--tls-verify=false", "--authfile", b.dockerConfig).Output()
	if loginErr != nil {
		return fmt.Errorf("Error trying to login to internal registry:\nOutput:%s\nError:%s", loginOut, loginErr)
	}
	logger.Infof("OK!\n")

	return nil
}

// CleanUp will clean up all the helper resources created by the builder
func (b *OsImageBuilder) CleanUp() error {
	logger.Infof("Cleanup image builder resources")
	if b.UseInternalRegistry {
		logger.Infof("Removing namespace %s", layeringTestsTmpNamespace)
		err := b.oc.Run("delete").Args("namespace", layeringTestsTmpNamespace, "--ignore-not-found").Execute()
		if err != nil {
			return fmt.Errorf("Error deleting namespace %s. Error: %s",
				layeringTestsTmpNamespace, err)
		}

		if b.cleanupRegistryRoute {
			logger.Infof("The internal registry route was exposed. Remove the exposed internal registry route to restore initial state.")
			expErr := b.oc.Run("patch").Args("configs.imageregistry.operator.openshift.io/cluster", "--patch", `{"spec":{"defaultRoute":false}}`, "--type=merge").Execute()
			if expErr != nil {
				return fmt.Errorf("Error exposing internal registry. Error: %s", expErr)
			}
		}
	} else {
		logger.Infof("Not using internal registry, nothing to clean")
	}
	return nil
}

func (b *OsImageBuilder) buildImage() error {
	exutil.By("Build image locally")

	logger.Infof("Base image: %s\n", b.baseImage)

	dockerFile := "FROM " + b.baseImage + "\n" + b.dockerFileCommands + "\n" + ExpirationDockerfileLabel
	logger.Infof(" Using Dockerfile:\n%s", dockerFile)

	buildDir, err := prepareDockerfileDirectory(b.tmpDir, dockerFile)
	if err != nil {
		return fmt.Errorf("Error creating the build directory with the Dockerfile. Error: %s", err)
	}

	podmanCLI := container.NewPodmanCLI()
	podmanCLI.ExecCommandPath = buildDir
	switch b.architecture {
	case architecture.AMD64, architecture.ARM64, architecture.PPC64LE, architecture.S390X:
		output, err := podmanCLI.Run("build").Args(buildDir, "--arch", b.architecture.String(), "--tag", b.osImage, "--authfile", b.dockerConfig).Output()
		if err != nil {
			msg := fmt.Sprintf("Podman failed building image %s with architecture %s:\n%s\n%s", b.osImage, b.architecture, output, err)
			logger.Errorf(msg)
			return fmt.Errorf(msg)
		}

		logger.Debugf(output)
	default:
		msg := fmt.Sprintf("architecture '%s' is not supported. ", b.architecture)
		logger.Errorf(msg)
		return fmt.Errorf(msg)
	}

	logger.Infof("OK!\n")
	return nil
}

func (b *OsImageBuilder) pushImage() error {
	if b.osImage == "" {
		return fmt.Errorf("There is no image to be pushed. Wast the osImage built?")
	}
	exutil.By("Push osImage")
	logger.Infof("Pushing image %s", b.osImage)
	podmanCLI := container.NewPodmanCLI()
	output, err := podmanCLI.Run("push").Args(b.osImage, "--tls-verify=false", "--authfile", b.dockerConfig).Output()
	if err != nil {
		msg := fmt.Sprintf("Podman failed pushing image %s:\n%s\n%s", b.osImage, output, err)
		logger.Errorf(msg)
		return fmt.Errorf(msg)
	}

	logger.Debugf(output)
	logger.Infof("OK!\n")
	return nil
}

func (b *OsImageBuilder) removeImage() error {
	if b.osImage == "" {
		return fmt.Errorf("There is no image to be removed. Wast the osImage built?")
	}
	logger.Infof("Removing image %s", b.osImage)

	podmanCLI := container.NewPodmanCLI()
	rmOutput, err := podmanCLI.Run("rmi").Args("-i", b.osImage).Output()
	if err != nil {
		msg := fmt.Sprintf("Podman failed removing image %s:\n%s\n%s", b.osImage, rmOutput, err)
		logger.Errorf(msg)
		return fmt.Errorf(msg)
	}

	logger.Debugf(rmOutput)
	logger.Infof("OK!\n")
	return nil
}

func (b *OsImageBuilder) digestImage() (string, error) {
	if b.osImage == "" {
		return "", fmt.Errorf("There is no image to be digested. Wast the osImage built?")
	}
	skopeoCLI := NewSkopeoCLI().SetAuthFile(b.dockerConfig)
	inspectInfo, err := skopeoCLI.Run("inspect").Args("--tls-verify=false", "docker://"+b.osImage).Output()
	if err != nil {
		msg := fmt.Sprintf("Skopeo failed inspecting image %s:\n%s\n%s", b.osImage, inspectInfo, err)
		logger.Errorf(msg)
		return "", fmt.Errorf(msg)
	}

	logger.Debugf(inspectInfo)

	inspectJSON := JSON(inspectInfo)
	digestedImage := inspectJSON.Get("Name").ToString() + "@" + inspectJSON.Get("Digest").ToString()

	logger.Infof("Image %s was digested as %s", b.osImage, digestedImage)

	return digestedImage, nil
}

// CreateAndDigestOsImage create the osImage and returns the image digested
func (b *OsImageBuilder) CreateAndDigestOsImage() (string, error) {
	if err := b.prepareEnvironment(); err != nil {
		return "", err
	}
	if err := b.buildImage(); err != nil {
		return "", err
	}
	if err := b.pushImage(); err != nil {
		return "", err
	}
	if err := b.removeImage(); err != nil {
		return "", err
	}
	return b.digestImage()
}

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

func generateUniqueTag(oc *exutil.CLI, baseImage string) (string, error) {
	var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567")

	baseImageSlice := strings.Split(baseImage, "@")
	if len(baseImageSlice) != 2 {
		return "", fmt.Errorf("The name of the base image %s is not properly formatted as a diggested image", baseImage)
	}
	rhelCoreosDigest := baseImageSlice[1]

	clusterName, err := exutil.GetInfraID(oc)
	if err != nil {
		return "", nil
	}

	testCaseID := GetCurrentTestPolarionIDNumber()

	s := fmt.Sprintf("%s%s%s", rhelCoreosDigest, clusterName, uuid.NewString())
	uniqueTag := fmt.Sprintf("%s-%s", testCaseID,
		strings.TrimRight(encoding.EncodeToString(fnv.New64().Sum([]byte(s))), "=")[:(127-len(testCaseID))])

	logger.Infof("Using unique tag %s", uniqueTag)
	return uniqueTag, nil
}
