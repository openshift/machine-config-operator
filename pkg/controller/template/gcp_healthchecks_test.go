package template

import (
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

func TestGCPHealthCheckSourceRanges(t *testing.T) {
	renderConfig := func(region string) RenderConfig {
		return RenderConfig{
			ControllerConfigSpec: &mcfgv1.ControllerConfigSpec{
				Infra: &configv1.Infrastructure{
					Status: configv1.InfrastructureStatus{
						PlatformStatus: &configv1.PlatformStatus{
							GCP: &configv1.GCPPlatformStatus{Region: region},
						},
					},
				},
			},
		}
	}

	tests := []struct {
		name     string
		cfg      RenderConfig
		expected []string
	}{
		{
			name:     "public region uses public GCP ranges",
			cfg:      renderConfig("us-central1"),
			expected: []string{"35.191.0.0/16", "130.211.0.0/22"},
		},
		{
			name:     "gcd berlin uses its own region ranges",
			cfg:      renderConfig("u-germany-northeast1"),
			expected: []string{"34.3.144.0/23", "34.3.151.0/26", "34.3.151.64/26", "136.124.104.0/22", "136.124.108.0/22"},
		},
		{
			name:     "gcd france uses its own region ranges",
			cfg:      renderConfig("u-france-east1"),
			expected: []string{"177.222.80.0/23", "177.222.87.0/26", "177.222.87.64/26", "136.124.104.0/22", "136.124.108.0/22"},
		},
		{
			name:     "nil gcp platform status falls back to public ranges",
			cfg:      RenderConfig{ControllerConfigSpec: &mcfgv1.ControllerConfigSpec{Infra: &configv1.Infrastructure{}}},
			expected: []string{"35.191.0.0/16", "130.211.0.0/22"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gcpHealthCheckSourceRanges(tt.cfg); !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("gcpHealthCheckSourceRanges() = %v, want %v", got, tt.expected)
			}
		})
	}
}
