# runtimeassets

The intent behind this package is to provide a place for assets that the MCD
must render at runtime. Generally speaking, these are used to work around edge
cases where it may not be desirable to have a config file or systemd unit
rendered as part of the normal MachineConfig flow.

These could be static files or they could be runtime-rendered templates based
upon the status of the MCD and/or objects the MCD can access, such as
ControllerConfig.

The RuntimeAsset interface specifies that any additional types or files added
to this package should be rendered as Ignition. This will allow the MCD to
write the files to the nodes' filesystem using the standard Ignition file
writing paths that currently exist.

In the future, items in this package could be expanded and used in the
certificate-writer path as well as other similar paths.

## RevertService

This is used for reverting from a layeed MachineConfigPool to a non-layered
MachineConfigPool. This is because the systemd unit that performs this function
should not be part of the default MachineConfig. Instead, it should be rendered
and applied on an as-needed basis.
