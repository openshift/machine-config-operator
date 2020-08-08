module github.com/openshift/machine-config-operator

go 1.13

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/InVisionApp/go-health v2.1.0+incompatible
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.4.2 // indirect
	github.com/Masterminds/sprig v2.20.0+incompatible
	github.com/OpenPeeDeeP/depguard v1.0.1 // indirect
	github.com/apparentlymart/go-cidr v1.0.0
	github.com/ashcrow/osrelease v0.0.0-20180626175927-9b292693c55c
	github.com/clarketm/json v1.14.1
	github.com/containers/image v3.0.2+incompatible
	github.com/containers/image/v5 v5.5.1
	github.com/containers/storage v1.20.2
	github.com/coreos/fcct v0.5.0
	github.com/coreos/go-semver v0.3.0
	github.com/coreos/go-systemd v0.0.0-20190719114852-fd7a80b32e1f // indirect
	github.com/coreos/ign-converter v0.0.0-20200629171308-e40a44f244c5
	github.com/coreos/ignition v0.35.0
	github.com/coreos/ignition/v2 v2.3.0
	github.com/davecgh/go-spew v1.1.1
	github.com/docker/spdystream v0.0.0-20181023171402-6480d4af844c // indirect
	github.com/elazarl/goproxy v0.0.0-20190911111923-ecfe977594f1 // indirect
	github.com/elazarl/goproxy/ext v0.0.0-20190911111923-ecfe977594f1 // indirect
	github.com/emicklei/go-restful v2.10.0+incompatible // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-bindata/go-bindata/v3 v3.1.3
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/groupcache v0.0.0-20191002201903-404acd9df4cc // indirect
	github.com/golang/mock v1.3.1 // indirect
	github.com/golangci/golangci-lint v1.18.0
	github.com/google/go-cmp v0.3.1
	github.com/google/renameio v0.1.0
	github.com/googleapis/gnostic v0.3.1 // indirect
	github.com/gostaticanalysis/analysisutil v0.0.3 // indirect
	github.com/hashicorp/golang-lru v0.5.3 // indirect
	github.com/huandu/xstrings v1.2.0 // indirect
	github.com/imdario/mergo v0.3.9
	github.com/magiconair/properties v1.8.1 // indirect
	github.com/opencontainers/go-digest v1.0.0
	github.com/openshift/api v3.9.1-0.20191111211345-a27ff30ebf09+incompatible
	github.com/openshift/client-go v0.0.0-20200320150128-a906f3d8e723
	github.com/openshift/library-go v0.0.0-20200320155611-2a351bebf158
	github.com/openshift/runtime-utils v0.0.0-20191011150825-9169de69ebf6
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.1.0
	github.com/securego/gosec v0.0.0-20191002120514-e680875ea14d
	github.com/spf13/cobra v0.0.5
	github.com/spf13/jwalterweatherman v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	github.com/ultraware/funlen v0.0.2 // indirect
	github.com/vincent-petithory/dataurl v0.0.0-20160330182126-9a301d65acbb
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	google.golang.org/appengine v1.6.1 // indirect
	k8s.io/api v0.18.3
	k8s.io/apiextensions-apiserver v0.18.0
	k8s.io/apimachinery v0.18.3
	k8s.io/client-go v0.18.0
	k8s.io/code-generator v0.18.3
	k8s.io/kubectl v0.0.0
	k8s.io/kubelet v0.18.0
)

replace (
	github.com/InVisionApp/go-health => github.com/InVisionApp/go-health v1.1.7-0.20190926150048-b5cab38233bb
	github.com/go-log/log => github.com/go-log/log v0.1.1-0.20181211034820-a514cf01a3eb
	github.com/godbus/dbus => github.com/godbus/dbus v0.0.0-20190623212516-8a1682060722
	github.com/opencontainers/runtime-spec => github.com/opencontainers/runtime-spec v0.1.2-0.20190408193819-a1b50f621a48
	github.com/openshift/api => github.com/openshift/api v0.0.0-20200609191024-dca637550e8c
	github.com/openshift/cluster-api => github.com/openshift/cluster-api v0.0.0-20191004085540-83f32d3e7070
	github.com/securego/gosec => github.com/securego/gosec v0.0.0-20190709033609-4b59c948083c
	k8s.io/api => k8s.io/api v0.18.0
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.18.0
	k8s.io/apimachinery => k8s.io/apimachinery v0.18.0
	k8s.io/apiserver => k8s.io/apiserver v0.18.0
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.18.0
	k8s.io/client-go => k8s.io/client-go v0.18.0
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.18.0
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.18.0
	k8s.io/code-generator => k8s.io/code-generator v0.18.0
	k8s.io/component-base => k8s.io/component-base v0.18.0
	k8s.io/cri-api => k8s.io/cri-api v0.18.0
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.18.0
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.18.0
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.18.0
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.18.0
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.18.0
	k8s.io/kubectl => k8s.io/kubectl v0.18.0
	k8s.io/kubelet => k8s.io/kubelet v0.18.0
	k8s.io/kubernetes => k8s.io/kubernetes v1.18.0
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.18.0
	k8s.io/metrics => k8s.io/metrics v0.18.0
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.18.0
)
