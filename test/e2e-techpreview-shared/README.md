This test package should be used for e2e tests that need to be tested under the default featureset and the techpreview featureset. This would mean that your test 
has two paths, one when the feature gate is enabled and one when the feature gate is disabled. Tests added here will run under the test-e2e and test-e2e-techpreview
Makefile targets, which is used by ci/prow/e2e-gcp-op and ci/prow/e2e-gcp-op-techpreview test suites respectively.

Please remember to remove your test from this package when your feature is GA-ed(General Availability) and no longer behind the feature gate.