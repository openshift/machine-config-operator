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

	"github.com/openshift/machine-config-operator/internal"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/state"
	corev1 "k8s.io/api/core/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	corev1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

const (
	// defaultWriterQueue the number of pending writes to queue
	defaultWriterQueue = 25

	nodeWriterKubeconfigPath = "/var/lib/kubelet/kubeconfig"
)

type response struct {
	node *corev1.Node
	err  error
}

// message wraps a client and responseChannel
type message struct {
	annos           map[string]string
	annosToDelete   []string
	responseChannel chan response
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

	clusterNodeWriterMetricEvents record.EventRecorder
	stateControllerPod            *corev1.Pod
}

// NodeWriter is the interface to implement a single writer to Kubernetes to prevent race conditions
type NodeWriter interface {
	Run(stop <-chan struct{}, clusterNodeWriterMetricEvents record.EventRecorder)
	SetDone(*stateAndConfigs) error
	SetWorking() error
	SetUnreconcilable(err error) error
	SetDegraded(err error) error
	SetAnnotations(annos map[string]string) (*corev1.Node, error)
	SetDesiredDrainer(value string) error
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

	klog.Infof("NodeWriter initialized with credentials from %s", nodeWriterKubeconfigPath)
	informer := informers.NewSharedInformerFactory(kubeClient, ctrlcommon.DefaultResyncPeriod()())
	nodeInformer := informer.Core().V1().Nodes()
	nodeLister := nodeInformer.Lister()
	nodeListerSynced := nodeInformer.Informer().HasSynced

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.V(2).Infof)
	eventBroadcaster.StartRecordingToSink(&corev1client.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	nw := &clusterNodeWriter{
		nodeName:         nodeName,
		client:           kubeClient.CoreV1().Nodes(),
		lister:           nodeLister,
		nodeListerSynced: nodeListerSynced,
		recorder:         ctrlcommon.NamespacedEventRecorder(eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "machineconfigdaemon", Host: nodeName})),
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
func (nw *clusterNodeWriter) Run(stop <-chan struct{}, clusterNodeWriterMetricEvents record.EventRecorder) {

	healthPod, err := state.StateControllerPod(nw.kubeClient)
	if err != nil {
		klog.Error(err)
	}

	if healthPod != nil {
		nw.stateControllerPod = healthPod
	}

	nw.clusterNodeWriterMetricEvents = clusterNodeWriterMetricEvents

	if !cache.WaitForCacheSync(stop, nw.nodeListerSynced) {
		klog.Fatal("failed to sync initial listers cache")
	}

	for {
		select {
		case <-stop:
			return
		case msg := <-nw.writer:
			r := implSetNodeAnnotations(nw.client, nw.lister, nw.nodeName, msg.annos, msg.annosToDelete)
			msg.responseChannel <- r
		}
	}
}

// SetDone sets the state to Done.
func (nw *clusterNodeWriter) SetDone(state *stateAndConfigs) error {
	// To address some confusion around why SetDone() sets the annotations to
	// currentConfig and currentImage instead of desiredConfig and desiredImage:
	//
	// 1. After we've applied the update, we write the "new config" and "new image"
	// to the nodes' filesystem in `/etc/machine-config-daemon/currentconfig` and
	// `/etc/machine-config-daemon/currentimage`, respectively. In this case, the
	// "new config" and "new image" are the `desiredConfig` and `desiredImage`. We
	// then reboot the node (for updates that require it, anyway).
	// 2. After we reboot, when we call `getCurrentConfigOnDisk()`, the values it
	// loads become `currentConfig` and `currentImage`, respectively.
	// 3. SetDone() gets called after the node reboot (for updates that require
	// it). Because of the above point, `SetDone()` gets called with the current on
	// disk config and image.
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateDone,
		constants.CurrentMachineConfigAnnotationKey:     state.currentConfig.GetName(),
		// clear out any Degraded/Unreconcilable reason
		constants.MachineConfigDaemonReasonAnnotationKey: "",
	}

	// If current image is not empty, update the annotation.
	if state.currentImage != "" {
		annos[constants.CurrentImageAnnotationKey] = state.currentImage
	}

	annosToDelete := []string{}
	// If current image is empty, delete the annotation, if it exists.
	if state.currentImage == "" {
		annosToDelete = append(annosToDelete, constants.CurrentImageAnnotationKey)
	}

	// If desired image is empty, delete the annotation, if it exists.
	if state.desiredImage == "" {
		annosToDelete = append(annosToDelete, constants.DesiredImageAnnotationKey)
	}

	nw.UpdateStateMetric("mcdState", constants.MachineConfigDaemonStateDone, "")
	respChan := make(chan response, 1)
	nw.writer <- message{
		annos:           annos,
		annosToDelete:   annosToDelete,
		responseChannel: respChan,
	}
	r := <-respChan
	return r.err
}

// SetWorking sets the state to Working.
func (nw *clusterNodeWriter) SetWorking() error {
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey: constants.MachineConfigDaemonStateWorking,
	}
	nw.UpdateStateMetric("mcdState", constants.MachineConfigDaemonStateWorking, "")
	respChan := make(chan response, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	r := <-respChan
	return r.err
}

// SetUnreconcilable sets the state to Unreconcilable.
func (nw *clusterNodeWriter) SetUnreconcilable(err error) error {
	klog.Errorf("Marking Unreconcilable due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateUnreconcilable,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	nw.UpdateStateMetric("mcdState", constants.MachineConfigDaemonStateUnreconcilable, truncatedErr)
	respChan := make(chan response, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	r := <-respChan
	if r.err != nil {
		klog.Errorf("Error setting Unreconcilable annotation for node %s: %v", nw.nodeName, r.err)
	}
	return r.err
}

// SetDegraded logs the error and sets the state to Degraded.
// Returns an error if it couldn't set the annotation.
func (nw *clusterNodeWriter) SetDegraded(err error) error {
	klog.Errorf("Marking Degraded due to: %v", err)
	// truncatedErr caps error message at a reasonable length to limit the risk of hitting the total
	// annotation size limit (256 kb) at any point
	truncatedErr := fmt.Sprintf("%.2000s", err.Error())
	annos := map[string]string{
		constants.MachineConfigDaemonStateAnnotationKey:  constants.MachineConfigDaemonStateDegraded,
		constants.MachineConfigDaemonReasonAnnotationKey: truncatedErr,
	}
	nw.UpdateStateMetric("mcdState", constants.MachineConfigDaemonStateDegraded, truncatedErr)
	respChan := make(chan response, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	r := <-respChan
	if r.err != nil {
		klog.Errorf("Error setting Degraded annotation for node %s: %v", nw.nodeName, r.err)
	}
	return r.err
}

func (nw *clusterNodeWriter) SetAnnotations(annos map[string]string) (*corev1.Node, error) {
	respChan := make(chan response, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	resp := <-respChan
	return resp.node, resp.err
}

func (nw *clusterNodeWriter) SetDesiredDrainer(value string) error {
	annos := map[string]string{
		constants.DesiredDrainerAnnotationKey: value,
	}
	respChan := make(chan response, 1)
	nw.writer <- message{
		annos:           annos,
		responseChannel: respChan,
	}
	r := <-respChan
	return r.err
}

func (nw *clusterNodeWriter) Eventf(eventtype, reason, messageFmt string, args ...interface{}) {
	if nw.node == nil {
		return
	}
	nw.recorder.Eventf(getNodeRef(nw.node), eventtype, reason, messageFmt, args...)
}

func implSetNodeAnnotations(client corev1client.NodeInterface, lister corev1lister.NodeLister, nodeName string, m map[string]string, toDel []string) response {
	node, err := internal.UpdateNodeRetry(client, lister, nodeName, func(node *corev1.Node) {
		if toDel != nil {
			for _, anno := range toDel {
				if _, ok := node.Annotations[anno]; ok {
					klog.V(4).Infof("Deleted annotation %s", anno)
					delete(node.Annotations, anno)
				}
			}
		}

		for k, v := range m {
			node.Annotations[k] = v
		}
	})
	return response{
		node: node,
		err:  err,
	}
}

func (nw *clusterNodeWriter) UpdateStateMetric(metric string, labels ...string) {
	// metric.Reset()
	// Migrate the metric update to eventing
	var annos map[string]string
	if nw.node == nil {
		annos = state.WriteMetricAnnotations("None", "Firstboot")
	} else {
		annos = state.WriteMetricAnnotations(string(mcfgv1.Node), nw.node.Name)
	}
	state.EmitMetricEvent(nw.clusterNodeWriterMetricEvents, nw.stateControllerPod, nw.kubeClient, annos, corev1.EventTypeNormal, metric, fmt.Sprintf("reset"))

	// metric.WithLabelValues(labels...).SetToCurrentTime()
	// Migrate the metric update to eventing TBD
	// Need to find a common event msg/ struct to store metric update information
}
