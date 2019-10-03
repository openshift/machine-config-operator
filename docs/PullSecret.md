# Changing the payload pull secret

The pull secret is used to pull the release payload image and it's currently stored as a secret in openshift-config/pull-secret.
The ControllerConfig has an ObjectRefrence pointing to said secret and the Operator sets this up during its sync.
The reference is then used by the TemplateController to grab the actual pull secret and generate the templates for MachineConfigs using it.
That results in the pull secret being laid down *on every node*, therefore nodes are now be able to pull the release payload image.

The pull secret has an expiration, and when it expires you need to change it as follows:

```
oc edit secrets pull-secret -n openshift-config
```

Changing the `.dockerconfigjson` in the secret and saving it will result in the MCO syncing the new pull secret with the templates which then
result in the new pull secret being laid down on nodes.

Note that the `.dockerconfigjson` field is a base64 encoded JSON.

The pull secret in `openshift-config` is a cluster wide pull secret. Changing it will cause every node in every pool, including the `master` pool to reboot.
