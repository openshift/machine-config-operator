package scanner

import "errors"

var (
	// ErrNodeUnassigned is returned when the node's labels match no MachineConfigPool.
	ErrNodeUnassigned = errors.New("node is not assigned to a machineconfigpool")
	// ErrMultipleCustomPools is returned when the node matches more than one custom pool.
	ErrMultipleCustomPools = errors.New("node belongs to multiple custom machineconfigpools")
	// ErrWindowsNode is returned when the node is Windows and therefore not MCO-managed.
	ErrWindowsNode = errors.New("node is a windows node")
)
