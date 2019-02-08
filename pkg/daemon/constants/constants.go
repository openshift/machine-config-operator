package constants

const (
	// XXX
	//
	// Add a constant here, if and only if: it's exported (of course) and it's reused across all the project.
	// Otherwise, prefer an unexported const in a specific package.
	//
	// XXX

	// CurrentMachineConfigAnnotationKey is used to fetch current MachineConfig for a machine
	CurrentMachineConfigAnnotationKey = "machineconfiguration.openshift.io/currentConfig"
	// DesiredMachineConfigAnnotationKey is used to specify the desired MachineConfig for a machine
	DesiredMachineConfigAnnotationKey = "machineconfiguration.openshift.io/desiredConfig"
	// MachineConfigDaemonStateAnnotationKey is used to fetch the state of the daemon on the machine.
	MachineConfigDaemonStateAnnotationKey = "machineconfiguration.openshift.io/state"
	// MachineConfigDaemonStateWorking is set by daemon when it is applying an update.
	MachineConfigDaemonStateWorking = "Working"
	// MachineConfigDaemonStateDone is set by daemon when it is done applying an update.
	MachineConfigDaemonStateDone = "Done"
	// MachineConfigDaemonStateDegraded is set by daemon when update cannot be applied.
	MachineConfigDaemonStateDegraded = "Degraded"
	// InitialNodeAnnotationsFilePath defines the path at which it will find the node annotations it needs to set on the node once it comes up for the first time.
	// The Machine Config Server writes the node annotations to this path.
	InitialNodeAnnotationsFilePath = "/etc/machine-config-daemon/node-annotations.json"
)
