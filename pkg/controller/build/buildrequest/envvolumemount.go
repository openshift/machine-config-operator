package buildrequest

import (
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	corev1 "k8s.io/api/core/v1"
)

// Keeps all of the env var and volume mount options in one place for
// consistently constructing these objects.
type envVolumeAndMountOpts struct {
	name       string
	envVarName string
	mountpoint string
}

// Gets the options for handling etc-pki-entitlements.
func optsForEtcPkiEntitlements() envVolumeAndMountOpts {
	return envVolumeAndMountOpts{
		name:       constants.EtcPkiEntitlementSecretName,
		envVarName: "ETC_PKI_ENTITLEMENT_MOUNTPOINT",
		mountpoint: "/etc/pki/entitlement",
	}
}

// Gets the options for handling /etc/yum.repos.d.
func optsForEtcYumReposD() envVolumeAndMountOpts {
	return envVolumeAndMountOpts{
		name:       constants.EtcYumReposDConfigMapName,
		envVarName: "ETC_YUM_REPOS_D_MOUNTPOINT",
		mountpoint: "/etc/yum.repos.d",
	}
}

// Gets the options for handling etc-pki-rpm-gpg.
func optsForEtcRpmGpgKeys() envVolumeAndMountOpts {
	return envVolumeAndMountOpts{
		name:       constants.EtcPkiRpmGpgSecretName,
		envVarName: "ETC_PKI_RPM_GPG_MOUNTPOINT",
		mountpoint: "/etc/pki/rpm-gpg",
	}
}

func (e *envVolumeAndMountOpts) mountMode() *int32 {
	// Octal: 0755.
	var mountMode int32 = 493
	return &mountMode
}

// Constructs the volume object to refer to a ConfigMap.
func (e *envVolumeAndMountOpts) volumeForConfigMap() corev1.Volume {
	return corev1.Volume{
		Name: e.name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				DefaultMode: e.mountMode(),
				LocalObjectReference: corev1.LocalObjectReference{
					Name: e.name,
				},
			},
		},
	}
}

// Constructs the volume object to refer to a Secret.
func (e *envVolumeAndMountOpts) volumeForSecret(secretName string) corev1.Volume {
	return corev1.Volume{
		Name: e.name,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				DefaultMode: e.mountMode(),
				SecretName:  secretName,
			},
		},
	}
}

// Constructs the envvar object.
func (e *envVolumeAndMountOpts) envVar() corev1.EnvVar {
	return corev1.EnvVar{
		Name:  e.envVarName,
		Value: e.mountpoint,
	}
}

// Constructs the volume mount object.
func (e *envVolumeAndMountOpts) volumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      e.name,
		MountPath: e.mountpoint,
	}
}
