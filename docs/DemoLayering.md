# Trying out OCP CoreOS Layering

Launch (or upgrade a cluster to) `registry.ci.openshift.org/coreos/release-layering`. If installing extensions is desired, a cluster must be launched with a pull secret that is associated with entitlements (which is not true of cluster-bot).


# Building an image outside (or inside) the target cluster

A toplevel pattern we want to support is using standard container build tooling to create custom RHEL CoreOS images that can apply to multiple clusters.

Build a container image using any tooling you like that is `FROM registry.ci.openshift.org/rhcos-devel/rhel-coreos:4.11`.  There are a growing number of examples in [coreos/coreos-layering-examples](https://github.com/coreos/coreos-layering-examples).  (Be sure to check out e.g. [update to rhcos branch](https://github.com/coreos/coreos-layering-examples/pull/15))

Note that this is a non-production location; the future location would likely look more like `registry.redhat.io/rhel-coreos:4.11`.

One strawman proposal is `oc tag --source=docker quay.io/examplecorp/rhel-coreos:4.11 machine-config-operator/coreos-external:latest`.

# Enabling Layered Mode


## Create `layered` custom pool
```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfigPool
metadata:
  name: layered
spec:
  machineConfigSelector:
    matchExpressions:
      - {key: machineconfiguration.openshift.io/role, operator: In, values: [worker,layered]}
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/layered: ""
```

## Alternative: label your worker pool
`$ oc label machineconfigpool worker machineconfiguration.openshift.io/layered=""`

# Seeing it work




## Observe imagestreams created

```
$ oc -n openshift-machine-config-operator get is
NAME                          IMAGE REPOSITORY                                                                                                 TAGS     UPDATED
coreos                        image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/coreos                        latest   About a minute ago
worker-mco-content            image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/worker-mco-content                     
worker-mco-content-custom     image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/worker-mco-content-custom              
worker-mco-content-external   image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/worker-mco-content-external            
worker-rendered-config        image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/worker-rendered-config                 
$
```

## Observe buildconfigs created
```
$ oc -n openshift-machine-config-operator get buildconfigs
NAME                        TYPE     FROM         LATEST
mco-build-content-layered   Docker   Dockerfile   1
``` 


## Observe buidconfig dockerfile contents
```
$ oc -n openshift-machine-config-operator describe bc mco-build-content-layered 
Name:		mco-build-content-layered
Namespace:	openshift-machine-config-operator
Created:	2 hours ago
Labels:		<none>
Annotations:	machineconfiguration.openshift.io/pool=layered
Latest Version:	6

Strategy:	Docker
Dockerfile:
  
  	# Multistage build, we need to grab the files from our config imagestream 
  	FROM image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-rendered-config AS machineconfig
  	
  	# We're actually basing on the "new format" image from the coreos base image stream 
  	FROM image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/coreos
  
  	# Pull in the files from our machineconfig stage 
  	COPY --from=machineconfig /machine-config-ignition.json /etc/machine-config-ignition.json
  
  	# Apply the config to the host 
  	ENV container=1
  	RUN exec -a ignition-apply /usr/lib/dracut/modules.d/30ignition/ignition --ignore-unsupported /etc/machine-config-ignition.json
 ... 
```

## Observe builds

```
$ oc -n openshift-machine-config-operator get builds
NAME                          TYPE     FROM         STATUS                       STARTED          DURATION
mco-build-content-layered-1   Docker   Dockerfile   Failed (DockerBuildFailed)   10 minutes ago   1m30s
mco-build-content-layered-2   Docker   Dockerfile   Running                      36 seconds ago   
```

## Observe the build building
```
Pulling image image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-rendered-config
...
[1/2] STEP 1/2: FROM image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-rendered-config AS machineconfig
[1/2] STEP 2/2: ENV "BUILD_LOGLEVEL"="3"
[2/2] STEP 1/13: FROM image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/coreos
[2/2] STEP 2/13: ENV "BUILD_LOGLEVEL"="3"
[2/2] STEP 3/13: COPY --from=machineconfig /machine-config-ignition.json /etc/machine-config-ignition.json
[2/2] STEP 4/13: ENV container=1
[2/2] STEP 5/13: RUN exec -a ignition-apply /usr/lib/dracut/modules.d/30ignition/ignition --ignore-unsupported /etc/machine-config-ignition.json
INFO     : Ignition 2.13.0
INFO     : createFilesystemsFiles: createFiles: op(1): [started]  writing file "/etc/mco-butane.yaml"
INFO     : createFilesystemsFiles: createFiles: op(1): [finished] writing file "/etc/mco-butane.yaml"
INFO     : createFilesystemsFiles: createFiles: op(2): [started]  writing file "/etc/node-sizing-enabled.env"
INFO     : createFilesystemsFiles: createFiles: op(2): [finished] writing file "/etc/node-sizing-enabled.env"
...
```

## Observe that the imagestream is updated when the build completes
```
$ oc -n openshift-machine-config-operator get imagestream layered-mco-content
NAME                  IMAGE REPOSITORY                                                                                         TAGS     UPDATED
layered-mco-content   image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content   latest   About a minute ago
```

## Label a node into the custom pool (so there is something to receive the image)
```
$ oc label node jkyros-ux-demo-1-mkrnz-worker-b-sb9lp node-role.kubernetes.io/layered=""
```

## Observe the node's machine-config-daemon applying the image
```
$ oc -n openshift-machine-config-operator logs -f machine-config-daemon-9xn5j
...
I0414 03:50:35.125238    2140 rpm-ostree.go:329] Executing rebase to image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:6b90120c685998e07ac2eee13667c482a2204b8c260a7689a895e1289c88cf83
I0414 03:50:35.125273    2140 update.go:1883] Running: rpm-ostree rebase --experimental ostree-unverified-registry:image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:6b90120c685998e07ac2eee13667c482a2204b8c260a7689a895e1289c88cf83
Pulling manifest: ostree-unverified-image:docker://image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:6b90120c685998e07ac2eee13667c482a2204b8c260a7689a895e1289c88cf83
Importing: ostree-unverified-image:docker://image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:6b90120c685998e07ac2eee13667c482a2204b8c260a7689a895e1289c88cf83 (digest: sha256:6b90120c685998e07ac2eee13667c482a2204b8c260a7689a895e1289c88cf83)
Using base: sha256:48417bcc69239883efe34133c3aab1a1022529c855f181b047bccb9fb92c7231
Downloading layer: sha256:d955abb07b342d62b67c08de750f10eabae443c742ee968827d5e46761a0c343 (35.2 MB)
Staging deployment...done
...
```



# Try creating a test machineconfig

Create a MachineConfig object that applies to the `layered` pool.


## Extension
```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: layered
  name: usbguard
spec:
  extensions:
    - usbguard
```
Result:
```
$ oc debug node/jkyros-ux-demo-d2nkb-worker-b-k4k7r -- chroot /host usbguard -help

 Usage: usbguard [OPTIONS] <command> [COMMAND OPTIONS] ...

 Options:

 Commands:
  get-parameter <name>           Get the value of a runtime parameter.
  set-parameter <name> <value>   Set the value of a runtime parameter.
  list-devices                   List all USB devices recognized by the USBGuard daemon.
  allow-device <id|rule|p-rule>  Authorize a device to interact with the system.
  block-device <id|rule|p-rule>  Deauthorize a device.
  reject-device <id|rule|p-rule> Deauthorize and remove a device from the system.

  list-rules                     List the rule set (policy) used by the USBGuard daemon.
  append-rule <rule>             Append a rule to the rule set.
  remove-rule <id>               Remove a rule from the rule set.

  generate-policy                Generate a rule set (policy) based on the connected USB devices.
  watch                          Watch for IPC interface events and print them to stdout.
  read-descriptor                Read a USB descriptor from a file and print it in human-readable form.

  add-user <name>                Add USBGuard IPC user/group (requires root privilges)
  remove-user <name>             Remove USBGuard IPC user/group (requires root privileges)

command terminated with exit code 1
```

## Live Apply (does not cause reboot)
```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: layered
  name: live-apply-file
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
        - contents:
            source: data:text/plain;charset=utf-8;base64,V29vaG9vIGxpdmUgYXBwbHkgd29ya3MhCg==
          path: /etc/machine-config-daemon/no-reboot/containers-gpg.pub
```
Result:
```
...
I0414 05:28:23.193075    1769 update.go:1885] Running: rpm-ostree rebase --experimental ostree-unverified-registry:image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:0cec9e466aec364fb0892f23d106425386e07e9d624ccf8a7ad26da1a77b7d20
Pulling manifest: ostree-unverified-image:docker://image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:0cec9e466aec364fb0892f23d106425386e07e9d624ccf8a7ad26da1a77b7d20
Importing: ostree-unverified-image:docker://image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:0cec9e466aec364fb0892f23d106425386e07e9d624ccf8a7ad26da1a77b7d20 (digest: sha256:0cec9e466aec364fb0892f23d106425386e07e9d624ccf8a7ad26da1a77b7d20)
Using base: sha256:540bacb13843e1049ac2337ef8e9acee6ef690f3b125ade430f05f7b6d0df082
Downloading layer: sha256:d7c0b8e1b2326f68c7fb553f131c93d27c03f256600b120a7897815aaffe5e46 (394.6 kB)
Staging deployment...done
Freed: 910.1 MB (pkgcache branches: 0)
Changes queued for next boot. Run "systemctl reboot" to start a reboot
I0414 05:28:31.844163    1769 rpm-ostree.go:426] Running captured: rpm-ostree status --json
I0414 05:28:31.900720    1769 rpm-ostree.go:426] Running captured: ostree diff 1d06a416db2d5a919441dc5a5daeff29a28239b5b39e8da634e6ae86dffb1b24 aeb8a74dc1a8525586b750d7edd330d56ad5b0793325720465c019016a96288e
I0414 05:28:32.023204    1769 rpm-ostree.go:426] Running captured: rsync -avh /usr/etc/ /etc/
I0414 05:28:32.615179    1769 drain.go:189] Changes do not require drain, skipping.
I0414 05:28:32.615214    1769 rpm-ostree.go:338] Applying live
I0414 05:28:32.615226    1769 update.go:1885] Running: rpm-ostree ex apply-live --allow-replacement
Computing /etc diff to preserve...done
Updating /usr...done
Updating /etc...done
Running systemd-tmpfiles for /run and /var...done
Successfully updated running filesystem tree.
I0414 05:28:34.236051    1769 update.go:1885] Running: systemctl reload crio
I0414 05:28:34.252291    1769 update.go:1900] crio config reloaded successfully! Desired config image-registry.openshift-image-registry.svc:5000/openshift-machine-config-operator/layered-mco-content@sha256:0cec9e466aec364fb0892f23d106425386e07e9d624ccf8a7ad26da1a77b7d20 has been applied, skipping reboot
...
```

## File 
```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: layered
  name: file
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
      - contents:
          source: data:text/plain;charset=utf-8;base64,SSB3YXMgY3JlYXRlZCBieSBhIE1hY2hpbmVDb25maWc=
        path: /etc/test-file
```

## Kernel Arguments 
```
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: layered
  name: kargs
spec:
  kernelArguments:
    - 'hugepagesz=4M'
```
Result:
```
$ mcd_exec worker 0 cat /rootfs/proc/cmdline
BOOT_IMAGE=(hd0,gpt3)/ostree/rhcos-b5b854bdb9791bf0c59d606dec288584fbcc276e1e13fc1826b93a8c4227ddb3/vmlinuz-4.18.0-369.el8.x86_64 random.trust_cpu=on console=tty0 console=ttyS0,115200n8 ostree=/ostree/boot.0/rhcos/b5b854bdb9791bf0c59d606dec288584fbcc276e1e13fc1826b93a8c4227ddb3/0 ignition.platform.id=aws root=UUID=8face9b9-35b8-4dc9-bea3-b319f94f0f35 rw rootflags=prjquota boot=UUID=13b85e7b-f56f-4a21-bf13-b7718a0c1333 hugepagesz=4M
```

# Try using a custom base image

TODO: In the future we will support replacing the CoreOS base image.

## Tag an image into the external imagestream

## Edit the buildconfig 
