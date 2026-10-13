# Signed release payload acceptance criteria

## Goal

A signed OCP release payload carries a cosign signature next to the release image.
The signature is stored in the same `/openshift/release-images` repository as an
extra tag named `sha256-<digest>.sig`. It is not a release image and must never be
counted as one: the NoRegistryClusterInstall feature currently supports a single
release, so counting the signature makes the manager reject a registry that holds
exactly one valid release.

Signed payloads come from release builds, which are tagged by version
(`4.22.16-x86_64`). CI and nightly payloads are tagged by bare image digest
(`68bdf244...`). Both have to be supported, and both have to be reported by
digest: the MachineConfigNode API enforces a `@sha256:<digest>` reference on
`status.internalReleaseImage.releases[].image` through a CEL rule, so a tag
reference is rejected by the API server and the status update fails.

## Scenario: resolve the release tag when a cosign signature is present

Given the local IRI registry is active
And `/openshift/release-images` holds one release tag and one `.sig` tag

When the manager reconciles

Then it should report the release in the MachineConfigNode status
And the release should be available and not degraded
And the `.sig` tag should not be reported as a release

## Scenario: resolve a version-tagged release build to its digest

Given the release tag is a version tag, such as `4.22.16-x86_64`

When the manager reports the release

Then it should read the manifest digest from the registry
And the pull spec should reference the release by that digest

## Scenario: reference a CI or nightly build by digest

Given the release tag is a bare image digest

When the manager reports the release

Then the pull spec should reference the release by digest, as `@sha256:<tag>`
And no manifest lookup should be needed

## Scenario: the release tag is malformed

Given the release tag does not match the registry tag format

When the manager reports the release

Then it should fail with an invalid-tag error
And it should not query the registry, since a malformed tag could alter the
manifest URL and resolve the digest of a different image

## Scenario: the reported digest does not match its manifest

Given the release tag is a version tag
And the `Docker-Content-Digest` is missing, or is not the digest of the returned
manifest

When the manager reports the release

Then it should fail with a digest mismatch error, rather than report a release
the registry cannot back

## Scenario: more than one release image is still unsupported

Given `/openshift/release-images` holds two release tags, not counting `.sig` tags

When the manager reconciles

Then it should fail with an unsupported-release error

## Scenario: no release image found

Given `/openshift/release-images` holds no tags, or only `.sig` tags

When the manager reconciles

Then it should fail with a missing-release error
And it should not panic
