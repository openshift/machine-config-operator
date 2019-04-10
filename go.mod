module github.com/openshift/machine-config-operator

go 1.12

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/Masterminds/semver v1.4.2 // indirect
	github.com/Masterminds/sprig v2.15.0+incompatible
	github.com/Microsoft/go-winio v0.4.11 // indirect
	github.com/ajeddeloh/go-json v0.0.0-20170920214419-6a2fe990e083 // indirect
	github.com/ajeddeloh/yaml v0.0.0-20170912190910-6b94386aeefd // indirect
	github.com/alecthomas/units v0.0.0-20151022065526-2efee857e7cf // indirect
	github.com/aokoli/goutils v1.0.1 // indirect
	github.com/apparentlymart/go-cidr v1.0.0
	github.com/ashcrow/osrelease v0.0.0-20180626175927-9b292693c55c
	github.com/blang/semver v3.5.1+incompatible
	github.com/containerd/cgroups v0.0.0-20181219155423-39b18af02c41 // indirect
	github.com/containers/image v0.0.0-20190205230957-1c10a197331c
	github.com/containers/storage v0.0.0-20190204185450-0b67c788f2d2
	github.com/coreos/container-linux-config-transpiler v0.9.0
	github.com/coreos/go-semver v0.2.0 // indirect
	github.com/coreos/go-systemd v0.0.0-20180511133405-39ca1b05acc7 // indirect
	github.com/coreos/ignition v0.26.0
	github.com/davecgh/go-spew v1.1.1
	github.com/docker/distribution v2.7.1+incompatible // indirect
	github.com/docker/docker v1.3.3 // indirect
	github.com/docker/go-units v0.3.3 // indirect
	github.com/evanphx/json-patch v4.1.0+incompatible // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-log/log v0.1.0 // indirect
	github.com/godbus/dbus v4.1.0+incompatible // indirect
	github.com/gogo/protobuf v1.1.1 // indirect
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/groupcache v0.0.0-20180513044358-24b0969c4cb7 // indirect
	github.com/google/btree v0.0.0-20180124185431-e89373fe6b4a // indirect
	github.com/google/gofuzz v0.0.0-20170612174753-24818f796faf // indirect
	github.com/google/renameio v0.1.0
	github.com/google/uuid v0.0.0-20161128191214-064e2069ce9c // indirect
	github.com/googleapis/gnostic v0.2.0 // indirect
	github.com/gregjones/httpcache v0.0.0-20180305231024-9cad4c3443a7 // indirect
	github.com/hashicorp/golang-lru v0.0.0-20180201235237-0fb14efe8c47 // indirect
	github.com/huandu/xstrings v1.0.0 // indirect
	github.com/imdario/mergo v0.3.5
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/json-iterator/go v0.0.0-20180701071628-ab8a2e0c74be // indirect
	github.com/kubernetes-sigs/cri-o v0.0.0-20190310204925-0809b248a469
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v0.0.0-20180701023420-4b7aa43c6742 // indirect
	github.com/onsi/ginkgo v1.8.0 // indirect
	github.com/onsi/gomega v1.5.0 // indirect
	github.com/opencontainers/go-digest v1.0.0-rc1 // indirect
	github.com/opencontainers/image-spec v1.0.1 // indirect
	github.com/opencontainers/runc v0.1.1 // indirect
	github.com/opencontainers/runtime-spec v0.0.0-20190207185410-29686dbc5559 // indirect
	github.com/openshift/api v0.0.0-20190313204618-5e45fff0f89e
	github.com/openshift/client-go v0.0.0-20190313214351-8ae2a9c33ba2
	github.com/openshift/kubernetes-drain v0.0.0-20180831174519-c2e51be1758e
	github.com/openshift/library-go v0.0.0-20190128162333-86f3eb4ba0e6
	github.com/pborman/uuid v0.0.0-20170612153648-e790cca94e6c // indirect
	github.com/peterbourgon/diskv v2.0.1+incompatible // indirect
	github.com/pkg/errors v0.8.0
	github.com/sirupsen/logrus v1.3.0 // indirect
	github.com/spf13/cobra v0.0.3
	github.com/spf13/pflag v1.0.1 // indirect
	github.com/stretchr/testify v1.3.0
	github.com/vincent-petithory/dataurl v0.0.0-20160330182126-9a301d65acbb
	go4.org v0.0.0-20180417224846-9599cf28b011 // indirect
	golang.org/x/oauth2 v0.0.0-20190115181402-5dab4167f31c // indirect
	golang.org/x/time v0.0.0-20180412165947-fbb02b2291d2 // indirect
	google.golang.org/genproto v0.0.0-20190201180003-4b09977fb922 // indirect
	google.golang.org/grpc v1.18.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/api v0.0.0-20181128191700-6db15a15d2d3
	k8s.io/apiextensions-apiserver v0.0.0-20190118124337-a384d17938fe
	k8s.io/apimachinery v0.0.0-20181128191346-49ce2735e507
	k8s.io/apiserver v0.0.0-20190118115647-a748535592ba // indirect
	k8s.io/client-go v9.0.0+incompatible
	k8s.io/kube-openapi v0.0.0-20180719232738-d8ea2fe547a4 // indirect
	k8s.io/kubelet v0.0.0-20181128200626-dbc73c1cf048
	k8s.io/kubernetes v1.12.5
	k8s.io/utils v0.0.0-20190125223540-2b1ea019a370 // indirect
)
