package kubeletconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	configv1 "github.com/openshift/api/config/v1"
	osev1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
)

func TestRunKubeletBootstrap(t *testing.T) {
	customSelector := metav1.AddLabelToSelector(&metav1.LabelSelector{}, "node-role/custom", "")

	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			pools := []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
				helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0"),
				helpers.NewMachineConfigPool("custom", nil, customSelector, "v0"),
			}

			kcRaw, err := EncodeKubeletConfig(&kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeJSON)
			if err != nil {
				panic(err)
			}
			tempDir := t.TempDir()
			kubeletConfDir := filepath.Join(tempDir, "kubelet.conf.d")
			err = os.Mkdir(kubeletConfDir, 0755)
			require.NoError(t, err, "Failed to create kubelet.conf.d directory")
			// kubeletconfigs for master, worker， custom pool respectively
			expectedMCNames := []string{"99-master-generated-kubelet", "99-worker-generated-kubelet", "99-custom-generated-kubelet"}
			cfgs := []*mcfgv1.KubeletConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "kcfg-master"},
					Spec: mcfgv1.KubeletConfigSpec{
						DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
							ConfigDirectory: kubeletConfDir,
							ConfigFile:      "10-kubelet.conf",
							KubeletConfig: runtime.RawExtension{
								Raw: kcRaw,
							},
						},
						MachineConfigPoolSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"pools.operator.machineconfiguration.openshift.io/master": "",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "kcfg-worker"},
					Spec: mcfgv1.KubeletConfigSpec{
						DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
							ConfigDirectory: kubeletConfDir,
							ConfigFile:      "10-kubelet.conf",
							KubeletConfig: runtime.RawExtension{
								Raw: kcRaw,
							},
						},
						MachineConfigPoolSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"pools.operator.machineconfiguration.openshift.io/worker": "",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "kcfg-custom", Labels: map[string]string{"node-role/custom": ""}},
					Spec: mcfgv1.KubeletConfigSpec{
						DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
							ConfigDirectory: kubeletConfDir,
							ConfigFile:      "10-kubelet.conf",
							KubeletConfig: runtime.RawExtension{
								Raw: kcRaw,
							},
						},
						MachineConfigPoolSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"pools.operator.machineconfiguration.openshift.io/custom": "",
							},
						},
					},
				},
			}

			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"Example"}, nil)
			mcs, err := RunKubeletBootstrap("../../../templates", cfgs, cc, fgHandler, nil, pools, nil)
			require.NoError(t, err)
			require.Len(t, mcs, len(cfgs))

			for i := range mcs {
				require.Equal(t, expectedMCNames[i], mcs[i].Name)
				verifyKubeletConfigYAMLContents(t, mcs[i], mcs[i].Name, cc.Spec.ReleaseImage)
			}
		})
	}
}

func verifyKubeletConfigYAMLContents(t *testing.T, mc *mcfgv1.MachineConfig, mcName string, releaseImageReg string) {
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	require.NoError(t, err)
	regfile := ignCfg.Storage.Files[0]
	conf, err := ctrlcommon.DecodeIgnitionFileContents(regfile.Contents.Source, regfile.Contents.Compression)
	require.NoError(t, err)
	require.Contains(t, string(conf), `maxPods: 100`)
}

func TestGenerateDefaultManagedKeyKubelet(t *testing.T) {
	workerPool := helpers.NewMachineConfigPool("worker", nil, helpers.WorkerSelector, "v0")
	masterPool := helpers.NewMachineConfigPool("master", nil, helpers.WorkerSelector, "v0")
	kcRaw, err := EncodeKubeletConfig(&kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, kubeletconfigv1beta1.SchemeGroupVersion, runtime.ContentTypeJSON)
	if err != nil {
		panic(err)
	}
	tempDir := t.TempDir()
	kubeletConfDir := filepath.Join(tempDir, "kubelet.conf.d")
	err = os.Mkdir(kubeletConfDir, 0755)
	require.NoError(t, err, "Failed to create kubelet.conf.d directory")
	// valid case, only 1 kubeletconfig per pool
	managedKeyExist := make(map[string]bool)
	for _, tc := range []struct {
		kubeletconfig      *mcfgv1.KubeletConfig
		pool               *mcfgv1.MachineConfigPool
		expectedManagedKey string
	}{
		{
			&mcfgv1.KubeletConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "kcfg-default"},
				Spec: mcfgv1.KubeletConfigSpec{
					DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
						ConfigDirectory: kubeletConfDir,
						ConfigFile:      "10-kubelet.conf",
						KubeletConfig: runtime.RawExtension{
							Raw: kcRaw,
						},
					},
				},
			},
			workerPool,
			"99-worker-generated-kubelet",
		},
		{
			&mcfgv1.KubeletConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "kcfg-default"},
				Spec: mcfgv1.KubeletConfigSpec{
					DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
						ConfigDirectory: kubeletConfDir,
						ConfigFile:      "10-kubelet.conf",
						KubeletConfig: runtime.RawExtension{
							Raw: kcRaw,
						},
					},
				},
			},
			masterPool,
			"99-master-generated-kubelet", // kubeletconfig apply to master pool, expected managedKey for master pool
		},
	} {
		res, err := generateBootstrapManagedKeyKubelet(tc.pool, managedKeyExist)
		require.NoError(t, err)
		require.Equal(t, tc.expectedManagedKey, res)
	}

	// error case, 2 kubeletconfig applied for master pool
	managedKeyExist = make(map[string]bool)
	for _, tc := range []struct {
		kubeletconfig      *mcfgv1.KubeletConfig
		pool               *mcfgv1.MachineConfigPool
		expectedManagedKey string
		expectedErr        error
	}{
		{
			&mcfgv1.KubeletConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "kcfg-default"},
				Spec: mcfgv1.KubeletConfigSpec{
					DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
						ConfigDirectory: kubeletConfDir,
						ConfigFile:      "10-kubelet.conf",
						KubeletConfig: runtime.RawExtension{
							Raw: kcRaw,
						},
					},
				},
			},
			workerPool,
			"99-worker-generated-kubelet",
			nil,
		},
		{
			&mcfgv1.KubeletConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "kcfg-default"},
				Spec: mcfgv1.KubeletConfigSpec{
					DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
						ConfigDirectory: kubeletConfDir,
						ConfigFile:      "10-kubelet.conf",
						KubeletConfig: runtime.RawExtension{
							Raw: kcRaw,
						},
					},
				},
			},
			masterPool,
			"99-master-generated-kubelet", // kubeletconfig apply to master pool, expected managedKey for master pool
			nil,
		},
		{
			&mcfgv1.KubeletConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "kcfg-1"},
				Spec: mcfgv1.KubeletConfigSpec{
					DropInConfig: &mcfgv1.KubeletDropInDirConfigDetails{
						ConfigDirectory: kubeletConfDir,
						ConfigFile:      "10-kubelet.conf",
						KubeletConfig: runtime.RawExtension{
							Raw: kcRaw,
						},
					},
				},
			},
			masterPool,
			"",
			fmt.Errorf("Error found multiple KubeletConfigs targeting MachineConfigPool master. Please apply only one KubeletConfig manifest for each pool during installation"),
		},
	} {
		res, err := generateBootstrapManagedKeyKubelet(tc.pool, managedKeyExist)
		require.Equal(t, tc.expectedErr, err)
		require.Equal(t, tc.expectedManagedKey, res)
	}
}

func TestAddKubeletCfgAfterBootstrapKubeletCfg(t *testing.T) {
	for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.NonePlatformType, "unrecognized"} {
		t.Run(string(platform), func(t *testing.T) {
			f := newFixture(t)
			fgHandler := ctrlcommon.NewFeatureGatesHardcodedHandler([]osev1.FeatureGateName{"Example"}, nil)
			f.newController(fgHandler)

			cc := newControllerConfig(ctrlcommon.ControllerConfigName, platform)
			pools := []*mcfgv1.MachineConfigPool{
				helpers.NewMachineConfigPool("master", nil, helpers.MasterSelector, "v0"),
			}
			// kc for bootstrap mode
			kc := newKubeletConfig("kcfg-master", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			f.ccLister = append(f.ccLister, cc)
			f.mcpLister = append(f.mcpLister, pools[0])
			f.mckLister = append(f.mckLister, kc)
			f.objects = append(f.objects, kc)

			mcs, err := RunKubeletBootstrap("../../../templates", []*mcfgv1.KubeletConfig{kc}, cc, fgHandler, nil, pools, nil)
			require.NoError(t, err)
			require.Len(t, mcs, 1)

			// add kc1 after bootstrap
			kc1 := newKubeletConfig("smaller-max-pods", &kubeletconfigv1beta1.KubeletConfiguration{MaxPods: 100}, metav1.AddLabelToSelector(&metav1.LabelSelector{}, "pools.operator.machineconfiguration.openshift.io/master", ""))

			f.mckLister = append(f.mckLister, kc1)
			f.objects = append(f.objects, kc1)
			c := f.newController(fgHandler)
			err = c.syncHandler(getKey(kc1, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}

			// resync kc and check the managedKey
			c = f.newController(fgHandler)
			err = c.syncHandler(getKey(kc, t))
			if err != nil {
				t.Errorf("syncHandler returned: %v", err)
			}
			val := kc.GetAnnotations()[ctrlcommon.MCNameSuffixAnnotationKey]
			require.Equal(t, "", val)
		})
	}
}
