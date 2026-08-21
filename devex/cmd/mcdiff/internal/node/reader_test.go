package node

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHostFilePath(t *testing.T) {
	t.Parallel()

	got, err := hostFilePath("/etc/ssh/sshd_config")
	require.NoError(t, err)
	assert.Equal(t, "/rootfs/etc/ssh/sshd_config", got)

	_, err = hostFilePath("etc/ssh/sshd_config")
	require.Error(t, err)

	_, err = hostFilePath("")
	require.Error(t, err)
}

func TestReadFileMatch(t *testing.T) {
	t.Parallel()

	execer := &scriptedExec{statOut: []byte("644\n"), catOut: []byte("PermitRootLogin no\n")}
	r := newTestReader(t, execer, testNode("worker-0"), testMCDPod("worker-0", corev1.PodRunning))

	content, mode, err := r.ReadFile(context.Background(), "worker-0", "/etc/ssh/sshd_config")
	require.NoError(t, err)
	assert.Equal(t, []byte("PermitRootLogin no\n"), content)
	require.NotNil(t, mode)
	assert.Equal(t, 0o644, *mode)
	require.Len(t, execer.cmds, 2)
	assert.Equal(t, []string{"stat", "-c", "%a", "/rootfs/etc/ssh/sshd_config"}, execer.cmds[0])
	assert.Equal(t, []string{"cat", "/rootfs/etc/ssh/sshd_config"}, execer.cmds[1])
}

func TestReadFileEmpty(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, &scriptedExec{statOut: []byte("644\n"), catOut: []byte{}}, testNode("worker-0"), testMCDPod("worker-0", corev1.PodRunning))
	content, mode, err := r.ReadFile(context.Background(), "worker-0", "/etc/empty")
	require.NoError(t, err)
	assert.Equal(t, []byte{}, content)
	require.NotNil(t, mode)
}

func TestReadFileMissing(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, &scriptedExec{
		statErr:    errors.New("stat: cannot statx '/rootfs/etc/missing': No such file or directory"),
		statErrOut: []byte("stat: cannot statx '/rootfs/etc/missing': No such file or directory\n"),
	}, testNode("worker-0"), testMCDPod("worker-0", corev1.PodRunning))

	_, _, err := r.ReadFile(context.Background(), "worker-0", "/etc/missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFileNotFound)
}

func TestReadFilePermissionDenied(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, &scriptedExec{
		statErr:    errors.New("exit 1"),
		statErrOut: []byte("stat: cannot statx '/rootfs/etc/shadow': Permission denied\n"),
	}, testNode("worker-0"), testMCDPod("worker-0", corev1.PodRunning))

	_, _, err := r.ReadFile(context.Background(), "worker-0", "/etc/shadow")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermissionDenied)
}

func TestReadFileNodeNotFound(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, &scriptedExec{}, testNode("other"))
	_, _, err := r.ReadFile(context.Background(), "worker-0", "/etc/ssh/sshd_config")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNodeNotFound)
}

func TestReadFileMCDMissing(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, &scriptedExec{}, testNode("worker-0"))
	_, _, err := r.ReadFile(context.Background(), "worker-0", "/etc/ssh/sshd_config")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMCDUnavailable)
}

func TestReadFileMCDNotRunning(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, &scriptedExec{}, testNode("worker-0"), testMCDPod("worker-0", corev1.PodPending))
	_, _, err := r.ReadFile(context.Background(), "worker-0", "/etc/ssh/sshd_config")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMCDUnavailable)
	assert.Contains(t, err.Error(), "not running")
}

func TestReadFileTimeout(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, &scriptedExec{statErr: context.DeadlineExceeded}, testNode("worker-0"), testMCDPod("worker-0", corev1.PodRunning))
	_, _, err := r.ReadFile(context.Background(), "worker-0", "/etc/ssh/sshd_config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

type scriptedExec struct {
	statOut    []byte
	statErrOut []byte
	statErr    error
	catOut     []byte
	catErrOut  []byte
	catErr     error
	cmds       [][]string
}

func (s *scriptedExec) Exec(_ context.Context, _, _, _ string, command []string) ([]byte, []byte, error) {
	s.cmds = append(s.cmds, append([]string(nil), command...))
	if len(command) > 0 && command[0] == "stat" {
		return s.statOut, s.statErrOut, s.statErr
	}
	return s.catOut, s.catErrOut, s.catErr
}

func newTestReader(t *testing.T, execer commandExecutor, objs ...runtime.Object) *kubeReader {
	t.Helper()
	return &kubeReader{
		kube:    fake.NewSimpleClientset(objs...),
		execer:  execer,
		timeout: time.Second,
	}
}

func testNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func testMCDPod(nodeName string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("machine-config-daemon-%s", nodeName),
			Namespace: ctrlcommon.MCONamespace,
			Labels:    map[string]string{mcdDaemonLabel: mcdDaemonValue},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: mcdContainer}},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}
