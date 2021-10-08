package common

import (
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal/clients"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
)

const (
	// LeaseDuration is the default duration for the leader election lease.
	LeaseDuration = 137 * time.Second
	// RenewDeadline is the default duration for the leader renewal.
	RenewDeadline = 107 * time.Second
	// RetryPeriod is the default duration for the leader electrion retrial.
	RetryPeriod = 26 * time.Second
)

// CreateResourceLock returns an interface for the resource lock.
func CreateResourceLock(cb *clients.Builder, componentNamespace, componentName string) resourcelock.Interface {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	recorder := record.
		NewBroadcaster().
		NewRecorder(scheme, corev1.EventSource{Component: componentName})

	id, err := os.Hostname()
	if err != nil {
		glog.Fatalf("error creating lock: %v", err)
	}

	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id = id + "_" + string(uuid.NewUUID())

	return &resourcelock.ConfigMapLock{
		ConfigMapMeta: metav1.ObjectMeta{
			Namespace: componentNamespace,
			Name:      componentName,
		},
		Client: cb.KubeClientOrDie("leader-election").CoreV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		},
	}
}
