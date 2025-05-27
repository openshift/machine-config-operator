package builders

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/openshift/machine-config-operator/devex/internal/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

type BuilderType string

const (
	BuilderTypePodman    BuilderType = "podman"
	BuilderTypeDocker    BuilderType = "docker"
	BuilderTypeOpenshift BuilderType = "openshift"
	BuilderTypeUnknown   BuilderType = "unknown-builder-type"
)

const (
	localPullspec string = "localhost/machine-config-operator:latest"
)

type Builder interface {
	Build() error
	Push() error
}

type Opts struct {
	RepoRoot       string
	PullSecretPath string
	PushSecretPath string
	FinalPullspec  string
	DockerfileName string
	BuildMode      string
}

func (o *Opts) isDirectClusterPush() bool {
	return strings.Contains(o.FinalPullspec, "image-registry-openshift-image-registry")
}

func NewLocalBuilder(opts Opts) Builder {
	return newPodmanBuilder(opts)
}

func GetBuilderTypes() sets.Set[BuilderType] {
	return GetLocalBuilderTypes().Insert(BuilderTypeOpenshift)
}

func GetLocalBuilderTypes() sets.Set[BuilderType] {
	return sets.New[BuilderType](BuilderTypePodman, BuilderTypeDocker)
}

func GetDefaultBuilderTypeForPlatform() BuilderType {
	if _, err := exec.LookPath("podman"); err == nil {
		return BuilderTypePodman
	}

	if _, err := exec.LookPath("docker"); err == nil {
		return BuilderTypeDocker
	}

	return BuilderTypeUnknown
}

func pushWithSkopeo(opts Opts, builder BuilderType) error {
	imgStorageMap := map[BuilderType]string{
		BuilderTypePodman: "containers-storage",
		BuilderTypeDocker: "docker-daemon",
	}

	imgStorage, ok := imgStorageMap[builder]
	if !ok {
		return fmt.Errorf("unknown builder type %s", imgStorage)
	}

	skopeoOpts := []string{
		"--dest-authfile",
		opts.PushSecretPath,
		fmt.Sprintf("%s:%s", imgStorage, localPullspec),
		fmt.Sprintf("docker://%s", opts.FinalPullspec),
	}

	if opts.isDirectClusterPush() {
		skopeoOpts = append([]string{"copy", "--dest-tls-verify=false"}, skopeoOpts...)
	} else {
		skopeoOpts = append([]string{"copy"}, skopeoOpts...)
	}

	cmd := exec.Command("skopeo", skopeoOpts...)
	klog.Infof("Running $ %s", cmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return errors.NewExecErrorNoOutput(cmd, err)
	}

	return nil
}
