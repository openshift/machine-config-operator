package common

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/openshift/machine-config-operator/internal/clients"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"context"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusterstatus"
	"github.com/openshift/library-go/pkg/config/leaderelection"
	"k8s.io/client-go/rest"
	k8sleaderelection "k8s.io/client-go/tools/leaderelection"
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
		klog.Fatalf("error creating lock: %v", err)
	}

	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id = id + "_" + string(uuid.NewUUID())

	kubeClient := cb.KubeClientOrDie("leader-election")

	lock, err := resourcelock.New(
		resourcelock.LeasesResourceLock,
		componentNamespace,
		componentName,
		kubeClient.CoreV1(),
		kubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		},
	)

	if err != nil {
		panic(err)
	}

	return lock
}

type RunOpts struct {
	Namespace     string
	ComponentName string
	Builder       *clients.Builder
	OnStart       func(context.Context)
	OnStop        func()
}

func DoLeaderElectionAndRunOrDie(ctx context.Context, o *RunOpts) {
	onStop := func() {
		if o.OnStop != nil {
			o.OnStop()
		}

		klog.Infof("Stopped leading, terminating %s", o.ComponentName)
	}

	cfg := GetLeaderElectionConfig(o.Builder.GetBuilderConfig())

	k8sleaderelection.RunOrDie(ctx, k8sleaderelection.LeaderElectionConfig{
		Lock:            CreateResourceLock(o.Builder, o.Namespace, o.ComponentName),
		ReleaseOnCancel: true,
		LeaseDuration:   cfg.LeaseDuration.Duration,
		RenewDeadline:   cfg.RenewDeadline.Duration,
		RetryPeriod:     cfg.RetryPeriod.Duration,
		Callbacks: k8sleaderelection.LeaderCallbacks{
			OnStartedLeading: o.OnStart,
			OnStoppedLeading: onStop,
		},
	})
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
		klog.Warningf("unable to get cluster infrastructure status, using HA cluster values for leader election: %v", err)
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
	klog.Infof("Shutting down due to: %s", sig)
	// if we're shutting down, cancel the context so everything else will stop
	runCancel()
	klog.Infof("Context cancelled")
	sig = <-ch
	klog.Fatalf("Received shutdown signal twice, exiting: %s", sig)

}
