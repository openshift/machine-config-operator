package extended

import (
	"encoding/json"
	"fmt"
	"reflect"

	exutil "github.com/openshift/machine-config-operator/test/extended-priv/util"
)

// NodeDisruptionPolicy, represents content of machineconfigurations.operator.openshift.io/cluster
type NodeDisruptionPolicy struct {
	Resource `json:"-"`
	Snapshot string    `json:"-"`
	Files    []*Policy `json:"files,omitempty"`
	Units    []*Policy `json:"units,omitempty"`
	SSHKey   *Policy   `json:"sshkey,omitempty"`
}

// Policy, represents content of every policy
type Policy struct {
	Name    *string  `json:"name,omitempty"`
	Path    *string  `json:"path"`
	Actions []Action `json:"actions"`
}

// Action, represents content of every action in policy
type Action struct {
	Type    string   `json:"type"`
	Reload  *Service `json:"reload,omitempty"`
	Restart *Service `json:"restart,omitempty"`
}

// Service represents service info in reload/restart
type Service struct {
	Name string `json:"serviceName"`
}

// NewNodeDisruptionPolicy constructor of NodeDisruptionPolicy
func NewNodeDisruptionPolicy(oc *exutil.CLI) *NodeDisruptionPolicy {
	ndp := NodeDisruptionPolicy{Resource: *NewResource(oc.AsAdmin(), "machineconfigurations.operator.openshift.io", "cluster")}
	ndp.Snapshot = ndp.GetOrFail("{.spec.nodeDisruptionPolicy}")
	return &ndp
}

// NewPolicyWithParams constructor of Policy
func NewPolicyWithParams(path, name *string, actions ...Action) Policy {
	return Policy{Path: path, Name: name, Actions: actions}
}

// NewActionWithParams constrctor of Action
func NewActionWithParams(actnType string, reload, restart *Service) Action {
	return Action{Type: actnType, Reload: reload, Restart: restart}
}

// NewService constructor of Service
func NewService(name string) *Service {
	return &Service{Name: name}
}

// NewReloadAction create new reload action
func NewReloadAction(serviceName string) Action {
	return NewActionWithParams("Reload", NewService(serviceName), nil)
}

// NewRestartAction create new restart action
func NewRestartAction(serviceName string) Action {
	return NewActionWithParams("Restart", nil, NewService(serviceName))
}

// NewCommonAction create new common action, only has type
func NewCommonAction(actnType string) Action {
	return NewActionWithParams(actnType, nil, nil)
}

// Equals deep equal policies
func (p Policy) Equals(policy Policy) bool {
	return reflect.DeepEqual(p, policy)
}

// IsUpdated check whether polcies in this object are synced to status
func (ndp NodeDisruptionPolicy) IsUpdated() (bool, error) {
	latest := NewNodeDisruptionPolicy(ndp.oc)
	err := json.Unmarshal([]byte(ndp.GetOrFail("{.status.nodeDisruptionPolicyStatus.clusterPolicies}")), &latest)
	if err != nil {
		return false, err
	}

	updatedPolices := 0
	for _, file := range ndp.Files {
		for _, latestFile := range latest.Files {
			if file.Equals(*latestFile) {
				updatedPolices++
			}
		}
	}

	for _, unit := range ndp.Units {
		for _, latestUnit := range latest.Units {
			if unit.Equals(*latestUnit) {
				updatedPolices++
			}
		}
	}

	if ndp.SSHKey != nil && ndp.SSHKey.Equals(*latest.SSHKey) {
		updatedPolices++
	}

	currentPolicies := len(ndp.Files) + len(ndp.Units)
	if ndp.SSHKey != nil {
		currentPolicies++
	}

	return updatedPolices == currentPolicies, nil

}

// Rollback rollback the spec to the original values, it should be called in defer block
func (ndp NodeDisruptionPolicy) Rollback() {
	if ndp.Snapshot != "" {
		ndp.Patch("json", fmt.Sprintf(`[{"op": "replace", "path": "/spec/nodeDisruptionPolicy", "value": %s}]`, ndp.Snapshot))
	} else {
		ndp.Patch("json", `[{"op": "remove", "path": "/spec/nodeDisruptionPolicy"}]`)
	}
}

// AddFilePolicy add file based policy
func (ndp NodeDisruptionPolicy) AddFilePolicy(path string, actions ...Action) NodeDisruptionPolicy {
	policy := NewPolicyWithParams(&path, nil, actions...)
	ndp.Files = append(ndp.Files, &policy)
	return ndp
}

// AddUnitPolicy add unit based policy
func (ndp NodeDisruptionPolicy) AddUnitPolicy(name string, actions ...Action) NodeDisruptionPolicy {
	policy := NewPolicyWithParams(nil, &name, actions...)
	ndp.Units = append(ndp.Units, &policy)
	return ndp
}

// SetSSHKeyPolicy set actions for sshkey based policy
func (ndp NodeDisruptionPolicy) SetSSHKeyPolicy(actions ...Action) NodeDisruptionPolicy {
	policy := NewPolicyWithParams(nil, nil, actions...)
	ndp.SSHKey = &policy
	return ndp
}

// Apply apply changes to machineconfiguration/cluster
func (ndp NodeDisruptionPolicy) Apply() error {
	bytes, err := json.Marshal(ndp)
	if err != nil {
		return err
	}

	err = ndp.Patch("merge", fmt.Sprintf(`{"spec":{"nodeDisruptionPolicy":%s}}`, string(bytes)))
	if err != nil {
		return err
	}

	return nil
}
