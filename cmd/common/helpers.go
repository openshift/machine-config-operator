package common

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal/clients"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"

	"context"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusterstatus"
	"github.com/openshift/library-go/pkg/config/leaderelection"
	"k8s.io/client-go/rest"
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

// GetLeaderElectionConfig returns leader election configs defaults based on the cluster topology
func GetLeaderElectionConfig(restcfg *rest.Config) configv1.LeaderElection {

	// Defaults follow conventions
	// https://github.com/openshift/enhancements/blob/master/CONVENTIONS.md#high-availability
	defaultLeaderElection := leaderelection.LeaderElectionDefaulting(
		configv1.LeaderElection{},
		"", "",
	)

	if infra, err := clusterstatus.GetClusterInfraStatus(context.TODO(), restcfg); err == nil && infra != nil {
		if infra.ControlPlaneTopology == configv1.SingleReplicaTopologyMode {
			return leaderelection.LeaderElectionSNOConfig(defaultLeaderElection)
		}
	} else {
		glog.Warningf("unable to get cluster infrastructure status, using HA cluster values for leader election: %v", err)
	}

	return defaultLeaderElection
}

// SignalHandler catches SIGINT/SIGTERM signals and makes sure the passed context gets cancelled when those signals happen. This allows us to use a
// context to shut down our operations cleanly when we are signalled to shutdown.
func SignalHandler(runCancel context.CancelFunc) {

	// make a signal handling channel for os signals
	ch := make(chan os.Signal, 1)
	// stop listening for signals when we leave this function
	defer func() { signal.Stop(ch) }()
	// catch SIGINT and SIGTERM
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	sig := <-ch
	glog.Infof("Shutting down due to: %s", sig)
	// if we're shutting down, cancel the context so everything else will stop
	runCancel()
	glog.Infof("Context cancelled")
	sig = <-ch
	glog.Fatalf("Received shutdown signal twice, exiting: %s", sig)

}
