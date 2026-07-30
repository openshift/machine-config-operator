# e2e IRI tests

This test package should be used for testing the behaviors of the InternalReleaseImage controller.
Currently it is deployed as part of the Agent Installer for OpenShift-Virt (OVE) - and specifically for the NoRegistryClusterInstall feature.
To run the tests, deploy first a baremetal cluster using the OVE ISO.

# Available suites
- `iri_test.go`: This suite contains all the standard tests.
- `iri_deletion_test.go`: This suite verifies the scenario where the IRI resource is deleted.