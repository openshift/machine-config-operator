package common

import (
	"bytes"
	"io/ioutil"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal/clients"
	"github.com/pkg/errors"
	"gopkg.in/fsnotify.v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
)

const (
	// LeaseDuration is the default duration for the leader election lease.
	LeaseDuration = 90 * time.Second
	// RenewDeadline is the default duration for the leader renewal.
	RenewDeadline = 60 * time.Second
	// RetryPeriod is the default duration for the leader electrion retrial.
	RetryPeriod = 30 * time.Second

	// defaultTrustedCABundle is the fully qualified path of the trusted CA bundle
	// that is mounted from configmap openshift-ingress-operator/trusted-ca.
	defaultTrustedCABundle = "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
)

// CreateResourceLock returns an interface for the resource lock.
func CreateResourceLock(cb *clients.Builder, componentNamespace, componentName string) resourcelock.Interface {
	recorder := record.
		NewBroadcaster().
		NewRecorder(runtime.NewScheme(), corev1.EventSource{Component: componentName})

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

// TrustedCAWatcher is an helper type to setup a file watcher on the trusted CA
type TrustedCAWatcher struct {
	watcher  *fsnotify.Watcher
	original []byte
}

// NewTrustedCAWatcher creates a new instance of the trusted CA watcher
func NewTrustedCAWatcher() (trustedCAWatcher *TrustedCAWatcher, retErr error) {
	// Set up and start the file watcher.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create watcher")
	}
	defer func() {
		if retErr != nil {
			watcher.Close()
		}
	}()
	if err := watcher.Add(defaultTrustedCABundle); err != nil {
		return nil, errors.Wrapf(err, "failed to add file %q to watcher", defaultTrustedCABundle)
	}
	orig, err := ioutil.ReadFile(defaultTrustedCABundle)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read watched file %q", defaultTrustedCABundle)
	}
	trustedCAWatcher = &TrustedCAWatcher{}
	trustedCAWatcher.watcher = watcher
	trustedCAWatcher.original = orig
	return
}

// Close closes the internal watcher
func (trustedCAWatcher *TrustedCAWatcher) Close() {
	trustedCAWatcher.watcher.Close()
}

// Run loops listening to file events. Most of the time you'd want this to be run in a goroutine
func (trustedCAWatcher *TrustedCAWatcher) Run(stopCh chan struct{}) {
	for {
		select {
		case event := <-trustedCAWatcher.watcher.Events:
			if event.Op == fsnotify.Remove {
				trustedCAWatcher.watcher.Remove(event.Name)
				trustedCAWatcher.watcher.Add(defaultTrustedCABundle)
			}
			latest, err := ioutil.ReadFile(defaultTrustedCABundle)
			if err != nil {
				glog.Errorf("failed to read watched file %q: %v", defaultTrustedCABundle, err)
				close(stopCh)
				return
			}
			if !bytes.Equal(trustedCAWatcher.original, latest) {
				glog.Infof("watched file %q changed, stopping operator", defaultTrustedCABundle)
				close(stopCh)
				return
			}
		case err := <-trustedCAWatcher.watcher.Errors:
			if err != nil {
				glog.Errorf("file watch error: %v", err)
			}
		}
	}
}
