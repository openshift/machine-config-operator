# Ignition Support in MachineConfigs

`MachineConfig` objects are the defacto way of customizing the nodes in an
OpenShift cluster. Customizations can be applied "day 1" during the install of
the cluster or "day 2" after the cluster has been installed.

The way the customizations are defined are mostly through the use of the
Ignition specification. However, not all of the specification is supported.
This attempts to codify which parts of the [Ignition 3.2.0 specification](https://github.com/coreos/ignition/blob/master/docs/configuration-v3_2.md)
are supported and provide examples for **day 2** customizations.

---
**WARNING** These examples are valid as of OpenShift 4.7; they are not guaranteed to
work with older or newer versions of OpenShift.  It is recommended that users
reference the official [OpenShift documentation](https://docs.openshift.com/container-platform/4.7/post_installation_configuration/machine-configuration-tasks.html)
for up to date information about how to use `MachineConfig` objects.

---

## Ignition

The `ignition` object contains metadata about the Ignition configuration. When
used in the scope of the `MachineConfig` object, the `version` key should be
provided at a minimum.

```yaml
# This MachineConfig doesn't actually do anything
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-version
spec:
  config:
    ignition:
      version: 3.2.0
```

Specifying any other objects under the top-level `ignition` object in a
MachineConfig is **NOT** supported.

## Storage

The top-level `storage` object of the Ignition specification drives how disks,
filesystems, and files are defined on the host. When using a MachineConfig for
"day 2" customizations, **ONLY** the `files` object is supported.

To define a file that will be written to the node, users **MUST** minimally
supply `contents` and `path` for the file.

The `contents` of the file **MUST** be specified using the `data:` format. (See
<https://tools.ietf.org/html/rfc2397>)

```yaml
# This MachineConfig demonstrates the minimal requirements for writing out a
# file to the node
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-files-minimal
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
        - path: /var/srv/hello-world.txt
          contents:
            source: data:text/plain;charset=utf-8;base64,aGVsbG8gd29ybGQK
```

Users can specify that the contents of the file should overwrite an existing
file.

```yaml
# This MachineConfig demonstrates overwriting an existing file
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-files-overwrite
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
        - path: /var/srv/hello-world.txt
          contents:
            source: data:text/plain;charset=utf-8;base64,Z29vZGJ5ZSB3b3JsZAo=          
          overwrite: true
```

However, the `append` boolean for files is **NOT** supported at this time.

Users can configure the `mode`, `user`, and `group` ownership as well. The mode
can be specified as octal (`0644`) or as decimal (`420`). Users and groups can
be specified by their IDs or by their names.

```yaml
# This MachineConfig demonstrates configuring file attributes
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-files-attributes
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
        - path: /var/srv/owned-by-root.txt
          contents:
            source: data:text/plain;charset=utf-8;base64,b3duZWQgYnkgcm9vdAo=
          mode: 0644
          user:
            id: 0
          group:
            name: root
```

Optionally, users can provide the `verification` object as part of the file
definition, which will force the node to verify the contents of the file before
it is written to disk. The current supported verification scheme is sha512.

```yaml
# This MachineConfig demonstrates using the verification ability
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-files-verify
spec:
  config:
    ignition:
      version: 3.2.0
    storage:
      files:
        - path: /var/srv/owned-by-core.txt
          contents:
            source: data:text/plain;charset=utf-8;base64,b3duZWQgYnkgY29yZQo=
            verification:
              hash: sha512-5ee67ed4a5d2f4b9fc0039bca54deaf293e7ada3298fee84557761b54324ebcd0979e2753921a3fc9a0ffb2f0b4468487d253a3cae07c12b2051f599dc6819e8
          mode: 420
          user:
            name: core
          group:
            id: 1000
```

## Systemd

Users may want to affect change on their nodes through the use of a custom
script or set of commands. Since customization of nodes interactively is strongly
discouraged, this means users have to use `systemd` units to run commands or scripts.  
The Ignition specification treats `systemd` units as top-level objects.

In the simplest form, a `MachineConfig` can be used to disable (or enable) a pre-existing
service on the node.

```yaml
# This MachineConfig disables the sshd.service
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-sshd-disable
spec:
  config:
    ignition:
      version: 3.2.0
    systemd:
      units:
        - name: sshd.service
          enabled: false
```

Or a service can be masked completely.

```yaml
# This MachineConfig masks the sshd.service
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-sshd-masked
spec:
  config:
    ignition:
      version: 3.2.0
    systemd:
      units:
        - name: sshd.service
          masked: true
```

Users can also customize existing `systemd` units with dropins.

```yaml
# This MachineConfig changes the EnvironmentFile location for the
# sshd.service
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-sshd-change-env-file
spec:
  config:
    ignition:
      version: 3.2.0
    systemd:
      units:
        - name: sshd.service
          dropins:
            - name: 10-change-env-file.conf
              contents: |
                [Service]
                EnvironmentFile=
                EnvironmentFile=-/var/sysconfig/sshd
```

Users could create their own service to run a set of commands.

```yaml
# This MachineConfig defines a systemd service that runs `ping`
# multiple times
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-simple-ping-service
spec:
  config:
    ignition:
      version: 3.2.0
    systemd:
      units:
        - name: simple-ping.service
          enabled: true
          contents: |
            [Unit]
            Description=Ping some IP addresses
            After=network-online.target
            Wants=network-online.target

            [Service]
            Type=oneshot
            ExecStart=-ping -c 3 127.0.0.1
            ExecStart=-ping -c 3 1.1.1.1
            ExecStart=-ping -c 3 8.8.8.8

            [Install]
            WantedBy=multi-user.target
```

Alteratively, users can define their own service that runs a script.
This requires a MachineConfig that combines the use of the `files:`
object to write out the script and the `systemd` object to configure
the service.

```yaml
# This MachineConfig defines a systemd service that runs a script
# which just runs `ping` multiple times.
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-ping-script-service
spec:
  config:
    ignition:
      version: 3.2.0
    stroage:
      files:
          - path: /usr/local/bin/ping.sh
            contents:
              source: data:text/plain;charset=utf-8;base64,IyEvdXNyL2Jpbi9iYXNoCnBpbmcgLWMgMyAxLjEuMS4xCnBpbmcgLWMgMyA4LjguOC44CnBpbmcgLWMgMyAxMjcuMC4wLjEK
            user:
              name: root
            mode: 0744
    systemd:
      units:
        - name: ping-script.service
          enabled: true
          contents: |
            [Unit]
            Description=Run the ping.sh script
            After=network-online.target
            Wants=network-online.target

            [Service]
            Type=oneshot
            ExecStart=-/usr/local/bin/ping.sh

            [Install]
            WantedBy=multi-user.target
```

## Passwd

The `passwd` object is the last top-level object from the Ignition
specification that can be used in `MachineConfigs`.  However, support
is limited to just updating the authorized SSH keys for the `core` user.

```yaml
# This MachineConfig adds a new authorized SSH key for the core user
apiVersion: machineconfiguration.openshift.io/v1
kind: MachineConfig
metadata:
  labels:
    machineconfiguration.openshift.io/role: worker
  name: 99-ignition-passwd-core-sshkey
spec:
  config:
    ignition:
      version: 3.2.0
    passwd:
      users:
        - name: core
          sshAuthorizedKeys:
            - ssh-rsa AAAAB3NzaC1y....
```
