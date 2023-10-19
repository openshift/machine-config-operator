package build

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	buildv1 "github.com/openshift/api/build/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	mcPoolAnnotation          string = "machineconfiguration.openshift.io/pool"
	machineConfigJSONFilename string = "machineconfig.json.gz"
	buildahImagePullspec      string = "quay.io/buildah/stable:latest"
)

//go:embed assets/Dockerfile.on-cluster-build-template
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
	// The target MachineConfigPool
	Pool *mcfgv1.MachineConfigPool
	// The base OS image (derived from the machine-config-osimageurl ConfigMap)
	BaseImage ImageInfo
	// The extensions image (derived from the machine-config-osimageurl ConfigMap)
	ExtensionsImage ImageInfo
	// The final OS image (desired from the on-cluster-build-config ConfigMap)
	FinalImage ImageInfo
	// The OpenShift release version (derived from the machine-config-osimageurl ConfigMap)
	ReleaseVersion string
	// An optional user-supplied Dockerfile that gets injected into the build.
	CustomDockerfile string
}

type buildInputs struct {
	onClusterBuildConfig *corev1.ConfigMap
	osImageURL           *corev1.ConfigMap
	customDockerfiles    *corev1.ConfigMap
	pool                 *mcfgv1.MachineConfigPool
	machineConfig        *mcfgv1.MachineConfig
}

// Constructs a simple ImageBuildRequest.
func newImageBuildRequest(pool *mcfgv1.MachineConfigPool) ImageBuildRequest {
	return ImageBuildRequest{
		Pool: pool.DeepCopy(),
	}
}

// Populates the final image info from the on-cluster-build-config ConfigMap.
func newFinalImageInfo(inputs *buildInputs) ImageInfo {
	return ImageInfo{
		Pullspec: inputs.onClusterBuildConfig.Data[FinalImagePullspecConfigKey],
		PullSecret: corev1.LocalObjectReference{
			Name: inputs.onClusterBuildConfig.Data[FinalImagePushSecretNameConfigKey],
		},
	}
}

// Populates the base image info from both the on-cluster-build-config and
// machine-config-osimageurl ConfigMaps.
func newBaseImageInfo(inputs *buildInputs) ImageInfo {
	return ImageInfo{
		Pullspec: inputs.osImageURL.Data[baseOSContainerImageConfigKey],
		PullSecret: corev1.LocalObjectReference{
			Name: inputs.onClusterBuildConfig.Data[BaseImagePullSecretNameConfigKey],
		},
	}
}

// Populates the extensions image info from both the on-cluster-build-config
// and machine-config-osimageurl ConfigMaps.
func newExtensionsImageInfo(inputs *buildInputs) ImageInfo {
	return ImageInfo{
		Pullspec: inputs.osImageURL.Data[baseOSExtensionsContainerImageConfigKey],
		PullSecret: corev1.LocalObjectReference{
			Name: inputs.onClusterBuildConfig.Data[BaseImagePullSecretNameConfigKey],
		},
	}
}

// Constructs an ImageBuildRequest with all of the images populated from ConfigMaps
func newImageBuildRequestFromBuildInputs(inputs *buildInputs) ImageBuildRequest {
	customDockerfile := ""
	if inputs.customDockerfiles != nil {
		customDockerfile = inputs.customDockerfiles.Data[inputs.pool.Name]
	}

	return ImageBuildRequest{
		Pool:             inputs.pool.DeepCopy(),
		BaseImage:        newBaseImageInfo(inputs),
		FinalImage:       newFinalImageInfo(inputs),
		ExtensionsImage:  newExtensionsImageInfo(inputs),
		ReleaseVersion:   inputs.osImageURL.Data[releaseVersionConfigKey],
		CustomDockerfile: customDockerfile,
	}
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

// Creates an OpenShift Image Builder build object prewired with all ConfigMaps
// / Secrets / etc.
func (i ImageBuildRequest) toBuild() *buildv1.Build {
	skipLayers := buildv1.ImageOptimizationSkipLayers

	// The Build API requires the Dockerfile field to be set, even if you
	// override it via a ConfigMap.
	dockerfile := "FROM scratch"

	return &buildv1.Build{
		TypeMeta: metav1.TypeMeta{
			Kind: "Build",
		},
		ObjectMeta: i.getObjectMeta(i.getBuildName()),
		Spec: buildv1.BuildSpec{
			CommonSpec: buildv1.CommonSpec{
				// TODO: We may need to configure a Build Input here so we can wire up
				// the pull secrets for the base OS image and the extensions image.
				Source: buildv1.BuildSource{
					Type:       buildv1.BuildSourceDockerfile,
					Dockerfile: &dockerfile,
					ConfigMaps: []buildv1.ConfigMapBuildSource{
						{
							// Provides the rendered MachineConfig in a gzipped /
							// base64-encoded format.
							ConfigMap: corev1.LocalObjectReference{
								Name: i.getMCConfigMapName(),
							},
							DestinationDir: "machineconfig",
						},
						{
							// Provides the rendered Dockerfile.
							ConfigMap: corev1.LocalObjectReference{
								Name: i.getDockerfileConfigMapName(),
							},
						},
					},
				},
				Strategy: buildv1.BuildStrategy{
					DockerStrategy: &buildv1.DockerBuildStrategy{
						// Squashing layers is good as long as it doesn't cause problems with what
						// the users want to do. It says "some syntax is not supported"
						ImageOptimizationPolicy: &skipLayers,
					},
					Type: buildv1.DockerBuildStrategyType,
				},
				Output: buildv1.BuildOutput{
					To: &corev1.ObjectReference{
						Name: i.FinalImage.Pullspec,
						Kind: "DockerImage",
					},
					PushSecret: &i.FinalImage.PullSecret,
					ImageLabels: []buildv1.ImageLabel{
						{Name: "io.openshift.machineconfig.pool", Value: i.Pool.Name},
					},
				},
			},
		},
	}
}

// Creates a custom image build pod to build the final OS image with all
// ConfigMaps / Secrets / etc. wired into it.
func (i ImageBuildRequest) toBuildPod() *corev1.Pod {
	return i.toBuildahPod()
}

// This reflects an attempt to use Podman to perform the OS build.
// Unfortunately, it was difficult to get this to run unprivileged and I was
// not able to figure out a solution. Nevertheless, I will leave it here for
// posterity.
func (i ImageBuildRequest) toPodmanPod() *corev1.Pod {
	env := []corev1.EnvVar{
		{
			Name:  "DIGEST_CONFIGMAP_NAME",
			Value: i.getDigestConfigMapName(),
		},
		{
			Name:  "HOME",
			Value: "/tmp",
		},
		{
			Name:  "TAG",
			Value: i.FinalImage.Pullspec,
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

	// See: https://access.redhat.com/solutions/6964609
	// TL;DR: Trying to get podman / buildah to run in an unprivileged container
	// is quite involved. However, OpenShift Builder runs in privileged
	// containers, which sets a precedent.
	// This requires that one run: $ oc adm policy add-scc-to-user -z machine-os-builder privileged
	securityContext := &corev1.SecurityContext{
		Privileged: helpers.BoolToPtr(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type:             "Localhost",
			LocalhostProfile: helpers.StrToPtr("profiles/unshare.json"),
		},
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
					// Additionally, it takes the digestfile which podman creates, which
					// contains the SHA256 from the container registry, and stores this
					// in a ConfigMap which is consumed after the pod stops.
					Name:            "image-build",
					Image:           i.BaseImage.Pullspec,
					Env:             env,
					Command:         append(command, podmanBuildScript),
					ImagePullPolicy: corev1.PullIfNotPresent,
					SecurityContext: securityContext,
					VolumeMounts:    volumeMounts,
				},
			},
			// We probably cannot count on the 'builder' service account being
			// present in the future. If we cannot use the builder service account
			// means that we'll need to:
			// 1. Create a SecurityContextConstraint.
			// 2. Additional RBAC / ClusterRole / etc. work to suss this out.
			ServiceAccountName: "machine-os-builder",
			Volumes: []corev1.Volume{ // nolint:dupl // I don't want to deduplicate this yet since there are still some unknowns.
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
							SecretName: i.BaseImage.PullSecret.Name,
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
							SecretName: i.FinalImage.PullSecret.Name,
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
			},
		},
	}
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
			Value: i.FinalImage.Pullspec,
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
					VolumeMounts:    volumeMounts,
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
					Image:           i.BaseImage.Pullspec,
					ImagePullPolicy: corev1.PullAlways,
					SecurityContext: securityContext,
					VolumeMounts:    volumeMounts,
				},
			},
			ServiceAccountName: "machine-os-builder",
			Volumes: []corev1.Volume{
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
							SecretName: i.BaseImage.PullSecret.Name,
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
							SecretName: i.FinalImage.PullSecret.Name,
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
			},
		},
	}
}

// Constructs a common metav1.ObjectMeta object with the namespace, labels, and annotations set.
func (i ImageBuildRequest) getObjectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: ctrlcommon.MCONamespace,
		Labels: map[string]string{
			ctrlcommon.OSImageBuildPodLabel: "",
			targetMachineConfigPoolLabel:    i.Pool.Name,
			desiredConfigLabel:              i.Pool.Spec.Configuration.Name,
		},
		Annotations: map[string]string{
			mcPoolAnnotation: "",
		},
	}
}

// Computes the Dockerfile ConfigMap name based upon the MachineConfigPool name.
func (i ImageBuildRequest) getDockerfileConfigMapName() string {
	return fmt.Sprintf("dockerfile-%s", i.Pool.Spec.Configuration.Name)
}

// Computes the MachineConfig ConfigMap name based upon the MachineConfigPool name.
func (i ImageBuildRequest) getMCConfigMapName() string {
	return fmt.Sprintf("mc-%s", i.Pool.Spec.Configuration.Name)
}

// Computes the build name based upon the MachineConfigPool name.
func (i ImageBuildRequest) getBuildName() string {
	return fmt.Sprintf("build-%s", i.Pool.Spec.Configuration.Name)
}

func (i ImageBuildRequest) getDigestConfigMapName() string {
	return fmt.Sprintf("digest-%s", i.Pool.Spec.Configuration.Name)
}
