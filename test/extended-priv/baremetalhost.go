package extended

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
	logger "github.com/openshift/machine-config-operator/test/extended-priv/util/logext"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// ErrBareMetalHostUnavailable indicates that a host can no longer be claimed.
var ErrBareMetalHostUnavailable = errors.New("bare-metal host is not available")

// BareMetalHost struct to handle BareMetalHost resources
type BareMetalHost struct {
	Resource
}

// BareMetalHostList struct to handle lists of BareMetalHost resources
type BareMetalHostList struct {
	ResourceList
}

// NewBareMetalHost constructs a new BareMetalHost struct
func NewBareMetalHost(oc *exutil.CLI, namespace, name string) *BareMetalHost {
	return &BareMetalHost{*NewNamespacedResource(oc, BareMetalHostFullName, namespace, name)}
}

// NewBareMetalHostList constructs a new BareMetalHostList struct
func NewBareMetalHostList(oc *exutil.CLI, namespace string) *BareMetalHostList {
	return &BareMetalHostList{*NewNamespacedResourceList(oc, BareMetalHostFullName, namespace)}
}

// GetAll returns a []*BareMetalHost slice with all existing BareMetalHosts
func (bl *BareMetalHostList) GetAll() ([]*BareMetalHost, error) {
	allHostResources, err := bl.ResourceList.GetAll()
	if err != nil {
		return nil, err
	}

	allHosts := make([]*BareMetalHost, 0, len(allHostResources))
	for _, hostRes := range allHostResources {
		allHosts = append(allHosts, NewBareMetalHost(bl.oc, hostRes.GetNamespace(), hostRes.GetName()))
	}

	return allHosts, nil
}

// IsAvailable returns whether the host can be claimed for provisioning.
func (h *BareMetalHost) IsAvailable() (bool, error) {
	data, err := h.GetCleanJSON()
	if err != nil {
		return false, err
	}
	return bareMetalHostAvailable(data), nil
}

// HealthCheck returns an error describing why the host is not operationally
// healthy, or nil when it reports OK status with no error message.
func (h *BareMetalHost) HealthCheck() error {
	data, err := h.GetCleanJSON()
	if err != nil {
		return err
	}
	return bareMetalHostHealthError(data)
}

// VerifiedTestDisks returns the by-path names of the two non-root test disks in
// the host inventory, or an error explaining why they cannot be proven safe.
func (h *BareMetalHost) VerifiedTestDisks() ([]string, error) {
	data, err := h.GetCleanJSON()
	if err != nil {
		return nil, err
	}
	return bareMetalTestDisks(data)
}

// Pinned checks both sides of the temporary host-to-machine selector.
func (h *BareMetalHost) Pinned(machineSet ManagedMachineResource, label string) (bool, error) {
	selectorJSON, err := bareMetalHostSelectorJSON(machineSet)
	if err != nil {
		return false, err
	}
	data, err := h.GetCleanJSON()
	if err != nil {
		return false, err
	}
	matches, err := bareMetalHostMatchesSelector(data, selectorJSON)
	if err != nil || !matches {
		return false, err
	}
	hostLabels := gjson.Get(data, "metadata.labels").Map()
	selectorLabels := gjson.Get(selectorJSON, "matchLabels").Map()
	return label != "" && hostLabels[label].String() == "true" && selectorLabels[label].String() == "true", nil
}

// Pin adds a temporary label to the host and restricts the given machine
// resource to hosts carrying that label. The returned cleanup removes the
// label; it does not reserve the host against other consumers.
func (h *BareMetalHost) Pin(machineSet ManagedMachineResource) (string, func() error, error) {
	data, err := h.GetCleanJSON()
	if err != nil {
		return "", nil, fmt.Errorf("getting %s: %w", h, err)
	}
	if !bareMetalHostAvailable(data) {
		return "", nil, fmt.Errorf("%s: %w", h, ErrBareMetalHostUnavailable)
	}
	if healthErr := bareMetalHostHealthError(data); healthErr != nil {
		return "", nil, fmt.Errorf("%s: %w: %w", h, healthErr, ErrBareMetalHostUnavailable)
	}
	label := "machineconfiguration.openshift.io/bmh-" + exutil.GetRandomString()
	if err := h.AddLabel(label, "true"); err != nil {
		return "", nil, fmt.Errorf("labeling %s: %w", h, err)
	}
	cleanup := func() error { return h.RemoveLabel(label) }
	if err := pinMachineResourceToHost(machineSet, label); err != nil {
		pinErr := fmt.Errorf("selecting %s in %s: %w", h, machineSet, err)
		if cleanupErr := cleanup(); cleanupErr != nil {
			return label, cleanup, errors.Join(pinErr, fmt.Errorf("removing label from %s: %w", h, cleanupErr))
		}
		return "", nil, pinErr
	}
	return label, cleanup, nil
}

// pinMachineResourceToHost restricts the machine resource's metal3 host
// selector to hosts carrying the given label.
func pinMachineResourceToHost(machineSet ManagedMachineResource, label string) error {
	switch ms := machineSet.(type) {
	case *MachineSet:
		patch, err := json.Marshal(map[string]interface{}{
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"providerSpec": map[string]interface{}{
							"value": map[string]interface{}{
								"hostSelector": map[string]interface{}{
									"matchLabels": map[string]string{label: "true"},
								},
							},
						},
					},
				},
			},
		})
		if err != nil {
			return err
		}
		return ms.Patch("merge", string(patch))
	case *CAPIMachineSet:
		// The metal3 host selector lives in the CAPI infrastructure template.
		// Templates are immutable, so recreate the template with the selector added.
		return ms.recreateTemplateWithModification(func(resString string) (string, error) {
			return sjson.Set(resString, "template.spec.hostSelector.matchLabels."+strings.ReplaceAll(label, "/", "\\/"), "true")
		})
	default:
		return fmt.Errorf("bare-metal host pinning requires a MAPI or CAPI MachineSet, got %T", machineSet)
	}
}

// VerifyNode checks that the host is consumed by a machine owned by the given
// machine resource and that the machine has created the given node.
func (h *BareMetalHost) VerifyNode(machineSet ManagedMachineResource, node *Node) error {
	consumer, err := h.Get(`{.spec.consumerRef.name}`)
	if err != nil {
		return err
	}
	if consumer == "" {
		return fmt.Errorf("host %s has no Machine consumer", h)
	}
	var (
		machine    ManagedMachine
		ownerLabel string
	)
	switch ms := machineSet.(type) {
	case *MachineSet:
		machine = NewMachine(h.oc, MachineAPINamespace, consumer)
		ownerLabel = "machine.openshift.io/cluster-api-machineset"
	case *CAPIMachineSet:
		// In CAPI, the host consumer is the infrastructure machine, which
		// shares the name of the CAPI Machine that owns it.
		machine = NewCAPIMachine(h.oc, ms.GetNamespace(), consumer)
		ownerLabel = "cluster.x-k8s.io/set-name"
	default:
		return fmt.Errorf("bare-metal host verification requires a MAPI or CAPI MachineSet, got %T", machineSet)
	}
	owner, err := machine.GetLabel(ownerLabel)
	if err != nil {
		return err
	}
	if owner != machineSet.GetName() {
		return fmt.Errorf("host %s is consumed by MachineSet %s, not %s", h, owner, machineSet)
	}
	ownedNode, err := machine.GetNode()
	if err != nil {
		return err
	}
	if ownedNode.GetName() != node.GetName() {
		return fmt.Errorf("host %s belongs to node %s, not %s", h, ownedNode, node)
	}
	return nil
}

// FindAvailableBareMetalHost finds a free, healthy host matching the metal3
// host selector of the given machine resource and passing the optional filter.
// It never deprovisions workers; a nil host means no suitable host is available.
func FindAvailableBareMetalHost(oc *exutil.CLI, machineSet ManagedMachineResource, filter func(*BareMetalHost) error) (*BareMetalHost, error) {
	selectorJSON, err := bareMetalHostSelectorJSON(machineSet)
	if err != nil {
		return nil, err
	}
	hosts := NewBareMetalHostList(oc, MachineAPINamespace)
	hosts.SetItemsFilter(`?(@.status.provisioning.state=="available")`)
	all, err := hosts.GetAll()
	if err != nil {
		return nil, fmt.Errorf("listing available BareMetalHosts: %w", err)
	}
	for _, host := range all {
		data, err := host.GetCleanJSON()
		if err != nil {
			return nil, fmt.Errorf("getting %s: %w", host, err)
		}
		if !bareMetalHostAvailable(data) {
			continue
		}
		if healthErr := bareMetalHostHealthError(data); healthErr != nil {
			logger.Infof("Skipping host %s: %s", host, healthErr)
			continue
		}
		matches, err := bareMetalHostMatchesSelector(data, selectorJSON)
		if err != nil {
			return nil, fmt.Errorf("checking host selector for %s: %w", host, err)
		}
		if !matches {
			continue
		}
		if filter != nil {
			if err := filter(host); err != nil {
				logger.Infof("Skipping host %s: %s", host, err)
				continue
			}
		}
		return host, nil
	}
	return nil, nil
}

// bareMetalHostSelectorJSON returns the metal3 host selector that governs host
// selection for the given machine resource: the hostSelector in the MAPI
// MachineSet spec or in the CAPI infrastructure template spec. An empty
// selector matches every host.
func bareMetalHostSelectorJSON(machineSet ManagedMachineResource) (string, error) {
	var (
		spec         string
		selectorPath string
		err          error
	)
	switch ms := machineSet.(type) {
	case *MachineSet:
		spec, err = ms.GetSpec()
		selectorPath = "template.spec.providerSpec.value.hostSelector"
	case *CAPIMachineSet:
		tmpl, tmplErr := ms.getInfrastructureTemplateResource()
		if tmplErr != nil {
			return "", fmt.Errorf("error getting infrastructure template for %s: %w", ms.GetName(), tmplErr)
		}
		spec, err = tmpl.GetSpec()
		selectorPath = "template.spec.hostSelector"
	default:
		return "", fmt.Errorf("bare-metal host selection requires a MAPI or CAPI MachineSet, got %T", machineSet)
	}
	if err != nil {
		return "", err
	}
	return gjson.Get(spec, selectorPath).Raw, nil
}

// bareMetalHostMatchesSelector checks the host labels against a metal3 host selector.
func bareMetalHostMatchesSelector(hostJSON, selectorJSON string) (bool, error) {
	if !gjson.Valid(hostJSON) {
		return false, fmt.Errorf("invalid host JSON")
	}
	selector := labels.NewSelector()
	if selectorJSON != "" && selectorJSON != "null" {
		if !gjson.Valid(selectorJSON) {
			return false, fmt.Errorf("invalid host selector JSON")
		}
		for key, value := range gjson.Get(selectorJSON, "matchLabels").Map() {
			requirement, err := labels.NewRequirement(key, selection.Equals, []string{value.String()})
			if err != nil {
				return false, err
			}
			selector = selector.Add(*requirement)
		}
		for _, expression := range gjson.Get(selectorJSON, "matchExpressions").Array() {
			var values []string
			for _, value := range expression.Get("values").Array() {
				values = append(values, value.String())
			}
			requirement, err := labels.NewRequirement(expression.Get("key").String(), selection.Operator(expression.Get("operator").String()), values)
			if err != nil {
				return false, err
			}
			selector = selector.Add(*requirement)
		}
	}
	hostLabels := labels.Set{}
	for key, value := range gjson.Get(hostJSON, "metadata.labels").Map() {
		hostLabels[key] = value.String()
	}
	return selector.Matches(hostLabels), nil
}

// bareMetalHostAvailable reports whether an inspected host can be claimed for provisioning.
func bareMetalHostAvailable(hostJSON string) bool {
	if !gjson.Valid(hostJSON) {
		return false
	}
	host := gjson.Parse(hostJSON)
	return host.Get("status.provisioning.state").String() == "available" &&
		!host.Get("spec.externallyProvisioned").Bool() &&
		host.Get("spec.consumerRef").Type == gjson.Null &&
		host.Get("metadata.deletionTimestamp").String() == ""
}

// bareMetalHostHealthError returns an error describing why the host is not
// operationally healthy, or nil when it is.
func bareMetalHostHealthError(hostJSON string) error {
	if !gjson.Valid(hostJSON) {
		return fmt.Errorf("host has invalid status JSON")
	}
	host := gjson.Parse(hostJSON)
	operationalStatus := host.Get("status.operationalStatus").String()
	if operationalStatus != "OK" {
		return fmt.Errorf("operational status is %q", operationalStatus)
	}
	if errorMessage := host.Get("status.errorMessage").String(); errorMessage != "" {
		return fmt.Errorf("error message is set: %s", errorMessage)
	}
	return nil
}

// bareMetalTestDisks verifies the CI spare host's inventory before Ignition can write to it.
// Use the inspected by-path names rather than the potentially unstable /dev/vdX names.
func bareMetalTestDisks(hostJSON string) ([]string, error) {
	if !gjson.Valid(hostJSON) || !bareMetalHostAvailable(hostJSON) {
		return nil, fmt.Errorf("host is not available or has invalid inventory")
	}
	host := gjson.Parse(hostJSON)
	if !strings.Contains(host.Get("metadata.name").String(), "-extraworker-") ||
		!strings.Contains(host.Get("status.hardware.systemVendor.productName").String(), "KVM") {
		return nil, fmt.Errorf("host is not a dev-scripts spare worker")
	}
	if raid := host.Get("spec.raid"); raid.Exists() && raid.Type != gjson.Null {
		return nil, fmt.Errorf("host has a RAID configuration that may change the provisioning disk")
	}
	if host.Get("spec.automatedCleaningMode").String() != "metadata" {
		return nil, fmt.Errorf("host does not explicitly use metadata cleaning")
	}

	// An explicit spec hint takes precedence; without one, Metal3's resolved hint
	// from inspection identifies the provisioning disk on dev-scripts hosts.
	rootHint := host.Get("spec.rootDeviceHints.deviceName").String()
	if rootHint == "" {
		if hint := host.Get("spec.rootDeviceHints"); hint.Exists() && hint.Type != gjson.Null && len(hint.Map()) != 0 {
			return nil, fmt.Errorf("root device hints do not identify a specific device")
		}
		rootHint = host.Get("status.provisioning.rootDeviceHints.deviceName").String()
	}
	if rootHint == "" {
		return nil, fmt.Errorf("root device is unknown")
	}

	return bareMetalInventoryTestDisks(host.Get("status.hardware.storage").Array(), rootHint)
}

// bareMetalInventoryTestDisks identifies the test disks and checks their inventory names.
func bareMetalInventoryTestDisks(storage []gjson.Result, rootHint string) ([]string, error) {
	rootIndex := -1
	diskIndexes := []int{-1, -1}
	paths := make([]string, 2)
	namesByDisk := make([][]string, len(storage))
	for i, disk := range storage {
		names := []string{disk.Get("name").String()}
		for _, name := range disk.Get("alternateNames").Array() {
			names = append(names, name.String())
		}
		namesByDisk[i] = names
		for _, name := range names {
			if name == rootHint {
				if rootIndex != -1 && rootIndex != i {
					return nil, fmt.Errorf("root device matches multiple inventory disks")
				}
				rootIndex = i
			}
			for j, expected := range []string{"/dev/vda", "/dev/vdb"} {
				if name == expected {
					if diskIndexes[j] != -1 && diskIndexes[j] != i {
						return nil, fmt.Errorf("%s matches multiple inventory disks", expected)
					}
					diskIndexes[j] = i
					paths[j] = disk.Get("name").String()
					if disk.Get("sizeBytes").Int() != 16*1024*1024*1024 {
						return nil, fmt.Errorf("%s does not have the configured CI disk size", expected)
					}
				}
			}
		}
	}
	if rootIndex < 0 || diskIndexes[0] < 0 || diskIndexes[1] < 0 ||
		diskIndexes[0] == diskIndexes[1] || paths[0] == paths[1] ||
		rootIndex == diskIndexes[0] || rootIndex == diskIndexes[1] {
		return nil, fmt.Errorf("cannot prove the two test disks are distinct from the root disk")
	}
	selected := []int{rootIndex, diskIndexes[0], diskIndexes[1]}
	for i, disk := range selected {
		for _, other := range selected[i+1:] {
			for _, name := range namesByDisk[disk] {
				if name != "" && slices.Contains(namesByDisk[other], name) {
					return nil, fmt.Errorf("inventory disks share the device name %s", name)
				}
			}
		}
	}
	for _, diskPath := range paths {
		if !strings.HasPrefix(diskPath, "/dev/disk/by-path/") || path.Clean(diskPath) != diskPath ||
			strings.Contains(strings.TrimPrefix(diskPath, "/dev/disk/by-path/"), "/") {
			return nil, fmt.Errorf("disk %q has no stable by-path name", diskPath)
		}
	}
	return paths, nil
}
