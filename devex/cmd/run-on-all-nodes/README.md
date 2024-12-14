# run-on-all-nodes

```console
Automates running a command on all nodes in a given OpenShift cluster

Usage:
  run-on-all-nodes [flags] [command]
  run-on-all-nodes [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  version     Print the current version

Flags:
      --exit-zero                      Return zero even if a command fails
  -h, --help                           help for run-on-all-nodes
      --json                           Write output in JSON format
      --keep-going                     Do not stop on first command error
      --kubeconfig string              Paths to a kubeconfig. Only required if out-of-cluster.
      --label-selector string          Label selector for nodes.
      --log-flush-frequency duration   Maximum number of seconds between log flushes (default 5s)
  -v, --v Level                        number for the log level verbosity
      --vmodule moduleSpec             comma-separated list of pattern=N settings for file-filtered logging (only works for the default text log format)
      --write-logs                     Write command logs to disk under $PWD/<nodename>.log

Use "run-on-all-nodes [command] --help" for more information about a command.
```

This command allows one to run a command across all (or a subset) of their cluster nodes.

## How to use

Let's say that you want to run `$ rpm-ostree status` on all of your cluster nodes:

```console
$ run-on-all-nodes 'rpm-ostree status'
Running on nodes: [ip-10-0-11-145.ec2.internal ip-10-0-16-30.ec2.internal ip-10-0-34-4.ec2.internal ip-10-0-59-143.ec2.internal ip-10-0-59-75.ec2.internal ip-10-0-6-62.ec2.internal]

[ip-10-0-59-143.ec2.internal - [node-role.kubernetes.io/worker]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T13:20:23Z)


[ip-10-0-59-75.ec2.internal - [node-role.kubernetes.io/worker]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T13:20:13Z)


[ip-10-0-11-145.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T13:12:20Z)


[ip-10-0-16-30.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T13:12:34Z)


[ip-10-0-6-62.ec2.internal - [node-role.kubernetes.io/worker]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T15:46:41Z)


[ip-10-0-34-4.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T13:12:21Z)
```

Now, let's say that you only want to run it on your worker nodes. You can add the node-role label selector thusly:

```console
$ run-on-all-nodes --label-selector 'node-role.kubernetes.io/worker=' 'rpm-ostree status'

Using label selector: node-role.kubernetes.io/worker=
Running on nodes: [ip-10-0-59-143.ec2.internal ip-10-0-59-75.ec2.internal ip-10-0-6-62.ec2.internal]

[ip-10-0-59-143.ec2.internal - [node-role.kubernetes.io/worker]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T13:20:23Z)


[ip-10-0-59-75.ec2.internal - [node-role.kubernetes.io/worker]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T13:20:13Z)


[ip-10-0-6-62.ec2.internal - [node-role.kubernetes.io/worker]]:
$ rpm-ostree status
State: idle
Deployments:
* ostree-unverified-registry:registry.ci.openshift.org/ocp/4.16-2024-02-13-072746@sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                   Digest: sha256:6fb6e4d6d6e813ac88ad56f2b822cf28bfa4cf97ec8744df8301fdb817485636
                  Version: 416.94.202402060821-0 (2024-02-13T15:46:41Z)
```

The program will halt on the first error it encounters while running commands. For example, if you attempt to run an unknown command:

```console
$ run-on-all-nodes 'unknown-command'
E0213 11:41:15.559442    8081 run.go:74] "command failed" err="could not run command /Users/zzlotnik/bin/oc debug node/ip-10-0-16-30.ec2.internal -- chroot /host /bin/bash -c unknown-command: exit status 1"
```

To keep executing, use the `--keep-going` flag:

```console
$ run-on-all-nodes --keep-going 'unknown-command'
Running on nodes: [ip-10-0-11-145.ec2.internal ip-10-0-16-30.ec2.internal ip-10-0-34-4.ec2.internal ip-10-0-59-143.ec2.internal ip-10-0-59-75.ec2.internal ip-10-0-6-62.ec2.internal]

[ip-10-0-11-145.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ unknown-command
/bin/bash: line 1: unknown-command: command not found


[ip-10-0-6-62.ec2.internal - [node-role.kubernetes.io/worker]]:
$ unknown-command
/bin/bash: line 1: unknown-command: command not found


[ip-10-0-16-30.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ unknown-command
/bin/bash: line 1: unknown-command: command not found


[ip-10-0-59-143.ec2.internal - [node-role.kubernetes.io/worker]]:
$ unknown-command
/bin/bash: line 1: unknown-command: command not found


[ip-10-0-34-4.ec2.internal - [node-role.kubernetes.io/master node-role.kubernetes.io/control-plane]]:
$ unknown-command
/bin/bash: line 1: unknown-command: command not found


[ip-10-0-59-75.ec2.internal - [node-role.kubernetes.io/worker]]:
$ unknown-command
/bin/bash: line 1: unknown-command: command not found
```

To capture output from each node, use the `--write-logs` flag:

```console
$ run-on-all-nodes --write-logs 'uptime'
Running on nodes: [ip-10-0-11-145.ec2.internal ip-10-0-16-30.ec2.internal ip-10-0-34-4.ec2.internal ip-10-0-59-143.ec2.internal ip-10-0-59-75.ec2.internal ip-10-0-6-62.ec2.internal]

[ip-10-0-16-30.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ uptime
 17:14:37 up  1:18,  0 users,  load average: 6.99, 6.75, 6.71

Writing log to ip-10-0-16-30.ec2.internal.log

[ip-10-0-11-145.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ uptime
 17:14:37 up  1:23,  0 users,  load average: 7.02, 6.92, 7.07

Writing log to ip-10-0-11-145.ec2.internal.log

[ip-10-0-59-75.ec2.internal - [node-role.kubernetes.io/worker]]:
$ uptime
 17:14:37 up  1:36,  0 users,  load average: 6.21, 5.46, 5.26

Writing log to ip-10-0-59-75.ec2.internal.log

[ip-10-0-34-4.ec2.internal - [node-role.kubernetes.io/control-plane node-role.kubernetes.io/master]]:
$ uptime
 17:14:37 up  1:29,  0 users,  load average: 9.38, 7.76, 7.76

Writing log to ip-10-0-34-4.ec2.internal.log

[ip-10-0-6-62.ec2.internal - [node-role.kubernetes.io/worker]]:
$ uptime
 17:14:37 up  1:27,  0 users,  load average: 4.56, 4.28, 4.25

Writing log to ip-10-0-6-62.ec2.internal.log

[ip-10-0-59-143.ec2.internal - [node-role.kubernetes.io/worker]]:
$ uptime
 17:14:37 up  1:32,  0 users,  load average: 4.87, 5.05, 5.00

Writing log to ip-10-0-59-143.ec2.internal.log
```

The logs will be written to the current directory:

```console
$ ls -la *.log
-rwxr-xr-x@ 1 zzlotnik  staff  62 Feb 13 12:14 ip-10-0-11-145.ec2.internal.log
-rwxr-xr-x@ 1 zzlotnik  staff  62 Feb 13 12:14 ip-10-0-16-30.ec2.internal.log
-rwxr-xr-x@ 1 zzlotnik  staff  62 Feb 13 12:14 ip-10-0-34-4.ec2.internal.log
-rwxr-xr-x@ 1 zzlotnik  staff  62 Feb 13 12:14 ip-10-0-59-143.ec2.internal.log
-rwxr-xr-x@ 1 zzlotnik  staff  62 Feb 13 12:14 ip-10-0-59-75.ec2.internal.log
-rwxr-xr-x@ 1 zzlotnik  staff  62 Feb 13 12:14 ip-10-0-6-62.ec2.internal.log

$ cat ip-10-0-11-145.ec2.internal.log
 17:14:37 up  1:23,  0 users,  load average: 7.02, 6.92, 7.07
```

## How does it work?

This program shells out to the `oc` binary and uses the `oc debug` command. In
order to set up a suitable environment to run the command, we pass `chroot
/host /bin/bash -c "<command>"` which ensures that we can use all of the
binaries available on the host.

For speed, we spawn multiple concurrent instances of `oc debug` and wait for
them to complete. Care is taken to ensure that output from each command is kept
separate so there will be no output interleaving.

## Notes
- If a single command encounters an error, the rest of the commands may not execute. This behavior can be overridden using the `--keep-going` flag.
- The `--exit-zero` flag will cause `run-on-all-nodes` to exit with the exit code `0`, even when an error is encountered. This can be useful for running investigative commands that you know will fail.
