package extended

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	o "github.com/onsi/gomega"
	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	"github.com/openshift/machine-config-operator/test/extended-priv/util/architecture"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"k8s.io/apimachinery/pkg/util/wait"
)

// CAPIMachineSet struct to handle CAPI MachineSet resources
type CAPIMachineSet struct {
	Resource
}

// CAPIMachineSetList struct to handle lists of CAPI MachineSet resources
type CAPIMachineSetList struct {
	ResourceList
}

// NewCAPIMachineSet constructs a new CAPIMachineSet struct
func NewCAPIMachineSet(oc *exutil.CLI, namespace, name string) *CAPIMachineSet {
	return &CAPIMachineSet{*NewNamespacedResource(oc, CAPIMachineSetFullName, namespace, name)}
}

// NewCAPIMachineSetList constructs a new CAPIMachineSetList struct
func NewCAPIMachineSetList(oc *exutil.CLI, namespace string) *CAPIMachineSetList {
	return &CAPIMachineSetList{*NewNamespacedResourceList(oc, CAPIMachineSetFullName, namespace)}
}

func (ms CAPIMachineSet) String() string {
	return ms.GetName()
}

// ScaleTo scales the CAPI MachineSet to the exact given value
func (ms CAPIMachineSet) ScaleTo(scale int) error {
	return ms.Patch("merge", fmt.Sprintf(`{"spec": {"replicas": %d}}`, scale))
}

// AddToScale scales the CAPI MachineSet adding the given value (positive or negative)
func (ms CAPIMachineSet) AddToScale(delta int) error {
	currentReplicas, err := strconv.Atoi(ms.GetOrFail(`{.spec.replicas}`))
	if err != nil {
		return err
	}
	return ms.ScaleTo(currentReplicas + delta)
}

// GetReplicaOfSpec returns the replica number from spec
func (ms CAPIMachineSet) GetReplicaOfSpec() (string, error) {
	return ms.Get(`{.spec.replicas}`)
}

// GetOSStreamLabel returns the machineconfiguration.openshift.io/osstream label value
func (ms CAPIMachineSet) GetOSStreamLabel() (string, error) {
	return ms.GetLabel("machineconfiguration.openshift.io/osstream")
}

// AddOSStreamLabel adds or updates the machineconfiguration.openshift.io/osstream label
func (ms CAPIMachineSet) AddOSStreamLabel(streamName string) error {
	return ms.Patch("merge", fmt.Sprintf(`{"metadata":{"labels":{"machineconfiguration.openshift.io/osstream":"%s"}}}`, streamName))
}

// RemoveOSStreamLabel removes the machineconfiguration.openshift.io/osstream label
func (ms CAPIMachineSet) RemoveOSStreamLabel() error {
	return ms.Patch("merge", `{"metadata":{"labels":{"machineconfiguration.openshift.io/osstream":null}}}`)
}

// GetOSStream returns the OS stream used by this CAPI MachineSet.
// If the osstream label is set, it returns its value. Otherwise it defaults to rhel-10.
func (ms CAPIMachineSet) GetOSStream() string {
	stream, err := ms.GetOSStreamLabel()
	if err != nil || stream == "" {
		return OSImageStreamRHEL10
	}
	return stream
}

// GetIsReady returns true if the CAPI MachineSet instances are ready
func (ms CAPIMachineSet) GetIsReady() bool {
	configuredReplicasString, err := ms.Get(`{.spec.replicas}`)
	if err != nil {
		logger.Infof("Cannot get configured replicas. Err: %s", err)
		return false
	}

	configuredReplicas, err := strconv.Atoi(configuredReplicasString)
	if err != nil {
		logger.Infof("Could not parse configured replicas. Error: %s", err)
		return false
	}

	statusString, err := ms.Get(`{.status}`)
	if err != nil {
		logger.Infof("Cannot get status. Err: %s", err)
		return false
	}

	status := JSON(statusString)
	replicasData, err := status.GetSafe("replicas")
	if err != nil {
		logger.Infof("Cannot get the replicas in the status. Err: %s", err)
		return false
	}

	readyReplicasData, err := status.GetSafe("readyReplicas")
	if err != nil {
		logger.Infof("Cannot get the readyReplicas in the status. Err: %s", err)
		return false
	}

	if !replicasData.Exists() {
		logger.Infof("Replicasdata does not exist")
		return false
	}
	replicas := replicasData.ToInt()
	if replicas == 0 {
		if replicas == configuredReplicas {
			logger.Infof("Zero replicas")
			return true
		}
		logger.Infof("Zero replicas. Status not updated already.")
		return false
	}
	if !readyReplicasData.Exists() {
		logger.Infof("ReadyReplicasdata does not exist")
		return false
	}
	readyReplicas := readyReplicasData.ToInt()

	logger.Infof("Replicas %d, readyReplicas %d", replicas, readyReplicas)

	replicasAreReady := replicas == readyReplicas
	replicasAreConfigured := replicas == configuredReplicas

	return replicasAreReady && replicasAreConfigured
}

// WaitUntilReady waits until the CAPI MachineSet reports a Ready status
func (ms CAPIMachineSet) WaitUntilReady(duration string) error {
	pDuration, err := time.ParseDuration(duration)
	if err != nil {
		logger.Errorf("Error parsing duration %s. Error: %s", duration, err)
		return err
	}

	pollerr := wait.PollUntilContextTimeout(context.TODO(), 20*time.Second, pDuration, false, func(_ context.Context) (bool, error) {
		return ms.GetIsReady(), nil
	})

	return pollerr
}

// GetMachines returns a slice with the CAPI machines created for this MachineSet
func (ms CAPIMachineSet) GetMachines() ([]*CAPIMachine, error) {
	ml := NewCAPIMachineList(ms.oc, ms.GetNamespace())
	ml.ByLabel("cluster.x-k8s.io/set-name=" + ms.GetName())
	ml.SortByTimestamp()
	return ml.GetAll()
}

// GetMachinesByPhase gets CAPI machines by phase (e.g. Running, Provisioning, Deleting)
func (ms CAPIMachineSet) GetMachinesByPhase(phase string) ([]ManagedMachine, error) {
	var machines []ManagedMachine
	pollerr := wait.PollUntilContextTimeout(context.TODO(), 1*time.Second, 20*time.Second, true, func(_ context.Context) (bool, error) {
		ml := NewCAPIMachineList(ms.oc, ms.GetNamespace())
		ml.ByLabel("cluster.x-k8s.io/set-name=" + ms.GetName())
		ml.SetItemsFilter(fmt.Sprintf(`?(@.status.phase=="%s")`, phase))
		allMachines, err := ml.GetAll()
		if err != nil {
			return false, err
		}
		machines = make([]ManagedMachine, len(allMachines))
		for i, m := range allMachines {
			machines[i] = m
		}
		return len(machines) > 0, nil
	})

	return machines, pollerr
}

// GetNodes returns a slice with all nodes created for this CAPI MachineSet
func (ms CAPIMachineSet) GetNodes() ([]*Node, error) {
	machines, mErr := ms.GetMachines()
	if mErr != nil {
		return nil, mErr
	}

	nodes := []*Node{}
	for _, m := range machines {
		n, nErr := m.GetNode()
		if nErr != nil {
			return nil, nErr
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// GetNodesOrFail returns a slice with all nodes created for this CAPI MachineSet and fails the test on error
func (ms CAPIMachineSet) GetNodesOrFail() []*Node {
	nodes, err := ms.GetNodes()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the nodes that belong to %s", ms)
	return nodes
}

// AllNodesUpdated returns true if all nodes in this CAPI machineset are updated
func (ms CAPIMachineSet) AllNodesUpdated() (bool, error) {
	nodes, err := ms.GetNodes()
	if err != nil {
		return false, err
	}

	for _, node := range nodes {
		updated, err := node.IsUpdated()
		if err != nil {
			return false, err
		}
		if !updated {
			return false, nil
		}
	}

	return true, nil
}

// getInfrastructureTemplateName returns the name of the infrastructure template referenced by this MachineSet
func (ms CAPIMachineSet) getInfrastructureTemplateName() (string, error) {
	return ms.Get(`{.spec.template.spec.infrastructureRef.name}`)
}

// getInfrastructureTemplateKind returns the kind of the infrastructure template (e.g. AWSMachineTemplate)
func (ms CAPIMachineSet) getInfrastructureTemplateKind() (string, error) {
	return ms.Get(`{.spec.template.spec.infrastructureRef.kind}`)
}

// getInfrastructureTemplateResource returns the Resource representing the infrastructure template
func (ms CAPIMachineSet) getInfrastructureTemplateResource() (*Resource, error) {
	kind, err := ms.getInfrastructureTemplateKind()
	if err != nil {
		return nil, fmt.Errorf("error getting infrastructure template kind: %w", err)
	}

	name, err := ms.getInfrastructureTemplateName()
	if err != nil {
		return nil, fmt.Errorf("error getting infrastructure template name: %w", err)
	}

	// Convert kind to lowercase plural form for oc commands (e.g. AWSMachineTemplate -> awsmachinetemplates)
	kindLower := strings.ToLower(kind) + "s"

	return NewNamespacedResource(ms.oc, kindLower, ms.GetNamespace(), name), nil
}

// GetCoreOsBootImage returns the configured boot image from the infrastructure template
func (ms CAPIMachineSet) GetCoreOsBootImage() (string, error) {
	tmplRes, err := ms.getInfrastructureTemplateResource()
	if err != nil {
		return "", err
	}

	switch p := exutil.CheckPlatform(ms.oc); p {
	case AWSPlatform:
		return tmplRes.Get(`{.spec.template.spec.ami.id}`)
	case GCPPlatform:
		disksJSON, err := tmplRes.Get(`{.spec.template.spec.disks}`)
		if err != nil {
			return "", err
		}
		disks := gjson.Parse(disksJSON).Array()
		for _, disk := range disks {
			if disk.Get("boot").Bool() {
				return disk.Get("image").String(), nil
			}
		}
		return tmplRes.Get(`{.spec.template.spec.image}`)
	default:
		return "", fmt.Errorf("CAPIMachineSet.GetCoreOsBootImage is only supported for AWS and GCP platforms, got %s", p)
	}
}

// GetCoreOsBootImageOrFail returns the configured boot image and fails the test on error
func (ms CAPIMachineSet) GetCoreOsBootImageOrFail() string {
	img, err := ms.GetCoreOsBootImage()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the coreos boot image value in %s", ms)
	return img
}

// GetCoreOSBootImagePath returns the JSON patch path for the boot image in the infrastructure template
func (ms CAPIMachineSet) GetCoreOSBootImagePath(platform string) (string, error) {
	switch platform {
	case AWSPlatform:
		return "/spec/template/spec/ami/id", nil
	default:
		return "", fmt.Errorf("CAPIMachineSet.GetCoreOSBootImagePath is only supported for AWS platform, got %s", platform)
	}
}

// SetCoreOsBootImage sets the boot image in the infrastructure template.
// CAPI infrastructure templates are immutable, so this method reads the current template,
// deletes it, and re-creates it with the new boot image.
func (ms CAPIMachineSet) SetCoreOsBootImage(coreosBootImage string) error {
	tmplRes, err := ms.getInfrastructureTemplateResource()
	if err != nil {
		return err
	}

	bootImagePath, err := ms.GetCoreOSBootImagePath(exutil.CheckPlatform(ms.oc))
	if err != nil {
		return err
	}

	// Transform the JSON patch path to sjson dot-notation
	jsonBootImagePath := strings.ReplaceAll(strings.TrimPrefix(bootImagePath, "/"), "/", ".")

	// Save all template data before deleting
	templateName := tmplRes.GetName()
	templateKind := tmplRes.GetKind()
	templateNamespace := tmplRes.GetNamespace()
	templateOC := tmplRes.GetOC()

	jsonRes, err := GetClonedResourceJSONString(tmplRes, templateName, templateNamespace, func(resString string) (string, error) {
		return sjson.SetRaw(resString, jsonBootImagePath, QuoteIfNotJSON(coreosBootImage))
	})
	if err != nil {
		return fmt.Errorf("error preparing infrastructure template JSON: %w", err)
	}

	logger.Infof("Deleting immutable infrastructure template %s to re-create it with new boot image", templateName)
	if err := tmplRes.Delete(); err != nil {
		return fmt.Errorf("error deleting infrastructure template %s: %w", templateName, err)
	}

	_, err = CreateResourceFromJSON(templateOC, templateKind, templateName, templateNamespace, jsonRes)
	if err != nil {
		return fmt.Errorf("error re-creating infrastructure template %s with new boot image: %w", templateName, err)
	}

	return nil
}

// GetArchitecture returns the architecture configured for this CAPI MachineSet
func (ms CAPIMachineSet) GetArchitecture() (architecture.Architecture, error) {
	labeledArch, err := ms.Get(`{.metadata.annotations.capacity\.cluster-autoscaler\.kubernetes\.io/labels}`)
	if err != nil {
		return architecture.UNKNOWN, err
	}

	if !strings.Contains(labeledArch, "kubernetes.io/arch=") {
		logger.Infof("No arch annotation in the CAPI machineset. Getting architecture from existing nodes created by %s", ms.GetName())
		nodes, err := ms.GetNodes()
		if err != nil {
			return architecture.UNKNOWN, err
		}

		if len(nodes) == 0 {
			return architecture.UNKNOWN, fmt.Errorf("CAPI machineset %s has no replicas, so we cannot get the architecture from any existing node", ms.GetName())
		}

		narch, err := nodes[0].GetArchitecture()
		if err != nil {
			return architecture.UNKNOWN, err
		}

		return narch, nil
	}

	for _, label := range strings.Split(labeledArch, ",") {
		label = strings.TrimSpace(label)
		if archValue, found := strings.CutPrefix(label, "kubernetes.io/arch="); found {
			return architecture.FromString(archValue), nil
		}
	}

	return architecture.UNKNOWN, fmt.Errorf("kubernetes.io/arch label not found in annotation: %s", labeledArch)
}

// GetArchitectureOrFail returns the architecture and fails the test on error
func (ms CAPIMachineSet) GetArchitectureOrFail() architecture.Architecture {
	arch, err := ms.GetArchitecture()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the annotated architecture in %s", ms)
	return arch
}

// SetArchitecture sets the architecture annotation for this CAPI MachineSet
func (ms CAPIMachineSet) SetArchitecture(arch string) error {
	return ms.SetAutoscalerLabels("kubernetes.io/arch=" + arch)
}

// SetAutoscalerLabels sets the capacity.cluster-autoscaler.kubernetes.io/labels annotation
func (ms CAPIMachineSet) SetAutoscalerLabels(labels string) error {
	marshaledLabels, err := json.Marshal(labels)
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}
	return ms.Patch("json",
		fmt.Sprintf(`[{"op": "add", "path": "/metadata/annotations/capacity.cluster-autoscaler.kubernetes.io~1labels", "value": %s}]`,
			string(marshaledLabels)))
}

// GetUserDataSecret returns the secret used for user-data.
// In CAPI the secret is referenced via .spec.template.spec.bootstrap.dataSecretName
func (ms CAPIMachineSet) GetUserDataSecret() (*Secret, error) {
	secretName, err := ms.Get(`{.spec.template.spec.bootstrap.dataSecretName}`)
	if err != nil {
		return nil, err
	}
	return NewSecret(ms.GetOC(), ClusterAPINamespace, secretName), nil
}

// GetManagedUserDataSecret returns the user-data secret wrapped in a ManagedUserDataSecret for CAPI
func (ms CAPIMachineSet) GetManagedUserDataSecret() (ManagedUserDataSecret, error) {
	secret, err := ms.GetUserDataSecret()
	if err != nil {
		return nil, err
	}
	return NewCAPIUserDataSecret(secret), nil
}

// SetUserDataSecret configures the CAPI machineset to use the provided user-data secret
func (ms CAPIMachineSet) SetUserDataSecret(userDataSecretName string) error {
	return ms.Patch("json", `[{ "op": "replace", "path": "/spec/template/spec/bootstrap/dataSecretName", "value": "`+userDataSecretName+`" }]`)
}

// duplicateInfrastructureTemplate clones the infrastructure template referenced by this machineset
// with a new name, optionally applying extra modifications to the cloned JSON before creation.
func (ms CAPIMachineSet) duplicateInfrastructureTemplate(newTemplateName string, extraModifications func(string) (string, error)) (*Resource, error) {
	tmplRes, err := ms.getInfrastructureTemplateResource()
	if err != nil {
		return nil, err
	}

	clonedTmpl, err := CloneResource(tmplRes, newTemplateName, ms.GetNamespace(), extraModifications)
	if err != nil {
		return nil, fmt.Errorf("error cloning infrastructure template %s: %w", tmplRes.GetName(), err)
	}

	return clonedTmpl, nil
}

// Duplicate creates a new CAPI MachineSet by cloning, with 0 replicas.
// It also clones the infrastructure template so the new machineset has its own template.
func (ms CAPIMachineSet) Duplicate(newName string) (ManagedMachineResource, error) {
	newMs := NewCAPIMachineSet(ms.oc, ms.GetNamespace(), newName)
	newTemplateName := newName

	// Clone the infrastructure template
	clonedTmpl, err := ms.duplicateInfrastructureTemplate(newTemplateName, nil)
	if err != nil {
		return newMs, err
	}

	// Clone the machineset pointing to the new template
	_, err = CloneResource(&ms, newName, ms.GetNamespace(),
		func(resString string) (string, error) {
			newResString, err := sjson.Set(resString, "spec.replicas", 0)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.selector.matchLabels.cluster\.x-k8s\.io/set-name`, newName)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.template.metadata.labels.cluster\.x-k8s\.io/set-name`, newName)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, "spec.template.spec.infrastructureRef.name", newTemplateName)
			if err != nil {
				return "", err
			}

			return newResString, nil
		},
	)

	if err != nil {
		// Clean up the cloned template if machineset creation fails
		if delErr := clonedTmpl.Delete(); delErr != nil {
			logger.Errorf("Failed to delete cloned infrastructure template %s: %v", clonedTmpl.GetName(), delErr)
		}
		return newMs, err
	}

	logger.Infof("A new CAPI machineset %s has been created by cloning %s (template: %s)", newMs.GetName(), ms.GetName(), newTemplateName)
	return newMs, nil
}

// DuplicateWithBootImage creates a new CAPI MachineSet by cloning, with the boot image set
// in the infrastructure template at creation time.
func (ms CAPIMachineSet) DuplicateWithBootImage(newName, bootImage string) (ManagedMachineResource, error) {
	newMs := NewCAPIMachineSet(ms.oc, ms.GetNamespace(), newName)
	newTemplateName := newName
	platform := exutil.CheckPlatform(ms.oc)
	bootImagePath, err := ms.GetCoreOSBootImagePath(platform)
	if err != nil {
		return newMs, err
	}

	// Transform the JSON patch path to sjson dot-notation
	jsonBootImagePath := strings.ReplaceAll(strings.TrimPrefix(bootImagePath, "/"), "/", ".")

	// Clone the infrastructure template with the boot image modified at creation time
	clonedTmpl, err := ms.duplicateInfrastructureTemplate(newTemplateName, func(resString string) (string, error) {
		return sjson.SetRaw(resString, jsonBootImagePath, QuoteIfNotJSON(bootImage))
	})
	if err != nil {
		return newMs, err
	}

	// Clone the machineset pointing to the new template
	_, err = CloneResource(&ms, newName, ms.GetNamespace(),
		func(resString string) (string, error) {
			newResString, err := sjson.Set(resString, "spec.replicas", 0)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.selector.matchLabels.cluster\.x-k8s\.io/set-name`, newName)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, `spec.template.metadata.labels.cluster\.x-k8s\.io/set-name`, newName)
			if err != nil {
				return "", err
			}

			newResString, err = sjson.Set(newResString, "spec.template.spec.infrastructureRef.name", newTemplateName)
			if err != nil {
				return "", err
			}

			return newResString, nil
		},
	)

	if err != nil {
		if delErr := clonedTmpl.Delete(); delErr != nil {
			logger.Errorf("Failed to delete cloned infrastructure template %s: %v", clonedTmpl.GetName(), delErr)
		}
		return newMs, err
	}

	logger.Infof("A new CAPI machineset %s has been created by cloning %s with boot image %s (template: %s)", newMs.GetName(), ms.GetName(), bootImage, newTemplateName)
	return newMs, nil
}

// GetWorkspaceFolder is not applicable for CAPI MachineSets
func (ms CAPIMachineSet) GetWorkspaceFolder() (string, error) {
	return "", fmt.Errorf("GetWorkspaceFolder is not supported for CAPI MachineSets")
}

// GetVSphereFailureDomain is not applicable for CAPI MachineSets
func (ms CAPIMachineSet) GetVSphereFailureDomain() (string, error) {
	return "", fmt.Errorf("GetVSphereFailureDomain is not supported for CAPI MachineSets")
}

// GetVSphereConnectionInfo is not applicable for CAPI MachineSets
func (ms CAPIMachineSet) GetVSphereConnectionInfo() (*exutil.VSphereConnectionInfo, error) {
	return nil, fmt.Errorf("GetVSphereConnectionInfo is not supported for CAPI MachineSets")
}

// GetAll returns a []*CAPIMachineSet list with all existing CAPI machinesets
func (msl *CAPIMachineSetList) GetAll() ([]*CAPIMachineSet, error) {
	allMSResources, err := msl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}
	allMS := make([]*CAPIMachineSet, 0, len(allMSResources))

	for _, msRes := range allMSResources {
		allMS = append(allMS, NewCAPIMachineSet(msl.oc, msRes.GetNamespace(), msRes.GetName()))
	}

	return allMS, nil
}

// GetAllOrFail returns a []*CAPIMachineSet list and fails the test if retrieval fails
func (msl *CAPIMachineSetList) GetAllOrFail() []*CAPIMachineSet {
	allMs, err := msl.GetAll()
	o.ExpectWithOffset(1, err).NotTo(o.HaveOccurred(), "Error getting the list of existing CAPI MachineSets")
	o.ExpectWithOffset(1, allMs).NotTo(o.BeEmpty(), "No CAPI MachineSets found in namespace %s", msl.GetNamespace())

	return allMs
}

// GetReplicas returns a []*CAPIMachineSet list with all CAPI machinesets matching the replica condition
func (msl *CAPIMachineSetList) GetReplicas(comparison string, replicas int) ([]*CAPIMachineSet, error) {
	allowedComparisons := []string{"<", ">", "==", "!="}
	validComparison := false

	for _, ac := range allowedComparisons {
		if comparison == ac {
			validComparison = true
			break
		}
	}

	if !validComparison {
		return nil, fmt.Errorf("the provided comparison %s is not in the allowed comparisons list %s",
			comparison, allowedComparisons)
	}

	filter := fmt.Sprintf(`?(@.spec.replicas%s%d)`, comparison, replicas)
	msl.SetItemsFilter(filter)

	return msl.GetAll()
}

// Compile-time interface checks
var _ ManagedMachineResource = &CAPIMachineSet{}
var _ ManagedMachine = &CAPIMachine{}
