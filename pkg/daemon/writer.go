package daemon

import (
	"fmt"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"

	"k8s.io/client-go/tools/cache"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/golang/glog"
	"github.com/openshift/machine-config-operator/internal"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	corev1 "k8s.io/api/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
)

const (
	// defaultWriterQueue the number of pending writes to queue
	defaultWriterQueue = 25

	// machineConfigDaemonSSHAccessAnnotationKey is used to mark a node after it has been accessed via SSH
	machineConfigDaemonSSHAccessAnnotationKey = "machineconfiguration.openshift.io/ssh"
	// MachineConfigDaemonSSHAccessValue is the annotation value applied when ssh access is detected
	machineConfigDaemonSSHAccessValue = "accessed"

	nodeWriterKubeconfigPath = "/var/lib/kubelet/kubeconfig"
)

// message wraps a client and responseChannel
type message struct {
	annos           map[string]string
	responseChannel chan error
}

// clusterNodeWriter is a single writer to Kubernetes to prevent race conditions
type clusterNodeWriter struct {
	nodeName         string
	writer           chan message
	client           corev1client.NodeInterface
	lister           corev1lister.NodeLister
	nodeListerSynced cache.InformerSynced
	kubeClient       kubernetes.Interface
	// cached reference to node object - TODO change the daemon to read this too
	node     *corev1.Node
	recorder record.EventRecorder
}

// NodeWriter is the interface to implement a single writer to Kubernetes to prevent race conditions
type NodeWriter interface {
	Run(stop <-chan struct{})
	SetDone(dcAnnotation string) error
	SetWorking() error
	SetUnreconcilable(err error) error
	SetDegraded(err error) error
	SetSSHAccessed() error
	SetAnnotations(annos map[string]string) error
	Eventf(eventtype, reason, messageFmt string, args ...interface{})
}

func (nw *clusterNodeWriter) handleNodeWriterEvent(node interface{}) {
	n := node.(*corev1.Node)
	if n.Name != nw.nodeName {
		return
	}
	nw.node = n
}

// newNodeWriter Create a new NodeWriter
func newNodeWriter(nodeName string, stopCh <-chan struct{}) (NodeWriter, error) {
	config, err := clientcmd.BuildConfigFromFlags("", nodeWriterKubeconfigPath)
	if err != nil {
		return &clusterNodeWriter{}, err
	}
	kubeClient, err := kubernetes.NewForConfig(rest.AddUserAgent(config, "machine-config-daemon"))
	if err != nil {
		return &clusterNodeWriter{}, err
	}

	glog.Infof("NodeWriter initialized with credentials from %s", nodeWriterKubeconfigPath)
	informer := informers.NewSharedInformerFactory(kubeClient, ctrlcommon.DefaultResyncPeriod()())
	nodeInformer := informer.Core().V1().Nodes()
	nodeLister := nodeInformer.Lister()
	nodeListerSynced := nodeInformer.Informer().HasSynced

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.V(2).Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	nw := &clusterNodeWriter{
		nodeName:         nodeName,
		client:           kubeClient.CoreV1().Nodes(),
		lister:           nodeLister,
		nodeListerSynced: nodeListerSynced,
		recorder:         eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigdaemon", Host: nodeName}),
		writer:           make(chan message, defaultWriterQueue),
		kubeClient:       kubeClient,
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    nw.handleNodeWriterEvent,
		UpdateFunc: func(oldObj, newObj interface{}) { nw.handleNodeWriterEvent(newObj) },
	})

	informer.Start(stopCh)

	return nw, nil
}

// Run reads from the writer channel and sets the node annotation. It will
// return if the stop channel is closed. Intended to be run via a goroutine.
func (nw *clusterNodeWriter) Run(stop <-chan struct{}) {
	if !cache.WaitForCacheSync(stop, nw.nodeListerSynced) {
		glog.Fatal("failed to sync initial listers cache")
	}

	for {
		select {
		case <-stop:
			return
		case msg := <-nw.writer:
			_, err := setNodeAnnotations(nw.client, nw.lister, nw.nodeName, msg.annos)
			msg.responseChannel <- err
		}
	}
}

// SetDone sets the state to Done.
func (nw *clusterNodeWriter) SetDone(dcAnnotation string) error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
		constants.CurrentMachineConfigAnnotationKey:     dcAnnotation,
		// clear out any Degraded/Unreconcilable reason
		constants.MachineConfigDaemonReasonAnnotationKey: "",
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateDone, "").SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetWorking sets the state to Working.
func (nw *clusterNodeWriter) SetWorking() error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateWorking,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateWorking, "").SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

// SetUnreconcilable sets the state to Unreconcilable.
func (nw *clusterNodeWriter) SetUnreconcilable(err error) error {
	glog.Errorf("Marking Unreconcilable due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateUnreconcilable,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateUnreconcilable, truncatedErr).SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	clientErr := <-respChan
	if clientErr != nil {
		glog.Errorf("Error setting Unreconcilable annotation for node %s: %v", nw.nodeName, clientErr)
	}
	return clientErr
}

// SetDegraded logs the error and sets the state to Degraded.
// Returns an error if it couldn't set the annotation.
func (nw *clusterNodeWriter) SetDegraded(err error) error {
	glog.Errorf("Marking Degraded due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateDegraded,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	MCDState.WithLabelValues(constants.MachineConfigDaemonStateDegraded, truncatedErr).SetToCurrentTime()
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	clientErr := <-respChan
	if clientErr != nil {
		glog.Errorf("Error setting Degraded annotation for node %s: %v", nw.nodeName, clientErr)
	}
	return clientErr
}

// SetSSHAccessed sets the ssh annotation to accessed
func (nw *clusterNodeWriter) SetSSHAccessed() error {
	MCDSSHAccessed.Inc()
	annos := map[string]string{
		machineConfigDaemonSSHAccessAnnotationKey: machineConfigDaemonSSHAccessValue,
	}
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	return <-respChan
}

func (nw *clusterNodeWriter) SetAnnotations(annos map[string]string) error {
	respChan := make(chan error, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	err := <-respChan
	return err
}

func (nw *clusterNodeWriter) Eventf(eventtype, reason, messageFmt string, args ...interface{}) {
	if nw.node == nil {
		return
	}
	nw.recorder.Eventf(getNodeRef(nw.node), eventtype, reason, messageFmt, args...)
}

func setNodeAnnotations(client corev1client.NodeInterface, lister corev1lister.NodeLister, nodeName string, m map[string]string) (*corev1.Node, error) {
	node, err := internal.UpdateNodeRetry(client, lister, nodeName, func(node *corev1.Node) {
		for k, v := range m {
			node.Annotations[k] = v
		}
	})
	return node, err
}
