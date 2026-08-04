package bootimage

import (
	"context"
	"crypto/tls"
	"fmt"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vapi/rest"
	"github.com/vmware/govmomi/vapi/tags"

	// Registers the vAPI (REST) endpoints, including tag management, on the simulator's
	// HTTP server. Required for attachTag/getClientsFromServerURL's REST login to work.
	_ "github.com/vmware/govmomi/vapi/simulator"

	osconfigv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// simulatedVCenter is one independent vcsim instance standing in for a real vCenter, with its
// resource names/paths discovered from the running model rather than hardcoded, so tests stay
// correct if govmomi's default VPX topology ever changes.
type simulatedVCenter struct {
	Server     string // host:port, used as providerSpec.Workspace.Server / VSpherePlatformVCenterSpec.Server
	Username   string
	Password   string
	Model      *simulator.Model
	HTTPServer *simulator.Server
	Client     *govmomi.Client
	TagManager *tags.Manager
	Finder     *find.Finder
	// Registry is this vCenter's own object registry, captured immediately after model.Create().
	// govmomi v0.45.1 has no Model.Map() accessor (added in later versions) and the package-level
	// simulator.Map var is reassigned by every model.Create() call, so with more than one
	// simulated vCenter alive at once it only ever reflects the most-recently-created one. Tests
	// that need to reach into a specific vCenter's simulated objects directly (e.g. to mutate a
	// VM's product version) must go through vc.Registry, never the simulator.Map global.
	Registry *simulator.Registry

	DatacenterName   string
	ClusterPath      string // e.g. /DC0/host/DC0_C0
	DatastorePath    string // e.g. /DC0/datastore/LocalDS_0
	NetworkName      string // e.g. "VM Network" - a simple name, joined onto ClusterPath by production code
	ResourcePoolPath string // e.g. /DC0/host/DC0_C0/Resources
	VMFolderPath     string // e.g. /DC0/vm

	cluster *object.ClusterComputeResource
}

// newSimulatedVCenter starts one vcsim instance with a single datacenter/cluster/host/datastore
// (deliberately minimal and deterministic - just enough for one failure domain's resources to
// resolve unambiguously) and connects a govmomi + REST/tags client to it. Cleanup is registered
// automatically via t.Cleanup.
func newSimulatedVCenter(t *testing.T) *simulatedVCenter {
	t.Helper()

	model := simulator.VPX()
	model.Datacenter = 1
	model.Cluster = 1
	model.Host = 1
	model.Datastore = 1
	if err := model.Create(); err != nil {
		t.Fatalf("failed to create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	registry := simulator.Map

	model.Service.TLS = new(tls.Config)
	model.Service.RegisterEndpoints = true
	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	ctx := context.Background()
	client, err := govmomi.NewClient(ctx, server.URL, true)
	if err != nil {
		t.Fatalf("failed to connect to simulator: %v", err)
	}

	password, _ := server.URL.User.Password()
	username := server.URL.User.Username()

	restClient := rest.NewClient(client.Client)
	if err := restClient.Login(ctx, server.URL.User); err != nil {
		t.Fatalf("failed to log in to simulator REST endpoint: %v", err)
	}
	t.Cleanup(func() { _ = restClient.Logout(context.Background()) })

	finder := find.NewFinder(client.Client, false)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		t.Fatalf("failed to find default datacenter: %v", err)
	}
	finder = finder.SetDatacenter(dc)

	cluster, err := finder.DefaultClusterComputeResource(ctx)
	if err != nil {
		t.Fatalf("failed to find default cluster: %v", err)
	}
	datastore, err := finder.DefaultDatastore(ctx)
	if err != nil {
		t.Fatalf("failed to find default datastore: %v", err)
	}
	// Resolved through the finder (not cluster.ResourcePool(ctx)) so that InventoryPath is
	// populated - object.ResourcePool built directly from a ComputeResource's raw property
	// reference does not carry an inventory path.
	pool, err := finder.ResourcePool(ctx, cluster.InventoryPath+"/Resources")
	if err != nil {
		t.Fatalf("failed to find cluster resource pool: %v", err)
	}
	folders, err := dc.Folders(ctx)
	if err != nil {
		t.Fatalf("failed to find datacenter folders: %v", err)
	}
	networks, err := finder.NetworkList(ctx, "*")
	if err != nil || len(networks) == 0 {
		t.Fatalf("failed to find any network: %v", err)
	}
	var networkName string
	for _, n := range networks {
		if net, ok := n.(*object.Network); ok {
			networkName = net.Name()
			break
		}
	}
	if networkName == "" {
		t.Fatalf("no standard (non-distributed) network found in simulator model")
	}

	return &simulatedVCenter{
		Server:           server.URL.Host,
		Username:         username,
		Password:         password,
		Model:            model,
		HTTPServer:       server,
		Client:           client,
		TagManager:       tags.NewManager(restClient),
		Finder:           finder,
		Registry:         registry,
		DatacenterName:   dc.Name(),
		ClusterPath:      cluster.InventoryPath,
		DatastorePath:    datastore.InventoryPath,
		NetworkName:      networkName,
		ResourcePoolPath: pool.InventoryPath,
		VMFolderPath:     folders.VmFolder.InventoryPath,
		cluster:          cluster,
	}
}

// activate makes vc's registry the active one for any subsequent operation that spawns a vcsim
// Task (import, rename, destroy, ...). govmomi v0.45.1's simulator tracks Task objects via the
// package-level simulator.Map global rather than per-Service state (see the "alias the global
// Map to reduce data races" comment in simulator/task.go), and model.Create() overwrites that
// global every time a new simulatedVCenter is constructed. With more than one simulated vCenter
// alive at once, any mutating call must re-activate its target vCenter first, or the resulting
// Task gets registered into whichever vCenter's registry happened to be created most recently
// instead of the one actually performing the operation.
func (vc *simulatedVCenter) activate() *simulatedVCenter {
	simulator.Map = vc.Registry
	return vc
}

// newSimulatedVCenters starts n independent simulated vCenters, useful for exercising
// multi-vCenter reconciliation (createNewVMTemplate must only touch the vCenter/failure domain
// matching a given providerSpec, never the others).
func newSimulatedVCenters(t *testing.T, n int) []*simulatedVCenter {
	t.Helper()
	vcenters := make([]*simulatedVCenter, n)
	for i := range vcenters {
		vcenters[i] = newSimulatedVCenter(t)
	}
	return vcenters
}

// buildFailureDomain builds a VSpherePlatformFailureDomainSpec whose topology fields resolve
// against the given simulated vCenter's actual discovered resources.
func buildFailureDomain(vc *simulatedVCenter, fdName string, modifiers ...func(*osconfigv1.VSpherePlatformFailureDomainSpec)) osconfigv1.VSpherePlatformFailureDomainSpec {
	fd := osconfigv1.VSpherePlatformFailureDomainSpec{
		Name:   fdName,
		Region: "region1",
		Zone:   "zone1",
		Server: vc.Server,
		Topology: osconfigv1.VSpherePlatformTopology{
			Datacenter:     vc.DatacenterName,
			ComputeCluster: vc.ClusterPath,
			Networks:       []string{vc.NetworkName},
			Datastore:      vc.DatastorePath,
			ResourcePool:   vc.ResourcePoolPath,
		},
	}
	for _, m := range modifiers {
		m(&fd)
	}
	return fd
}

// buildVSphereInfra builds an Infrastructure CR wiring together one or more simulated vCenters
// and failure domains, matching the shape createNewVMTemplate expects from
// infra.Spec.PlatformSpec.VSphere.
func buildVSphereInfra(infraName string, vcenters []*simulatedVCenter, failureDomains []osconfigv1.VSpherePlatformFailureDomainSpec) *osconfigv1.Infrastructure {
	vcenterSpecs := make([]osconfigv1.VSpherePlatformVCenterSpec, 0, len(vcenters))
	for _, vc := range vcenters {
		vcenterSpecs = append(vcenterSpecs, osconfigv1.VSpherePlatformVCenterSpec{
			Server:      vc.Server,
			Datacenters: []string{vc.DatacenterName},
		})
	}

	return &osconfigv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: osconfigv1.InfrastructureSpec{
			PlatformSpec: osconfigv1.PlatformSpec{
				Type: osconfigv1.VSpherePlatformType,
				VSphere: &osconfigv1.VSpherePlatformSpec{
					VCenters:       vcenterSpecs,
					FailureDomains: failureDomains,
				},
			},
		},
		Status: osconfigv1.InfrastructureStatus{
			InfrastructureName: infraName,
			PlatformStatus: &osconfigv1.PlatformStatus{
				Type: osconfigv1.VSpherePlatformType,
			},
		},
	}
}

// buildVSphereProviderSpec builds a VSphereMachineProviderSpec whose Workspace fields match the
// given failure domain's topology on the given simulated vCenter, as a real MachineSet's
// providerSpec would after being reconciled onto that failure domain.
func buildVSphereProviderSpec(vc *simulatedVCenter, fd osconfigv1.VSpherePlatformFailureDomainSpec, templateName string) *machinev1beta1.VSphereMachineProviderSpec {
	return &machinev1beta1.VSphereMachineProviderSpec{
		Template: templateName,
		Workspace: &machinev1beta1.Workspace{
			Server:       vc.Server,
			Datacenter:   fd.Topology.Datacenter,
			Datastore:    fd.Topology.Datastore,
			ResourcePool: fd.Topology.ResourcePool,
			Folder:       vc.VMFolderPath,
		},
	}
}

// buildVSphereCredsSecret builds the "vsphere-creds" Secret shape read by createNewVMTemplate /
// getClientsFromServerURL: one "<server>.username" / "<server>.password" key pair per vCenter.
func buildVSphereCredsSecret(vcenters []*simulatedVCenter) *corev1.Secret {
	data := map[string][]byte{}
	for _, vc := range vcenters {
		data[fmt.Sprintf("%s.username", vc.Server)] = []byte(vc.Username)
		data[fmt.Sprintf("%s.password", vc.Server)] = []byte(vc.Password)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vsphere-creds", Namespace: "kube-system"},
		Data:       data,
	}
}
