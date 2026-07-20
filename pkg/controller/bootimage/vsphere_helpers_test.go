package bootimage

import (
	"testing"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	osconfigv1 "github.com/openshift/api/config/v1"
)

// TestCreateNewVMTemplate_NoMatchingFailureDomain verifies that when a MachineSet's
// providerSpec.Workspace doesn't match any vCenter/failure domain in the Infrastructure object,
// createNewVMTemplate returns a descriptive error instead of silently no-op'ing. This is the
// degrade-on-no-match behavior added in "bootimage: degrade when vsphere fd not found" — it never
// reaches getClientsFromServerURL (no real vCenter connectivity needed), since the outer loop over
// infra.Spec.PlatformSpec.VSphere.VCenters has nothing to match against.
func TestCreateNewVMTemplate_NoMatchingFailureDomain(t *testing.T) {
	providerSpec := &machinev1beta1.VSphereMachineProviderSpec{
		Workspace: &machinev1beta1.Workspace{
			Server:       "vcenter.example.com",
			Datacenter:   "dc1",
			Datastore:    "datastore1",
			ResourcePool: "/dc1/host/cluster1/Resources",
		},
	}

	infra := &osconfigv1.Infrastructure{
		Spec: osconfigv1.InfrastructureSpec{
			PlatformSpec: osconfigv1.PlatformSpec{
				VSphere: &osconfigv1.VSpherePlatformSpec{
					// Deliberately empty: no vCenters/failure domains for providerSpec.Workspace
					// to match against.
				},
			},
		},
	}

	resolvedName, patchRequired, err := createNewVMTemplate(nil, providerSpec, infra, nil, nil, "x86_64", "9.6.20260210-0")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match any vCenter/failure domain")
	assert.Contains(t, err.Error(), "vcenter.example.com")
	assert.Empty(t, resolvedName)
	assert.False(t, patchRequired)
}
