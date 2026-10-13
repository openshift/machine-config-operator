# IRI feature disabled

## Goal

Avoid to consume cpu/mem resources when the NoRegistryClusterInstall is disabled. There could be two different scenarios to consider:

- The feature was never activated
- The user explicitly disabled the feature by deleting the IRI resource

In both the cases, we'd like to consume as few as possible resources when the feature is not enabled


## Scenario: skip everything if feature is disabled

Given the `/var/lib/iri-registry` does not exist
When the manager reconciles
Then skip all the actions and return immediately (requeue with a longer time)
