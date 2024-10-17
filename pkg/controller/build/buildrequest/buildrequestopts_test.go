package buildrequest

import (
	"context"
	"testing"

	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestBuildRequestOpts(t *testing.T) {
	testCases := []struct {
		name        string
		addlObjects []runtime.Object
		addlAsserts func(*testing.T, BuildRequestOpts)
	}{
		{
			name: "no entitlement data",
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.False(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.False(t, brOpts.HasEtcYumReposDConfigs)
				assert.False(t, brOpts.HasEtcPkiEntitlementKeys)
			},
		},
		{
			name: "with etc-pki-entitlement data",
			addlObjects: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiEntitlementSecretName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.False(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.False(t, brOpts.HasEtcYumReposDConfigs)
				assert.True(t, brOpts.HasEtcPkiEntitlementKeys)
			},
		},
		{
			name: "with etc-yum-repos-d data",
			addlObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcYumReposDConfigMapName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.False(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.True(t, brOpts.HasEtcYumReposDConfigs)
				assert.False(t, brOpts.HasEtcPkiEntitlementKeys)
			},
		},
		{
			name: "with etc-pki-rpm-gpg-keys data",
			addlObjects: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiRpmGpgSecretName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.True(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.False(t, brOpts.HasEtcYumReposDConfigs)
				assert.False(t, brOpts.HasEtcPkiEntitlementKeys)
			},
		},
		{
			name: "with all entitlements data",
			addlObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcYumReposDConfigMapName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiRpmGpgSecretName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      constants.EtcPkiEntitlementSecretName,
						Namespace: ctrlcommon.MCONamespace,
					},
				},
			},
			addlAsserts: func(t *testing.T, brOpts BuildRequestOpts) {
				assert.True(t, brOpts.HasEtcPkiRpmGpgKeys)
				assert.True(t, brOpts.HasEtcYumReposDConfigs)
				assert.True(t, brOpts.HasEtcPkiEntitlementKeys)
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			kubeclient, mcfgclient := getClientsForTest(addlObjects{
				kubeObjects: testCase.addlObjects,
			})

			lobj := newLayeredObjectsForTest("worker")

			brOpts, err := newBuildRequestOptsFromAPI(ctx, kubeclient, mcfgclient, lobj.mosb, lobj.mosc)
			assert.NoError(t, err)

			if testCase.addlAsserts != nil {
				assert.NoError(t, err)
				testCase.addlAsserts(t, *brOpts)
			}

			assert.NotNil(t, brOpts.MachineConfig)
			assert.NotNil(t, brOpts.MachineOSConfig)
			assert.NotNil(t, brOpts.MachineOSBuild)
			assert.NotNil(t, brOpts.Images)
			assert.NotNil(t, brOpts.OSImageURLConfig)
			assert.NotNil(t, brOpts.BaseImagePullSecret)
			assert.NotNil(t, brOpts.FinalImagePushSecret)
		})
	}
}

func TestGetOpts(t *testing.T) {

}
