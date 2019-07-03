package operator

import (
	"fmt"
	"strings"
	"testing"
)

func TestClusterDNSIP(t *testing.T) {
	tests := []struct {
		Range  string
		Output string
		Error  bool
	}{{
		Range:  "192.168.2.0/20",
		Output: "192.168.0.10",
	}, {
		Range:  "2001:db8::/32",
		Output: "2001:db8::a",
	}, {
		Range: "192.168.1.254/32",
		Error: true,
	}}
	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("clusterDNSIP(%#v)", test.Range)
			gotIP, err := clusterDNSIP(test.Range)
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				}
			}
			if gotIP != test.Output {
				t.Fatalf("%s failed: got = %s want = %s", desc, gotIP, test.Output)
			}
		})
	}
}

func TestRenderAsset(t *testing.T) {
	tests := []struct {
		Path         string
		RenderConfig *renderConfig
		FindExpected string
		Error        bool
	}{{
		// Simple test
		Path: "manifests/machineconfigcontroller/clusterrolebinding.yaml",
		RenderConfig: &renderConfig{
			TargetNamespace: "testing-namespace",
		},
		FindExpected: "namespace: testing-namespace",
	}, {
		// Nested field test
		Path: "manifests/machineconfigcontroller/deployment.yaml",
		RenderConfig: &renderConfig{
			TargetNamespace: "testing-namespace",
			Images: &RenderConfigImages{
				MachineConfigOperator: "{MCO: PLACEHOLDER}",
			},
		},
		FindExpected: "image: {MCO: PLACEHOLDER}",
	}, {
		// Render same template as previous test
		// But with a template field missing
		Path: "manifests/machineconfigcontroller/deployment.yaml",
		RenderConfig: &renderConfig{
			TargetNamespace: "testing-namespace",
		},
		Error: true,
	}, {
		// Bad path, will cause asset error
		Path:  "BAD PATH",
		Error: true,
	}}

	for idx, test := range tests {
		t.Run(fmt.Sprintf("case#%d", idx), func(t *testing.T) {
			desc := fmt.Sprintf("Path(%#v), RenderConfig(%#v)", test.Path, test.RenderConfig)
			buf, err := renderAsset(test.RenderConfig, test.Path)
			// The template lib will throw an err if a template field is missing
			if err != nil {
				if !test.Error {
					t.Fatalf("%s failed: %s", desc, err.Error())
				} else {
					return
				}
			}
			if buf == nil || len(buf) == 0 {
				t.Fatalf("Buffer is empty")
			}
			// Verify that the buf can be converted back into a string safely
			str := fmt.Sprintf("%s", buf)
			if str == "" || len(str) == 0 {
				t.Fatalf("Buffer is not a valid string!")
			}
			// Verify that any FindExpected values are actually in the string
			if test.FindExpected != "" {
				if !strings.Contains(str, test.FindExpected) {
					t.Fatalf("Rendered template does not contain expected values: %s, \nGot: %s", test.FindExpected, str)
				}
			}
		})
	}
}
