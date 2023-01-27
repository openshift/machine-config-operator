# Virtual Machine based tests

## Requirements

The following tools need to be installed on the host machine:
- yq: https://kislyuk.github.io/yq/
- kcli: https://github.com/karmab/kcli
- libvirt: https://libvirt.org/


## Run tests

The following command cna be used to run all test in `configure-ovs-test.bats` suite

```sh
make runtest
```

To run a subset of tests, run:

```sh
WHAT="Bonding NICs" make runtest
```

## Run a sample VM

To inspect virtual machines manually, run the following `kcli` commands:

```sh
kcli create plan -f plans/single-nic.yml
kcli ssh vm3

# and when you finished
kcli delete vm vm3
```
