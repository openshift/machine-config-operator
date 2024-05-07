package build

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	mcPoolAnnotation          string = "machineconfiguration.openshift.io/pool"
	machineConfigJSONFilename string = "machineconfig.json.gz"
	buildahImagePullspec      string = "quay.io/buildah/stable:latest"
)

//go:embed assets/Containerfile.on-cluster-build-template
var dockerfileTemplate string

//go:embed assets/wait.sh
var waitScript string

//go:embed assets/buildah-build.sh
var buildahBuildScript string

//go:embed assets/podman-build.sh
var podmanBuildScript string

// Represents a given image pullspec and the location of the pull secret.
type ImageInfo struct {
	// The pullspec for a given image (e.g., registry.hostname.com/orp/repo:tag)
	Pullspec string
	// The name of the K8s secret required for pulling the aforementioned image.
	PullSecret corev1.LocalObjectReference
}

// Represents the request to build a layered OS image.
type ImageBuildRequest struct {
	// The target Build object
	MachineOSBuild *mcfgv1alpha1.MachineOSBuild
	// the cofig the build is based off of
	MachineOSConfig *mcfgv1alpha1.MachineOSConfig
	// the containerfile designated from the MachineOSConfig
	Containerfile string
	// Has /etc/pki/entitlement
	HasEtcPkiEntitlementKeys bool
	// Has /etc/yum.repos.d configs
	HasEtcYumReposDConfigs bool
	// Has /etc/pki/rpm-gpg configs
	HasEtcPkiRpmGpgKeys bool
}

// Constructs a simple ImageBuildRequest.
func newImageBuildRequest(mosc *mcfgv1alpha1.MachineOSConfig, mosb *mcfgv1alpha1.MachineOSBuild) *ImageBuildRequest {
	ibr := &ImageBuildRequest{
		MachineOSConfig: mosc,
		MachineOSBuild:  mosb,
	}

	// only support noArch for now
	for _, file := range mosc.Spec.BuildInputs.Containerfile {
		if file.ContainerfileArch == mcfgv1alpha1.NoArch {
			ibr.Containerfile = file.Content
			break
		}
	}

	return ibr
}

// Populates the base image info from both the on-cluster-build-config and
// machine-config-osimageurl ConfigMaps.
func newBaseImageInfo(osImageURL *corev1.ConfigMap, mosc *mcfgv1alpha1.MachineOSConfig) ImageInfo {
	return ImageInfo{
		Pullspec: osImageURL.Data[baseOSContainerImageConfigKey],
		PullSecret: corev1.LocalObjectReference{
			Name: mosc.Spec.BuildInputs.BaseImagePullSecret.Name,
		},
	}
}

// Populates the extensions image info from both the on-cluster-build-config
// and machine-config-osimageurl ConfigMaps.
func newExtensionsImageInfo(osImageURL *corev1.ConfigMap, mosc *mcfgv1alpha1.MachineOSConfig) ImageInfo {
	return ImageInfo{
		Pullspec: osImageURL.Data[baseOSExtensionsContainerImageConfigKey],
		PullSecret: corev1.LocalObjectReference{
			Name: mosc.Spec.BuildInputs.BaseImagePullSecret.Name,
		},
	}
}

// Constructs an ImageBuildRequest with all of the images populated from ConfigMaps
func newImageBuildRequestFromBuildInputs(mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) ImageBuildRequest {
	ibr := &ImageBuildRequest{
		MachineOSConfig: mosc,
		MachineOSBuild:  mosb,
	}

	// only support noArch for now
	for _, file := range mosc.Spec.BuildInputs.Containerfile {
		if file.ContainerfileArch == mcfgv1alpha1.NoArch {
			ibr.Containerfile = file.Content
			break
		}
	}

	return *ibr
}

// Renders our Dockerfile and injects it into a ConfigMap for consumption by the image builder.
func (i ImageBuildRequest) dockerfileToConfigMap() (*corev1.ConfigMap, error) {
	dockerfile, err := i.renderDockerfile()
	if err != nil {
		return nil, err
	}

	configmap := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: i.getObjectMeta(i.getDockerfileConfigMapName()),
		Data: map[string]string{
			"Dockerfile": dockerfile,
		},
	}

	return configmap, nil
}

// Stuffs a given MachineConfig into a ConfigMap, gzipping and base64-encoding it.
func (i ImageBuildRequest) toConfigMap(mc *mcfgv1.MachineConfig) (*corev1.ConfigMap, error) {
	out, err := json.Marshal(mc)
	if err != nil {
		return nil, fmt.Errorf("could not encode MachineConfig %s: %w", mc.Name, err)
	}

	// TODO: Check for size here and determine if its too big. ConfigMaps and
	// Secrets have a size limit of 1 MB. Compressing and encoding the
	// MachineConfig provides us with additional headroom. However, if the
	// MachineConfig grows large enough, we may need to do something more
	// involved.
	compressed, err := compressAndEncode(out)
	if err != nil {
		return nil, fmt.Errorf("could not compress or encode MachineConfig %s: %w", mc.Name, err)
	}

	configmap := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: i.getObjectMeta(i.getMCConfigMapName()),
		Data: map[string]string{
			machineConfigJSONFilename: compressed.String(),
		},
	}

	return configmap, nil
}

// Renders our Dockerfile template.
//
// TODO: Figure out how to parse the Dockerfile using
// https://github.com/openshift/imagebuilder/tree/master/dockerfile/parser to
// ensure that we've generated a valid Dockerfile.
//
// TODO: Figure out how to programatically generate the Dockerfile using a
// higher-level abstraction than just naïvely rendering a text template and
// hoping for the best.
func (i ImageBuildRequest) renderDockerfile() (string, error) {
	tmpl, err := template.New("dockerfile").Parse(dockerfileTemplate)
	if err != nil {
		return "", err
	}

	out := &strings.Builder{}

	if err := tmpl.Execute(out, i); err != nil {
		return "", err
	}

	return out.String(), nil
}

// Creates a custom image build pod to build the final OS image with all
// ConfigMaps / Secrets / etc. wired into it.
func (i ImageBuildRequest) toBuildPod() *corev1.Pod {
	return i.toBuildahPod()
}

// We're able to run the Buildah image in an unprivileged pod provided that the
// machine-os-builder service account has the anyuid security constraint
// context enabled to allow us to use UID 1000, which maps to the UID within
// the official Buildah image.
// nolint:dupl // I don't want to deduplicate this yet since there are still some unknowns.
func (i ImageBuildRequest) toBuildahPod() *corev1.Pod {
	env := []corev1.EnvVar{
		{
			Name:  "DIGEST_CONFIGMAP_NAME",
			Value: i.getDigestConfigMapName(),
		},
		{
			Name:  "HOME",
			Value: "/home/build",
		},
		{
			Name:  "TAG",
			Value: i.MachineOSConfig.Status.CurrentImagePullspec,
		},
		{
			Name:  "BASE_IMAGE_PULL_CREDS",
			Value: "/tmp/base-image-pull-creds/config.json",
		},
		{
			Name:  "FINAL_IMAGE_PUSH_CREDS",
			Value: "/tmp/final-image-push-creds/config.json",
		},
	}

	var uid int64 = 1000
	var gid int64 = 1000

	securityContext := &corev1.SecurityContext{
		RunAsUser:  &uid,
		RunAsGroup: &gid,
	}

	command := []string{"/bin/bash", "-c"}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "machineconfig",
			MountPath: "/tmp/machineconfig",
		},
		{
			Name:      "dockerfile",
			MountPath: "/tmp/dockerfile",
		},
		{
			Name:      "base-image-pull-creds",
			MountPath: "/tmp/base-image-pull-creds",
		},
		{
			Name:      "final-image-push-creds",
			MountPath: "/tmp/final-image-push-creds",
		},
		{
			Name:      "done",
			MountPath: "/tmp/done",
		},
	}

	volumes := []corev1.Volume{
		{
			// Provides the rendered Dockerfile.
			Name: "dockerfile",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: i.getDockerfileConfigMapName(),
					},
				},
			},
		},
		{
			// Provides the rendered MachineConfig in a gzipped / base64-encoded
			// format.
			Name: "machineconfig",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: i.getMCConfigMapName(),
					},
				},
			},
		},
		{
			// Provides the credentials needed to pull the base OS image.
			Name: "base-image-pull-creds",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: i.MachineOSConfig.Spec.BuildInputs.BaseImagePullSecret.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  corev1.DockerConfigJsonKey,
							Path: "config.json",
						},
					},
				},
			},
		},
		{
			// Provides the credentials needed to push the final OS image.
			Name: "final-image-push-creds",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: i.MachineOSConfig.Spec.BuildInputs.RenderedImagePushSecret.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  corev1.DockerConfigJsonKey,
							Path: "config.json",
						},
					},
				},
			},
		},
		{
			// Provides a way for the "image-build" container to signal that it
			// finished so that the "wait-for-done" container can retrieve the
			// iamge SHA.
			Name: "done",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		},
		{
			// This provides a dedicated place for Buildah to store / cache its
			// images during the build. This seems to be required for the build-time
			// volume mounts to work correctly, most likely due to an issue with
			// SELinux that I have yet to figure out. Despite being called a cache
			// directory, it gets removed whenever the build pod exits
			Name: "buildah-cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Octal: 0755.
	var mountMode int32 = 493

	// If the etc-pki-entitlement secret is found, mount it into the build pod.
	if i.HasEtcPkiEntitlementKeys {
		mountPoint := "/etc/pki/entitlement"

		env = append(env, corev1.EnvVar{
			Name:  "ETC_PKI_ENTITLEMENT_MOUNTPOINT",
			Value: mountPoint,
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      etcPkiEntitlementSecretName,
			MountPath: mountPoint,
		})

		volumes = append(volumes, corev1.Volume{
			Name: etcPkiEntitlementSecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					DefaultMode: &mountMode,
					SecretName:  etcPkiEntitlementSecretName,
				},
			},
		})
	}

	// If the etc-yum-repos-d ConfigMap is found, mount it into the build pod.
	if i.HasEtcYumReposDConfigs {
		mountPoint := "/etc/yum.repos.d"

		env = append(env, corev1.EnvVar{
			Name:  "ETC_YUM_REPOS_D_MOUNTPOINT",
			Value: mountPoint,
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      etcYumReposDConfigMapName,
			MountPath: mountPoint,
		})

		volumes = append(volumes, corev1.Volume{
			Name: etcYumReposDConfigMapName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &mountMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: etcYumReposDConfigMapName,
					},
				},
			},
		})
	}

	// If the etc-pki-rpm-gpg secret is found, mount it into the build pod.
	if i.HasEtcPkiRpmGpgKeys {
		mountPoint := "/etc/pki/rpm-gpg"

		env = append(env, corev1.EnvVar{
			Name:  "ETC_PKI_RPM_GPG_MOUNTPOINT",
			Value: mountPoint,
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      etcPkiRpmGpgSecretName,
			MountPath: mountPoint,
		})

		volumes = append(volumes, corev1.Volume{
			Name: etcPkiRpmGpgSecretName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					DefaultMode: &mountMode,
					SecretName:  etcPkiRpmGpgSecretName,
				},
			},
		})
	}

	// TODO: We need pull creds with permissions to pull the base image. By
	// default, none of the MCO pull secrets can directly pull it. We can use the
	// pull-secret creds from openshift-config to do that, though we'll need to
	// mirror those creds into the MCO namespace. The operator portion of the MCO
	// has some logic to detect whenever that secret changes.
	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: i.getObjectMeta(i.getBuildName()),
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					// This container performs the image build / push process.
					Name: "image-build",
					// TODO: Figure out how to not hard-code this here.
					Image:           buildahImagePullspec,
					Env:             env,
					Command:         append(command, buildahBuildScript),
					ImagePullPolicy: corev1.PullAlways,
					SecurityContext: securityContext,
					// Only attach the buildah-cache volume mount to the buildah container.
					VolumeMounts: append(volumeMounts, corev1.VolumeMount{
						Name:      "buildah-cache",
						MountPath: "/home/build/.local/share/containers",
					}),
				},
				{
					// This container waits for the aforementioned container to finish
					// building so we can get the final image SHA. We do this by using
					// the base OS image (which contains the "oc" binary) to create a
					// ConfigMap from the digestfile that Buildah creates, which allows
					// us to avoid parsing log files.
					Name:            "wait-for-done",
					Env:             env,
					Command:         append(command, waitScript),
					Image:           i.MachineOSConfig.Spec.BuildInputs.BaseOSImagePullspec,
					ImagePullPolicy: corev1.PullAlways,
					SecurityContext: securityContext,
					VolumeMounts:    volumeMounts,
				},
			},
			ServiceAccountName: "machine-os-builder",
			Volumes:            volumes,
		},
	}
}

// Constructs a common metav1.ObjectMeta object with the namespace, labels, and annotations set.
func (i ImageBuildRequest) getObjectMeta(name string) metav1.ObjectMeta {
	objectMeta := metav1.ObjectMeta{
		Name:      name,
		Namespace: ctrlcommon.MCONamespace,
		Labels: map[string]string{
			ctrlcommon.OSImageBuildPodLabel: "",
			targetMachineConfigPoolLabel:    i.MachineOSConfig.Spec.MachineConfigPool.Name,
			desiredConfigLabel:              i.MachineOSBuild.Spec.DesiredConfig.Name,
		},
		Annotations: map[string]string{
			mcPoolAnnotation: "",
		},
	}

	hasOptionalBuildInputTemplate := "machineconfiguration.openshift.io/has-%s"

	if i.HasEtcPkiEntitlementKeys {
		objectMeta.Annotations[fmt.Sprintf(hasOptionalBuildInputTemplate, etcPkiEntitlementSecretName)] = ""
	}

	if i.HasEtcYumReposDConfigs {
		objectMeta.Annotations[fmt.Sprintf(hasOptionalBuildInputTemplate, etcYumReposDConfigMapName)] = ""
	}

	if i.HasEtcPkiRpmGpgKeys {
		objectMeta.Annotations[fmt.Sprintf(hasOptionalBuildInputTemplate, etcPkiRpmGpgSecretName)] = ""
	}

	return objectMeta
}

// Computes the Dockerfile ConfigMap name based upon the MachineConfigPool name.
func (i ImageBuildRequest) getDockerfileConfigMapName() string {
	return fmt.Sprintf("dockerfile-%s", i.MachineOSBuild.Spec.DesiredConfig.Name)
}

// Computes the MachineConfig ConfigMap name based upon the MachineConfigPool name.
func (i ImageBuildRequest) getMCConfigMapName() string {
	return fmt.Sprintf("mc-%s", i.MachineOSBuild.Spec.DesiredConfig.Name)
}

// Computes the build name based upon the MachineConfigPool name.
func (i ImageBuildRequest) getBuildName() string {
	return fmt.Sprintf("build-%s", i.MachineOSBuild.Spec.DesiredConfig.Name)
}

func (i ImageBuildRequest) getDigestConfigMapName() string {
	return fmt.Sprintf("digest-%s", i.MachineOSBuild.Spec.DesiredConfig.Name)
}
