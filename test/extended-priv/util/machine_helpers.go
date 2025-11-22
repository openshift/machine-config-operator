package util

import (
	"strings"
)

// CheckPlatform returns the platform type of the cluster
func CheckPlatform(oc *CLI) string {
	output, _ := oc.AsAdmin().WithoutNamespace().Run("get").Args("infrastructure", "cluster", "-o=jsonpath={.status.platformStatus.type}").Output()
	return strings.ToLower(output)
}
