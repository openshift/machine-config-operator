# e2e Shared Tests

To reduce duplication across multiple e2e test suites, tests which can be
reused across all MCO test suites should live within this package.

## To Add A New Shared Test

1. Create a new file in this directory named after your test. Do not follow the
   Golang `_test.go` convention since symbols within files with the `_test.go`
   suffix cannot be imported from another package.
2. Create a struct implementing the `SharedTest` interface to contain your test.
3. In `shared.go`, add any options your test may require to the `SharedTestOpts` struct.
4. Wire your new test struct into the testCase in `shared.go`.
5. Update the `TestRunShared` tests in `./test/e2e/mcd_test.go` and
   `./test/e2e-single-node/sno_mcd_test.go` with the opts struct your new test
   takes (if it takes one).
