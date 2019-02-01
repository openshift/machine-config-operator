package operator

import (
	"fmt"
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
