# custom-os-images

This directory contains two Containerfiles, `Containerfile.rhel-coreos-9` and
`Containerfile.rhel-coreos-10`, respectively. These images are intended for
testing Image Mode OpenShift and are intended to be built by the CI system and
injected into the e2e test suites. They're ephemeral since they'll be discarded
when the CI run is finished.

To make maintaining and debugging these images easier, there is a Makefile
target that can be used locally by developers to validate that the images build
without issue. Just run `make custom-os-images`.
