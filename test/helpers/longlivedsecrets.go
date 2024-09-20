package helpers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authv1client "k8s.io/client-go/kubernetes/typed/authentication/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	authv1 "k8s.io/api/authentication/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"k8s.io/client-go/rest"

	"github.com/openshift/machine-config-operator/test/framework"
)

// Creates a long-lived image pull secret for a service account token.
func CreateLongLivedPullSecret(ctx context.Context, cs *framework.ClientSet, opts LongLivedSecretOpts) error {
	creator, secret, err := createLongLivedPullSecret(ctx, cs, opts)
	if err != nil {
		return err
	}

	return creator.createSecret(ctx, secret)
}

// Creates a long-lived image pull secret for a service account token. This
// version attaches test-specific metadata to the secret before injecting it
// and returns an idempotent cleanup function that will delete the secret.
func CreateLongLivedPullSecretForTest(ctx context.Context, t *testing.T, cs *framework.ClientSet, opts LongLivedSecretOpts) func() {
	t.Helper()

	creator, secret, err := createLongLivedPullSecret(ctx, cs, opts)
	require.NoError(t, err)

	SetMetadataOnObject(t, secret)
	require.NoError(t, creator.createSecret(ctx, secret))

	concatNameAndNamespace := func(objMeta metav1.ObjectMeta) string {
		return fmt.Sprintf("%s/%s", objMeta.Namespace, objMeta.Name)
	}

	saName := concatNameAndNamespace(opts.ServiceAccount)
	secretName := concatNameAndNamespace(opts.Secret)

	t.Logf("Created long-lived image pull secret %q from service account %q", secretName, saName)

	return MakeIdempotent(func() {
		// We create a new context because it is possible that the provided context
		// was cancelled and we still want secret cleanup to succeed otherwise.
		ctx := context.Background()
		require.NoError(t, cs.CoreV1Interface.Secrets(opts.Secret.Namespace).Delete(ctx, opts.Secret.Name, metav1.DeleteOptions{}))
		t.Logf("Deleted long-lived image pull secret %q from service account %q", secretName, saName)
	})
}

// Creates a long-lived image pull secret for a service account token.
func createLongLivedPullSecret(ctx context.Context, cs *framework.ClientSet, opts LongLivedSecretOpts) (*secretCreator, *corev1.Secret, error) {
	_, err := exec.LookPath("oc")
	if err != nil {
		return nil, nil, fmt.Errorf("could not locate oc command: %w", err)
	}

	if err := opts.validateOpts(); err != nil {
		return nil, nil, fmt.Errorf("could not validate opts: %w", err)
	}

	creator := &secretCreator{
		LongLivedSecretOpts: opts,
		cs:                  cs,
	}

	token, err := creator.createToken(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("could not create token: %w", err)
	}

	cfg, err := creator.getRESTConfigForToken(ctx, token)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get RESTConfig for token: %w", err)
	}

	pullSecretBytes, err := creator.getPullSecretForRESTConfig(ctx, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get pull secret: %w", err)
	}

	return creator, creator.createSecretSpecFromBytes(pullSecretBytes), nil
}

// Options for a long-lived image pull secret attached to a service account
type LongLivedSecretOpts struct {
	// Delete the secret if it already exists
	DeleteIfExists bool
	// The service account metadata to use for referencing the service account. Specifically, Name and Namespace.
	ServiceAccount metav1.ObjectMeta
	// How long the secret should last for as a parseable time.Duration string (e.g., 24h, 5m, 60s)
	Lifetime string
	// The secret metadata to use for creating the secret.
	Secret metav1.ObjectMeta
}

// Validates a provided metav1.ObjectMeta object to ensure that the namespace
// and name is not empty.
func (l *LongLivedSecretOpts) validateObjectMeta(use string, target metav1.ObjectMeta) error {
	if target.Name == "" {
		return fmt.Errorf("%s name empty", use)
	}

	if target.Namespace == "" {
		return fmt.Errorf("%s namespace empty", use)
	}

	return nil
}

// Validates the provided options to ensure that the service account, secret,
// and lifetime fields are set appropriately.
func (l *LongLivedSecretOpts) validateOpts() error {
	if _, err := time.ParseDuration(l.Lifetime); err != nil {
		return fmt.Errorf("could not parse lifetime %q: %w", l.Lifetime, err)
	}

	if err := l.validateObjectMeta("service account", l.ServiceAccount); err != nil {
		return fmt.Errorf("could not validate service account data: %w", err)
	}

	if err := l.validateObjectMeta("target secret", l.Secret); err != nil {
		return fmt.Errorf("could not validate target secret: %w", err)
	}

	return nil
}

// Holds all of the methods for creating a long-lived image pull secret. The
// reason why one might wish to create such a secret is because
// <insert-operator-name-here> automatically rotates the default image pull
// secrets for all service accounts in an OpenShift namespace on an hourly
// basis; even if they were just created.
type secretCreator struct {
	LongLivedSecretOpts
	cs *framework.ClientSet
}

// Creates an authentication token for a given service account in a given namespace.
func (s *secretCreator) createToken(ctx context.Context) (string, error) {
	parsed, err := time.ParseDuration(s.Lifetime)
	if err != nil {
		return "", fmt.Errorf("could not parse lifetime %q: %w", s.Lifetime, err)
	}

	// Note: parsed.Seconds() returns a float64 that we cast to an int64. It's
	// probably OK to do this here for use within a test, especially since this
	// does not need to be fine-grained. However, production code may demand a
	// different solution.
	exp := int64(parsed.Seconds())

	req := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			ExpirationSeconds: &exp,
		},
	}

	resp, err := s.cs.CoreV1Interface.ServiceAccounts(s.ServiceAccount.Namespace).CreateToken(ctx, s.ServiceAccount.Name, req, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("could not create token for service account %q in namespace %q: %w", s.ServiceAccount.Name, s.ServiceAccount.Namespace, err)
	}

	return resp.Status.Token, nil
}

// Gets the REST Config from the provided framework.ClientSet instance and
// configures it to use the newly created token and the provided service
// account.
func (s *secretCreator) getRESTConfigForToken(ctx context.Context, token string) (*rest.Config, error) {
	cfg := s.cs.GetRestConfig()
	cfg.BearerToken = token
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: s.getFullyQualifiedServiceAccountUsername(),
	}

	// Test the config to ensure that it is valid.
	if err := s.testRESTConfig(ctx, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Constructs the "fully-qualified" service account name with the name and namespace.
func (s *secretCreator) getFullyQualifiedServiceAccountUsername() string {
	// TODO: Is there a better way to construct this string?
	return fmt.Sprintf("system:serviceaccount:%s:%s", s.ServiceAccount.Namespace, s.ServiceAccount.Name)
}

// Tests the newly created REST config to ensure that it is configured
// correctly. This essentially performs the same thing as $ oc whoami except
// that it verifies the returned data.
func (s *secretCreator) testRESTConfig(ctx context.Context, cfg *rest.Config) error {
	authclient := authv1client.NewForConfigOrDie(cfg)

	ssr := &authv1.SelfSubjectReview{}

	res, err := authclient.SelfSubjectReviews().Create(ctx, ssr, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("could not create self subject review: %w", err)
	}

	username := s.getFullyQualifiedServiceAccountUsername()
	if res.Status.UserInfo.Username != username {
		return fmt.Errorf("incorrect username, expected %q, got %q", username, res.Status.UserInfo.Username)
	}

	return nil
}

// Writes the given REST Config to the given filename in a format that is
// usable as a KUBECONFIG.
func (s *secretCreator) writeRESTConfigToFile(cfg *rest.Config, filename string) error {
	// Adapted from: https://github.com/kubernetes/client-go/issues/711#issuecomment-730112049
	clusterName := "default-cluster"
	contextName := "default-context"

	clientConfig := clientcmdapi.Config{
		Kind:       "Config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: {
				Server:                   cfg.Host,
				CertificateAuthorityData: cfg.TLSClientConfig.CAData,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			contextName: {
				Cluster:   clusterName,
				Namespace: s.ServiceAccount.Namespace,
				AuthInfo:  s.ServiceAccount.Namespace,
			},
		},
		CurrentContext: "default-context",
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			s.ServiceAccount.Namespace: {
				Impersonate: cfg.Impersonate.UserName,
				Token:       cfg.BearerToken,
			},
		},
	}

	return clientcmd.WriteToFile(clientConfig, filename)
}

// This takes the given REST Config for the service account and performs the
// following actions with it to generate an internal image registry pull
// secret:
//
// 1. Writes it to a temporary directory (this dir is cleaned up upon return).
// 2. The oc login and oc registry login commands consume it via setting the
// KUBECONFIG env var to point to it.
// 3. After registry login, the generated pull secret is read and returned.
//
// We shell out to oc to perform these steps because it would be a nontrivial
// amount of work to reproduce / reimplement the oc registry login command.
func (s *secretCreator) getPullSecretForRESTConfig(ctx context.Context, cfg *rest.Config) ([]byte, error) {
	tmpdir, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, fmt.Errorf("could not create temp directory: %w", err)
	}

	defer os.RemoveAll(tmpdir)

	kubeconfigPath := filepath.Join(tmpdir, "kubeconfig")

	if err := s.writeRESTConfigToFile(cfg, kubeconfigPath); err != nil {
		return nil, fmt.Errorf("could not write REST config to %q: %w", kubeconfigPath, err)
	}

	// If we encounter an error, we should scrub the bearer token from any
	// output.
	redactSensitiveInfo := func(in string) string {
		toReplace := []string{
			cfg.BearerToken,
		}

		for _, item := range toReplace {
			in = strings.ReplaceAll(in, item, "---REDACTED---")
		}

		return in
	}

	// Runs the given command and returns its combined output in the form of a string.
	runCmdWithOutput := func(name string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		// Ensure that we use the KUBECONFIG we wrote to our temp dir instead of
		// the value inherited by this process.
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("could not run %q, got %q: %w", redactSensitiveInfo(cmd.String()), redactSensitiveInfo(string(output)), err)
		}

		return string(output), nil
	}

	// Runs the given command but does not return any output.
	runCmd := func(name string, args ...string) error {
		_, err := runCmdWithOutput(name, args...)
		return err
	}

	// Run: "$ oc login --token=<token> --server=<hostname>"
	if err := runCmd("oc", "login", "--token", cfg.BearerToken, "--server", cfg.Host); err != nil {
		return nil, fmt.Errorf("could not log in: %w", err)
	}

	// Run: "$ oc whoami"
	// Not strictly required, but provides a good sanity check.
	whoami, err := runCmdWithOutput("oc", "whoami")
	if err != nil {
		return nil, fmt.Errorf("could not run oc whoami: %w", err)
	}

	// Validate that the fully qualified service account name is present in the output.
	username := s.getFullyQualifiedServiceAccountUsername()
	if !strings.Contains(whoami, username) {
		return nil, fmt.Errorf("expected oc whoami to return %q, got %q", username, whoami)
	}

	pullsecretPath := filepath.Join(tmpdir, "config.json")

	// Run: "$ oc registry login --to=<path to target file in temp dir>"
	if err := runCmd("oc", "registry", "login", "--to", pullsecretPath); err != nil {
		return nil, fmt.Errorf("could not registry login: %w", err)
	}

	// Read the temp file containing the image pull secret bytes.
	return os.ReadFile(pullsecretPath)
}

// Instantiates the secret spec from the provided bytes.
func (s *secretCreator) createSecretSpecFromBytes(secretBytes []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: s.Secret,
		Type:       corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: secretBytes,
		},
	}
}

// Creates the secret in the Kube API server, deleting a preexisting one if it
// exists and the option is set.
func (s *secretCreator) createSecret(ctx context.Context, secret *corev1.Secret) error {
	_, err := s.cs.CoreV1Interface.Secrets(s.Secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})

	if k8serrors.IsAlreadyExists(err) && s.DeleteIfExists {
		if err := s.cs.CoreV1Interface.Secrets(s.Secret.Namespace).Delete(ctx, s.Secret.Name, metav1.DeleteOptions{}); err != nil {
			return err
		}

		return s.createSecret(ctx, secret)
	}

	if err != nil {
		return fmt.Errorf("cannot create secret %q: %w", secret.Name, err)
	}

	return nil
}
