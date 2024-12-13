package builders

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/openshift/machine-config-operator/hack/internal/pkg/errors"
	"k8s.io/klog/v2"
)

type podmanBuilder struct {
	opts Opts
}

func newPodmanBuilder(opts Opts) Builder {
	return &podmanBuilder{opts: opts}
}

func (p *podmanBuilder) Build() error {

	if err := p.buildContainer(); err != nil {
		return fmt.Errorf("unable to build container: %w", err)
	}

	return nil
}

func (p *podmanBuilder) Push() error {
	if err := p.tagContainerForPush(); err != nil {
		return fmt.Errorf("could not tag container: %w", err)
	}

	if err := p.pushContainer(); err != nil {
		klog.Info("Push failed, falling back to Skopeo")
		return pushWithSkopeo(p.opts, BuilderTypePodman)
	}

	return nil
}

func (p *podmanBuilder) tagContainerForPush() error {
	cmd := exec.Command("podman", "tag", localPullspec, p.opts.FinalPullspec)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.NewExecError(cmd, out, err)
	}

	return nil
}

func (p *podmanBuilder) buildContainer() error {
	podmanOpts := []string{"build", "-t", localPullspec, "--network", "slirp4netns", "--jobs", "3", "--file", p.opts.DockerfileName, p.opts.RepoRoot}
	if p.opts.PullSecretPath != "" {
		podmanOpts = append([]string{"--authfile", p.opts.PullSecretPath}, podmanOpts...)
	}

	cmd := exec.Command("podman", podmanOpts...)
	cmd.Dir = p.opts.RepoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	klog.Infof("Running %s", cmd)
	return cmd.Run()
}

func (p *podmanBuilder) pushContainer() error {
	podmanPushOpts := []string{"--authfile", p.opts.PushSecretPath, p.opts.FinalPullspec}
	if p.opts.isDirectClusterPush() {
		podmanPushOpts = append([]string{"--tls-verify=false"}, podmanPushOpts...)
	}

	podmanPushOpts = append([]string{"push"}, podmanPushOpts...)

	cmd := exec.Command("podman", podmanPushOpts...)
	cmd.Dir = p.opts.RepoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	klog.Infof("Running %s", cmd)
	return cmd.Run()
}
