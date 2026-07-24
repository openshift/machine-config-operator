package containerruntimeconfig

import (
	"strings"
	"testing"

	apicfgv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// decodeTLSDropinFromMC parses the ignition config from a MachineConfig and
// returns the decoded contents of the CRI-O TLS drop-in file.
func decodeTLSDropinFromMC(t *testing.T, mc *mcfgv1.MachineConfig) string {
	t.Helper()
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	require.NoError(t, err, "failed to parse ignition config")
	for _, f := range ignCfg.Storage.Files {
		if f.Path != CRIODropInFilePathTLS {
			continue
		}
		if f.Contents.Source == nil {
			return ""
		}
		data, err := ctrlcommon.DecodeIgnitionFileContents(f.Contents.Source, f.Contents.Compression)
		require.NoError(t, err, "failed to decode ignition file contents")
		return string(data)
	}
	return ""
}

func TestAddKubeletCfgAfterBootstrapKubeletCfg(t *testing.T) {
	for _, platform := range []apicfgv1.PlatformType{apicfgv1.AWSPlatformType, apicfgv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			f.newController()

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			pools := []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			}
			// ctrcfg for bootstrap mode
			ctrcfg := newContainerRuntimeConfig("log-level", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, pools[0])
			f.mccrLister = append(f.mccrLister, ctrcfg)
			f.objects = append(f.objects, ctrcfg)

			mcs, err := RunContainerRuntimeBootstrap("../../../templates", []*mcfgv1.ContainerRuntimeConfig{ctrcfg}, cc, pools, nil /* fgHandler */)
			require.NoError(t, err)
			require.Len(t, mcs, 1)

			// TLS bootstrap is now a separate function
			tlsMCs, tlsErr := RunTLSBootstrap(pools, nil /* kubeletConfigs */, nil /* apiServer */)
			require.NoError(t, tlsErr)
			require.Len(t, tlsMCs, 1)

			// add ctrcfg1 after bootstrap
			ctrcfg1 := newContainerRuntimeConfig("log-level-master", &mcfgv1.ContainerRuntimeConfiguration{LogLevel: "debug"}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			f.mccrLister = append(f.mccrLister, ctrcfg1)
			f.objects = append(f.objects, ctrcfg1)
			c := f.newController()
			err = c.syncHandler(getKey(ctrcfg1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			// resync ctrcfg and check the managedKey
			c = f.newController()
			err = c.syncHandler(getKey(ctrcfg, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			val := ctrcfg.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)
		})
	}
}

func TestRunTLSBootstrap(t *testing.T) {
	workerPool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "worker",
			Labels: map[string]string{"pools.operator.machineconfiguration.openshift.io/worker": ""},
		},
	}
	masterPool := &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "master",
			Labels: map[string]string{"pools.operator.machineconfiguration.openshift.io/master": ""},
		},
	}

	t.Run("NilKubeletConfigsAndNilAPIServer_FallsBackToIntermediate", func(t *testing.T) {
		pools := []*mcfgv1.MachineConfigPool{workerPool, masterPool}
		mcs, err := RunTLSBootstrap(pools, nil, nil)
		require.NoError(t, err)
		require.Len(t, mcs, 2)

		for _, mc := range mcs {
			assert.Contains(t, mc.Name, "generated-containerruntime-tls")
			content := decodeTLSDropinFromMC(t, mc)
			assert.Contains(t, content, "VersionTLS12", "nil APIServer should fall back to Intermediate (TLS 1.2)")
		}
		assert.True(t, strings.HasPrefix(mcs[0].Name, "99-worker-"))
		assert.True(t, strings.HasPrefix(mcs[1].Name, "99-master-"))
	})

	t.Run("KubeletConfigCustomTLS13_OverridesAPIServer", func(t *testing.T) {
		kc := &mcfgv1.KubeletConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "custom-tls",
				CreationTimestamp: metav1.Now(),
			},
			Spec: mcfgv1.KubeletConfigSpec{
				MachineConfigPoolSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"pools.operator.machineconfiguration.openshift.io/worker": ""},
				},
				TLSSecurityProfile: &apicfgv1.TLSSecurityProfile{
					Type: apicfgv1.TLSProfileCustomType,
					Custom: &apicfgv1.CustomTLSProfile{
						TLSProfileSpec: apicfgv1.TLSProfileSpec{
							MinTLSVersion: apicfgv1.VersionTLS13,
						},
					},
				},
			},
		}
		apiServer := &apicfgv1.APIServer{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: apicfgv1.APIServerSpec{
				TLSSecurityProfile: &apicfgv1.TLSSecurityProfile{
					Type: apicfgv1.TLSProfileIntermediateType,
				},
			},
		}
		pools := []*mcfgv1.MachineConfigPool{workerPool, masterPool}
		mcs, err := RunTLSBootstrap(pools, []*mcfgv1.KubeletConfig{kc}, apiServer)
		require.NoError(t, err)
		require.Len(t, mcs, 2)

		var workerMC, masterMC *mcfgv1.MachineConfig
		for _, mc := range mcs {
			if strings.Contains(mc.Name, "worker") {
				workerMC = mc
			} else {
				masterMC = mc
			}
		}
		require.NotNil(t, workerMC)
		require.NotNil(t, masterMC)
		workerContent := decodeTLSDropinFromMC(t, workerMC)
		masterContent := decodeTLSDropinFromMC(t, masterMC)
		assert.Contains(t, workerContent, "VersionTLS13", "worker should get KubeletConfig TLS 1.3")
		assert.Contains(t, masterContent, "VersionTLS12", "master should fall back to APIServer Intermediate (TLS 1.2)")
	})

	t.Run("CustomProfileWithCiphers_PropagatesCipherSuites", func(t *testing.T) {
		kc := &mcfgv1.KubeletConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "custom-ciphers",
				CreationTimestamp: metav1.Now(),
			},
			Spec: mcfgv1.KubeletConfigSpec{
				MachineConfigPoolSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"pools.operator.machineconfiguration.openshift.io/worker": ""},
				},
				TLSSecurityProfile: &apicfgv1.TLSSecurityProfile{
					Type: apicfgv1.TLSProfileCustomType,
					Custom: &apicfgv1.CustomTLSProfile{
						TLSProfileSpec: apicfgv1.TLSProfileSpec{
							MinTLSVersion: apicfgv1.VersionTLS12,
							Ciphers:       []string{"ECDHE-RSA-AES128-GCM-SHA256", "ECDHE-RSA-AES256-GCM-SHA384"},
						},
					},
				},
			},
		}
		pools := []*mcfgv1.MachineConfigPool{workerPool}
		mcs, err := RunTLSBootstrap(pools, []*mcfgv1.KubeletConfig{kc}, nil)
		require.NoError(t, err)
		require.Len(t, mcs, 1)

		content := decodeTLSDropinFromMC(t, mcs[0])
		assert.Contains(t, content, "VersionTLS12")
		assert.Contains(t, content, "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "cipher suites should be converted to IANA names")
		assert.Contains(t, content, "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384", "cipher suites should be converted to IANA names")
	})

	t.Run("MCNameAndAnnotations", func(t *testing.T) {
		pools := []*mcfgv1.MachineConfigPool{workerPool}
		mcs, err := RunTLSBootstrap(pools, nil, nil)
		require.NoError(t, err)
		require.Len(t, mcs, 1)

		mc := mcs[0]
		assert.Equal(t, "99-worker-generated-containerruntime-tls", mc.Name)
		assert.NotEmpty(t, mc.Annotations[ctrlcommon.GeneratedByControllerVersionAnnotationKey])
	})
}
