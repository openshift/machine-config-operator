package containers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAddLatestTagIfMissing(t *testing.T) {
	testCases := []struct {
		name        string
		pullspec    string
		expectedTag string
		errExpected bool
	}{
		{
			name:        "no tag provided",
			pullspec:    "quay.io/example/image",
			expectedTag: ":latest",
		},
		{
			name:        "tag provided",
			pullspec:    "quay.io/example/image:tag",
			expectedTag: ":tag",
		},
		{
			name:        "fully qualified pullspec",
			pullspec:    "quay.io/example/image@sha256:544d9fd59f8c711929d53e50ac22b19b329d95c2fcf1093cb590ac255267b2d8",
			expectedTag: "sha256:544d9fd59f8c711929d53e50ac22b19b329d95c2fcf1093cb590ac255267b2d8",
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			result, err := AddLatestTagIfMissing(testCase.pullspec)

			if testCase.errExpected {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.True(t, strings.HasSuffix(result, testCase.expectedTag))
			}
		})
	}
}
