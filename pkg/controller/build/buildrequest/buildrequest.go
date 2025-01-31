package buildrequest

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	tektonv1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
)

//go:embed assets/Containerfile.on-cluster-build-template
var containerfileTemplate string

//go:embed assets/wait.sh
var waitScript string

//go:embed assets/buildah-build.sh
var buildahBuildScript string

//go:embed assets/podman-build.sh
var podmanBuildScript string

const (
	// Filename for the machineconfig JSON tarball expected by the build job
	machineConfigJSONFilename string = "machineconfig.json.gz"
)

// Represents the request to build a layered OS image.
type buildRequestImpl struct {
	opts              BuildRequestOpts
	userContainerfile string
}

// Constructs an imageBuildRequest from the Kube API server.
func NewBuildRequestFromAPI(ctx context.Context, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1alpha1.MachineOSBuild, mosc *mcfgv1alpha1.MachineOSConfig) (BuildRequest, error) {
	opts, err := newBuildRequestOptsFromAPI(ctx, kubeclient, mcfgclient, mosb, mosc)
	if err != nil {
		return nil, err
	}

	return newBuildRequest(*opts), nil
}

// Constructs an imageBuildRequest from the provided options.
func newBuildRequest(opts BuildRequestOpts) BuildRequest {
	br := &buildRequestImpl{
		opts: opts,
	}

	// only support noArch for now
	for _, file := range opts.MachineOSConfig.Spec.BuildInputs.Containerfile {
		if file.ContainerfileArch == mcfgv1alpha1.NoArch {
			br.userContainerfile = file.Content
			break
		}
	}

	return br
}

// Returns the options used within the imageBuildRequest.
func (br buildRequestImpl) Opts() BuildRequestOpts {
	return br.opts
}

// Creates the Build object.
func (br buildRequestImpl) Builder(kubeclient clientset.Interface) (Builder, error) {
	if br.opts.MachineOSConfig.Spec.BuildInputs.ImageBuilder.ImageBuilderType == mcfgv1alpha1.PipelineBuilder {
		return newBuilder(br.createPipelineRun(kubeclient))
	}
	return newBuilder(br.podToJob(br.toBuildahPod()), nil)
}

func (br buildRequestImpl) createPipelineRun(kubeclient clientset.Interface) (*tektonv1beta1.PipelineRun, error) {
	// TODO(rsaini) create pipeline run object and return

	containerfile, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), br.getContainerfileConfigMapName(), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get rendered containerfile: %w", err)
	}

	machineconfig, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), br.getMCConfigMapName(), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get rendered machineconfig: %w", err)
	}

	additionalTrustBundle, err := kubeclient.CoreV1().ConfigMaps(ctrlcommon.MCONamespace).Get(context.TODO(), br.getAdditionalTrustBundleConfigMapName(), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get rendered containerfile: %w", err)
	}


	pipelineRun := &tektonv1beta1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "build-and-push-pipelinerun",
			Namespace: ctrlcommon.MCONamespace,
		},
		Spec: tektonv1beta1.PipelineRunSpec{
			PipelineRef: &tektonv1beta1.PipelineRef{Name: "build-and-push-pipeline"},
			ServiceAccountName: "machine-os-builder",
			Params: []tektonv1beta1.Param{
				{Name: "logLevel", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "DEBUG"}},
				{Name: "storageDriver", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "vfs"}},
				{Name: "authfileBuild", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "/tmp/base-image-pull-creds/config.json"}},
				{Name: "authfilePush", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "/tmp/final-image-push-creds/config.json"}},
				{Name: "tag", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: br.opts.MachineOSBuild.Spec.RenderedImagePushspec}},
				{Name: "containerFile", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: containerfile.Data["Containerfile"]}},
				{Name: "machineConfig", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: machineconfig.Data[machineConfigJSONFilename]}},
				{Name: "additionalTrustBundle", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: string(additionalTrustBundle.BinaryData["openshift-config-user-ca-bundle.crt"])}},
				{Name: "buildContext", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: "/context"}},
				{Name: "image", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: br.opts.getBaseOSImagePullspec()}},
			},
			Workspaces: []tektonv1beta1.WorkspaceBinding{
				{
					Name: "source",
					EmptyDir: &corev1.EmptyDirVolumeSource{},
					/*
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: "source-pvc",
					},
					*/
				},
			},
		},
	}

	if br.opts.Proxy != nil {
		proxyParams := []tektonv1beta1.Param{
			{Name: "httpProxy", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: br.opts.Proxy.HTTPProxy}},
			{Name: "httpsProxy", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: br.opts.Proxy.HTTPSProxy}},
			{Name: "noProxy", Value: tektonv1beta1.ArrayOrString{Type: tektonv1beta1.ParamTypeString, StringVal: br.opts.Proxy.NoProxy}},
		}
		pipelineRun.Spec.Params = append(pipelineRun.Spec.Params, proxyParams...)
	}

	return pipelineRun, nil
}

// Takes the configured secrets and creates an ephemeral clone of them, canonicalizing them, if needed.
func (br buildRequestImpl) Secrets() ([]*corev1.Secret, error) {
	baseImagePullSecret, err := br.canonicalizeSecret(br.getBasePullSecretName(), br.opts.BaseImagePullSecret)
	if err != nil {
		return nil, fmt.Errorf("could not canonicalize secret %s: %w", br.opts.BaseImagePullSecret.Name, err)
	}

	finalImagePushSecret, err := br.canonicalizeSecret(br.getFinalPushSecretName(), br.opts.FinalImagePushSecret)
	if err != nil {
		return nil, fmt.Errorf("could not canonicalize secret %s: %w", br.opts.FinalImagePushSecret.Name, err)
	}

	return []*corev1.Secret{
		baseImagePullSecret,
		finalImagePushSecret,
	}, nil
}

// Creates all of the ConfigMap objects needed for the build such as the
// Containerfile, MachineConfig and AdditionalTrustBundle ConfigMaps.
func (br buildRequestImpl) ConfigMaps() ([]*corev1.ConfigMap, error) {
	containerfile, err := br.containerfileToConfigMap()
	if err != nil {
		return nil, err
	}

	machineconfig, err := br.machineconfigToConfigMap(br.opts.MachineConfig)
	if err != nil {
		if br.opts.MachineConfig != nil {
			return nil, fmt.Errorf("could not convert MachineConfig %q into ConfigMap %q: %w", br.opts.MachineConfig.Name, br.getMCConfigMapName(), err)
		}

		return nil, fmt.Errorf("could not convert MachineConfig into ConfigMap %q: %w", br.getMCConfigMapName(), err)
	}

	additionaltrustbundle := br.additionaltrustbundleToConfigMap()

	return []*corev1.ConfigMap{containerfile, machineconfig, additionaltrustbundle}, nil
}

func (br buildRequestImpl) canonicalizeSecret(name string, secret *corev1.Secret) (*corev1.Secret, error) {
	canonicalized, err := canonicalizePullSecret(secret)
	if err != nil {
		return nil, fmt.Errorf("could not canonicalize pull secret %q: %w", name, err)
	}

	// Overwrite the ObjectMeta so that we get all of the labels and annotations.
	objMeta := br.getObjectMeta(name)
	for k, v := range canonicalized.Labels {
		objMeta.Labels[k] = v
	}

	canonicalized.ObjectMeta = objMeta
	return canonicalized, nil
}

// Renders our Containerfile and injects it into a ConfigMap for consumption by the image builder.
func (br buildRequestImpl) containerfileToConfigMap() (*corev1.ConfigMap, error) {
	containerfile, err := br.renderContainerfile()
	if err != nil {
		return nil, fmt.Errorf("could not get rendered containerfile: %w", err)
	}

	configmap := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: br.getObjectMeta(br.getContainerfileConfigMapName()),
		Data: map[string]string{
			"Containerfile": containerfile,
		},
	}

	return configmap, nil
}

// Stuffs a given MachineConfig into a ConfigMap, gzipping and base64-encoding it.
func (br buildRequestImpl) machineconfigToConfigMap(mc *mcfgv1.MachineConfig) (*corev1.ConfigMap, error) {
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
		ObjectMeta: br.getObjectMeta(br.getMCConfigMapName()),
		// TODO: ConfigMaps also have a BinaryData field which does not require
		// that we Base64 encode things since the API server will do it for us.
		// This could make this code a bit less complicated.
		Data: map[string]string{
			machineConfigJSONFilename: compressed.String(),
		},
	}

	return configmap, nil
}

// Gets the Additional Trust Bundle and injects it into a ConfigMap for consumption by the image builder.
func (br buildRequestImpl) additionaltrustbundleToConfigMap() *corev1.ConfigMap {
	configmap := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: br.getObjectMeta(br.getAdditionalTrustBundleConfigMapName()),
		BinaryData: map[string][]byte{
			"openshift-config-user-ca-bundle.crt": br.opts.AdditionalTrustBundle,
		},
	}

	return configmap
}

// Renders our Containerfile template.
//
// TODO: Figure out how to parse the Containerfile using
// https://github.com/openshift/imagebuilder/tree/master/containerfile/parser to
// ensure that we've generated a valid Containerfile.
//
// TODO: Figure out how to programatically generate the Containerfile using a
// higher-level abstraction than just naïvely rendering a text template and
// hoping for the best.
func (br buildRequestImpl) renderContainerfile() (string, error) {
	tmpl, err := template.New("containerfile").Parse(containerfileTemplate)
	if err != nil {
		return "", fmt.Errorf("could not parse containerfile template: %w", err)
	}

	extPkgs, err := br.opts.getExtensionsPackages()
	if err != nil {
		return "", err
	}

	out := &strings.Builder{}

	// This anonymous struct is necessary because templates cannot access
	// lowercase fields. Additionally, since there are a few fields where we
	// default to a value from a different location, it makes more sense for us
	// to implement that logic in Go as opposed to the Go template language.
	items := struct {
		MachineOSBuild     *mcfgv1alpha1.MachineOSBuild
		MachineOSConfig    *mcfgv1alpha1.MachineOSConfig
		UserContainerfile  string
		ReleaseVersion     string
		BaseOSImage        string
		ExtensionsImage    string
		ExtensionsPackages []string
	}{
		MachineOSBuild:     br.opts.MachineOSBuild,
		MachineOSConfig:    br.opts.MachineOSConfig,
		UserContainerfile:  br.userContainerfile,
		ReleaseVersion:     br.opts.getReleaseVersion(),
		BaseOSImage:        br.opts.getBaseOSImagePullspec(),
		ExtensionsImage:    br.opts.getExtensionsImagePullspec(),
		ExtensionsPackages: extPkgs,
	}

	if err := tmpl.Execute(out, items); err != nil {
		return "", fmt.Errorf("could not execute containerfile template: %w", err)
	}

	return out.String(), nil
}

// podToJob creates a Job with the spec of the given Pod
func (br buildRequestImpl) podToJob(pod *corev1.Pod) *batchv1.Job {
	// Set the backoffLimit to 3 so the job will retry 4 times before reporting a failure
	var backoffLimit int32 = 3
	// Set completion to 1 so that as soon as the pod has completed successfully the job is
	// considered a success
	var completions int32 = 1
	return &batchv1.Job{
		ObjectMeta: pod.ObjectMeta,
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Completions:  &completions,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{},
				Spec:       pod.Spec,
			},
		},
	}
}

// We're able to run the Buildah image in an unprivileged pod provided that the
// machine-os-builder service account has the anyuid security constraint
// context enabled to allow us to use UID 1000, which maps to the UID within
// the official Buildah image.
// nolint:dupl // I don't want to deduplicate this yet since there are still some unknowns.
func (br buildRequestImpl) toBuildahPod() *corev1.Pod {
	var httpProxy, httpsProxy, noProxy string
	if br.opts.Proxy != nil {
		httpProxy = br.opts.Proxy.HTTPProxy
		httpsProxy = br.opts.Proxy.HTTPSProxy
		noProxy = br.opts.Proxy.NoProxy
	}
	env := []corev1.EnvVar{
		// How many times the build / push steps should be retried. In the future,
		// this should be wired up to the MachineOSConfig or other higher-level
		// APbr. This is useful for retrying builds / pushes when they fail due to a
		// transient condition such as a temporary network issue. It does *NOT*
		// handle situations where the build pod is evicted or rescheduled. A
		// higher-level abstraction will be needed such as a Kubernetes Job
		// (https://kubernetes.io/docs/concepts/workloads/controllers/job/).
		{
			Name:  "MAX_RETRIES",
			Value: "3",
		},
		{
			Name:  "DIGEST_CONFIGMAP_NAME",
			Value: br.getDigestConfigMapName(),
		},
		{
			Name: "DIGEST_CONFIGMAP_LABELS",
			// Gets the labels for all objects created by imageBuildRequest, converts
			// them into a string representation, and replaces the separating commas
			// with spaces.
			Value: strings.ReplaceAll(labels.Set(br.getLabelsForObjectMeta()).String(), ",", " "),
		},
		{
			Name:  "HOME",
			Value: "/home/build",
		},
		{
			Name:  "TAG",
			Value: br.opts.MachineOSBuild.Spec.RenderedImagePushspec,
		},
		{
			Name:  "BASE_IMAGE_PULL_CREDS",
			Value: "/tmp/base-image-pull-creds/config.json",
		},
		{
			Name:  "FINAL_IMAGE_PUSH_CREDS",
			Value: "/tmp/final-image-push-creds/config.json",
		},
		{
			Name:  "BUILDAH_ISOLATION",
			Value: "chroot",
		},
		{
			Name:  "HTTP_PROXY",
			Value: httpProxy,
		},
		{
			Name:  "HTTPS_PROXY",
			Value: httpsProxy,
		},
		{
			Name:  "NO_PROXY",
			Value: noProxy,
		},
	}

	securityContext := &corev1.SecurityContext{}

	command := []string{"/bin/bash", "-c"}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "machineconfig",
			MountPath: "/tmp/machineconfig",
		},
		{
			Name:      "containerfile",
			MountPath: "/tmp/containerfile",
		},
		{
			Name:      "additional-trust-bundle",
			MountPath: "/etc/pki/ca-trust/source/anchors",
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
			// Provides the rendered Containerfile.
			Name: "containerfile",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: br.getContainerfileConfigMapName(),
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
						Name: br.getMCConfigMapName(),
					},
				},
			},
		},
		{
			// Provides the user defined Additional Trust Bundle
			Name: "additional-trust-bundle",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: br.getAdditionalTrustBundleConfigMapName(),
					},
				},
			},
		},
		{
			// Provides the credentials needed to pull the base OS image.
			Name: "base-image-pull-creds",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: br.getBasePullSecretName(),
					// SecretName: br.opts.MachineOSConfig.Spec.BuildInputs.BaseImagePullSecret.Name,
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
					// SecretName: br.opts.MachineOSConfig.Spec.BuildInputs.RenderedImagePushSecret.Name,
					SecretName: br.getFinalPushSecretName(),
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

	// If the etc-pki-entitlement secret is found, mount it into the build pod.
	if br.opts.HasEtcPkiEntitlementKeys {
		opts := optsForEtcPkiEntitlements()
		env = append(env, opts.envVar())
		volumeMounts = append(volumeMounts, opts.volumeMount())
		volumes = append(volumes, opts.volumeForSecret(constants.EtcPkiEntitlementSecretName+"-"+br.opts.MachineOSConfig.Spec.MachineConfigPool.Name))
	}

	// If the etc-yum-repos-d ConfigMap is found, mount it into the build pod.
	if br.opts.HasEtcYumReposDConfigs {
		opts := optsForEtcYumReposD()
		env = append(env, opts.envVar())
		volumeMounts = append(volumeMounts, opts.volumeMount())
		volumes = append(volumes, opts.volumeForConfigMap())
	}

	// If the etc-pki-rpm-gpg secret is found, mount it into the build pod.
	if br.opts.HasEtcPkiRpmGpgKeys {
		opts := optsForEtcRpmGpgKeys()
		env = append(env, opts.envVar())
		volumeMounts = append(volumeMounts, opts.volumeMount())
		volumes = append(volumes, opts.volumeForSecret(constants.EtcPkiRpmGpgSecretName))
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
		ObjectMeta: br.getObjectMeta(br.getBuildName()),
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					// This container performs the image build / push process.
					Name:            "image-build",
					Image:           br.opts.Images.MachineConfigOperator,
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
					Command:         append(command, waitScript),
					Image:           br.opts.getBaseOSImagePullspec(),
					Env:             env,
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

// Populates the labels map for all objects created by imageBuildRequest
func (br buildRequestImpl) getLabelsForObjectMeta() map[string]string {
	return map[string]string{
		constants.EphemeralBuildObjectLabelKey:    "",
		constants.OnClusterLayeringLabelKey:       "",
		constants.RenderedMachineConfigLabelKey:   br.opts.MachineOSBuild.Spec.DesiredConfig.Name,
		constants.TargetMachineConfigPoolLabelKey: br.opts.MachineOSConfig.Spec.MachineConfigPool.Name,
		constants.MachineOSConfigNameLabelKey:     br.opts.MachineOSConfig.Name,
		constants.MachineOSBuildNameLabelKey:      br.opts.MachineOSBuild.Name,
	}
}

// Populates the annotations map for all objects created by imageBuildRequest.
// Conditionally sets annotations for entitled builds if the appropriate
// secrets / ConfigMaps are present.
func (br buildRequestImpl) getAnnotationsForObjectMeta() map[string]string {
	annos := map[string]string{
		constants.MachineOSConfigNameAnnotationKey: br.opts.MachineOSConfig.Name,
		constants.MachineOSBuildNameAnnotationKey:  br.opts.MachineOSBuild.Name,
	}

	if br.opts.HasEtcPkiEntitlementKeys {
		annos[constants.EtcPkiEntitlementAnnotationKey] = ""
	}

	if br.opts.HasEtcYumReposDConfigs {
		annos[constants.EtcYumReposDAnnotationKey] = ""
	}

	if br.opts.HasEtcPkiRpmGpgKeys {
		annos[constants.EtcPkiRpmGpgAnnotationKey] = ""
	}

	return annos
}

// Constructs a common metav1.ObjectMeta object with the namespace, labels, and annotations set.
func (br buildRequestImpl) getObjectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        name,
		Namespace:   ctrlcommon.MCONamespace,
		Labels:      br.getLabelsForObjectMeta(),
		Annotations: br.getAnnotationsForObjectMeta(),
	}
}

// Computes the AdditionalTrustBundle ConfigMap name based upon the MachineConfigPool name.
func (br buildRequestImpl) getAdditionalTrustBundleConfigMapName() string {
	return utils.GetAdditionalTrustBundleConfigMapName(br.opts.MachineOSBuild)
}

// Computes the Containerfile ConfigMap name based upon the MachineConfigPool name.
func (br buildRequestImpl) getContainerfileConfigMapName() string {
	return utils.GetContainerfileConfigMapName(br.opts.MachineOSBuild)
}

// Computes the MachineConfig ConfigMap name based upon the MachineConfigPool name.
func (br buildRequestImpl) getMCConfigMapName() string {
	return utils.GetMCConfigMapName(br.opts.MachineOSBuild)
}

// Computes the build name based upon the MachineConfigPool name.
func (br buildRequestImpl) getBuildName() string {
	return utils.GetBuildName(br.opts.MachineOSBuild)
}

func (br buildRequestImpl) getDigestConfigMapName() string {
	return utils.GetDigestConfigMapName(br.opts.MachineOSBuild)
}

func (br buildRequestImpl) getBasePullSecretName() string {
	return utils.GetBasePullSecretName(br.opts.MachineOSBuild)
}

func (br buildRequestImpl) getFinalPushSecretName() string {
	return utils.GetFinalPushSecretName(br.opts.MachineOSBuild)
}
