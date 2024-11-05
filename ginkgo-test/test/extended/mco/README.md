# MCO

## Resources

In order to handle generic resources we can use the Resource struct.

There are 2 kinds of resources, the namespaced resources and the cluster scoped resources.

To handle a namespaced resource we use  `NewNamespacedResource`  to construct the Resource and to handle a cluster scoped resource we use `NewResource`.


### Get

To retrieve information from the resource we use the `Get(jsonPath string) (string, error)` method.

Example:

```go
svc := NewNamespacedResource(oc, "service", "openshift-ingress", "router-default")

ip, err := svc.Get("{.spec.clusterIP}")
if err != nil {
        e2e.Logf("Error:\n %s", err)
}

port, err := svc.Get("{spec.ports[0].port}")
if err != nil {
        e2e.Logf("Error:\n %s", err)
}

```


### GetSafe

We can use `GetSafe(jsonPath string, defaltValue string) (error)` too to get information from the resource.

With this method we provide a default value in case of error, and we don't have to handle any errors

Example:

```go
svc := NewNamespacedResource(oc, "service", "openshift-ingress", "router-default")

ip := svc.GetSafe("{.spec.clusterIP}", "")
port := svc.GetSafe("{spec.ports[0].port}", "")
```


### GetOrFail

We can use `GetOrFail(jsonPath string) (string)` too to get information from the resource.

With this method if there is any failure trying to retrieve the jsonpath's return value the test will be automatically failed.

Example:

```go
svc := NewNamespacedResource(oc, "service", "openshift-ingress", "router-default")

ip := svc.GetOrFail("{.spec.clusterIP}")
port := svc.GetOrFail("{spec.ports[0].port}")
```


### Get All Resources

In order to get a list of Resource structs we can use  `NewResourceList` and `NewNamespacedResourceList`. GetAll() method will return a list of Resource structs with all resources.

We can use the SortByTimestamp() method to specify that we want the list sorted by creation timestamp.

Example:

```go
resList = NewResourceList(oc.AsAdmin(), "mc")
resList.SortByTimestamp()
allMcs, err := resList.GetAll()
if err != nil {
	e2e.Logf("Error:\n %s", err)
}
for _, mc := range allMcs {
	// Using get safe method, providing a default value if the value does not exit
	name, _ := mc.Get("{.metadata.name}")
	ignitionVersion, _ := mc.Get("{.spec.config.ignition.version}")
	timeStamp, _ := mc.Get("{.metadata.creationTimestamp}")

	e2e.Logf("[%s] -- Machine config [%s] using ignition version [%s]", timeStamp, name, ignitionVersion)

}
```


### Delete

We can use the `Delete() error` method to delete the resource.

Example:

```go
svc := NewNamespacedResource(oc, "service", "my-test-namespace", "my-svc-name")
err := svc.Delete()
o.Expect(err).NotTo(o.HaveOccurred())
```


### Exist Assertion

We can use the `Exists() (bool)` method to check if a resource exists or not.

In order to execute gomega assertions we can use the `Exist` matcher, like this:

Example:

```go
svc := NewNamespacedResource(oc, "service", "openshift-ingress", "router-default")
o.Expect(svc).Should(Exist())
// or
o.Expect(svc).ShouldNot(Exist())
```


### Eventually/Consistently assertions

Sometimes we need to check that any resource's field will match a certain condition, but not inmedialtly, it will take some time to syncrhonize.

In order to do that, we can use the Eventually/Consistently gomega functionality using the `Poll(jsonPath) func()string` method. `Poll` method returns a function that accepts no parameters and returns a string with a new value of the given field every time it is invoked. This function will be used by the gomega Eventually/Consistently functionality in order to Poll the data and assert its value.

Example:

```go
svc := NewNamespacedResource(oc, "service", "openshift-ingress", "router-default")
o.Eventually(svc.Poll(".spec.clusterIP")).Should(o.Equal("172.30.17.216"))
```

We can use the `Exist` assertion with Eventually/Consistently.

Example:

```go
svc := NewNamespacedResource(oc, "service", "my-test-namespace", "my-svc-name")
// It consistently exists
oc.Consistently(svc).Should(Exist())

err := svc.Delete()
o.Expect(err).NotTo(o.HaveOccurred())

// after deletion it will eventually not exist any more
oc.Eventually(svc).ShouldNot(Exist())

```


### Handling resources with different users

The user interacting with the resource represented by the Resource struct is the one configured in the `exutil.CLI` struct.

We can see in the following example how to use 2 different users (a regular user and the admin user) to handle different resources.

When we create a new Resource, if we want the admin user to handle it, we use NewResource(oc.AsAdmin(),....
When we create a new Resource, if we want a regular user to handle it, we use NewResource(oc,....

Example:

```go
// Create CM resources handled by a regular user
DUregularCM := NewNamespacedResource(oc, "cm", "regular-cm", "regular-namespace")                 // a CM that can be read by a regular user
DUadminCM := NewNamespacedResource(oc, "cm", "admin-only-cm", "only-admin-can-read-namespace")    // a CM that can be read only by admin

// Create CM resources handled by the admin user
AadminCM := NewNamespacedResource(oc.AsAdmin(), "cm", "admin-only-cm", "only-admin-can-read-namespace")      // a CM that can be read only by admin

// VERIFY

// The regular user can see the regular CM
//    but he cannot see the CM in the namespace that can only be read by admin
o.Expect(DUregularCM).To(Exist())
o.Expect(DUadminCM).NotTo(Exist())

// Admin user can see the CM in the namespace that can only be read by admin
o.Expect(AadminCM).To(Exist())
```



## JSONData

Utility struct to handle json data

### JSON

A function that will return a JSONData struct containing the given value

```go
                resource := NewResource(oc.AsAdmin(), "mcp", "worker")
                spec := JSON(resource.GetOrFail("{.spec}"))
```

### Get

Gets a JSONData struct containing the value of the given key. It will fail if the current JSONData object is not a map[string]interface{}

```go
                resource := NewResource(oc.AsAdmin(), "mcp", "worker")
                spec := JSON(resource.GetOrFail("{.spec}"))
                e2e.Logf("machineConfigSelector %v", spec.Get("machineConfigSelector").Get("matchLabels"))
```

### Item

Returns a JSONData struct containing the value of the given index. It will fail if the current JSONData object is not a list []interface{}

```go
                resource := NewResource(oc.AsAdmin(), "mcp", "worker")
                spec := JSON(resource.GetOrFail("{.spec}"))
                e2e.Logf("machineConfigSelector %v", spec.Get("configuration").Get("source").Item(0).Get("name"))  // name of the first MC as interface
                e2e.Logf("machineConfigSelector %s", spec.Get("configuration").Get("source").Item(0).Get("name").ToString()) // name of the first MC as string
```

### Items

Returns a list of JSONData structs containing the values in the list. It will fail if the current JSONData object is not a list []interface{}


The difference with ToList() method is that ToList() method will return `[]interface` and Items() method will return `[]JSONData`

```go
                resource := NewResource(oc.AsAdmin(), "mc", "00-master")
                owners := JSON(resource.GetOrFail("{.metadata.ownerReferences}"))
		if owners.Exists() {
			for _, owner := range owners.Items() {
                		e2e.Logf("Owner name %s", owner.Get("name").ToString()) // print owner.name field
                		e2e.Logf("Owner kind %s", owner.Get("kind").ToString()) // print owner.kind field
			}
		}
```

### GetJSONPath

Executes a jsonpath query and returns the result. For simplicity the result will be flattened

Will always return a list of JSONPath structs, since filter like [?(@.type=="RenderDegraded")] or [\*] are never guaranteed to return unique values.

```go
                allData := JSON(resource.GetOrFail("{}"))
                values, err := allData.GetJSONPath(`{.status.conditions[?(@.type=="RenderDegraded")]}`)
		o.Expect(err).NotTo(o.HaveOccurred())
                condition, err := values[0].AsJSONString()  // print the  RenderDegraded condition in json format string
		o.Expect(err).NotTo(o.HaveOccurred())
                e2e.Logf("condition:\n %s", condition)
```

### ToInt, ToFloat, ToString, ToMap, ToList, ToInterface

Will return the value stored in the JSONData struct casting it to the corresponding type

### Exists

It is true if the stored value is not nil

```go
                resource := NewResource(oc.AsAdmin(), "mcp", "worker")
                spec := JSON(resource.GetOrFail("{.spec}"))
                o.Expect(spec.Get("configuration").Exists()).To(o.BeTrue())  // Make sure that the MCP spec contains the "configuration" key values
```

## Log Extension

Integrate logging framework `github.com/rs/zerolog` with `ginkgo.GinkgoWriter`, support debugg logging, user needs to export environment variable `GINKGO_TEST_ENABLE_DEBUG_LOG`

```
export GINKGO_TEST_ENABLE_DEBUG_LOG=yes
```

```
import (
        logger "github.com/openshift/machine-config-operator/ginkgo-test/test/extended/mco/logext"
)

g.It("Author:rioliu-Critical-12345-log ext demo [Serial]", func() {
        logger.Infof("Hello %s", name)
        logger.Errorf("Test is failed %v", err)
        logger.Debugf("Debug logging %s", output)
})

Output:
Jul 27 15:12:01.173: INFO: Hello Openshift
Jul 27 15:12:02.123: ERROR: Test is failed return code is -1
Jul 27 15:12:02.165: DEBUG: Debug logging <<<<^_^>>>>
```
