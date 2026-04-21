package template

import (
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

func TestCloudPlatformLoadBalancerIPsOrdering(t *testing.T) {
	// Test IPs in different orders to verify sorting
	ip1 := configv1.IP("192.168.1.1")
	ip2 := configv1.IP("10.0.0.1")
	ip3 := configv1.IP("172.16.0.1")

	// Expected sorted order: 10.0.0.1, 172.16.0.1, 192.168.1.1
	expectedSorted := []configv1.IP{ip2, ip3, ip1}

	// Three different input orderings
	order1 := []configv1.IP{ip1, ip2, ip3}
	order2 := []configv1.IP{ip3, ip1, ip2}
	order3 := []configv1.IP{ip2, ip3, ip1}

	tests := []struct {
		platform configv1.PlatformType
		ips      []configv1.IP
	}{
		{configv1.GCPPlatformType, order1},
		{configv1.AWSPlatformType, order2},
		{configv1.AzurePlatformType, order3},
	}

	for _, tt := range tests {
		t.Run(string(tt.platform), func(t *testing.T) {
			cfg := buildRenderConfig(tt.platform, tt.ips)

			var got interface{}
			var err error

			got, err = cloudPlatformAPIIntLoadBalancerIPs(cfg)
			if err != nil {
				t.Fatalf("unexpected error getting cloud APIInt LB IPs: %v", err)
			}

			if !reflect.DeepEqual(got, expectedSorted) {
				t.Errorf("Cloud APIInt LB IPs not sorted correctly: got=%v, want=%v", got, expectedSorted)
			}
			got, err = cloudPlatformAPILoadBalancerIPs(cfg)
			if err != nil {
				t.Fatalf("unexpected error getting cloud API LB IPs: %v", err)
			}

			if !reflect.DeepEqual(got, expectedSorted) {
				t.Errorf("Cloud API LB IPs not sorted correctly: got=%v, want=%v", got, expectedSorted)
			}
			got, err = cloudPlatformIngressLoadBalancerIPs(cfg)
			if err != nil {
				t.Fatalf("unexpected error getting cloud Ingress LB IPs: %v", err)
			}

			if !reflect.DeepEqual(got, expectedSorted) {
				t.Errorf("Cloud Ingress LB IPs not sorted correctly: got=%v, want=%v", got, expectedSorted)
			}

		})
	}
}

// Helper function to build RenderConfig for testing
func buildRenderConfig(platform configv1.PlatformType, ips []configv1.IP) RenderConfig {
	config := &mcfgv1.ControllerConfigSpec{
		Infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				Platform: platform,
				PlatformStatus: &configv1.PlatformStatus{
					Type: platform,
				},
			},
		},
	}

	lbConfig := &configv1.CloudLoadBalancerConfig{
		DNSType:       configv1.ClusterHostedDNSType,
		ClusterHosted: &configv1.CloudLoadBalancerIPs{},
	}
	lbConfig.ClusterHosted.APIIntLoadBalancerIPs = append([]configv1.IP(nil), ips...)
	lbConfig.ClusterHosted.APILoadBalancerIPs = append([]configv1.IP(nil), ips...)
	lbConfig.ClusterHosted.IngressLoadBalancerIPs = append([]configv1.IP(nil), ips...)

	// Set platform-specific config
	switch platform {
	case configv1.GCPPlatformType:
		config.Infra.Status.PlatformStatus.GCP = &configv1.GCPPlatformStatus{
			CloudLoadBalancerConfig: lbConfig,
		}
	case configv1.AWSPlatformType:
		config.Infra.Status.PlatformStatus.AWS = &configv1.AWSPlatformStatus{
			CloudLoadBalancerConfig: lbConfig,
		}
	case configv1.AzurePlatformType:
		config.Infra.Status.PlatformStatus.Azure = &configv1.AzurePlatformStatus{
			CloudLoadBalancerConfig: lbConfig,
		}
	}

	return RenderConfig{
		ControllerConfigSpec: config,
	}
}
