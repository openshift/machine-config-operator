package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

// Handles graceful shutdowns of the OS Build Controller.
type shutdownDelayHandler struct {
	// Holds the listers struct containing all of the listers we've previously
	// instantiated.
	*listers
	// A Clock object which is mockable for testing purposes.
	clock clock.Clock
}

// Instantiates the shutdown delay handler.
func newShutdownDelayHandler(l *listers) *shutdownDelayHandler {
	return &shutdownDelayHandler{
		listers: l,
		clock:   clock.RealClock{},
	}
}

// The entrypoint into the shutdown delay state loop. This loop will check for
// child objects at the poll interval that was passed. It will return under the
// following conditions:.
// 1. No child objects were foud.
// 2. The provided context was cancelled.
// 3. An error is encountered.
func (s *shutdownDelayHandler) handleShutdown(ctx context.Context, pollInterval time.Duration) error {
	klog.Infof("Polling every %s to check for child objects", pollInterval)
	start := time.Now()
	for {
		select {
		case <-s.clock.After(pollInterval):
			// After every interval, we check to see whether another additional delay is needed.
			isDelayNeeded, err := s.isDelayNeeded()
			if err != nil {
				return err
			}

			if !isDelayNeeded {
				klog.Infof("All child objects are either deleted, marked for deletion, or not orphaned after %s", time.Since(start))
				return nil
			} else {
				klog.Infof("Checking for non-pending objects again after %s", pollInterval)
			}
		case <-ctx.Done():
			// If the context is cancelled, then we should shut down immediately since our time is up.
			klog.Warningf("Shutdown delay handler did not exit cleanly after %s.", time.Since(start))
			return errors.Join(ctx.Err(), s.printCleanupMessages())
		}
	}
}

// Prints cleanup messages to the console log that aid in the cleanup process.
func (s *shutdownDelayHandler) printCleanupMessages() error {
	orphaned, err := s.findAllNonPendingObjects()
	if err != nil {
		return err
	}

	if orphaned.Len() != 0 {
		klog.Infof("The following command(s) may be used to clean up any remaining orphaned object(s):")
		for _, namekind := range getObjectNamesAndKinds(orphaned.UnsortedSlice()) {
			klog.Infof("oc delete %s -n %s", namekind, ctrlcommon.MCONamespace)
		}
	}

	return nil
}

// Finds all of the objects related to a MachineOSConfig or a MachineOSBuild
// that are not pending deletion. Also searches for orphaned objects.
func (s *shutdownDelayHandler) findAllNonPendingObjects() (*utils.UniqueObjects, error) {
	objs, err := newObjectsForShutdownFromListers(s.listers)
	if err != nil {
		return nil, err
	}

	return objs.findAllNonPendingObjects(), nil
}

// Determines if we need to delay the shutdown process. This is determined by
// the number of objects that are not pending deletion. If that number is zero,
// then there is no need to delay the shutdown process.
func (s *shutdownDelayHandler) isDelayNeeded() (bool, error) {
	objs, err := s.findAllNonPendingObjects()
	if err != nil {
		return false, err
	}

	isNeeded := objs.Len() != 0
	if isNeeded {
		namekinds := getObjectNamesAndKinds(objs.UnsortedSlice())
		klog.Infof("Found %d child object(s) not pending deletion: %v", objs.Len(), namekinds)
	}

	return isNeeded, nil
}

// Holds all of the objects to interrogate in order to determine if we need to
// delay the shutdown process.
type objectsForShutdown struct {
	// Map of MachineOSConfigs by name.
	moscMap map[string]*mcfgv1.MachineOSConfig
	// Map of MachineOSBuilds by name.
	mosbMap map[string]*mcfgv1.MachineOSBuild
	// Map of Jobs by name.
	jobMap map[string]*batchv1.Job
	// All relevant objects.
	all []metav1.Object
}

// Fetches all of the objects from the listers.
func newObjectsForShutdownFromListers(l *listers) (*objectsForShutdown, error) {
	moscList, err := l.machineOSConfigLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	mosbList, err := l.machineOSBuildLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	// Select for all ephemeral build objects.
	sel := utils.EphemeralBuildObjectSelector()

	jobList, err := l.jobLister.List(sel)
	if err != nil {
		return nil, err
	}

	cmList, err := l.configmapLister.List(sel)
	if err != nil {
		return nil, err
	}

	secretList, err := l.secretLister.List(sel)
	if err != nil {
		return nil, err
	}

	return newObjectsForShutdownFromLists(moscList, mosbList, jobList, cmList, secretList), nil
}

func newObjectsForShutdownFromLists(moscs []*mcfgv1.MachineOSConfig, mosbs []*mcfgv1.MachineOSBuild, jobs []*batchv1.Job, configmaps []*corev1.ConfigMap, secrets []*corev1.Secret) *objectsForShutdown {
	all := []metav1.Object{}

	// Since MachineOSConfigs, MachineOSBuilds, and Jobs can be owners, use maps
	// for fast and easy lookups.
	moscMap := map[string]*mcfgv1.MachineOSConfig{}
	mosbMap := map[string]*mcfgv1.MachineOSBuild{}
	jobMap := map[string]*batchv1.Job{}

	for _, mosc := range moscs {
		moscMap[mosc.Name] = mosc
		all = append(all, mosc)
	}

	for _, mosb := range mosbs {
		mosbMap[mosb.Name] = mosb
		all = append(all, mosb)
	}

	for _, job := range jobs {
		jobMap[job.Name] = job
		all = append(all, job)
	}

	for _, configmap := range configmaps {
		all = append(all, configmap)
	}

	for _, secret := range secrets {
		all = append(all, secret)
	}

	return &objectsForShutdown{moscMap: moscMap, mosbMap: mosbMap, jobMap: jobMap, all: all}
}

func (o *objectsForShutdown) shouldObjectBePendingDeletion(obj metav1.Object) bool {
	// If the object references an owner, was the owner deleted or is it pending
	// deletion?.
	if o.isObjectOwnerDeletedOrPending(obj) {
		return true
	}

	// If the object refers to a MachineOSConfig and/or MachineOSBuild via its
	// labels, do those exist or are they pending deletion?
	if o.isObjectLabelRefDeletedOrPending(obj) {
		return true
	}

	return false
}

// Iterates through all of the objects we've loaded in order to determine any
// orphaned or soon-to-be orphaned objects.
func (o *objectsForShutdown) findAllNonPendingObjects() *utils.UniqueObjects {
	foundObjs := utils.NewUniqueObjects()

	for _, obj := range o.all {
		if o.shouldObjectBePendingDeletion(obj) && !o.isObjectPendingDeletion(obj) {
			foundObjs.Insert(obj)
		}
	}

	return foundObjs
}

// Finds an owner reference for either a MachineOSBuild, MachineOSConfig, or
// Job. Returns nil if none are found.
func (o *objectsForShutdown) findRelevantOwnerRef(obj metav1.Object) *metav1.OwnerReference {
	kinds := sets.New[string]("MachineOSBuild", "MachineOSConfig", "Job")

	for _, ownerRef := range obj.GetOwnerReferences() {
		ownerRef := ownerRef
		if kinds.Has(ownerRef.Kind) && ownerRef.Name != "" {
			return &ownerRef
		}
	}

	return nil
}

// For a given object, try to find its owner based upon the owner reference.
// Then determine whether it is pending deletion or already deleted. Returns
// true when the object's owner is not found or is pending deletion.
func (o *objectsForShutdown) isObjectOwnerDeletedOrPending(obj metav1.Object) bool {
	ref := o.findRelevantOwnerRef(obj)

	if ref == nil {
		return false
	}

	var ownerObj metav1.Object
	var ownerObjOK bool

	switch obj.(type) {
	case *mcfgv1.MachineOSBuild:
		// MachineOSBuilds are owned by MachineOSConfigs
		ownerObj, ownerObjOK = o.moscMap[ref.Name]
	case *batchv1.Job:
		// Jobs are owned by MachineOSBuilds
		ownerObj, ownerObjOK = o.mosbMap[ref.Name]
	case *corev1.ConfigMap:
		// ConfigMaps are owned by Jobs.
		ownerObj, ownerObjOK = o.jobMap[ref.Name]
	case *corev1.Secret:
		// Secrets are owned by Jobs.
		ownerObj, ownerObjOK = o.jobMap[ref.Name]
	default:
		// We can't determine ownership of anything else.
		return false
	}

	// If the owner was not found, it was deleted, so return true.
	if !ownerObjOK {
		return true
	}

	// Determine whether the object we found is pending deletion.
	return o.isObjectPendingDeletion(ownerObj)
}

// For a given object, examine its labels to determine if the MachineOSConfig
// or MachineOSBuild it references is deleted or is pending deletion. Returns
// true when the MachineOSConfig or MachineOSBuild is not found or when it is
// pending deletion.
func (o *objectsForShutdown) isObjectLabelRefDeletedOrPending(obj metav1.Object) bool {
	objMeta := meta.AsPartialObjectMetadata(obj).ObjectMeta

	// If we have the MachineOSConfig name label, determine if the
	// MachineOSConfig is present and if it is pending deletion.
	if metav1.HasLabel(objMeta, constants.MachineOSConfigNameLabelKey) {
		moscName := obj.GetLabels()[constants.MachineOSConfigNameLabelKey]
		mosc, ok := o.moscMap[moscName]
		if !ok {
			// We couldn't find it, so assume it is deleted.
			return true
		}

		// We found it,
		if o.isObjectPendingDeletion(mosc) {
			return true
		}
	}

	if metav1.HasLabel(objMeta, constants.MachineOSBuildNameLabelKey) {
		mosbName := obj.GetLabels()[constants.MachineOSBuildNameLabelKey]
		mosb, ok := o.mosbMap[mosbName]
		if !ok {
			// We couldn't find it, so assume it is deleted.
			return true
		}

		// We found it,
		if o.isObjectPendingDeletion(mosb) {
			return true
		}
	}

	return false
}

// Determines whether a given object is pending deletion. An object is said to
// be pending deletion whenever the deletion timestamp is not nil.
func (o *objectsForShutdown) isObjectPendingDeletion(obj metav1.Object) bool {
	return obj.GetDeletionTimestamp() != nil
}

// Gets a string representation of the objects' name and kind.
func getObjectNameAndKind(obj metav1.Object) string {
	kind, err := utils.GetKindForObject(obj.(runtime.Object))
	if err != nil {
		kind = "<unknown kind>"
	}

	return fmt.Sprintf("%s/%s", strings.ToLower(kind), obj.GetName())
}

// Gets a string representation of several objects' names and kinds.
func getObjectNamesAndKinds(objs []metav1.Object) []string {
	out := []string{}

	for _, obj := range objs {
		namekind := getObjectNameAndKind(obj)
		out = append(out, namekind)
	}

	return out
}
