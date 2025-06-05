package util

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	kapiv1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kclientset "k8s.io/client-go/kubernetes"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	e2e "k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/testfiles"
	e2etestingmanifests "k8s.io/kubernetes/test/e2e/testing-manifests"

	// this appears to inexplicably auto-register global flags.
	_ "k8s.io/kubernetes/test/e2e/storage/drivers"

	configv1 "github.com/openshift/api/config/v1"
	projectv1 "github.com/openshift/api/project/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	securityv1client "github.com/openshift/client-go/security/clientset/versioned"
)

const (
	EnvIsExternalOIDCCluster = "ENV_IS_EXTERNAL_OIDC_CLUSTER"
	EnvIsKubernetesCluster   = "ENV_IS_KUBERNETES_CLUSTER"
)

var (
	reportFileName string
	syntheticSuite string
	quiet          bool
)

var TestContext *e2e.TestContextType = &e2e.TestContext

var (
	IsExternalOIDCClusterFlag = ""
	IsKubernetesClusterFlag   = ""
)

func InitStandardFlags() {
	e2e.RegisterCommonFlags(flag.CommandLine)
	e2e.RegisterClusterFlags(flag.CommandLine)

	// replaced by a bare import above.
	// e2e.RegisterStorageFlags()
}

func InitTest(dryRun bool) error {
	InitDefaultEnvironmentVariables()
	// interpret synthetic input in `--ginkgo.focus` and/or `--ginkgo.skip`
	ginkgo.BeforeEach(checkSyntheticInput)

	TestContext.DeleteNamespace = os.Getenv("DELETE_NAMESPACE") != "false"
	TestContext.VerifyServiceAccount = true
	testfiles.AddFileSource(e2etestingmanifests.GetE2ETestingManifestsFS())
	TestContext.KubectlPath = "kubectl"
	TestContext.KubeConfig = KubeConfigPath()
	os.Setenv("KUBECONFIG", TestContext.KubeConfig)

	// "debian" is used when not set. At least GlusterFS tests need "custom".
	// (There is no option for "rhel" or "centos".)
	TestContext.NodeOSDistro = "custom"
	TestContext.MasterOSDistro = "custom"

	// load and set the host variable for kubectl
	if !dryRun {
		clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: TestContext.KubeConfig}, &clientcmd.ConfigOverrides{})
		cfg, err := clientConfig.ClientConfig()
		if err != nil {
			return err
		}
		TestContext.Host = cfg.Host
	}

	reportFileName = os.Getenv("TEST_REPORT_FILE_NAME")
	if reportFileName == "" {
		reportFileName = "junit"
	}

	quiet = os.Getenv("TEST_OUTPUT_QUIET") == "true"

	// Ensure that Kube tests run privileged (like they do upstream)
	TestContext.CreateTestingNS = createTestingNS

	return nil
}

func AnnotateTestSuite() {
	// qe take different method to select case, so no need to annotate it.
	waitErr := wait.PollUntilContextTimeout(context.Background(), 3*time.Second, 30*time.Second, false, func(_ context.Context) (bool, error) {
		out, err := kubectlCmd("get", "node").CombinedOutput()
		if err != nil && strings.Contains(string(out), "Service Unavailable") {
			e2e.Logf("Fail to get the cluster:%v, error: %v, try again", string(out), err)
			return false, nil
		}
		return true, nil
	})
	if waitErr != nil {
		e2e.Logf("Fail to get the cluster")
		os.Exit(1)
	}
}

// PreDetermineExternalOIDCCluster checks if the cluster is using external OIDC preflight to avoid to check it everytime.
func PreDetermineExternalOIDCCluster() (bool, error) {

	clientConfig, err := e2e.LoadConfig(true)
	if err != nil {
		e2e.Logf("clientConfig err: %v", err)
		return false, err
	}
	client, err := configclient.NewForConfig(clientConfig)
	if err != nil {
		e2e.Logf("client err: %v", err)
		return false, err
	}

	var auth *configv1.Authentication
	var errAuth error
	err = wait.PollUntilContextTimeout(context.Background(), 3*time.Second, 9*time.Second, true, func(_ context.Context) (bool, error) {
		auth, errAuth = client.ConfigV1().Authentications().Get(context.Background(), "cluster", metav1.GetOptions{})
		if errAuth != nil {
			e2e.Logf("auth err: %v", errAuth)
			return false, nil
		}
		return true, nil
	})

	if err != nil {
		return false, errAuth
	}

	// auth.Spec.Type is optionial. if it does not exist, auth.Spec.Type is empty string
	// if it exists and set as "", it is also empty string
	e2e.Logf("Found authentication type used: %v", string(auth.Spec.Type))
	return string(auth.Spec.Type) == string(configv1.AuthenticationTypeOIDC), nil
}

// PreDetermineK8sCluster checks if the active cluster is a Kubernetes cluster (as opposed to OpenShift).
func PreDetermineK8sCluster() (isK8s bool, err error) {
	ctx := context.Background()

	kubeClient, err := e2e.LoadClientset(true)
	if err != nil {
		return false, fmt.Errorf("failed to load Kubernetes clientset: %w", err)
	}

	err = wait.PollUntilContextTimeout(ctx, 3*time.Second, 9*time.Second, true, func(ctx context.Context) (done bool, err error) {
		isOpenShift, isOCPErr := IsOpenShiftCluster(ctx, kubeClient.CoreV1().Namespaces())
		if isOCPErr != nil {
			e2e.Logf("failed to check if the active cluster is OpenShift: %v", isOCPErr)
			return false, nil
		}
		isK8s = !isOpenShift
		return true, nil
	})

	if err != nil {
		return false, fmt.Errorf("error during polling: %w", err)
	}

	return isK8s, nil
}

func PreSetEnvK8s() (res string) {
	isK8s, err := PreDetermineK8sCluster()
	switch {
	case err != nil:
		res = "unknown"
	case isK8s:
		res = "yes"
	default:
		res = "no"
	}
	_ = os.Setenv(EnvIsKubernetesCluster, res)
	return res
}

func PreSetEnvOIDCCluster() (res string) {
	isOIDC, err := PreDetermineExternalOIDCCluster()
	switch {
	case err != nil:
		res = "unknown"
	case isOIDC:
		res = "yes"
	default:
		res = "no"
	}
	_ = os.Setenv(EnvIsExternalOIDCCluster, res)
	return res
}

func kubectlCmd(args ...string) *exec.Cmd {
	kubectlArgs := []string{}

	// Reference a --server option so tests can run anywhere.
	if TestContext.Host != "" {
		kubectlArgs = append(kubectlArgs, "--"+clientcmd.FlagAPIServer+"="+TestContext.Host)
	}
	if TestContext.KubeConfig != "" {
		kubectlArgs = append(kubectlArgs, "--"+clientcmd.RecommendedConfigPathFlag+"="+TestContext.KubeConfig)

		// Reference the KubeContext
		if TestContext.KubeContext != "" {
			kubectlArgs = append(kubectlArgs, "--"+clientcmd.FlagContext+"="+TestContext.KubeContext)
		}

	} else if TestContext.CertDir != "" {
		kubectlArgs = append(kubectlArgs,
			fmt.Sprintf("--certificate-authority=%s", filepath.Join(TestContext.CertDir, "ca.crt")),
			fmt.Sprintf("--client-certificate=%s", filepath.Join(TestContext.CertDir, "kubecfg.crt")),
			fmt.Sprintf("--client-key=%s", filepath.Join(TestContext.CertDir, "kubecfg.key")))
	}
	kubectlArgs = append(kubectlArgs, args...)

	// We allow users to specify path to kubectl, so you can test either "kubectl" or "cluster/kubectl.sh"
	// and so on.
	cmd := exec.Command(TestContext.KubectlPath, kubectlArgs...)

	// caller will invoke this and wait on it.
	return cmd
}

func getNetworkSkips() []string {
	out, err := kubectlCmd("get", "network.operator.openshift.io", "cluster", "--template", "{{.spec.defaultNetwork.type}}{{if .spec.defaultNetwork.openshiftSDNConfig}} {{.spec.defaultNetwork.type}}/{{.spec.defaultNetwork.openshiftSDNConfig.mode}}{{end}}").CombinedOutput()
	if err != nil {
		e2e.Logf("Could not get network operator configuration: not adding any plugin-specific skips.")
		return nil
	}
	return strings.Split(string(out), " ")
}

func newGinkgoTestRenamerFromGlobals(provider string, networkSkips []string) *ginkgoTestRenamer {
	var allLabels []string
	matches := make(map[string]*regexp.Regexp)
	stringMatches := make(map[string][]string)
	excludes := make(map[string]*regexp.Regexp)

	for label, items := range testMaps {
		sort.Strings(items)
		allLabels = append(allLabels, label)
		var remain []string
		for _, item := range items {
			re := regexp.MustCompile(item)
			if p, ok := re.LiteralPrefix(); ok {
				stringMatches[label] = append(stringMatches[label], p)
			} else {
				remain = append(remain, item)
			}
		}
		if len(remain) > 0 {
			matches[label] = regexp.MustCompile(strings.Join(remain, `|`))
		}
	}
	for label, items := range labelExcludes {
		sort.Strings(items)
		excludes[label] = regexp.MustCompile(strings.Join(items, `|`))
	}
	sort.Strings(allLabels)

	if provider != "" {
		excludedTests = append(excludedTests, fmt.Sprintf(`\[Skipped:%s\]`, provider))
	}
	for _, network := range networkSkips {
		excludedTests = append(excludedTests, fmt.Sprintf(`\[Skipped:Network/%s\]`, network))
	}
	klog.V(4).Infof("openshift-tests-private excluded test regex is %q", strings.Join(excludedTests, `|`))
	excludedTestsFilter := regexp.MustCompile(strings.Join(excludedTests, `|`))

	return &ginkgoTestRenamer{
		allLabels:     allLabels,
		stringMatches: stringMatches,
		matches:       matches,
		excludes:      excludes,

		excludedTestsFilter: excludedTestsFilter,
	}
}

type ginkgoTestRenamer struct {
	allLabels     []string
	stringMatches map[string][]string
	matches       map[string]*regexp.Regexp
	excludes      map[string]*regexp.Regexp

	excludedTestsFilter *regexp.Regexp
}

func (r *ginkgoTestRenamer) maybeRenameTest(name string, _ types.TestSpec) {
	labels := ""
	for {
		count := 0
		for _, label := range r.allLabels {
			if strings.Contains(name, label) {
				continue
			}

			var hasLabel bool
			for _, segment := range r.stringMatches[label] {
				hasLabel = strings.Contains(name, segment)
				if hasLabel {
					break
				}
			}
			if !hasLabel {
				if re := r.matches[label]; re != nil {
					hasLabel = r.matches[label].MatchString(name)
				}
			}

			if hasLabel {
				// TODO: remove when we no longer need it
				if re, ok := r.excludes[label]; ok && re.MatchString(name) {
					continue
				}
				count++
				labels += " " + label
				name += " " + label
			}
		}
		if count == 0 {
			break
		}
	}
}

// ProwGCPSetup makes sure certain required env vars are available in the case
// that extended tests are invoked directly via calls to ginkgo/extended.test
func InitDefaultEnvironmentVariables() {
	if ad := os.Getenv("ARTIFACT_DIR"); strings.TrimSpace(ad) == "" {
		os.Setenv("ARTIFACT_DIR", filepath.Join(os.TempDir(), "artifacts"))
	}
}

// TODO: Use either explicit tags (k8s.io) or https://github.com/onsi/ginkgo/v2/pull/228 to implement this.
// isPackage determines wether the test is in a package.  Ideally would be implemented in ginkgo.
func isPackage(pkg string) bool {
	return strings.Contains(ginkgo.CurrentSpecReport().FileName(), pkg)
}

// TODO: For both is*Test functions, use either explicit tags (k8s.io) or https://github.com/onsi/ginkgo/v2/pull/228
func isOriginTest() bool {
	return isPackage("/origin/test/")
}

func isKubernetesE2ETest() bool {
	return isPackage("/kubernetes/test/e2e/")
}

func testNameContains(name string) bool {
	return strings.Contains(ginkgo.CurrentSpecReport().FullText(), name)
}

func skipTestNamespaceCustomization() bool {
	return (isPackage("/kubernetes/test/e2e/namespace.go") && (testNameContains("should always delete fast") || testNameContains("should delete fast enough")))
}

// createTestingNS ensures that kubernetes e2e tests have their service accounts in the privileged and anyuid SCCs
func createTestingNS(ctx context.Context, baseName string, c kclientset.Interface, labels map[string]string) (*kapiv1.Namespace, error) {
	if !strings.HasPrefix(baseName, "e2e-") {
		baseName = "e2e-" + baseName
	}

	ns, err := e2e.CreateTestingNS(ctx, baseName, c, labels)
	if err != nil {
		return ns, err
	}

	// Add anyuid and privileged permissions for upstream tests
	if strings.HasPrefix(baseName, "e2e-k8s-") || (isKubernetesE2ETest() && !skipTestNamespaceCustomization()) {
		clientConfig, err := getClientConfig(KubeConfigPath())
		if err != nil {
			return ns, err
		}
		securityClient, err := securityv1client.NewForConfig(clientConfig)
		if err != nil {
			return ns, err
		}
		e2e.Logf("About to run a Kube e2e test, ensuring namespace is privileged")
		// add the "privileged" scc to ensure pods that explicitly
		// request extra capabilities are not rejected
		addE2EServiceAccountsToSCC(securityClient, []kapiv1.Namespace{*ns}, "privileged")
		// add the "anyuid" scc to ensure pods that don't specify a
		// uid don't get forced into a range (mimics upstream
		// behavior)
		addE2EServiceAccountsToSCC(securityClient, []kapiv1.Namespace{*ns}, "anyuid")
		// add the "hostmount-anyuid" scc to ensure pods using hostPath
		// can execute tests
		addE2EServiceAccountsToSCC(securityClient, []kapiv1.Namespace{*ns}, "hostmount-anyuid")

		// The intra-pod test requires that the service account have
		// permission to retrieve service endpoints.
		rbacClient, err := rbacv1client.NewForConfig(clientConfig)
		if err != nil {
			return ns, err
		}
		addRoleToE2EServiceAccounts(rbacClient, []kapiv1.Namespace{*ns}, "view")

		// in practice too many kube tests ignore scheduling constraints
		allowAllNodeScheduling(c, ns.Name)
	}

	return ns, err
}

var (
	testMaps = map[string][]string{
		// tests that are known flaky
		"[Flaky]": {
			`Job should run a job to completion when tasks sometimes fail and are not locally restarted`, // seems flaky, also may require too many resources
			`openshift mongodb replication creating from a template`,                                     // flaking on deployment

			// TODO(node): test works when run alone, but not in the suite in CI
			`\[Feature:HPA\] Horizontal pod autoscaling \(scale resource: CPU\) \[sig-autoscaling\] ReplicationController light Should scale from 1 pod to 2 pods`,
		},
		// tests that must be run without competition
		"[Serial]": {
			`\[Disruptive\]`,
			`\[Exclusive\]`,
		},
	}

	// labelExcludes temporarily block tests out of a specific suite
	labelExcludes = map[string][]string{}

	excludedTests = []string{
		`\[Disabled:`,
		`\[Disruptive\]`,
		`\[Exclusive\]`,
		`\[Skipped\]`,
		`\[Slow\]`,
		`\[Flaky\]`,
		`\[local\]`,
		`\[Suite:openshift/test-cmd\]`,
	}
)

// checkSyntheticInput selects tests based on synthetic skips or focuses
func checkSyntheticInput() {
	checkSuiteSkips()
}

// checkSuiteSkips ensures Origin/Kubernetes synthetic skip labels are applied
// Deprecated: remove in a future release
func checkSuiteSkips() {
	suiteConfig, _ := ginkgo.GinkgoConfiguration()
	switch {
	case isOriginTest():
		skip := strings.Join(suiteConfig.SkipStrings, "|")
		if strings.Contains(skip, "Synthetic Origin") {
			ginkgo.Skip("skipping all openshift/origin tests")
		}
	case isKubernetesE2ETest():
		skip := strings.Join(suiteConfig.SkipStrings, "|")
		if strings.Contains(skip, "Synthetic Kubernetes") {
			ginkgo.Skip("skipping all k8s.io/kubernetes tests")
		}
	}
}

var longRetry = wait.Backoff{Steps: 100}

// allowAllNodeScheduling sets the annotation on namespace that allows all nodes to be scheduled onto.
func allowAllNodeScheduling(c kclientset.Interface, namespace string) {
	err := retry.RetryOnConflict(longRetry, func() error {
		ns, err := c.CoreV1().Namespaces().Get(context.Background(), namespace, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if ns.Annotations == nil {
			ns.Annotations = make(map[string]string)
		}
		ns.Annotations[projectv1.ProjectNodeSelector] = ""
		_, err = c.CoreV1().Namespaces().Update(context.Background(), ns, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		FatalErr(err)
	}
}

func addE2EServiceAccountsToSCC(securityClient securityv1client.Interface, namespaces []kapiv1.Namespace, sccName string) {
	// Because updates can race, we need to set the backoff retries to be > than the number of possible
	// parallel jobs starting at once. Set very high to allow future high parallelism.
	err := retry.RetryOnConflict(longRetry, func() error {
		scc, err := securityClient.SecurityV1().SecurityContextConstraints().Get(context.Background(), sccName, metav1.GetOptions{})
		if err != nil {
			if apierrs.IsNotFound(err) {
				return nil
			}
			return err
		}

		for _, ns := range namespaces {
			if isE2ENamespace(ns.Name) {
				scc.Groups = append(scc.Groups, fmt.Sprintf("system:serviceaccounts:%s", ns.Name))
			}
		}
		if _, err := securityClient.SecurityV1().SecurityContextConstraints().Update(context.Background(), scc, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		FatalErr(err)
	}
}

func isE2ENamespace(_ string) bool {
	return true
}

func addRoleToE2EServiceAccounts(rbacClient rbacv1client.RbacV1Interface, namespaces []kapiv1.Namespace, roleName string) {
	err := retry.RetryOnConflict(longRetry, func() error {
		for _, ns := range namespaces {
			if isE2ENamespace(ns.Name) && ns.Status.Phase != kapiv1.NamespaceTerminating {
				_, err := rbacClient.RoleBindings(ns.Name).Create(context.Background(), &rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{GenerateName: "default-" + roleName, Namespace: ns.Name},
					RoleRef: rbacv1.RoleRef{
						Kind: "ClusterRole",
						Name: roleName,
					},
					Subjects: []rbacv1.Subject{
						{Name: "default", Namespace: ns.Name, Kind: rbacv1.ServiceAccountKind},
					},
				}, metav1.CreateOptions{})
				if err != nil {
					e2e.Logf("Warning: Failed to add role to e2e service account: %v", err)
				}
			}
		}
		return nil
	})
	if err != nil {
		FatalErr(err)
	}
}
