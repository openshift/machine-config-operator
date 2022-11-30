package envtest

import (
	"context"
	"testing"

	"github.com/openshift/machine-config-operator/internal/clients"
	ctrlcommon "github.com/openshift/machine-config-operator/pkg/controller/common"
	"github.com/openshift/machine-config-operator/test/framework"
	"github.com/openshift/machine-config-operator/test/helpers"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
)

type EnvTestCase struct { //nolint:revive // This name is fine.
	// The name of the test
	Name string
	// The Kube objects to create in the test environment before starting the test.
	KubeObjs []runtime.Object
	// The setup function which initializes and starts the controller under test.
	SetupFunc func(context.Context, *testing.T, *framework.ClientSet, *ctrlcommon.ControllerContext)
	// The test function which verifies that the controller reaches a specified state.
	TestFunc func(context.Context, *testing.T, *framework.ClientSet)
	// The controller context to use. This is private because it is the
	// responsibility of this package to initialize it.
	ctrlctx *ctrlcommon.ControllerContext
	// The ClientSet to use. This is private because it is the responsibility of
	// this package to initialize it and configure it to point to the test
	// environment.
	clientSet *framework.ClientSet
	// The config used for the ClientSet. Private because it is the responsibility
	// of this package to start the test environment.
	cfg *rest.Config
}

func (e *EnvTestCase) testFunc(ctx context.Context, t *testing.T) {
	// Create a testcase-level context so that we can be sure everything is shut
	// down when the testcase is finished.
	testCtx, testCtxCancel := context.WithCancel(ctx)
	t.Cleanup(testCtxCancel)

	// Initialize a new clientset object
	e.clientSet = framework.NewClientSetFromConfig(e.cfg)

	// Check that we have a clean environment
	helpers.TimeIt(t, "Checking environment", func() {
		CheckCleanEnvironment(t, e.clientSet)
	})

	// Create our test objects
	helpers.TimeIt(t, "Creating objects in kube-apiserver", func() {
		CreateObjects(t, e.clientSet, e.KubeObjs...)
	})

	// Ensure that our test objects are cleaned up when the test completes.
	t.Cleanup(func() {
		helpers.TimeIt(t, "Cleaning environment", func() {
			CleanEnvironment(t, e.clientSet)
		})
	})

	// Create a context for the controller. This allows us to shut down the
	// controller before we clean the test environment.
	controlCtx, controlCancel := context.WithCancel(testCtx)
	t.Cleanup(controlCancel)

	// Create the controller context which can be used to set up the controller
	// under test. We wire its stop function to our controller context above.
	e.ctrlctx = ctrlcommon.CreateControllerContext(clients.BuilderFromConfig(e.cfg), controlCtx.Done(), ctrlcommon.MCONamespace)

	// Run the setup function which initializes the controller under test.
	e.SetupFunc(testCtx, t, e.clientSet, e.ctrlctx)

	// Run the actual test function which ensures that the controller reaches the desired state.
	e.TestFunc(testCtx, t, e.clientSet)
}

func (e *EnvTestCase) run(ctx context.Context, t *testing.T) {
	t.Run(e.Name, func(t *testing.T) {
		e.testFunc(ctx, t)
	})
}

// Used to run a list of test cases with the background context.
func RunEnvTestCases(t *testing.T, testCases []*EnvTestCase) {
	RunEnvTestCasesWithContext(context.Background(), t, testCases)
}

// Used to run a list of test cases with a provided context to enable
// cancelation / deadlines / timeouts / etc. from the calling function.
func RunEnvTestCasesWithContext(ctx context.Context, t *testing.T, testCases []*EnvTestCase) {
	// Create a suite-level context so that we can be sure that everything is
	// shut down before we shut down the envtest environment. Otherwise, we time
	// out waiting for the envtest environment.
	sCtx, sCancel := context.WithCancel(ctx)
	t.Cleanup(sCancel)

	runTestCasesWithContext(sCtx, t, testCases)
}

func runTestCasesWithContext(ctx context.Context, t *testing.T, testCases []*EnvTestCase) {
	t.Run("EnvTestCase", func(t *testing.T) {
		testEnv := NewTestEnv(t)
		testEnv.ErrorIfCRDPathMissing = true

		var cfg *rest.Config
		var err error

		helpers.TimeIt(t, "Started EnvTest", func() {
			cfg, err = testEnv.Start()
			require.NoError(t, err)
		})

		t.Cleanup(func() {
			helpers.TimeIt(t, "Shut down EnvTest", func() {
				require.NoError(t, testEnv.Stop())
			})
		})

		for _, testCase := range testCases {
			testCase := testCase
			testCase.cfg = cfg
			t.Run(testCase.Name, func(t *testing.T) {
				testCase.testFunc(ctx, t)
			})
		}
	})
}
