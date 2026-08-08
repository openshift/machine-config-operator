package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenInBand(t *testing.T) {
	cases := []struct {
		name                  string
		token, floor, ceiling string
		expected              bool
	}{
		{"within band", "9.6", "9.2", "10.2", true},
		{"equal to floor", "9.2", "9.2", "10.2", true},
		{"equal to ceiling", "10.2", "9.2", "10.2", true},
		{"below floor", "9.1", "9.2", "10.2", false},
		{"above ceiling", "10.3", "9.2", "10.2", false},
		{"floor equals ceiling, exact match", "9.2", "9.2", "9.2", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tokenInBand(tc.token, tc.floor, tc.ceiling))
		})
	}
}
