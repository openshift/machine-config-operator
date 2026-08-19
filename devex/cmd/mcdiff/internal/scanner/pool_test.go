package scanner

import (
	"testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolvePrimaryPoolWorker(t *testing.T) {
	t.Parallel()

	worker := poolWithSelector("worker", map[string]string{"node-role.kubernetes.io/worker": ""})
	n := labeledNode("worker-0", map[string]string{"node-role.kubernetes.io/worker": ""})

	got, err := ResolvePrimaryPool(n, []*mcfgv1.MachineConfigPool{worker})
	require.NoError(t, err)
	assert.Equal(t, "worker", got.Name)
}

func TestResolvePrimaryPoolCustomBeatsWorker(t *testing.T) {
	t.Parallel()

	worker := poolWithSelector("worker", map[string]string{"node-role.kubernetes.io/worker": ""})
	infra := poolWithSelector("infra", map[string]string{"node-role.kubernetes.io/infra": ""})
	n := labeledNode("infra-0", map[string]string{
		"node-role.kubernetes.io/worker": "",
		"node-role.kubernetes.io/infra":  "",
	})

	got, err := ResolvePrimaryPool(n, []*mcfgv1.MachineConfigPool{worker, infra})
	require.NoError(t, err)
	assert.Equal(t, "infra", got.Name)
}

func TestResolvePrimaryPoolMasterBeatsWorker(t *testing.T) {
	t.Parallel()

	master := poolWithSelector("master", map[string]string{"node-role.kubernetes.io/master": ""})
	worker := poolWithSelector("worker", map[string]string{"node-role.kubernetes.io/worker": ""})
	n := labeledNode("master-0", map[string]string{
		"node-role.kubernetes.io/master": "",
		"node-role.kubernetes.io/worker": "",
	})

	got, err := ResolvePrimaryPool(n, []*mcfgv1.MachineConfigPool{master, worker})
	require.NoError(t, err)
	assert.Equal(t, "master", got.Name)
}

func TestResolvePrimaryPoolMultipleCustom(t *testing.T) {
	t.Parallel()

	infra := poolWithSelector("infra", map[string]string{"node-role.kubernetes.io/infra": ""})
	edge := poolWithSelector("edge", map[string]string{"node-role.kubernetes.io/edge": ""})
	n := labeledNode("custom-0", map[string]string{
		"node-role.kubernetes.io/infra": "",
		"node-role.kubernetes.io/edge":  "",
	})

	_, err := ResolvePrimaryPool(n, []*mcfgv1.MachineConfigPool{infra, edge})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMultipleCustomPools)
}

func TestResolvePrimaryPoolUnassigned(t *testing.T) {
	t.Parallel()

	worker := poolWithSelector("worker", map[string]string{"node-role.kubernetes.io/worker": ""})
	n := labeledNode("other-0", map[string]string{"foo": "bar"})

	_, err := ResolvePrimaryPool(n, []*mcfgv1.MachineConfigPool{worker})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNodeUnassigned)
}

func TestResolvePrimaryPoolWindows(t *testing.T) {
	t.Parallel()

	worker := poolWithSelector("worker", map[string]string{"node-role.kubernetes.io/worker": ""})
	n := labeledNode("win-0", map[string]string{
		"node-role.kubernetes.io/worker": "",
		"kubernetes.io/os":               "windows",
	})

	_, err := ResolvePrimaryPool(n, []*mcfgv1.MachineConfigPool{worker})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWindowsNode)
}

func poolWithSelector(name string, matchLabels map[string]string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: mcfgv1.MachineConfigPoolSpec{
			NodeSelector: &metav1.LabelSelector{MatchLabels: matchLabels},
		},
	}
}

func labeledNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}
