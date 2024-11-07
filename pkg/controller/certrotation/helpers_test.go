package certrotationcontroller

import (
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func getGoodMAOSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MachineAPINamespace,
		},
		Data: map[string][]byte{"disableTemplating": []byte("true"), "userData": []byte(`{"ignition":{"config":{"merge":[{"source":"https://test-cluster-api:22623/config/worker"}]},"security":{"tls":{"certificateAuthorities":[{"source":"data:text/plain;charset=utf-8;base64,LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tClJPT1QgQ0EgREFUQQotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="}]}},"version":"3.2.0"}}`)},
	}
}

func getBadMAOSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MachineAPINamespace,
		},
		Data: map[string][]byte{"disableTemplating": []byte("true"), "userData": []byte(`bad data`)},
	}
}

func getMachineSet(name string) *machinev1beta1.MachineSet {
	return &machinev1beta1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ctrlcommon.MachineAPINamespace,
		},
	}
}
