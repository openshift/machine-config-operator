module github.com/openshift/machine-config-operator

go 1.12

require (
	github.com/14rcole/gopopulate v0.0.0-20180821133914-b175b219e774 // indirect
	github.com/BurntSushi/toml v0.3.1
	github.com/InVisionApp/go-health v2.1.0+incompatible
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.4.2 // indirect
	github.com/Masterminds/sprig v2.20.0+incompatible
	github.com/ajeddeloh/go-json v0.0.0-20170920214419-6a2fe990e083 // indirect
	github.com/ajeddeloh/yaml v0.0.0-20170912190910-6b94386aeefd // indirect
	github.com/alecthomas/units v0.0.0-20190924025748-f65c72e2690d // indirect
	github.com/apparentlymart/go-cidr v1.0.0
	github.com/ashcrow/osrelease v0.0.0-20180626175927-9b292693c55c
	github.com/containerd/continuity v0.0.0-20190827140505-75bee3e2ccb6 // indirect
	github.com/containers/image v3.0.2+incompatible
	github.com/containers/storage v1.13.4
	github.com/coreos/container-linux-config-transpiler v0.9.0
	github.com/coreos/ignition v0.26.0
	github.com/creack/pty v1.1.9 // indirect
	github.com/cri-o/cri-o v1.9.0-beta.2.0.20191003162030-4775e1c05c26
	github.com/davecgh/go-spew v1.1.1
	github.com/docker/docker-credential-helpers v0.6.3 // indirect
	github.com/docker/go-metrics v0.0.1 // indirect
	github.com/elazarl/goproxy v0.0.0-20190911111923-ecfe977594f1 // indirect
	github.com/elazarl/goproxy/ext v0.0.0-20190911111923-ecfe977594f1 // indirect
	github.com/emicklei/go-restful v2.10.0+incompatible // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-bindata/go-bindata v3.1.1+incompatible
	github.com/go-log/log v0.0.0-00010101000000-000000000000 // indirect
	github.com/go-openapi/jsonreference v0.19.3 // indirect
	github.com/go-openapi/spec v0.19.3 // indirect
	github.com/gogo/protobuf v1.3.0 // indirect
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/groupcache v0.0.0-20191002201903-404acd9df4cc // indirect
	github.com/golangci/golangci-lint v1.18.0
	github.com/google/go-cmp v0.3.1 // indirect
	github.com/google/renameio v0.1.0
	github.com/googleapis/gnostic v0.3.1 // indirect
	github.com/gorilla/mux v1.7.3 // indirect
	github.com/hashicorp/golang-lru v0.5.3 // indirect
	github.com/huandu/xstrings v1.2.0 // indirect
	github.com/imdario/mergo v0.3.7
	github.com/joho/godotenv v1.3.0
	github.com/mailru/easyjson v0.7.0 // indirect
	github.com/onsi/ginkgo v1.10.2 // indirect
	github.com/opencontainers/go-digest v1.0.0-rc1
	github.com/openshift/api v3.9.1-0.20191001124347-8033e226059b+incompatible
	github.com/openshift/client-go v0.0.0-20191001081553-3b0e988f8cb0
	github.com/openshift/cluster-api v0.0.0-20190923092624-4024de4fa64d
	github.com/openshift/library-go v0.0.0-20191002112944-09fe7ddc84d4
	github.com/pkg/errors v0.8.1
	github.com/securego/gosec v0.0.0-20191002120514-e680875ea14d
	github.com/spf13/cobra v0.0.5
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.4.0
	github.com/vincent-petithory/dataurl v0.0.0-20160330182126-9a301d65acbb
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	go4.org v0.0.0-20190313082347-94abd6928b1d // indirect
	golang.org/x/crypto v0.0.0-20191002192127-34f69633bfdc // indirect
	golang.org/x/exp v0.0.0-20191002040644-a1355ae1e2c3 // indirect
	golang.org/x/net v0.0.0-20191002035440-2ec189313ef0 // indirect
	golang.org/x/sys v0.0.0-20191002091554-b397fe3ad8ed // indirect
	golang.org/x/time v0.0.0-20190308202827-9d24e82272b4
	golang.org/x/tools v0.0.0-20191002234911-9ade4c73f2af // indirect
	gonum.org/v1/gonum v0.0.0-20190929233944-b20cf7805fc4 // indirect
	gonum.org/v1/netlib v0.0.0-20190926062253-2d6e29b73a19 // indirect
	gopkg.in/yaml.v2 v2.2.4 // indirect
	k8s.io/api v0.0.0
	k8s.io/apiextensions-apiserver v0.0.0
	k8s.io/apimachinery v0.0.0
	k8s.io/client-go v0.0.0
	k8s.io/code-generator v0.0.0
	k8s.io/gengo v0.0.0-20190907103519-ebc107f98eab // indirect
	k8s.io/klog v1.0.0 // indirect
	k8s.io/kube-openapi v0.0.0-20190918143330-0270cf2f1c1d // indirect
	k8s.io/kubelet v0.0.0
)

replace (
	github.com/InVisionApp/go-health => github.com/InVisionApp/go-health v1.1.7-0.20190926150048-b5cab38233bb
	github.com/go-log/log => github.com/go-log/log v0.1.1-0.20181211034820-a514cf01a3eb
	github.com/godbus/dbus => github.com/godbus/dbus v0.0.0-20190623212516-8a1682060722
	github.com/opencontainers/runtime-spec => github.com/opencontainers/runtime-spec v0.1.2-0.20190408193819-a1b50f621a48
	github.com/openshift/cluster-api => github.com/openshift/cluster-api v0.0.0-20191004085540-83f32d3e7070
	github.com/securego/gosec => github.com/securego/gosec v0.0.0-20190709033609-4b59c948083c
	k8s.io/api => k8s.io/api v0.0.0-20190905175716-d9fae7ce8d9e
	k8s.io/apiextensions-apiserver => github.com/openshift/kubernetes-apiextensions-apiserver v0.0.0-20190918161926-8f644eb6e783
	k8s.io/apimachinery => k8s.io/apimachinery v0.0.0-20190913080033-27d36303b655
	k8s.io/apiserver => github.com/openshift/kubernetes-apiserver v0.0.0-20190918160949-bfa5e2e684ad
	k8s.io/cli-runtime => github.com/openshift/kubernetes-cli-runtime v0.0.0-20190918162238-f783a3654da8
	k8s.io/client-go => k8s.io/client-go v0.0.0-20190929040153-a39e818a0987
	k8s.io/cloud-provider => github.com/openshift/kubernetes-cloud-provider v0.0.0-20190918163234-a9c1f33e9fb9
	k8s.io/cluster-bootstrap => github.com/openshift/kubernetes-cluster-bootstrap v0.0.0-20190918163108-da9fdfce26bb
	k8s.io/code-generator => k8s.io/code-generator v0.0.0-20190927045949-f81bca4f5e85
	k8s.io/component-base => github.com/openshift/kubernetes-component-base v0.0.0-20190918160511-547f6c5d7090
	k8s.io/cri-api => github.com/openshift/kubernetes-cri-api v0.0.0-20190828162817-608eb1dad4ac
	k8s.io/csi-translation-lib => github.com/openshift/kubernetes-csi-translation-lib v0.0.0-20190918163402-db86a8c7bb21
	k8s.io/kube-aggregator => github.com/openshift/kubernetes-kube-aggregator v0.0.0-20190918161219-8c8f079fddc3
	k8s.io/kube-controller-manager => github.com/openshift/kubernetes-kube-controller-manager v0.0.0-20190918162944-7a93a0ddadd8
	k8s.io/kube-proxy => github.com/openshift/kubernetes-kube-proxy v0.0.0-20190918162534-de037b596c1e
	k8s.io/kube-scheduler => github.com/openshift/kubernetes-kube-scheduler v0.0.0-20190918162820-3b5c1246eb18
	k8s.io/kubectl => k8s.io/kubectl v0.0.0-20190831152136-eb175a4e3db6
	k8s.io/kubelet => k8s.io/kubelet v0.0.0-20190831152136-ba9cf7ec6904
	k8s.io/kubernetes => github.com/openshift/kubernetes v1.16.0-beta.0.0.20190913145653-2bd9643cee5b
	k8s.io/legacy-cloud-providers => github.com/openshift/kubernetes-legacy-cloud-providers v0.0.0-20190918163543-cfa506e53441
	k8s.io/metrics => github.com/openshift/kubernetes-metrics v0.0.0-20190918162108-227c654b2546
	k8s.io/sample-apiserver => github.com/openshift/kubernetes-sample-apiserver v0.0.0-20190918161442-d4c9c65c82af
)
