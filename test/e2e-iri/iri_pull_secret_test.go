//go:build !iri_delete

package e2e_iri_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/pkg/daemon/constants"
	"github.com/openshift/machine-config-operator/pkg/secrets"
	"github.com/openshift/machine-config-operator/test/framework"
)

// iriRegistryHosts returns the registry hostnames that IRI credentials are expected
// to be present for. The IRI registry is reachable through api-int on every node and
// additionally through localhost on masters, where it runs locally and the
// registries.conf mirror rules address it that way. Both are derived the same way the
// merge itself derives them, from the cluster base domain.
func iriRegistryHosts(t *testing.T, cs *framework.ClientSet, ctx context.Context) []string {
	t.Helper()

	cc, err := cs.ControllerConfigs().Get(ctx, ctrlcommon.ControllerConfigName, v1.GetOptions{})
	require.NoError(t, err, "failed to get ControllerConfig")
	require.NotNil(t, cc.Spec.DNS, "ControllerConfig should carry the cluster DNS object")

	baseDomain := cc.Spec.DNS.Spec.BaseDomain
	require.NotEmpty(t, baseDomain, "cluster base domain should not be empty")

	return []string{
		fmt.Sprintf("api-int.%s:%d", baseDomain, ctrlcommon.IRIRegistryPort),
		fmt.Sprintf("localhost:%d", ctrlcommon.IRIRegistryPort),
	}
}

// requireIRIAuths asserts that a .dockerconfigjson pull secret carries a usable
// credential for every expected IRI registry host. The password is generated per
// cluster so it is not compared against a fixed value; instead the entry is decoded
// far enough to prove it is a well-formed credential for the IRI registry user rather
// than an empty placeholder.
func requireIRIAuths(t *testing.T, pullSecretRaw []byte, hosts []string, description string) {
	t.Helper()

	var pullSecret secrets.DockerConfigJSON
	require.NoError(t, json.Unmarshal(pullSecretRaw, &pullSecret), "failed to parse %s", description)
	require.NotEmpty(t, pullSecret.Auths, "%s has no auths", description)

	for _, host := range hosts {
		entry, ok := pullSecret.Auths[host]
		require.True(t, ok, "%s is missing an auth entry for IRI registry host %s", description, host)
		require.NotEmpty(t, entry.Auth, "%s has an empty auth for IRI registry host %s", description, host)

		decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
		require.NoError(t, err, "%s has a non-base64 auth for IRI registry host %s", description, host)

		username, password, found := strings.Cut(string(decoded), ":")
		require.True(t, found, "%s has a malformed auth for IRI registry host %s, expected user:password", description, host)
		require.Equal(t, ctrlcommon.IRIRegistryUsername, username, "%s has an unexpected username for IRI registry host %s", description, host)
		require.NotEmpty(t, password, "%s has an empty password for IRI registry host %s", description, host)

		t.Logf("Confirmed %s carries IRI registry credentials for %s", description, host)
	}
}

// TestIRICredentialsInKubeletPullSecret verifies that the IRI registry credentials
// reach the pull secret rendered onto every node.
//
// The credentials are merged into the cluster pull secret at render time rather than
// being written back to openshift-config/pull-secret, so the 00-<role> MachineConfigs
// are where they become observable. That rendered file is what both kubelet and CRI-O
// authenticate image pulls with, so without it a node cannot pull from the IRI
// registry at all.
func TestIRICredentialsInKubeletPullSecret(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	hosts := iriRegistryHosts(t, cs, ctx)
	t.Logf("Expecting IRI registry credentials for: %v", hosts)

	// Common templates are only rendered into 00-<role>, which is where the pull
	// secret file lives. See GenerateMachineConfigsForRole.
	for _, mcName := range []string{"00-master", "00-worker"} {
		t.Run(mcName, func(t *testing.T) {
			mc, err := cs.MachineConfigs().Get(ctx, mcName, v1.GetOptions{})
			require.NoError(t, err, "failed to get MachineConfig %s", mcName)

			ign, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
			require.NoError(t, err, "failed to parse ignition config from MachineConfig %s", mcName)

			pullSecretRaw, err := ctrlcommon.GetIgnitionFileDataByPath(&ign, constants.KubeletAuthFile)
			require.NoError(t, err, "failed to read %s from MachineConfig %s", constants.KubeletAuthFile, mcName)
			require.NotEmpty(t, pullSecretRaw, "MachineConfig %s does not write %s", mcName, constants.KubeletAuthFile)

			requireIRIAuths(t, pullSecretRaw, hosts, fmt.Sprintf("%s rendered %s", mcName, constants.KubeletAuthFile))
		})
	}
}

// TestIRICredentialsInInternalRegistryPullSecret verifies that the IRI registry
// credentials also reach ControllerConfig.Spec.InternalRegistryPullSecret.
//
// That field is a separate auth source from the kubelet pull secret checked above: it
// is what the OS update path (rpm-ostree/bootc) uses when pulling during an upgrade.
// Both have to carry the credentials, and a regression in either one alone would be
// invisible to a test that only looked at the other, so they are asserted separately.
func TestIRICredentialsInInternalRegistryPullSecret(t *testing.T) {
	skipIfNoBaremetal(t)

	cs := framework.NewClientSet("")
	ctx := context.Background()

	hosts := iriRegistryHosts(t, cs, ctx)
	t.Logf("Expecting IRI registry credentials for: %v", hosts)

	cc, err := cs.ControllerConfigs().Get(ctx, ctrlcommon.ControllerConfigName, v1.GetOptions{})
	require.NoError(t, err, "failed to get ControllerConfig")
	require.NotEmpty(t, cc.Spec.InternalRegistryPullSecret, "ControllerConfig should have an internal registry pull secret")

	requireIRIAuths(t, cc.Spec.InternalRegistryPullSecret, hosts, "ControllerConfig internal registry pull secret")
}
