package mco

import (
	"fmt"
	"path/filepath"
	"strings"

	exutil "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util"
	container "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/container"
	logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/util/logext"
	e2e "k8s.io/kubernetes/test/e2e/framework"
)

// OsImageBuilderInNode encapsulates the functionality to build custom osImages inside a cluster node
type OsImageBuilderInNode struct {
	node Node
	baseImage,
	osImage,
	dockerFileCommands, // Full docker file but the "FROM basOsImage..." that will be calculated
	dockerConfig,
	httpProxy,
	httpsProxy,
	noProxy,
	tmpDir,
	remoteTmpDir,
	remoteKubeconfig,
	remoteDockerConfig,
	remoteDockerfile string
	UseInternalRegistry bool
}

func (b *OsImageBuilderInNode) prepareEnvironment() error {
	var err error

	if b.dockerConfig == "" {
		logger.Infof("No docker config file was provided to the osImage builder. Generating a new docker config file")
		exutil.By("Extract pull-secret")
		pullSecret := GetPullSecret(b.node.oc.AsAdmin())
		tokenDir, err := pullSecret.Extract()
		if err != nil {
			return fmt.Errorf("Error extracting pull-secret. Error: %s", err)
		}
		logger.Infof("Pull secret has been extracted to: %s\n", tokenDir)
		b.dockerConfig = filepath.Join(tokenDir, ".dockerconfigjson")
	}
	logger.Infof("Using docker config file: %s\n", b.dockerConfig)

	b.remoteTmpDir = filepath.Join("/root", e2e.TestContext.OutputDir, fmt.Sprintf("mco-test-%s", exutil.GetRandomString()))
	_, err = b.node.DebugNodeWithChroot("mkdir", "-p", b.remoteTmpDir)
	if err != nil {
		return fmt.Errorf("Error creating tmp dir %s in node %s. Error: %s", b.remoteTmpDir, b.node.GetName(), err)
	}

	if b.remoteKubeconfig == "" {
		b.remoteKubeconfig = filepath.Join(b.remoteTmpDir, "kubeconfig")
	}
	if b.remoteDockerConfig == "" {
		b.remoteDockerConfig = filepath.Join(b.remoteTmpDir, ".dockerconfigjson")
	}
	if b.remoteDockerfile == "" {
		b.remoteDockerfile = filepath.Join(b.remoteTmpDir, "Dockerfile")
	}

	exutil.By("Prepare remote docker config file")
	logger.Infof("Copy cluster config.json file")
	_, cpErr := b.node.DebugNodeWithChroot("cp", "/var/lib/kubelet/config.json", b.remoteDockerConfig)
	if cpErr != nil {
		logger.Errorf("Error copying cluster config.json file to a temporary directory")
		return cpErr
	}

	b.baseImage, err = getImageFromReleaseInfo(b.node.oc.AsAdmin(), LayeringBaseImageReleaseInfo, b.dockerConfig)
	if err != nil {
		return fmt.Errorf("Error getting the base image to build new osImages. Error: %s", err)
	}

	uniqueTag, err := generateUniqueTag(b.node.oc.AsAdmin(), b.baseImage)
	if err != nil {
		return err
	}

	if b.UseInternalRegistry {
		// The images must be created inside the MCO namespace or MCO will not have permissions to pull them
		b.osImage = fmt.Sprintf("%s/%s/%s:%s", InternalRegistrySvcURL, MachineConfigNamespace, "layering", uniqueTag)
		if err := b.preparePushToInternalRegistry(); err != nil {
			return err
		}
	} else if b.osImage == "" {
		b.osImage = getLayeringTestImageRepository(uniqueTag)
	}
	logger.Infof("Building image: %s", b.osImage)

	if b.tmpDir == "" {
		b.tmpDir = e2e.TestContext.OutputDir
	}

	logger.Infof("Gathering proxy information")
	proxy := NewResource(b.node.oc, "proxy", "cluster")
	if proxy.Exists() {
		b.httpProxy = proxy.GetOrFail(`{.status.httpProxy}`)
		b.httpsProxy = proxy.GetOrFail(`{.status.httpsProxy}`)
		b.noProxy = proxy.GetOrFail(`{.status.noProxy}`)
	}

	logger.Infof("OK!\n")

	return nil
}

func (b *OsImageBuilderInNode) preparePushToInternalRegistry() error {
	logger.Infof("Create namespace to store the service account to access the internal registry")
	nsExistsErr := b.node.oc.Run("get").Args("namespace", layeringTestsTmpNamespace).Execute()
	if nsExistsErr != nil {
		err := b.node.oc.Run("create").Args("namespace", layeringTestsTmpNamespace).Execute()
		if err != nil {
			return fmt.Errorf("Error creating namespace %s to store the tmp SAs. Error: %s",
				layeringTestsTmpNamespace, err)
		}
	} else {
		logger.Infof("Namespace %s already exists. Skip namespace creation", layeringTestsTmpNamespace)
	}

	logger.Infof("Create service account with registry admin permissions to store the imagestream")
	saExistsErr := b.node.oc.Run("get").Args("-n", layeringTestsTmpNamespace, "serviceaccount", layeringRegistryAdminSAName).Execute()
	if saExistsErr != nil {
		cErr := b.node.oc.Run("create").Args("-n", layeringTestsTmpNamespace, "serviceaccount", layeringRegistryAdminSAName).Execute()
		if cErr != nil {
			return fmt.Errorf("Error creating ServiceAccount %s/%s: %s", layeringTestsTmpNamespace, layeringRegistryAdminSAName, cErr)
		}
	} else {
		logger.Infof("SA %s/%s already exists. Skip SA creation", layeringTestsTmpNamespace, layeringRegistryAdminSAName)
	}

	admErr := b.node.oc.Run("adm").Args("-n", layeringTestsTmpNamespace, "policy", "add-cluster-role-to-user", "registry-admin", "-z", layeringRegistryAdminSAName).Execute()
	if admErr != nil {
		return fmt.Errorf("Error creating ServiceAccount %s: %s", layeringRegistryAdminSAName, admErr)
	}

	logger.Infof("Get SA token")
	saToken, err := b.node.oc.Run("create").Args("-n", layeringTestsTmpNamespace, "token", layeringRegistryAdminSAName).Output()
	if err != nil {
		logger.Errorf("Error getting token for SA %s", layeringRegistryAdminSAName)
		return err
	}
	logger.Debugf("SA TOKEN: %s", saToken)
	logger.Infof("OK!\n")

	logger.Infof("Loging as registry admin to internal registry")
	loginOut, loginErr := b.node.DebugNodeWithChroot("podman", "login", InternalRegistrySvcURL, "-u", layeringRegistryAdminSAName, "-p", saToken, "--authfile", b.remoteDockerConfig)
	if loginErr != nil {
		return fmt.Errorf("Error trying to login to internal registry:\nOutput:%s\nError:%s", loginOut, loginErr)
	}
	logger.Infof("OK!\n")

	return nil
}

// CleanUp will clean up all the helper resources created by the builder
func (b *OsImageBuilderInNode) CleanUp() error {
	logger.Infof("Cleanup image builder resources")
	if b.UseInternalRegistry {
		logger.Infof("Removing namespace %s", layeringTestsTmpNamespace)
		err := b.node.oc.Run("delete").Args("namespace", layeringTestsTmpNamespace, "--ignore-not-found").Execute()
		if err != nil {
			return fmt.Errorf("Error deleting namespace %s. Error: %s",
				layeringTestsTmpNamespace, err)
		}

	} else {
		logger.Infof("Not using internal registry, nothing to clean")
	}
	return nil
}

func (b *OsImageBuilderInNode) buildImage() error {
	exutil.By("Get base osImage locally")

	logger.Infof("Base image: %s\n", b.baseImage)

	exutil.By("Prepare remote dockerFile directory")
	dockerFile := "FROM " + b.baseImage + "\n" + b.dockerFileCommands + "\n" + ExpirationDockerfileLabel
	logger.Infof(" Using Dockerfile:\n%s", dockerFile)

	localBuildDir, err := prepareDockerfileDirectory(b.tmpDir, dockerFile)
	if err != nil {
		return fmt.Errorf("Error creating the build directory with the Dockerfile. Error: %s", err)
	}

	cpErr := b.node.CopyFromLocal(filepath.Join(localBuildDir, "Dockerfile"), b.remoteDockerfile)
	if cpErr != nil {
		return fmt.Errorf("Error creating the Dockerfile in the remote node. Error: %s", cpErr)
	}
	logger.Infof("OK!\n")

	exutil.By("Build osImage")
	podmanCLI := container.NewPodmanCLI()
	buildPath := filepath.Dir(b.remoteDockerfile)
	podmanCLI.ExecCommandPath = buildPath

	logger.Infof("Copy the /etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt file to the build dir, so that it can be used if needed")
	_, err = b.node.DebugNodeWithChroot("cp", "/etc/pki/ca-trust/source/anchors/openshift-config-user-ca-bundle.crt", buildPath)
	if err != nil {
		return err
	}

	buildCommand := "NO_PROXY=" + b.noProxy + " HTTPS_PROXY=" + b.httpsProxy + " HTTP_PROXY=" + b.httpProxy + " podman build " + buildPath + " --tag " + b.osImage + " --authfile " + b.remoteDockerConfig
	logger.Infof("Executing build command: %s", buildCommand)

	output, err := b.node.DebugNodeWithChroot("bash", "-c", buildCommand)
	if err != nil {
		msg := fmt.Sprintf("Podman failed building image %s:\n%s\n%s", b.osImage, output, err)
		logger.Errorf(msg)
		return fmt.Errorf(msg)
	}

	logger.Debugf(output)
	logger.Infof("OK!\n")

	return nil
}

func (b *OsImageBuilderInNode) pushImage() error {
	exutil.By("Push osImage")
	pushCommand := "NO_PROXY=" + b.noProxy + " HTTPS_PROXY=" + b.httpsProxy + " HTTP_PROXY=" + b.httpProxy + " podman push " + b.osImage + " --authfile " + b.remoteDockerConfig
	logger.Infof("Executing push command: %s", pushCommand)

	output, err := b.node.DebugNodeWithChroot("bash", "-c", pushCommand)
	if err != nil {
		msg := fmt.Sprintf("Podman failed pushing image %s:\n%s\n%s", b.osImage, output, err)
		logger.Errorf(msg)
		return fmt.Errorf(msg)
	}

	// If we don't have permissions to push the image, the `oc debug` command will not return an error
	//  so we need to check manually that there is no unauthorized error
	if strings.Contains(output, "unauthorized: access to the requested resource is not authorized") {
		msg := fmt.Sprintf("Podman was not authorized to push the image %s:\n%s\n", b.osImage, output)
		logger.Errorf(msg)
		return fmt.Errorf(msg)
	}

	logger.Debugf(output)
	logger.Infof("OK!\n")
	return nil
}

func (b *OsImageBuilderInNode) removeImage() error {
	exutil.By("Remove osImage")
	rmOutput, err := b.node.DebugNodeWithChroot("podman", "rmi", "-i", b.osImage)
	if err != nil {
		msg := fmt.Sprintf("Podman failed removing image %s:\n%s\n%s", b.osImage, rmOutput, err)
		logger.Errorf(msg)
		return fmt.Errorf(msg)
	}

	logger.Debugf(rmOutput)
	logger.Infof("OK!\n")
	return nil
}

func (b *OsImageBuilderInNode) digestImage() (string, error) {
	exutil.By("Digest osImage")
	skopeoCommand := "NO_PROXY=" + b.noProxy + " HTTPS_PROXY=" + b.httpsProxy + " HTTP_PROXY=" + b.httpProxy + " skopeo inspect docker://" + b.osImage + " --authfile " + b.remoteDockerConfig
	logger.Infof("Executing skopeo command: %s", skopeoCommand)

	inspectInfo, _, err := b.node.DebugNodeWithChrootStd("bash", "-c", skopeoCommand)
	if err != nil {
		msg := fmt.Sprintf("Skopeo failed inspecting image %s:\n%s\n%s", b.osImage, inspectInfo, err)
		logger.Errorf(msg)
		return "", fmt.Errorf(msg)
	}

	logger.Debugf(inspectInfo)

	inspectJSON := JSON(inspectInfo)
	digestedImage := inspectJSON.Get("Name").ToString() + "@" + inspectJSON.Get("Digest").ToString()

	logger.Infof("Image %s was built and pushed properly", b.osImage)
	logger.Infof("Image %s was digested as %s", b.osImage, digestedImage)

	logger.Infof("OK!\n")
	return digestedImage, nil
}

// CreateAndDigestOsImage create the osImage and returns the image digested
func (b *OsImageBuilderInNode) CreateAndDigestOsImage() (string, error) {
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
