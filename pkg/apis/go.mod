module github.com/openshift/machine-config-operator/pkg/apis

go 1.17

require (
	github.com/openshift/api v0.0.0-20220504105152-6f735e7109c8
	k8s.io/api v0.23.4
	k8s.io/apimachinery v0.23.4
)

require (
	github.com/go-logr/logr v1.2.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/google/go-cmp v0.5.7 // indirect
	github.com/google/gofuzz v1.1.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	golang.org/x/net v0.0.0-20220127200216-cd36cc0744dd // indirect
	golang.org/x/text v0.3.7 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	k8s.io/klog/v2 v2.30.0 // indirect
	k8s.io/utils v0.0.0-20220210201930-3a6ce19ff2f9 // indirect
	sigs.k8s.io/json v0.0.0-20211020170558-c049b76a60c6 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.2.0 // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)

replace (
	github.com/InVisionApp/go-health => github.com/InVisionApp/go-health v1.1.7-0.20190926150048-b5cab38233bb
	github.com/go-log/log => github.com/go-log/log v0.1.1-0.20181211034820-a514cf01a3eb
	github.com/godbus/dbus => github.com/godbus/dbus v0.0.0-20190623212516-8a1682060722
	github.com/googleapis/gnostic => github.com/googleapis/gnostic v0.4.1
	github.com/opencontainers/runtime-spec => github.com/opencontainers/runtime-spec v0.1.2-0.20190408193819-a1b50f621a48
	github.com/openshift/cluster-api => github.com/openshift/cluster-api v0.0.0-20191129101638-b09907ac6668
	github.com/securego/gosec => github.com/securego/gosec v0.0.0-20190709033609-4b59c948083c
	k8s.io/api => k8s.io/api v0.23.0
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.23.0
	k8s.io/apimachinery => k8s.io/apimachinery v0.23.0
	k8s.io/apiserver => k8s.io/apiserver v0.23.0
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.23.0
	k8s.io/client-go => k8s.io/client-go v0.23.0
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.23.0
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.23.0
	k8s.io/code-generator => k8s.io/code-generator v0.23.0
	k8s.io/component-base => k8s.io/component-base v0.23.0
	k8s.io/component-helpers => k8s.io/component-helpers v0.23.0
	k8s.io/controller-manager => k8s.io/controller-manager v0.23.0
	k8s.io/cri-api => k8s.io/cri-api v0.23.0
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.23.0
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.23.0
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.23.0
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.23.0
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.23.0
	k8s.io/kubectl => k8s.io/kubectl v0.23.0
	k8s.io/kubelet => k8s.io/kubelet v0.23.0
	k8s.io/kubernetes => k8s.io/kubernetes v1.23.0
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.23.0
	k8s.io/metrics => k8s.io/metrics v0.23.0
	k8s.io/mount-utils => k8s.io/mount-utils v0.23.0
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.23.0
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.23.0
)
