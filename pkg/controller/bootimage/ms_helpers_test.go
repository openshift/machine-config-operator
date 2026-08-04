package bootimage

import (
	"encoding/json"
	"testing"

	"github.com/coreos/stream-metadata-go/stream"
	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestGetMAPIBootImageValue(t *testing.T) {
	const rawProviderSpecJSON = `{"template":"some-raw-provider-spec"}`

	machineSet := &machinev1beta1.MachineSet{
		Spec: machinev1beta1.MachineSetSpec{
			Template: machinev1beta1.MachineTemplateSpec{
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: machinev1beta1.ProviderSpec{
						Value: &runtime.RawExtension{Raw: []byte(rawProviderSpecJSON)},
					},
				},
			},
		},
	}

	vsphereInfra := &osconfigv1.Infrastructure{
		Status: osconfigv1.InfrastructureStatus{
			PlatformStatus: &osconfigv1.PlatformStatus{Type: osconfigv1.VSpherePlatformType},
		},
	}
	awsInfra := &osconfigv1.Infrastructure{
		Status: osconfigv1.InfrastructureStatus{
			PlatformStatus: &osconfigv1.PlatformStatus{Type: osconfigv1.AWSPlatformType},
		},
	}

	streamConfigMap := func(t *testing.T, s *stream.Stream) *corev1.ConfigMap {
		t.Helper()
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("failed to marshal test stream: %v", err)
		}
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "coreos-bootimages"},
			Data:       map[string]string{StreamConfigMapKey: string(data)},
		}
	}

	validVSphereStream := &stream.Stream{
		Architectures: map[string]stream.Arch{
			"x86_64": {
				Artifacts: map[string]stream.PlatformArtifacts{
					"vmware": {Release: "417.94.20250101"},
				},
			},
		},
	}

	cases := []struct {
		name      string
		infra     *osconfigv1.Infrastructure
		configMap *corev1.ConfigMap
		arch      string
		want      string // "" means "fall back to raw providerSpec bytes"
	}{
		{
			name:      "non-vSphere platform: raw providerSpec bytes",
			infra:     awsInfra,
			configMap: streamConfigMap(t, validVSphereStream),
			arch:      "x86_64",
		},
		{
			name:      "nil infra: raw providerSpec bytes",
			infra:     nil,
			configMap: streamConfigMap(t, validVSphereStream),
			arch:      "x86_64",
		},
		{
			name:      "nil PlatformStatus: raw providerSpec bytes",
			infra:     &osconfigv1.Infrastructure{},
			configMap: streamConfigMap(t, validVSphereStream),
			arch:      "x86_64",
		},
		{
			name:      "nil configMap: raw providerSpec bytes",
			infra:     vsphereInfra,
			configMap: nil,
			arch:      "x86_64",
		},
		{
			name:      "vSphere with valid release for arch: release string",
			infra:     vsphereInfra,
			configMap: streamConfigMap(t, validVSphereStream),
			arch:      "x86_64",
			want:      "417.94.20250101",
		},
		{
			name:      "vSphere but arch not present in stream: raw providerSpec bytes",
			infra:     vsphereInfra,
			configMap: streamConfigMap(t, validVSphereStream),
			arch:      "arm64",
		},
		{
			name:  "vSphere but vmware artifact has empty release: raw providerSpec bytes",
			infra: vsphereInfra,
			configMap: streamConfigMap(t, &stream.Stream{
				Architectures: map[string]stream.Arch{
					"x86_64": {Artifacts: map[string]stream.PlatformArtifacts{"vmware": {Release: ""}}},
				},
			}),
			arch: "x86_64",
		},
		{
			name:  "vSphere but configMap data is unparseable: raw providerSpec bytes",
			infra: vsphereInfra,
			configMap: &corev1.ConfigMap{
				Data: map[string]string{StreamConfigMapKey: "not-json"},
			},
			arch: "x86_64",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := getMAPIBootImageValue(machineSet, tc.configMap, tc.infra, tc.arch)
			want := rawProviderSpecJSON
			if tc.want != "" {
				want = tc.want
			}
			if string(got) != want {
				t.Errorf("getMAPIBootImageValue() = %q, want %q", got, want)
			}
		})
	}
}
