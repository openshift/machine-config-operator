# Using OCP CoreOS Layering Phase 0

In service to our [OCP CoreOS Layering Enhancement](https://github.com/openshift/enhancements/blob/master/enhancements/ocp-coreos-layering/ocp-coreos-layering.md#phase-0).

## Layering

Layering lets you "layer" additional content on top of a Base OS Image using "containerfile" syntax and apply it to an OpenShift cluster using the MCO.

As of 4.12:

- The MCO uses the `rhel-coreos-8` [native format](https://coreos.github.io/rpm-ostree/container/) base OS image by default instead of `machine-os-content`
- You can "layer" user content on top of that `rhel-coreos-8` image using a container build, and that content will be applied during a rebase
- The MCO will allow `OSImageURL` to be overridden *on a per-pool basis* with such an layered image

While layering is powerful, it's also an "advanced" use of the MCO, and it comes with some trade-offs.

## How it works right now

1. Get your base image
1. "Layer" some content on top of it (Dockerfile "FROM" base image, install some packages, files, run your ansible playbook, whatever)
1. Push your layered image somewhere where it can be pulled
1. Override `osImageURL` in a `MachineConfig` with that image for your desired pool
1. Wait for the MCO to roll it out
1. **You are now responsible for keeping that image up to date for that pool** :smile:

Currently this process is somewhat unwieldy and very self-service, but as we progress through the phases of [OCP CoreOS Layering](https://github.com/openshift/enhancements/blob/master/enhancements/ocp-coreos-layering/ocp-coreos-layering.md#phase-1), this should get more robust and easier to use.

## Example

### 1. Get Your Base Image

Nothing will stop you at this point from using a completely arbitrary image, but if you want to succeed, you should derive your layered image from the base image matching your cluster.

> NOTE: at some point we intend these images to be published to a predictable place for you to easily retrieve, but for now you will want to pull the image out of the release payload matching your cluster

#### On an existing cluster

```bash
oc adm release info --image-for rhel-coreos-8
```

#### Or before you build your cluster

```bash
oc adm release info --image-for rhel-coreos-8 quay.io/openshift-release-dev/ocp-release:your_release_here
```

### 2. "Layer" Some Content On Top Of It

#### Generic Examples

There are a lot of things you can do here, I suggest using the [Layering Examples](https://github.com/coreos/coreos-layering-examples) as a guideline for now, but they were written for Fedora, and obviously you'll need to use RPM packages that match your OS and version (RHCOS, SCOS, etc).

#### Entitled Packages

For RHCOS, using an entitled RHEL host is the easy way to go, podman should pass the entitlements through and it should just work:

```dockerfile
FROM quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:69cf9251c36c4df35cd676657c7fc1cb7706a4dccdf5dab4743115e2cac48f3b
RUN rpm-ostree install libreswan && \
    rpm-ostree cleanup -m && \
    ostree container commit && rm /etc/yum.repos.d/redhat.repo
```

> WARNING: take note of the removal of the redhat.repo file. Its presence is a side-effect of the entitled build. If you leave it there, rpm-ostree will try to use it later and fail because the certificates that enable its use will no longer be present after the build.

> NOTE: For now, the standard `rpm-ostree` rules apply, so if you install packages that do things like [install into /opt/](https://github.com/coreos/rpm-ostree/issues/233)  you might not be completely successful.

> NOTE: `MachineConfig` takes precedence over config files included in a derived image. Conflicts will not break anything, but right now `MachineConfig` always wins and will just overwrite the change.

#### "I Just Want To See It Work"

We recommend against using CentOS stream packages in lieu of entitled packages for RHCOS, but if you're just playing with it and want to see it work, you *could* do something totally unsupported like this:

```dockerfile
FROM quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:ffa3568233298408421ff7da60e5c594fb63b2551c6ab53843eb51c8cf6838ba
RUN rpm-ostree install http://mirror.centos.org/centos/8-stream/AppStream/x86_64/os/Packages/ltrace-0.7.91-28.el8.x86_64.rpm && rpm-ostree cleanup -m
```

> WARNING: Take care that you run your cleanup as part of your installation command so the temp files/caches don't end up in your final image. If you need to do it in multiple steps, you can run your podman build with `--squash` to prevent the inclusion of the intermediate layers.

#### Build It

```bash
podman build -t localhost/layered-test-1 .
```

### 3. Push Your Image

The image obviously needs to be pushed somewhere where the MCO can pull it. For now, `rpm-ostree` uses the cluster global pull secret to pull the images, so just make sure anything required is set up there.

`podman push localhost/layered-test-1 quay.io/jkyros/derived-images:layered-test-1`

### 4. Override OSImageURL in MachineConfig

`OSImageURL` can be overridden per-pool. In this example we're overridng `OSImageURL` for the `worker` pool.

You can also create a [custom pool](https://github.com/openshift/machine-config-operator/blob/master/docs/custom-pools.md#creating-a-custom-pool) and try it there.

```yaml
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-override-image-worker
spec:
  osImageURL: "quay.io/jkyros/derived-images@sha256:d0a75edb1e24b52a2c7ae68daeb1a4a63e50fe22bf7df65122debeb8c29bd01a"
```

( In this example,  `quay.io/jkyros/derived-images@sha256:d0a75ed...` is the digest of `quay.io/jkyros/derived-images:layered-test-1"`)

Once you apply this `MachineConfig`, the MCO will do the rest, and your nodes will eventually start to rebase and reboot into it.

> WARNING: It is HIGHLY RECOMMENDED that you specify an `osImageURL` that is an image digest (`foo@sha256:1234...`) rather than an image tag (`foo:latest`), otherwise your nodes may end up on different images.
>
> The MCO *will* let you use a tag as an `osImageURL`, but each Node's `machine-config-daemon` pulls the image for itself -- which means that if the image the tag points at were to change after the first node were updated, other nodes could potentially pull a different image depending on when they pull it.

### 5. Wait for the MCO to apply it (just like any other `MachineConfig` change)

```bash
NAME     CONFIG                                             UPDATED   UPDATING   DEGRADED   MACHINECOUNT   READYMACHINECOUNT   UPDATEDMACHINECOUNT   DEGRADEDMACHINECOUNT   AGE
master   rendered-master-a513edaf3843642a0d503dd99848cc98   True      False      False      3              3                   3                     0                      26d
worker   rendered-worker-02182a0cdb7f8379a63e05cd77371cd5   False     True       False      3              2                   3                     0                      26d
```

> WARNING: Downgrading to "older" images will work in some cases, but we mostly test going forwards not backwards. If you go too far back you can end up with weird happenings like [this bug](https://issues.redhat.com/browse/OCPBUGS-1035)  when something like the kubelet gets backrev'd too far.

### 6. You are now responsible for keeping that image up to date

If you upgrade your cluster while `OSImageURL` is overridden, the MCO will prefer the "overridden" `OSImageURL` image over any image supplied with the upgrade payload, and the base OS will *not* be upgraded as part of the upgrade *for any pool where you have overridden `OSImageURL`*. Should you choose to upgrade your cluster, you will need to rebuild and apply a new custom layered image following the upgrade for any pool where you are using layering.

> WARNING: once you apply a custom image, you are effectively "taking the wheel" when it comes to managing the OS image during upgrades. You are responsible for making sure that custom layered image gets updated!

## FAQ

### Can I override OSImageURL at install time?

Yes, you can. If you supply a `MachineConfig` containing an overridden `OSImageURL` at install time, the cluster will build and use it.

> NOTE: This *will not* affect the bootimage, so if there is some hardware support or something you need for initial boot, this method will not help you (but we intend to deal with this at some point)

### If I upgrade my cluster while I'm overriding `OSImageURL`, will it upgrade my image?

It will for any pool that is using the default `OSImageURL` (e.g. any pool where you aren't using layering)

In pools where you are overriding `OSImageURL` (e.g. you are using layering), that image is 100% your responsibility as long as it's overridden.

At some point we'd like to give you a way to supply an upgrade image or have one built in advance of an upgrade, but we're not there yet.

For now we recommend that after you upgrade your cluster, you rebuild your custom layered image FROM the base OS/`rhel-coreos-8` that your cluster was upgraded to and apply it to your cluster. (See [Get Your Base Image](#1-get-your-base-image) )

### Can I go back once I've used layering?

Yes. If you remove your override (e.g. delete the `MachineConfig` containing your `OSImageURL`) the MCO will move back to the image that came with the release your cluster is on, and the cluster will take over managing upgrades for that pool again.

```bash
oc delete mc 99-override-image-worker
```

### Can I build a layered image in-cluster?

Technically yes, but probably not as seamlessly as you want. We need to do some work getting the MCO and the internal registry to seamlessly trust each other: [OCPBUGS-988](https://issues.redhat.com/browse/OCPBUGS-988)

### What if there are conflicts between the files in the custom image and the files in `MachineConfig`?

For now, *`MachineConfig` always wins*.

Currently `MachineConfig` gets written "on top" of the image as "loose files", so it will overwrite any user overrides/changes included in the image. In the future we want to get the `MachineConfig` files into the image also so all we apply is the image, but we aren't there yet.
