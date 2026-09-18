package resourceapply

import (
	"fmt"
	"testing"
)

func TestIsApplyErrorRetriable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "rpc error is retriable",
			err:      fmt.Errorf("rpc error: code = Unavailable desc = transport is closing"),
			expected: true,
		},
		{
			name:     "x509 certificate error is retriable",
			err:      fmt.Errorf("x509: certificate is valid for 10.0.0.1, not 172.30.0.1"),
			expected: true,
		},
		{
			name:     "x509 certificate signed by unknown authority is retriable",
			err:      fmt.Errorf("x509: certificate signed by unknown authority"),
			expected: true,
		},
		{
			name:     "tls failed to verify certificate is retriable",
			err:      fmt.Errorf("tls: failed to verify certificate: x509: certificate is valid for 10.0.0.1, not 172.30.0.1"),
			expected: true,
		},
		{
			name:     "wrapped x509 error is retriable",
			err:      fmt.Errorf("Get \"https://kubernetes.default.svc:443/api\": x509: certificate is valid for 10.0.0.1, not 172.30.0.1"),
			expected: true,
		},
		{
			name:     "generic error is not retriable",
			err:      fmt.Errorf("some other error"),
			expected: false,
		},
		{
			name:     "connection refused is not retriable",
			err:      fmt.Errorf("dial tcp 10.0.0.1:6443: connect: connection refused"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsApplyErrorRetriable(tt.err)
			if result != tt.expected {
				t.Errorf("IsApplyErrorRetriable(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}
