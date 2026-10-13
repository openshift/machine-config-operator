package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v2"
)

func TestParseVersion(t *testing.T) {
	verdata := `rpm-ostree:
  Version: '2022.10'
  Git: 6b302116c969397fd71899e3b9bb3b8c100d1af9
  Features:
   - rust
   - compose
   - rhsm
`
	var q RpmOstreeVersionData
	if err := yaml.UnmarshalStrict([]byte(verdata), &q); err != nil {
		panic(err)
	}

	assert.Equal(t, "2022.10", q.Root.Version)
	assert.Contains(t, q.Root.Features, "rust")
	assert.NotContains(t, q.Root.Features, "container")
}
