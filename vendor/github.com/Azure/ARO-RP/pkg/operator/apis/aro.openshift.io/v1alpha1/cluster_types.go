package v1alpha1

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/openshift/api/operator/v1"
)

type BannerContent string

const (
	// not using iota to force a stable value mapping
	BannerDisabled       BannerContent = ""
	BannerContactSupport BannerContent = "ContactSupport"

	SingletonClusterName        = "cluster"
	InternetReachableFromMaster = "InternetReachableFromMaster"
	InternetReachableFromWorker = "InternetReachableFromWorker"
	MachineValid                = "MachineValid"
	ServicePrincipalValid       = "ServicePrincipalValid"

	ManagedUpgradeOperatorStatus = "ManagedUpgradeOperatorStatus"

	// advisor checks
	DefaultIngressCertificate = "DefaultIngressCertificate"
	DefaultClusterDNS         = "DefaultClusterDNS"
	GuardRailsStatus          = "GuardRailsStatus"
)

// AllConditionTypes is a operator conditions currently in use, any condition not in this list is not
// added to the operator.status.conditions list
func AllConditionTypes() []string {
	return []string{
		InternetReachableFromMaster,
		InternetReachableFromWorker,
		MachineValid,
		ServicePrincipalValid,
		ManagedUpgradeOperatorStatus,
		DefaultIngressCertificate,
		DefaultClusterDNS,
		GuardRailsStatus,
	}
}

// ClusterChecksTypes represents checks performed on the cluster to verify basic functionality
func ClusterChecksTypes() []string {
	return []string{
		InternetReachableFromMaster,
		InternetReachableFromWorker,
		MachineValid,
		ServicePrincipalValid,
	}
}

type GenevaLoggingSpec struct {
	// +kubebuilder:validation:Pattern:=`[0-9]+.[0-9]+`
	ConfigVersion string `json:"configVersion,omitempty"`
	// +kubebuilder:validation:Enum=AROClusterLogsINT;AROClusterLogsPROD;AROClusterLogs
	MonitoringGCSAccount string `json:"monitoringGCSAccount,omitempty"`
	// +kubebuilder:validation:Enum=DiagnosticsProd;Test;CaFairfax
	MonitoringGCSEnvironment string `json:"monitoringGCSEnvironment,omitempty"`
	// +kubebuilder:validation:Enum=AROClusterLogsINT;AROClusterLogsPROD;AROClusterLogs
	MonitoringGCSNamespace string `json:"monitoringGCSNamespace,omitempty"`
}

type InternetCheckerSpec struct {
	URLs []string `json:"urls,omitempty"`
}

type OperatorFlags map[string]string

func (f OperatorFlags) GetWithDefault(key string, sentinel string) string {
	val, ext := f[key]
	if !ext {
		return sentinel
	}
	return val
}

func (f OperatorFlags) GetSimpleBoolean(key string) bool {
	v, ext := f[key]
	if ext {
		// Only accept a literal true, rather than accepting anything other than
		// literal false as true
		// TODO: Is this the best behaviour?
		if strings.EqualFold(v, "true") {
			return true
		}
	}
	return false
}

// ClusterSpec defines the desired state of Cluster
type ClusterSpec struct {
	// ResourceID is the Azure resourceId of the cluster
	ResourceID               string              `json:"resourceId,omitempty"`
	ClusterResourceGroupID   string              `json:"clusterResourceGroupId,omitempty"`
	Domain                   string              `json:"domain,omitempty"`
	ACRDomain                string              `json:"acrDomain,omitempty"`
	AZEnvironment            string              `json:"azEnvironment,omitempty"`
	Location                 string              `json:"location,omitempty"`
	InfraID                  string              `json:"infraId,omitempty"`
	StorageSuffix            string              `json:"storageSuffix,omitempty"`
	ArchitectureVersion      int                 `json:"architectureVersion,omitempty"`
	GenevaLogging            GenevaLoggingSpec   `json:"genevaLogging,omitempty"`
	InternetChecker          InternetCheckerSpec `json:"internetChecker,omitempty"`
	VnetID                   string              `json:"vnetId,omitempty"`
	APIIntIP                 string              `json:"apiIntIP,omitempty"`
	IngressIP                string              `json:"ingressIP,omitempty"`
	GatewayDomains           []string            `json:"gatewayDomains,omitempty"`
	GatewayPrivateEndpointIP string              `json:"gatewayPrivateEndpointIP,omitempty"`
	Banner                   Banner              `json:"banner,omitempty"`
	ServiceSubnets           []string            `json:"serviceSubnets,omitempty"`

	// OperatorFlags defines feature gates for the ARO Operator
	OperatorFlags OperatorFlags `json:"operatorflags,omitempty"`
}

// Banner defines if a Banner should be shown to the customer
type Banner struct {
	Content BannerContent `json:"content,omitempty"`
}

// ClusterStatus defines the observed state of Cluster
type ClusterStatus struct {
	OperatorVersion   string                         `json:"operatorVersion,omitempty"`
	Conditions        []operatorv1.OperatorCondition `json:"conditions,omitempty"`
	RedHatKeysPresent []string                       `json:"redHatKeysPresent,omitempty"`
}

// Cluster is the Schema for the clusters API
// +kubebuilder:object:root=true
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterSpec   `json:"spec,omitempty"`
	Status ClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterList contains a list of Cluster
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cluster{}, &ClusterList{})
}
