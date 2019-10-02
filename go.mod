module github.com/openshift/machine-config-operator

go 1.12

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/InVisionApp/go-health v2.1.0+incompatible
	github.com/InVisionApp/go-logger v1.0.1 // indirect
	github.com/Masterminds/goutils v1.1.0 // indirect
	github.com/Masterminds/semver v1.4.2 // indirect
	github.com/Masterminds/sprig v2.20.0+incompatible
	github.com/Microsoft/go-winio v0.4.12 // indirect
	github.com/Microsoft/hcsshim v0.8.6 // indirect
	github.com/ajeddeloh/go-json v0.0.0-20170920214419-6a2fe990e083 // indirect
	github.com/ajeddeloh/yaml v0.0.0-20170912190910-6b94386aeefd // indirect
	github.com/alecthomas/units v0.0.0-20151022065526-2efee857e7cf // indirect
	github.com/apparentlymart/go-cidr v1.0.0
	github.com/ashcrow/osrelease v0.0.0-20180626175927-9b292693c55c
	github.com/containerd/cgroups v0.0.0-20190710130057-d596c78861b1 // indirect
	github.com/containerd/continuity v0.0.0-20190827140505-75bee3e2ccb6 // indirect
	github.com/containers/image v3.0.2+incompatible
	github.com/containers/storage v0.0.0-20190204185450-0b67c788f2d2
	github.com/coreos/container-linux-config-transpiler v0.9.0
	github.com/coreos/go-semver v0.3.0 // indirect
	github.com/coreos/go-systemd v0.0.0-20190620071333-e64a0ec8b42a // indirect
	github.com/coreos/ignition v0.26.0
	github.com/davecgh/go-spew v1.1.1
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/docker v0.0.0-20190528192756-b1239f0a9f5a // indirect
	github.com/docker/docker-credential-helpers v0.6.3 // indirect
	github.com/docker/go-connections v0.4.0 // indirect
	github.com/docker/go-metrics v0.0.1 // indirect
	github.com/docker/go-units v0.4.0 // indirect
	github.com/docker/libtrust v0.0.0-20160708172513-aabc10ec26b7 // indirect
	github.com/evanphx/json-patch v4.5.0+incompatible // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-bindata/go-bindata v0.0.0-20190711162640-ee3c2418e368
	github.com/go-log/log v0.1.0 // indirect
	github.com/godbus/dbus v0.0.0-20181101234600-2ff6f7ffd60f // indirect
	github.com/gogo/protobuf v1.2.2-0.20190723190241-65acae22fc9d // indirect
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/groupcache v0.0.0-20190702054246-869f871628b6 // indirect
	github.com/google/renameio v0.1.0
	github.com/google/uuid v1.1.1 // indirect
	github.com/googleapis/gnostic v0.3.0 // indirect
	github.com/gorilla/mux v1.7.3 // indirect
	github.com/hashicorp/golang-lru v0.5.1 // indirect
	github.com/huandu/xstrings v1.2.0 // indirect
	github.com/imdario/mergo v0.3.7
	github.com/joho/godotenv v1.3.0
	github.com/konsorten/go-windows-terminal-sequences v1.0.2 // indirect
	github.com/kubernetes-sigs/cri-o v1.12.10
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/mtrmac/gpgme v0.0.0-20170102180018-b2432428689c // indirect
	github.com/opencontainers/go-digest v1.0.0-rc1
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/opencontainers/runc v0.1.1 // indirect
	github.com/opencontainers/runtime-spec v0.0.0-20190207185410-29686dbc5559 // indirect
	github.com/openshift/api v0.0.0-20190725193935-b7d4eb0fa1e0
	github.com/openshift/client-go v0.0.0-20190627172412-c44a8b61b9f4
	github.com/openshift/cluster-api v0.0.0-20190829140302-072f7d777dc8
	github.com/openshift/library-go v0.0.0-20190704075327-f8abdcd57c46
	github.com/pborman/uuid v0.0.0-20180906182336-adf5a7427709 // indirect
	github.com/pkg/errors v0.8.1
	github.com/sirupsen/logrus v1.4.2 // indirect
	github.com/spf13/cobra v0.0.5
	github.com/spf13/pflag v1.0.3
	github.com/stretchr/testify v1.3.0
	github.com/vincent-petithory/dataurl v0.0.0-20160330182126-9a301d65acbb
	github.com/xeipuuv/gojsonpointer v0.0.0-20190905194746-02993c407bfb // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xeipuuv/gojsonschema v1.1.0 // indirect
	go4.org v0.0.0-20190313082347-94abd6928b1d // indirect
	golang.org/x/crypto v0.0.0-20191001170739-f9e2070545dc // indirect
	golang.org/x/net v0.0.0-20191002035440-2ec189313ef0 // indirect
	golang.org/x/oauth2 v0.0.0-20190604053449-0f29369cfe45 // indirect
	golang.org/x/sys v0.0.0-20191002063906-3421d5a6bb1c // indirect
	golang.org/x/time v0.0.0-20190308202827-9d24e82272b4
	golang.org/x/tools v0.0.0-20191001184121-329c8d646ebe // indirect
	google.golang.org/appengine v1.6.1 // indirect
	google.golang.org/genproto v0.0.0-20190708153700-3bdd9d9f5532 // indirect
	google.golang.org/grpc v1.22.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gotest.tools v2.2.0+incompatible // indirect
	k8s.io/api v0.0.0-00010101000000-000000000000
	k8s.io/apiextensions-apiserver v0.0.0-00010101000000-000000000000
	k8s.io/apimachinery v0.0.0-00010101000000-000000000000
	k8s.io/apiserver v0.0.0-00010101000000-000000000000 // indirect
	k8s.io/client-go v0.0.0-00010101000000-000000000000
	k8s.io/cloud-provider v0.0.0-20190704110555-622ee4517bee // indirect
	k8s.io/code-generator v0.0.0-20190925195306-32dfb485ddce
	k8s.io/gengo v0.0.0-20190822140433-26a664648505 // indirect
	k8s.io/klog v1.0.0 // indirect
	k8s.io/kube-openapi v0.0.0-20190816220812-743ec37842bf // indirect
	k8s.io/kubelet v0.0.0-00010101000000-000000000000
	k8s.io/kubernetes v0.0.0-00010101000000-000000000000
	k8s.io/utils v0.0.0-20190712101616-fac88abaa102 // indirect
)

replace (
	k8s.io/api => github.com/openshift/kubernetes-api v0.0.0-20190709164144-5b6d4ec96213
	k8s.io/apiextensions-apiserver => github.com/openshift/kubernetes-apiextensions-apiserver v0.0.0-20190729141842-ef1fb026cb0e
	k8s.io/apimachinery => github.com/openshift/kubernetes-apimachinery v0.0.0-20190321181449-eab709b58ad6
	k8s.io/apiserver => github.com/openshift/kubernetes-apiserver v0.0.0-20190723190532-392b5b3e5888
	k8s.io/client-go => github.com/openshift/kubernetes-client-go v11.0.1-0.20190701222832-70952d66b5d1+incompatible
	k8s.io/code-generator => github.com/openshift/kubernetes-code-generator v0.0.0-20171023130718-f40157d9638d
	k8s.io/kubelet => github.com/openshift/kubernetes-kubelet v0.0.0-20190802155351-60e43eba885b
	k8s.io/kubernetes => github.com/openshift/kubernetes v1.14.1-0.20190803235440-f1e14d6cbefa
)
