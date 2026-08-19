package node

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/klog/v2"
)

const (
	mcdContainer = "machine-config-daemon"
	// hostRoot is where the MCD mounts the node's root filesystem. This is the
	// same host tree oc debug node exposes at /host.
	hostRoot       = "/rootfs"
	defaultTimeout = 45 * time.Second
	mcdDaemonLabel = "k8s-app"
	mcdDaemonValue = "machine-config-daemon"
)

// Reader reads a file from a live node's host filesystem.
type Reader interface {
	ReadFile(ctx context.Context, nodeName, path string) (content []byte, mode *int, err error)
}

type commandExecutor interface {
	Exec(ctx context.Context, namespace, pod, container string, command []string) (stdout, stderr []byte, err error)
}

type kubeReader struct {
	kube    kubernetes.Interface
	execer  commandExecutor
	timeout time.Duration
}

// NewKubeReader returns a Reader that execs into the machine-config-daemon pod
// on the named node and reads from the host rootfs mount.
func NewKubeReader(kube kubernetes.Interface, config *rest.Config) Reader {
	return &kubeReader{
		kube:    kube,
		execer:  newSPDYExecutor(kube, config),
		timeout: defaultTimeout,
	}
}

func (r *kubeReader) ReadFile(ctx context.Context, nodeName, filePath string) ([]byte, *int, error) {
	if r == nil || r.kube == nil {
		return nil, nil, fmt.Errorf("node reader is not configured")
	}
	if nodeName == "" {
		return nil, nil, fmt.Errorf("node name must not be empty")
	}
	hostPath, err := hostFilePath(filePath)
	if err != nil {
		return nil, nil, err
	}

	if _, ok := ctx.Deadline(); !ok && r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	if _, err := r.kube.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, wrapNodeNotFound(nodeName, err)
		}
		return nil, nil, fmt.Errorf("failed to get node %q: %w", nodeName, err)
	}

	pod, err := r.mcdPod(ctx, nodeName)
	if err != nil {
		return nil, nil, err
	}
	if pod.Status.Phase != corev1.PodRunning {
		return nil, nil, wrapMCDUnavailable(nodeName, fmt.Errorf("pod %q is not running (phase %s)", pod.Name, pod.Status.Phase))
	}

	klog.V(2).Infof("reading %s on node %s via machine-config-daemon pod %s", filePath, nodeName, pod.Name)

	mode, err := r.statHostFile(ctx, pod.Name, nodeName, filePath, hostPath)
	if err != nil {
		return nil, nil, err
	}

	stdout, stderr, err := r.execer.Exec(ctx, ctrlcommon.MCONamespace, pod.Name, mcdContainer, []string{"cat", hostPath})
	if err != nil {
		return nil, mode, classifyExecError(nodeName, filePath, stderr, err)
	}
	if looksMissing(stderr) {
		return nil, nil, wrapFileNotFound(nodeName, filePath)
	}
	if looksDenied(stderr) {
		return nil, mode, wrapPermissionDenied(nodeName, filePath, nil)
	}
	if stdout == nil {
		stdout = []byte{}
	}
	return stdout, mode, nil
}

func (r *kubeReader) statHostFile(ctx context.Context, podName, nodeName, filePath, hostPath string) (*int, error) {
	stdout, stderr, err := r.execer.Exec(ctx, ctrlcommon.MCONamespace, podName, mcdContainer, []string{"stat", "-c", "%a", hostPath})
	if err != nil {
		return nil, classifyExecError(nodeName, filePath, stderr, err)
	}
	if looksMissing(stderr) || looksMissing(stdout) {
		return nil, wrapFileNotFound(nodeName, filePath)
	}
	if looksDenied(stderr) {
		return nil, wrapPermissionDenied(nodeName, filePath, nil)
	}
	mode, parseErr := parseOctalMode(strings.TrimSpace(string(stdout)))
	if parseErr != nil {
		klog.V(4).Infof("could not parse mode from stat on node %s path %s: %v", nodeName, filePath, parseErr)
		return nil, nil
	}
	return &mode, nil
}

func (r *kubeReader) mcdPod(ctx context.Context, nodeName string) (*corev1.Pod, error) {
	list, err := r.kube.CoreV1().Pods(ctrlcommon.MCONamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{mcdDaemonLabel: mcdDaemonValue}).String(),
		FieldSelector: fields.SelectorFromSet(fields.Set{"spec.nodeName": nodeName}).String(),
	})
	if err != nil {
		return nil, wrapMCDUnavailable(nodeName, err)
	}
	running := make([]corev1.Pod, 0, len(list.Items))
	for _, p := range list.Items {
		if p.DeletionTimestamp != nil {
			continue
		}
		running = append(running, p)
	}
	if len(running) == 0 {
		return nil, wrapMCDUnavailable(nodeName, fmt.Errorf("no machine-config-daemon pod on node %s", nodeName))
	}
	if len(running) > 1 {
		return nil, wrapMCDUnavailable(nodeName, fmt.Errorf("found %d machine-config-daemon pods on node %s", len(running), nodeName))
	}
	return &running[0], nil
}

func hostFilePath(filePath string) (string, error) {
	if filePath == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if !strings.HasPrefix(filePath, "/") {
		return "", fmt.Errorf("path %q must be an absolute Unix path", filePath)
	}
	if strings.Contains(filePath, "\x00") {
		return "", fmt.Errorf("path %q is invalid", filePath)
	}
	cleaned := path.Clean(filePath)
	if cleaned == "/" {
		return "", fmt.Errorf("path %q is not a file", filePath)
	}
	return path.Join(hostRoot, cleaned), nil
}

func parseOctalMode(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty mode")
	}
	var mode int
	n, err := fmt.Sscanf(s, "%o", &mode)
	if err != nil || n != 1 {
		return 0, fmt.Errorf("invalid mode %q", s)
	}
	return mode, nil
}

func classifyExecError(nodeName, filePath string, stderr []byte, err error) error {
	msg := string(stderr)
	if err != nil {
		msg += err.Error()
	}
	if looksDenied([]byte(msg)) {
		return wrapPermissionDenied(nodeName, filePath, err)
	}
	if looksMissing([]byte(msg)) {
		return wrapFileNotFound(nodeName, filePath)
	}
	if err == context.DeadlineExceeded || strings.Contains(err.Error(), "deadline exceeded") {
		return fmt.Errorf("timed out reading %q from node %q: %w", filePath, nodeName, err)
	}
	return fmt.Errorf("failed to read %q from node %q: %w", filePath, nodeName, err)
}

func looksMissing(b []byte) bool {
	return strings.Contains(strings.ToLower(string(b)), "no such file")
}

func looksDenied(b []byte) bool {
	return strings.Contains(strings.ToLower(string(b)), "permission denied")
}

type spdyExecutor struct {
	kube   kubernetes.Interface
	config *rest.Config
}

func newSPDYExecutor(kube kubernetes.Interface, config *rest.Config) commandExecutor {
	return &spdyExecutor{kube: kube, config: config}
}

func (s *spdyExecutor) Exec(ctx context.Context, namespace, pod, container string, command []string) ([]byte, []byte, error) {
	if s.config == nil {
		return nil, nil, fmt.Errorf("rest config is nil")
	}
	req := s.kube.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(s.config, "POST", req.URL())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create exec executor: %w", err)
	}

	var stdout, stderr strings.Builder
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return []byte(stdout.String()), []byte(stderr.String()), err
}
