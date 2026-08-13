package common

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// The in-cluster operator reads operand images from the
// machine-config-operator-images ConfigMap (substituted by the CVO from
// install/image-references), while the bootstrap discovers them from the
// release payload's image references. Any ControllerConfigImages field
// populated at bootstrap but missing from the ConfigMap renders different
// MachineConfig content at bootstrap vs in-cluster, which changes the
// rendered MC hash and degrades every master with a bootstrap MC mismatch
// (openshift/machine-config-operator#6326 e2e-openstack).
func TestInstallImagesConfigMapCoversControllerConfigImages(t *testing.T) {
	raw, err := os.ReadFile("../../../install/0000_80_machine-config_02_images.configmap.yaml")
	require.NoError(t, err)

	var cm struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cm))

	var imagesJSON map[string]string
	require.NoError(t, json.Unmarshal([]byte(cm.Data["images.json"]), &imagesJSON))

	// kubeVipImage is intentionally absent: the kube-vip image is not in the
	// release payload yet, so neither bootstrap nor the ConfigMap may carry
	// it (both sides render it empty, preserving parity). Once the payload
	// ships kube-vip, it must be added to the ConfigMap, image-references,
	// and the bootstrap lookup together.
	exceptions := map[string]bool{"kubeVipImage": true}

	typ := reflect.TypeOf(ControllerConfigImages{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		require.NotEmpty(t, tag, "field %s has no json tag", typ.Field(i).Name)
		if exceptions[tag] {
			require.NotContains(t, imagesJSON, tag,
				"%s is listed as payload-absent but present in the images ConfigMap; remove it from the exceptions", tag)
			continue
		}
		require.Contains(t, imagesJSON, tag,
			"images ConfigMap is missing %q: bootstrap and in-cluster template rendering would diverge and fail install with a bootstrap MC mismatch", tag)
		require.NotEmpty(t, imagesJSON[tag], "images ConfigMap has an empty %q", tag)
	}
}
