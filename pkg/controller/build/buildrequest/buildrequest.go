package buildrequest

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgclientset "github.com/openshift/client-go/machineconfiguration/clientset/versioned"
	command "github.com/openshift/imagebuilder/dockerfile/command"
	parser "github.com/openshift/imagebuilder/dockerfile/parser"
	"github.com/openshift/machine-config-operator/pkg/controller/build/constants"
	"github.com/openshift/machine-config-operator/pkg/controller/build/utils"
	chelpers "github.com/openshift/machine-config-operator/pkg/controller/common"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

//go:embed assets/Containerfile.on-cluster-build-template
var containerfileTemplate string

//go:embed assets/create-digest-cm.sh
var digestCMScript string

//go:embed assets/buildah-build.sh
var buildahBuildScript string

//go:embed assets/podman-build.sh
var podmanBuildScript string

const (
	// Filename for the machineconfig JSON tarball expected by the build job
	machineConfigJSONFilename string = "machineconfig.json.gz"
)

var basicSyntaxRegex = regexp.MustCompile(`(?m)(?i)^\s*FROM`)

// instructionRequirements maps Containerfile instructions that MUST have arguments to their requirement descriptions
// Instructions not in this map either don't require arguments or have optional arguments
var instructionRequirements = map[string]string{
	command.Add:        "source and destination paths",
	command.Arg:        "a variable name",
	command.Cmd:        "a command or arguments",
	command.Copy:       "source and destination paths",
	command.Entrypoint: "a command or arguments",
	command.Env:        "key=value pairs",
	command.Expose:     "a port",
	command.From:       "a base image",
	command.Label:      "key=value pairs",
	command.Onbuild:    "an instruction to execute",
	command.Run:        "a command",
	command.Shell:      "a shell command array",
	command.StopSignal: "a signal",
	command.User:       "a username or UID",
	command.Volume:     "a mount point",
	command.Workdir:    "a directory path",
}

// Represents the request to build a layered OS image.
type buildRequestImpl struct {
	opts              BuildRequestOpts
	userContainerfile string
}

// Constructs an imageBuildRequest from the Kube API server.
func NewBuildRequestFromAPI(ctx context.Context, kubeclient clientset.Interface, mcfgclient mcfgclientset.Interface, mosb *mcfgv1.MachineOSBuild, mosc *mcfgv1.MachineOSConfig) (BuildRequest, error) {
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
	for _, file := range opts.MachineOSConfig.Spec.Containerfile {
		if file.ContainerfileArch == mcfgv1.NoArch {
			br.userContainerfile = file.Content
			break
		}
	}

	// Validate user's Containerfile if provided
	if br.userContainerfile != "" {
		if err := br.validateContainerfileSyntax(br.userContainerfile); err != nil {
			klog.Warningf("User Containerfile validation failed")
		}
	}

	return br
}

// Returns the options used within the imageBuildRequest.
func (br buildRequestImpl) Opts() BuildRequestOpts {
	return br.opts
}

// Creates the Build Job object.
func (br buildRequestImpl) Builder() Builder {
	return newBuilder(br.podToJob(br.toBuildahPod()))
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

	etcPolicy, err := br.etcPolicyToConfigMap(br.opts.MachineConfig)
	if err != nil {
		return nil, fmt.Errorf("could not convert etc/containers registries files into ConfigMap %q: %w", br.getEtcPolicyConfigMapName(), err)
	}
	etcRegistries, err := br.etcRegistriesToConfigMap(br.opts.MachineConfig)
	if err != nil {
		return nil, fmt.Errorf("could not convert registries.conf files into ConfigMap %q: %w", br.getEtcRegistriesConfigMapName(), err)
	}

	configMaps := []*corev1.ConfigMap{containerfile, machineconfig, additionaltrustbundle}
	if etcPolicy != nil {
		configMaps = append(configMaps, etcPolicy)
	} else {
		klog.Warningf("/etc/containers/policy.json file not found in MachineConfig %q, could not create ConfigMap %q", br.opts.MachineConfig.Name, br.getEtcPolicyConfigMapName())
	}
	if etcRegistries != nil {
		configMaps = append(configMaps, etcRegistries)
	} else {
		klog.Warningf("/etc/containers/registries.conf file not found in MachineConfig %q, could not create ConfigMap %q", br.opts.MachineConfig.Name, br.getEtcRegistriesConfigMapName())
	}

	return configMaps, nil
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

func (br buildRequestImpl) etcPolicyToConfigMap(mc *mcfgv1.MachineConfig) (*corev1.ConfigMap, error) {
	// Build the ConfigMap data
	configMapData, err := br.ignitionFileToConfigMapData(mc, "/etc/containers/policy.json", "/etc/containers/")
	if err != nil {
		return nil, err
	}
	if len(configMapData) == 0 {
		return nil, nil
	}

	// Create the ConfigMap
	configmap := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: br.getObjectMeta(br.getEtcPolicyConfigMapName()),
		Data:       configMapData,
	}
	return configmap, nil
}

func (br buildRequestImpl) etcRegistriesToConfigMap(mc *mcfgv1.MachineConfig) (*corev1.ConfigMap, error) {
	// Build the ConfigMap data
	configMapData, err := br.ignitionFileToConfigMapData(mc, "/etc/containers/registries.conf", "/etc/containers/")
	if err != nil {
		return nil, err
	}
	if len(configMapData) == 0 {
		return nil, nil
	}

	// Create the ConfigMap
	configmap := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: br.getObjectMeta(br.getEtcRegistriesConfigMapName()),
		Data:       configMapData,
	}
	return configmap, nil
}

func (br buildRequestImpl) ignitionFileToConfigMapData(mc *mcfgv1.MachineConfig, filePath, prefixToTrim string) (map[string]string, error) {
	if len(mc.Spec.Config.Raw) == 0 {
		return nil, nil
	}
	// Build the ConfigMap data
	ignCfg, err := ctrlcommon.ParseAndConvertConfig(mc.Spec.Config.Raw)
	if err != nil {
		return nil, fmt.Errorf("parsing rendered MC Ignition config failed with error: %w", err)
	}

	for _, file := range ignCfg.Storage.Files {
		if file.Path != filePath {
			continue
		}
		if file.Contents.Source == nil {
			return nil, fmt.Errorf("nil source for %s", file.Path)
		}

		// Extract and decode the encoded data
		decodedData, err := chelpers.DecodeIgnitionFileContents(file.Contents.Source, file.Contents.Compression)
		if err != nil {
			return nil, fmt.Errorf("error decoding %s: %w", file.Path, err)
		}

		// Key in the configmap is the path without the prefix
		fileKey := strings.TrimPrefix(file.Path, prefixToTrim)
		return map[string]string{fileKey: string(decodedData)}, nil
	}
	klog.Infof("Could not find %s in MachineConfig %s, skipping configmap creation....", filePath, mc.Name)
	return nil, nil
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

	kernelType, kernelPackages, err := br.opts.getKernelPackages()
	if err != nil {
		return "", err
	}

	out := &strings.Builder{}

	// This anonymous struct is necessary because templates cannot access
	// lowercase fields. Additionally, since there are a few fields where we
	// default to a value from a different location, it makes more sense for us
	// to implement that logic in Go as opposed to the Go template language.
	items := struct {
		MachineOSBuild     *mcfgv1.MachineOSBuild
		MachineOSConfig    *mcfgv1.MachineOSConfig
		UserContainerfile  string
		BaseOSImage        string
		ExtensionsImage    string
		ExtensionsPackages []string
		KernelType         string
		KernelPackages     map[string][]string
	}{
		MachineOSBuild:     br.opts.MachineOSBuild,
		MachineOSConfig:    br.opts.MachineOSConfig,
		UserContainerfile:  br.userContainerfile,
		BaseOSImage:        br.opts.MachineConfig.Spec.OSImageURL,
		ExtensionsImage:    br.opts.MachineConfig.Spec.BaseOSExtensionsContainerImage,
		ExtensionsPackages: extPkgs,
		KernelType:         kernelType,
		KernelPackages:     kernelPackages,
	}

	if err := tmpl.Execute(out, items); err != nil {
		return "", fmt.Errorf("could not execute containerfile template: %w", err)
	}

	renderedContainerfile := out.String()

	if renderedContainerfile != "" {
		if err := br.validateContainerfileSyntax(renderedContainerfile); err != nil {
			return "", fmt.Errorf("rendered containerfile validation failed: %w", err)
		}
	}

	return renderedContainerfile, nil
}

// validateContainerfileSyntax validates the syntax of a Containerfile using basic checks and the imagebuilder parser.
func (br buildRequestImpl) validateContainerfileSyntax(containerfile string) error {
	if containerfile == "" || hasOnlyCommentsOrWhitespace(containerfile) {
		klog.V(4).Infof("Containerfile is empty or contains only comments, nothing to validate")
		return nil
	}

	if err := br.validateBasicSyntax(containerfile); err != nil {
		return err
	}

	result, err := parser.Parse(strings.NewReader(containerfile))
	if err != nil {
		return handleParserError(err, containerfile)
	}

	if result.AST == nil || len(result.AST.Children) == 0 {
		klog.V(4).Infof("Containerfile parsed with no instructions (may be all comments), nothing to validate")
		return nil
	}

	for _, child := range result.AST.Children {
		if err := br.validateInstruction(child); err != nil {
			return err
		}
	}

	return nil
}

// handleParserError translates a parser.Parse error into a validation outcome.
// Known parser limitations (heredoc, unquoted LABEL/ENV values) are allowed through;
// genuinely malformed instructions and all other errors fail.
func handleParserError(err error, containerfile string) error {
	switch {
	case isKnownParserLimitation(err):
		klog.V(4).Infof("Containerfile uses advanced syntax the parser doesn't support, skipping detailed parser validation: %v", err)
		return nil
	case isParseNameValError(err):
		// imagebuilders parseNameVal rejects LABEL/ENV values that contain unquoted spaces
		if malformed := findMalformedEnvLabel(containerfile); malformed != nil {
			return malformed
		}
		klog.V(4).Infof("Containerfile LABEL/ENV values contain unquoted spaces; imagebuilder cannot parse them but buildah/podman will: %v", err)
		return nil
	default:
		return fmt.Errorf("failed to parse Containerfile: %w", err)
	}
}

// validateBasicSyntax performs basic sanity checks that don't require parsing.
func (br buildRequestImpl) validateBasicSyntax(containerfile string) error {
	if !basicSyntaxRegex.MatchString(containerfile) {
		return fmt.Errorf("Containerfile must contain at least one FROM instruction")
	}

	return nil
}

// hasOnlyCommentsOrWhitespace reports whether every non-empty line in the
// containerfile is a comment line (starts with '#' after trimming whitespace).
func hasOnlyCommentsOrWhitespace(containerfile string) bool {
	for _, line := range strings.Split(containerfile, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return false
		}
	}
	return true
}

// isKnownParserLimitation checks if a parser error is from known limitations of the
// imagebuilder parser library that buildah/podman handle correctly.
func isKnownParserLimitation(err error) bool {
	if err == nil {
		return false
	}
	// Heredoc syntax (e.g. RUN bash <<'EOF') is valid in Podman/Buildah but not
	// supported by the imagebuilder parser.
	errMsg := err.Error()
	for _, token := range []string{"<<", "heredoc"} {
		if strings.Contains(errMsg, token) {
			return true
		}
	}
	return false
}

// isParseNameValError reports whether the error came from imagebuilder's parseNameVal,
// which rejects LABEL/ENV values containing unquoted spaces.
func isParseNameValError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "can't find = in") || strings.Contains(msg, "must be of the form: name=value")
}

// findMalformedEnvLabel scans containerfile lines for ENV or LABEL instructions that
// lack any '=' sign in their arguments, which means the instruction is genuinely malformed.
func findMalformedEnvLabel(containerfile string) error {
	for i, line := range strings.Split(containerfile, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		instruction, rest, ok := cutEnvLabelPrefix(trimmed)
		if !ok {
			continue
		}
		if !strings.Contains(rest, "=") {
			return fmt.Errorf("line %d: %s instruction must use key=value form", i+1, instruction)
		}
	}
	return nil
}

// cutEnvLabelPrefix returns the instruction name and the remainder of the line if the
// line begins with ENV or LABEL (case-insensitive). ok is false for all other instructions.
func cutEnvLabelPrefix(line string) (instruction, rest string, ok bool) {
	for _, inst := range []string{"ENV", "LABEL"} {
		prefix := inst + " "
		if len(line) > len(prefix) && strings.EqualFold(line[:len(prefix)], prefix) {
			return inst, strings.TrimSpace(line[len(prefix):]), true
		}
	}
	return "", "", false
}

// validateInstruction validates that an instruction is valid and has required arguments.
func (br buildRequestImpl) validateInstruction(node *parser.Node) error {
	if node == nil || node.Value == "" {
		return nil
	}

	instruction := node.Value

	// First check if this is a valid Dockerfile instruction
	if _, exists := command.Commands[instruction]; !exists {
		return fmt.Errorf("unknown Dockerfile instruction: %s", node.Value)
	}

	// NONE is valid without additional arguments
	if instruction == command.Healthcheck && node.Next != nil && strings.EqualFold(node.Next.Value, "NONE") {
		return nil
	}

	// Then check if it has required arguments
	requirement, requiresArgs := instructionRequirements[instruction]
	if requiresArgs {
		// Check if the node has arguments
		hasArgs := false
		if node.Next != nil && node.Next.Value != "" {
			hasArgs = true
		}
		// Also check if there's a raw attribute that contains args
		if !hasArgs && node.Attributes != nil && len(node.Attributes) > 0 {
			hasArgs = true
		}

		if !hasArgs {
			return fmt.Errorf("%s instruction requires %s", strings.ToUpper(instruction), requirement)
		}
	}

	return nil
}

// podToJob creates a Job with the spec of the given Pod
func (br buildRequestImpl) podToJob(pod *corev1.Pod) *batchv1.Job {
	// Set the backoffLimit to 3 so the job will retry 4 times before reporting a failure
	var backoffLimit int32 = constants.JobMaxRetries

	// Set completion to 1 so that as soon as the pod has completed successfully the job is
	// considered a success
	var completions int32 = constants.JobCompletions

	// Set the owner ref of the job to the MOSB
	oref := metav1.NewControllerRef(br.opts.MachineOSBuild, mcfgv1.SchemeGroupVersion.WithKind("MachineOSBuild"))

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pod.ObjectMeta.Name,
			Namespace:       pod.ObjectMeta.Namespace,
			Labels:          pod.ObjectMeta.Labels,
			Annotations:     pod.ObjectMeta.Annotations,
			OwnerReferences: []metav1.OwnerReference{*oref},
		},
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
			Value: string(br.opts.MachineOSBuild.Spec.RenderedImagePushSpec),
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
		{
			Name:  "BASE_OS_IMAGE_PULLSPEC",
			Value: br.opts.MachineConfig.Spec.OSImageURL,
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
			Name:      "etc-policy",
			MountPath: "/etc/containers/policy.json",
			SubPath:   "policy.json",
		},
		{
			Name:      "etc-registries",
			MountPath: "/etc/containers/registries.conf",
			SubPath:   "registries.conf",
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

	boolTrue := true
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
			// Provides the /etc/containers/policy.json content from the node
			Name: "etc-policy",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: br.getEtcPolicyConfigMapName(),
					},
					Optional: &boolTrue,
				},
			},
		},
		{
			// Provides the /etc/containers/registries.conf content from the node
			Name: "etc-registries",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: br.getEtcRegistriesConfigMapName(),
					},
					Optional: &boolTrue,
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

	var terminationGracePeriodSeconds int64 = 10

	return &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: br.getObjectMeta(br.getBuildName()),
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			// Run the build process in an init container so that we can report accurate
			// status if the build process is successful but the configmap creation container fails
			InitContainers: []corev1.Container{
				{
					// This container performs the image build / push process.
					Name:                     "image-build",
					Image:                    br.opts.Images.MachineConfigOperator,
					Env:                      env,
					Command:                  append(command, buildahBuildScript),
					ImagePullPolicy:          corev1.PullAlways,
					SecurityContext:          securityContext,
					TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
					// Only attach the buildah-cache volume mount to the buildah container.
					VolumeMounts: append(volumeMounts, corev1.VolumeMount{
						Name:      "buildah-cache",
						MountPath: "/home/build/.local/share/containers",
					}),
				},
			},
			Containers: []corev1.Container{
				{
					// This container waits for the init container doing the build to finish
					// building so we can get the final image SHA. We do this by using
					// the base OS image (which contains the "oc" binary) to create a
					// ConfigMap from the digestfile that Buildah creates, which allows
					// us to avoid parsing log files.
					Name:                     "create-digest-configmap",
					Command:                  append(command, digestCMScript),
					Image:                    br.opts.MachineConfig.Spec.OSImageURL,
					Env:                      env,
					ImagePullPolicy:          corev1.PullAlways,
					SecurityContext:          securityContext,
					TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
					VolumeMounts:             volumeMounts,
				},
			},
			ServiceAccountName:            "machine-os-builder",
			Volumes:                       volumes,
			TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
		},
	}
}

// Populates the labels map for all objects created by imageBuildRequest
func (br buildRequestImpl) getLabelsForObjectMeta() map[string]string {
	return map[string]string{
		constants.EphemeralBuildObjectLabelKey:    "",
		constants.OnClusterLayeringLabelKey:       "",
		constants.RenderedMachineConfigLabelKey:   br.opts.MachineOSBuild.Spec.MachineConfig.Name,
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

func (br buildRequestImpl) getEtcPolicyConfigMapName() string {
	return utils.GetEtcPolicyConfigMapName(br.opts.MachineOSBuild)
}

func (br buildRequestImpl) getEtcRegistriesConfigMapName() string {
	return utils.GetEtcRegistriesConfigMapName(br.opts.MachineOSBuild)
}

// Computes the build name based upon the MachineConfigPool name.
func (br buildRequestImpl) getBuildName() string {
	return utils.GetBuildJobName(br.opts.MachineOSBuild)
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
