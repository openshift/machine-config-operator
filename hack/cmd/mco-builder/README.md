# mco-builder

The `mco-builder` binary exists to make MCO developers lives easier. It's
purpose is to build an MCO image using ones local changes and automatically
push that image into the developers' sandbox cluster for quick and easy
iterative development.

## Quick Start

The quickest and easiest way to get started once you've downloaded this tool is to use the local build mode coupled with direct cluster pushes:

```console
$ mco-builder local --direct --repo-root /path/to/your/mco/git/repo
```

This mode will perform a local build on your machine and push the resulting
image directly into your clusters' internal image registry. Then it will roll
the image out.

## Repo Build Modes

There are three build modes available within the `mco-builder` binary. They are:

1. `normal` - Uses the Dockerfile contained within the MCO repository root to build. Using this mode will allow you to reflect the exact state that is in your current MCO repository. However, this mode is the slowest since it performs a complete build within the build context.
2. `fast` - This mode uses a hacked Dockerfile and Makefile in order to build the binaries outside of the container build context and then copy them into the container. Additionally, this mode uses a different final base image which may not always be up-to-date. However, this mode allows one to have the fastest possible local builds by leveraging Golang's incremental build capability as well as container image caching.
3. `cluster` - This mode uses also uses a hacked Dockerfile. However, this is optimized for the case where one wishes to leverage their OpenShift cluster to perform their builds instead of their local machine (see: Local vs. Cluster).

## Supported Local Image Builders

Right now, this tool supports building images locally with Podman and Docker.
However, it has a pluggable architecture that will allow other local image
builders (e.g., Buildah, Kaniko, et. al.) to be used in place of Podman or
Docker.

This has mostly only been tested using an Intel Mac host running Docker targeting an AMD64 OpenShift cluster. Other configurations may not work yet.

It is worth mentioning that using direct mode requires Skopeo since it offers
more control over how the image is pushed.

The default builder depends upon what platform this binary is being run on. If on a Linux machine, it will default to `podman`. On Mac, it will default to `docker`. Either of these options may be easily overridden by using the `--builder` flag.

## Local vs. Cluster

There are two main modes of operation that one can select from:

### Local Mode

```console
$ mco-builder local --help

Builds the MCO image locally using the specified builder and options. Can either push to a remote image registry (such as Quay.io) or can expose a route to enable pushing directly into ones sandbox cluster.

Usage:
  mco-builder local [flags]

Flags:
      --build-mode string             What build mode to use: [fast normal] (default "fast")
      --builder string                What image builder to use: [docker podman] (default "docker")
      --direct                        Exposes a route and pushes the image directly into ones cluster
      --final-image-pullspec string   Where to push the final image (not needed in direct mode)
  -h, --help                          help for local
      --push-secret string            Path to the push secret path needed to push to the provided pullspec (not needed in direct mode)
      --repo-root string              Path to the local MCO Git repo

Global Flags:
      --kubeconfig string              Paths to a kubeconfig. Only required if out-of-cluster.
      --log-flush-frequency duration   Maximum number of seconds between log flushes (default 5s)
  -v, --v Level                        number for the log level verbosity
      --vmodule moduleSpec             comma-separated list of pattern=N settings for file-filtered logging (only works for the default text log format)
```

This mode uses your local image builder (either Podman or Docker) to build the
image locally on your developer machine. When used with the `--direct` option,
one can push their built image directly into their cluster without the need of
an external container registry such as Quay.io. Using direct mode will
implicitly create an ImageStream within the MCO namespace called
`machine-config-operator` to push the image to.

If the `--direct` option is not used, one must also provide the path to a push
secret as well as the final image pullspec indicating where the image should be
pushed to.

### Cluster Mode

```console
$ mco-builder cluster --help
Performs the build operation within the sandbox cluster using an OpenShift Image Build

Usage:
  mco-builder cluster [flags]

Flags:
      --follow             Stream build logs (default true)
  -h, --help               help for cluster
      --repo-root string   Path to the local MCO Git repo

Global Flags:
      --kubeconfig string              Paths to a kubeconfig. Only required if out-of-cluster.
      --log-flush-frequency duration   Maximum number of seconds between log flushes (default 5s)
  -v, --v Level                        number for the log level verbosity
      --vmodule moduleSpec             comma-separated list of pattern=N settings for file-filtered logging (only works for the default text log format)
```

This mode requires that you push your changes to a personal Git fork of the MCO
repository. This will leverage the OpenShift Image Builder capability to clone
your repo, build the image, and apply it all from within your sandbox cluster.
This mode is ideally suited for situations where one either does not have fast
local compute or fast local network.

This mode creates an ImageStream within the MCO namespace called `machine-config-operator` to push the image to.

## Reverting

Should you wish to revert your sandbox cluster back to its original state, one can use the `revert` subcommand thusly:

```console
$ mco-builder revert
```

This command performs the following actions:
1. Rolls the MCO back to its original image for that OpenShift release.
2. Unexposes the internal image stream route.
3. Deletes any ImageStreams that were created.
