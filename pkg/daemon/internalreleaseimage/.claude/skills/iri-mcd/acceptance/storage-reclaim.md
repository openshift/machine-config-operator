# IRI storage reclaim acceptance criteria

## Goal

When the singleton InternalReleaseImage resource (name "cluster") is deleted, MCD should reclaim storage used by the local IRI registry backend without racing against registry shutdown.
Since the deletion of the IRI singleton resource will be used to opt-out from the NoRegistryClusterInstall feature, there's no specific need to 
report the progress. It's important that the manager will ensure that the storage is cleaned up when the IRI is not present and the registry is down.
The local IRI registry service will be stopped automatically when the IRI MachineConfigs will be deleted by the IRI controller

## Scenario: ensure disk cleanup on IRI deletion

Given the IRI resource is deleted
And the local IRI registry is not active on port 22625
And `/var/lib/iri-registry` is not empty

When the manager reconciles

Then it should remove `/var/lib/iri-registry` content


## Scenario: storage is not cleaned up during IRI deletion

Given the IRI resource is being deleted
And the local IRI registry is still active

When the manager reconciles

Then it should not remove `/var/lib/iri-registry`
And it should wait for the local IRI registry to be stopped
And avoid to perform any other task

