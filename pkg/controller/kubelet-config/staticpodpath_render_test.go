package kubeletconfig

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	ign3types "github.com/coreos/ignition/v2/config/v3_5/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
)

// renderedKubeletConf extracts the rendered kubelet.conf contents from the ignition file.
func renderedKubeletConf(t *testing.T, ignFile *ign3types.File) string {
	t.Helper()
	if ignFile == nil || ignFile.Contents.Source == nil {
		t.Fatal("no kubelet ignition file rendered")
	}
	src := *ignFile.Contents.Source
	idx := strings.Index(src, ",")
	if idx < 0 {
		t.Fatalf("unexpected ignition source format: %.60s", src)
	}
	payload := src[idx+1:]
	if strings.Contains(src[:idx], "base64") {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			t.Fatalf("could not base64-decode ignition contents: %v", err)
		}
		return string(decoded)
	}
	decoded, err := url.PathUnescape(payload)
	if err != nil {
		t.Fatalf("could not decode ignition contents: %v", err)
	}
	return decoded
}

func TestStaticPodPathEmptyRender(t *testing.T) {
	// simulate the worker template default
	originalKubeConfig := &kubeletconfigv1beta1.KubeletConfiguration{
		TypeMeta: metav1.TypeMeta{
			Kind:       "KubeletConfiguration",
			APIVersion: kubeletconfigv1beta1.SchemeGroupVersion.String(),
		},
		StaticPodPath: "/etc/kubernetes/manifests",
		ClusterDomain: "cluster.local",
	}

	// user KubeletConfig CR with staticPodPath: ""
	userCfg, err := json.Marshal(&kubeletconfigv1beta1.KubeletConfiguration{
		StaticPodPath: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	kc := &mcfgv1.KubeletConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "disable-static-pods"},
		Spec: mcfgv1.KubeletConfigSpec{
			KubeletConfig: &runtime.RawExtension{Raw: userCfg},
		},
	}

	kubeletIgn, _, _, err := generateKubeletIgnFiles(kc, originalKubeConfig)
	if err != nil {
		t.Fatalf("generateKubeletIgnFiles failed: %v", err)
	}

	conf := renderedKubeletConf(t, kubeletIgn)
	// marshaling the struct drops the empty key via omitempty, so this is
	// equivalent to the user not mentioning staticPodPath at all and the
	// template default must be retained
	if !strings.Contains(conf, "/etc/kubernetes/manifests") {
		t.Errorf("unset staticPodPath should keep the template default")
	}

	// also test the raw form a user would actually write, with explicit empty string key present
	rawJSON := []byte(`{"staticPodPath":""}`)
	kc2 := kc.DeepCopy()
	kc2.Spec.KubeletConfig = &runtime.RawExtension{Raw: rawJSON}
	original2 := originalKubeConfig.DeepCopy()

	kubeletIgn2, _, _, err := generateKubeletIgnFiles(kc2, original2)
	if err != nil {
		t.Fatalf("generateKubeletIgnFiles (raw) failed: %v", err)
	}
	conf2 := renderedKubeletConf(t, kubeletIgn2)
	if strings.Contains(conf2, "/etc/kubernetes/manifests") {
		t.Errorf("FUNCTIONAL GAP (raw form): rendered config still contains /etc/kubernetes/manifests")
	}

	// YAML payloads are accepted by DecodeKubeletConfig, so the empty value
	// detection must handle them too
	kc3 := kc.DeepCopy()
	kc3.Spec.KubeletConfig = &runtime.RawExtension{Raw: []byte("staticPodPath: \"\"\n")}
	original3 := originalKubeConfig.DeepCopy()

	kubeletIgn3, _, _, err := generateKubeletIgnFiles(kc3, original3)
	if err != nil {
		t.Fatalf("generateKubeletIgnFiles (yaml) failed: %v", err)
	}
	conf3 := renderedKubeletConf(t, kubeletIgn3)
	if strings.Contains(conf3, "/etc/kubernetes/manifests") {
		t.Errorf("FUNCTIONAL GAP (yaml form): rendered config still contains /etc/kubernetes/manifests")
	}
}
