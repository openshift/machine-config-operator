# devex

## Background

This directory contains Golang programs which are most likely to be of use to
fellow OpenShift developers, especially members of the
[machine-config-operator](https://github.com/openshift/machine-config-operator)
team. The helpers found here may be of use to you. They may not. They may
completely break entirely.

It is worth mentioning that these helpers may get your cluster into a
difficult-to-recover-from state. So do not use these on a production OpenShift
cluster.

## Installation

From the repo root, run: `make install-helpers`. Note: You'll periodically have
to update the helpers based upon the current state of the MCO repository.
