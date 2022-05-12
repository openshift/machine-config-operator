# BuildController

## This makes a bunch of imagestreams, what are they for?

|imagestream| intended purpose|
|---|---|
|`coreos`                            | The global default base coreos image (rhel-coreos) |
|`layered-base`                    |   The base image stream our buildconfigs draw from (defaults to coreos:latest) |
|`layered-external-base`           |External base image stream. If :latest tag is populated, layered-base uses this instead of coreos:latest|  
|`layered-mco-content`        | This is the image built by our mco dockerfile where ignition-apply runs |
|`layered-mco-content-custom`   |This gets populated if the user users the custom docker buildconfig to add content on top of mco-content. The pool will prefer this over mco-content if populated. |
|`layered-mco-content-external` |*HIGHLY EXPERIMENTAL* If you manage to find a way to build a sane image containing mco content outside the cluster, tag it in here, it will use it instead of what we built internally|

## Additional Implementation Details

|imagestream| intended purpose|
|---|---|
|`layered-rendered-config`     |This is the imagestream where the tiny scratch images containing machineconfig are pushed so we can use them to build mco-content images|

## And why are there two buildconfigs ?

|buildconfig| intended purpose|
|---|---|
|`layered-build-mco-content`| Dual-stage build where the MCO builds its content image with machineconfig| 
|`layered-build-mco-content-custom`| An empty buildconfig that a user could use to further derive from our mco-content image


## How do I tag in either a base or an external image?

Tagging in a base image instead of default coreos:
```
$ oc tag quay.io/jkyros/derived-images:rhel-coreos openshift-machine-config-operator/layered-external-base:latest
```

Tagging in an external content image (this is probably not useful without an external build system):
```
$ oc tag quay.io/jkyros/derived-images:some-super-secret-image-here openshift-machine-config-operator/layered-mco-content-external:latest
```

## Bugs I know this has:

- Extra builds on first setup. It will build once when $pool-base gets tagged in, and it will try to build again once rendered-config gets updated. Sometimes rendered-config isn't ready yet and the first build fails, sometimes it is and we build both. (It's moot because it's the same content and should have the same SHA someday when everything is repeatable, but it's noise and waste)

