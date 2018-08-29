package daemon

import (
	"errors"
	"testing"

	mcfgclientset "github.com/openshift/machine-config-operator/pkg/generated/clientset/versioned/fake"
	kubernetes "k8s.io/client-go/kubernetes/fake"
	kubernetescorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

// mockLoadNodeAnnotations returns an implementation of NodeAnnotationLoader.
// If succeed is true the NodeAnnotationLoader implementation will return nil error.
// If succeed is false the NodeAnnotationLoader implementation will return an error.
func mockLoadNodeAnnotations(succeed bool) func(kubernetescorev1.NodeInterface, string) error {
	return func(kubernetescorev1.NodeInterface, string) error {
		if succeed == true {
			return nil
		}
		return errors.New("mock failure")
	}
}

// TestNew ensures that proper Daemon instances are created
func TestNew(t *testing.T) {
	clientSet := mcfgclientset.NewSimpleClientset()
	kubeClient := kubernetes.NewSimpleClientset()
	rootFS := "/"
	nodeName := "node"

	// Verify a successful creation
	daemon, err := New(rootFS, nodeName, clientSet, kubeClient, mockLoadNodeAnnotations(true))
	if err != nil {
		t.Fatalf("unable to create Daemon: %s", err)
	}
	if daemon == nil {
		t.Fatalf("expected daemon to be of type daemon.Daemon, found nil")
	}

	// There should be no Daemon if loadNodeAnnotations doesn't work
	daemon, err = New(rootFS, nodeName, clientSet, kubeClient, mockLoadNodeAnnotations(false))
	if daemon != nil {
		t.Fatalf("expected daemon=nil, found daemon=%+v", daemon)
	}
	if err == nil {
		t.Fatalf("expected err to be returned, instead got nil")
	}
}
