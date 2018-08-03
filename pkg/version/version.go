package version

import (
	"fmt"
)

var (
	// Raw is the string representation of the version. This will be replaced
	// with the calculated version at build time.
	Raw = "was not built properly"

	// String is the human-friendly representation of the version.
	String = fmt.Sprintf("MachineConfigOperator %s", Raw)
)
