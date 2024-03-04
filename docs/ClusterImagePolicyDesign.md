## Summary
ClusterImagePolicy and ImagePolicy are CRDs that managed by ContainerRuntimeConfig controller. These CRDs allow setting up configurations for CRI-O  to verify the container images signed using [Sigstore](https://www.sigstore.dev/) tools.

## Goals
Generating corresponding CRI-O configuration files for image signature verification. Rollout ClusterImagePolicy to `/etc/containers/policy.json` for cluster wide configuration. Roll out ImagePolicy to `/etc/crio/policies/<NAMESPACE>.json` for pod namespace-separated signature policies configuration.

## Non-Goals
Rolling out configuration for OCP payload repositories. The (super scope of) OCP payload repositories will not be written to the configuration files. 

## CRD
[ClusterImagePolicy CRD](https://github.com/openshift/api/blob/master/config/v1alpha1/0000_10_config-operator_01_clusterimagepolicy-TechPreviewNoUpgrade.crd.yaml)

[ImagePolicy CRD](https://github.com/openshift/api/blob/master/config/v1alpha1/0000_10_config-operator_01_imagepolicy-TechPreviewNoUpgrade.crd.yaml)

## Example

## Validation and Troubleshooting

## Implementation Details
The ContainerRuntimeConfigController would perform the following steps:

1. Validate the ClusterImagePolicy and ImagePolicy objects on the cluster. Follow the table below to ignore the conflicting scopes.

|                                                                                                                 	|process the policies from the CRs                |                                                                                    	|   	|   	|
|-----------------------------------------------------------------------------------------------------------------	|------------------------------------------------	|-----------------------------------------------------------------------------------	|---	|---	|
| same scope in different CRs                                                                                     	| ImagePolicy                                    	| ClusterImagePolicy                                                                	|   	|   	|
| ClusterImagePolicy ImagePolicy (scope in the ClusterImagePolicy is equal to or broader than in the ImagePolicy) 	| Do not deploy non-global policy for this scope 	| Write the cluster policy to `/etc/containers/policy.json`  and `<NAMESPACE>.json` 	|   	|   	|
| ClusterImagePolicy ClusterImagePolicy                                                                           	| N/A                                            	| Append the policy to existing `etc/containers/policy.json`                        	|   	|   	|
| ImagePolicy ImagePolicy                                                                                         	| append the policy to <NAMESPACE>.json          	| N/A                                                                               	|   	|   	|

2. Render the current MachineConfigs (storage.files.contents[policy.json]) into the originalPolicyIgn

3. Serialize the cluster level policies to `policy.json`.

4. Copy the cluster policy.json to `<NAMESPACE>.json`, serialize the namespace level policies to `<NAMESPACE>.json`.

5. Add registries configuration to `/etc/containers/registries.d/sigstore-registries.yaml`. This configuration is used to specify the sigstore is being used as the image signature verification backend. 

6. Update the ignition file `/etc/containers/policy.json` within the `99-<pool>-generated-registries` MachineConfig.

7. Create or Update the ignition file `/etc/crio/policies/<NAMESPACE>.json` within the `99-<pool>-generated-imagepolicies` MachineConfig. 

After deletion all of the ClusterImagePolicy or the ImagePolicy instance the config will be reverted to the original policy.json.

## See Also
see **[containers-policy.json(5)](https://github.com/containers/image/blob/main/docs/containers-policy.json.5.md)**, **[containers-registries.d(5)](https://github.com/containers/image/blob/main/docs/containers-registries.d.5.md)**  for more information.


