package daemon

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang/groupcache/lru"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	mcfgv1alpha1 "github.com/openshift/api/machineconfiguration/v1alpha1"
	fakemco "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/daemon/cri"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	v1 "k8s.io/cri-api/pkg/apis/runtime/v1"
	apitest "k8s.io/cri-api/pkg/apis/testing"
	"k8s.io/klog/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMain(m *testing.M) {
	klog.InitFlags(nil)
	flag.Set("logtostderr", "true")
	flag.Set("v", "2")
	flag.Parse()
	os.Exit(m.Run())
}

const authFileContents = `{"auths":{
	"quay.io": {"auth": "Zm9vOmJhcg=="},
	"quay.io:443": {"auth": "Zm9vOmJheg=="}
}}`

func TestRegistryAuth(t *testing.T) {
	require := require.New(t)
	authFilePath := filepath.Join(t.TempDir(), "auth.json")
	bogusFilePath := filepath.Join(t.TempDir(), "bogus.json")
	err := os.WriteFile(authFilePath, []byte(authFileContents), 0644)
	require.NoError(err)
	tests := []struct {
		name         string
		authFile     string
		image        string
		wantAuth     v1.AuthConfig
		wantErr      error
		wantImageErr error
	}{
		{
			name:     "empty auth file",
			authFile: bogusFilePath,
			wantErr:  os.ErrNotExist,
		},
		{
			name:     "valid auth file",
			authFile: authFilePath,
			image:    "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:6134ca5fbcf8e4007de2d319758dcd7b4e5ded97a00c78a7ff490c1f56579d49",
			wantAuth: v1.AuthConfig{
				Username: "foo",
				Password: "bar",
			},
		},
		{
			name:     "valid auth file with port",
			authFile: authFilePath,
			image:    "quay.io:443/openshift-release-dev/ocp-v4.0-art-dev@sha256:6134ca5fbcf8e4007de2d319758dcd7b4e5ded97a00c78a7ff490c1f56579d49",
			wantAuth: v1.AuthConfig{
				Username: "foo",
				Password: "baz",
			},
		},
		{
			name:         "no valid registry auth for image",
			authFile:     authFilePath,
			image:        "docker.io/no/auth",
			wantImageErr: errNoAuthForImage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := newRegistryAuth(tt.authFile)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
			cfg, err := registry.getAuthConfigForImage(tt.image)
			if tt.wantImageErr != nil {
				require.ErrorIs(err, tt.wantImageErr)
				return
			}
			require.NoError(err)
			require.NotNil(cfg)
			require.Equal(tt.wantAuth, *cfg)
		})
	}
}

func TestEnsurePinnedImageSetForNode(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name               string
		nodes              []runtime.Object
		machineConfigPools []runtime.Object
		wantValidTarget    bool
		wantErr            error
		wantNotFound       bool
	}{
		{
			name: "happy path",
			machineConfigPools: []runtime.Object{
				fakeWorkerPoolPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
			wantValidTarget: true,
		},
		{
			name: "pool has no pinned image sets",
			machineConfigPools: []runtime.Object{
				fakeWorkerPoolNoPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
			wantValidTarget: true,
		},
		{
			name: "no matching pools for node",
			machineConfigPools: []runtime.Object{
				fakeMasterPoolPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeMCOClient := fakemco.NewSimpleClientset(tt.machineConfigPools...)
			fakeClient := fake.NewSimpleClientset(tt.nodes...)
			sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
			informerFactory := informers.NewSharedInformerFactory(fakeClient, noResyncPeriodFunc())
			mcpInformer := sharedInformers.Machineconfiguration().V1().MachineConfigPools()
			nodeInformer := informerFactory.Core().V1().Nodes()

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())
			sharedInformers.Start(ctx.Done())
			sharedInformers.WaitForCacheSync(ctx.Done())

			for _, mcp := range tt.machineConfigPools {
				err := mcpInformer.Informer().GetIndexer().Add(mcp)
				require.NoError(err)
			}
			for _, node := range tt.nodes {
				err := nodeInformer.Informer().GetIndexer().Add(node)
				require.NoError(err)
			}

			var localNodeName string
			if len(tt.nodes) == 0 {
				localNodeName = "bogus"
			} else {
				localNode, ok := tt.nodes[0].(*corev1.Node)
				require.True(ok)
				localNodeName = localNode.Name
			}

			pool := tt.machineConfigPools[0].(*mcfgv1.MachineConfigPool)
			validTarget, err := validateTargetPoolForNode(localNodeName, nodeInformer.Lister(), pool)
			require.NoError(err)
			require.Equal(tt.wantValidTarget, validTarget)

		})
	}
}

func TestPrefetchImageSets(t *testing.T) {
	require := require.New(t)

	// populate authfile
	authFilePath := filepath.Join(t.TempDir(), "auth.json")
	err := os.WriteFile(authFilePath, []byte(authFileContents), 0644)
	require.NoError(err)

	tests := []struct {
		name                    string
		nodes                   []runtime.Object
		machineConfigPools      []runtime.Object
		imageSets               []runtime.Object
		registryAvailableImages []string
		authFilePath            string
		wantValidTarget         bool
		wantErr                 error
		wantNotFound            bool
		wantPulledImages        int
	}{
		{
			name:         "happy path",
			authFilePath: authFilePath,
			machineConfigPools: []runtime.Object{
				fakeWorkerPoolPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
			wantValidTarget: true,
			imageSets: []runtime.Object{
				&mcfgv1alpha1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1alpha1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1alpha1.PinnedImageRef{
							{
								Name: availableImage,
							},
						},
					},
				},
			},
			registryAvailableImages: []string{availableImage},
			wantPulledImages:        1,
		},
		{
			name:         "timeout error while pull",
			authFilePath: authFilePath,
			machineConfigPools: []runtime.Object{
				fakeWorkerPoolPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
			wantValidTarget: true,
			imageSets: []runtime.Object{
				&mcfgv1alpha1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1alpha1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1alpha1.PinnedImageRef{
							{
								Name: slowImage,
							},
						},
					},
				},
			},
			registryAvailableImages: []string{availableImage},
			wantErr:                 wait.ErrWaitTimeout,
		},
		{
			name:         "image not found",
			authFilePath: authFilePath,
			machineConfigPools: []runtime.Object{
				fakeWorkerPoolPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
			wantValidTarget: true,
			imageSets: []runtime.Object{
				&mcfgv1alpha1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1alpha1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1alpha1.PinnedImageRef{
							{
								Name: availableImage,
							},
						},
					},
				},
			},
			registryAvailableImages: nil,
			wantErr:                 errFailedToPullImage,
		},
		{
			name:         "invalid auth file",
			authFilePath: "/etc/bogus",
			machineConfigPools: []runtime.Object{
				fakeWorkerPoolPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
			wantValidTarget: true,
			imageSets: []runtime.Object{
				&mcfgv1alpha1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1alpha1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1alpha1.PinnedImageRef{
							{
								Name: availableImage,
							},
						},
					},
				},
			},
			registryAvailableImages: nil,
			wantErr:                 os.ErrNotExist,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fakeMCOClient := fakemco.NewSimpleClientset(tt.machineConfigPools...)
			fakeClient := fake.NewSimpleClientset(tt.nodes...)
			sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
			informerFactory := informers.NewSharedInformerFactory(fakeClient, noResyncPeriodFunc())
			mcpInformer := sharedInformers.Machineconfiguration().V1().MachineConfigPools()
			imageSetInformer := sharedInformers.Machineconfiguration().V1alpha1().PinnedImageSets()
			nodeInformer := informerFactory.Core().V1().Nodes()

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())
			sharedInformers.Start(ctx.Done())
			sharedInformers.WaitForCacheSync(ctx.Done())

			for _, mcp := range tt.machineConfigPools {
				err := mcpInformer.Informer().GetIndexer().Add(mcp)
				require.NoError(err)
			}
			for _, node := range tt.nodes {
				err := nodeInformer.Informer().GetIndexer().Add(node)
				require.NoError(err)
			}
			for _, imageSet := range tt.imageSets {
				err := imageSetInformer.Informer().GetIndexer().Add(imageSet)
				require.NoError(err)
			}

			var localNodeName string
			if len(tt.nodes) == 0 {
				localNodeName = "bogus"
			} else {
				localNode, ok := tt.nodes[0].(*corev1.Node)
				require.True(ok)
				localNodeName = localNode.Name
			}

			// setup fake cri runtime with client
			runtime := newFakeRuntime([]string{}, tt.registryAvailableImages)
			listener, err := newTestListener()
			require.NoError(err)
			err = runtime.Start(listener)
			require.NoError(err)
			defer runtime.Stop()
			runtimeEndpoint := listener.Addr().String()

			criClient, err := cri.NewClient(ctx, runtimeEndpoint)
			require.NoError(err)

			p := &PinnedImageSetManager{
				nodeName:         localNodeName,
				criClient:        criClient,
				authFilePath:     tt.authFilePath,
				imageSetLister:   imageSetInformer.Lister(),
				imageSetSynced:   imageSetInformer.Informer().HasSynced,
				nodeLister:       nodeInformer.Lister(),
				nodeListerSynced: nodeInformer.Informer().HasSynced,
				prefetchCh:       make(chan prefetch, defaultPrefetchWorkers*2),
				backoff: wait.Backoff{
					Steps:    1,
					Duration: 10 * time.Millisecond,
					Factor:   retryFactor,
					Cap:      10 * time.Millisecond,
				},
				cache: lru.New(256),
			}

			imageSets := make([]*mcfgv1alpha1.PinnedImageSet, len(tt.imageSets))
			for i, obj := range tt.imageSets {
				imageSet, ok := obj.(*mcfgv1alpha1.PinnedImageSet)
				require.True(ok)
				imageSets[i] = imageSet
			}

			// start image prefetch worker
			go func() {
				p.prefetchWorker(ctx)
			}()

			err = p.prefetchImageSets(ctx, imageSets...)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
			require.Equal(tt.wantPulledImages, runtime.imagesPulled())
		})
	}
}

var (
	fakeStableStorageWorkerNode = fakeNode(
		"worker",
		"100Gi",
	).DeepCopy()

	fakeWorkerPoolPinnedImageSets = fakeMachineConfigPool(
		"worker",
		map[string]string{
			"pools.operator.machineconfiguration.openshift.io/worker": "",
		},
		[]mcfgv1.PinnedImageSetRef{
			{
				Name: "fake-pinned-images",
			},
		},
	)

	fakeWorkerPoolNoPinnedImageSets = fakeMachineConfigPool(
		"worker",
		map[string]string{
			"pools.operator.machineconfiguration.openshift.io/worker": "",
		},
		[]mcfgv1.PinnedImageSetRef{},
	)

	fakeMasterPoolPinnedImageSets = fakeMachineConfigPool(
		"master",
		map[string]string{
			"pools.operator.machineconfiguration.openshift.io/master": "",
			"pinnedimageset-opt-in":                                   "true",
		},
		[]mcfgv1.PinnedImageSetRef{
			{
				Name: "fake-master-pinned-images",
			},
		},
	)
)

func fakeNode(role, allocatableStorage string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-" + role,
			Labels: map[string]string{
				fmt.Sprintf("node-role.kubernetes.io/%s", role): "",
			},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage:          resource.MustParse(allocatableStorage),
				corev1.ResourceEphemeralStorage: resource.MustParse(allocatableStorage),
			},
		},
	}
}

func fakeMachineConfigPool(name string, labels map[string]string, pinnedImageSets []mcfgv1.PinnedImageSetRef) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			PinnedImageSets: pinnedImageSets,
			MachineConfigSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"machineconfiguration.openshift.io/role": name,
				},
			},
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					fmt.Sprintf("node-role.kubernetes.io/%s", name): "",
				},
			},
		},
	}
}

const (
	availableImage   = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	unavailableImage = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// slowImage is a fake image that is used to block the image puller. This simulates a slow image pull of 1 seconds.
	slowImage = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var _ runtimeapi.ImageServiceServer = (*FakeRuntime)(nil)

// FakeRuntime represents a fake remote container runtime.
type FakeRuntime struct {
	server *grpc.Server
	// Fake runtime service.
	ImageService *apitest.FakeImageService

	// images that are already stored in the fake runtime.
	localImages []runtimeapi.Image
	// images that are available to be pulled.
	availableImages []runtimeapi.Image
	// number of images pulled.
	pulledImages int
}

// newFakeRuntime creates a new FakeRuntime.
func newFakeRuntime(localImages, availableImages []string) *FakeRuntime {
	f := &FakeRuntime{
		server:       grpc.NewServer(),
		ImageService: apitest.NewFakeImageService(),
	}
	for _, image := range localImages {
		f.localImages = append(f.localImages, runtimeapi.Image{
			Spec: &runtimeapi.ImageSpec{
				Image: image,
			},
			Pinned: true,
		})
	}

	for _, image := range availableImages {
		f.availableImages = append(f.availableImages, runtimeapi.Image{
			Spec: &runtimeapi.ImageSpec{
				Image: image,
			},
		})
	}

	runtimeapi.RegisterImageServiceServer(f.server, f)
	return f
}

// ImageFsInfo implements v1.ImageServiceServer.
func (r *FakeRuntime) ImageFsInfo(ctx context.Context, req *runtimeapi.ImageFsInfoRequest) (*runtimeapi.ImageFsInfoResponse, error) {
	return r.ImageService.ImageFsInfo(ctx)
}

// ImageStatus implements v1.ImageServiceServer.
func (r *FakeRuntime) ImageStatus(_ context.Context, req *runtimeapi.ImageStatusRequest) (*runtimeapi.ImageStatusResponse, error) {
	image := req.Image.Image

	for _, img := range r.localImages {
		if img.Spec.Image == image {
			return &runtimeapi.ImageStatusResponse{
				Image: &img,
			}, nil
		}
	}

	return &runtimeapi.ImageStatusResponse{}, nil
}

// ListImages implements v1.ImageServiceServer.
func (*FakeRuntime) ListImages(context.Context, *runtimeapi.ListImagesRequest) (*runtimeapi.ListImagesResponse, error) {
	panic("unimplemented")
}

// PullImage implements v1.ImageServiceServer.
func (r *FakeRuntime) PullImage(ctx context.Context, req *runtimeapi.PullImageRequest) (*runtimeapi.PullImageResponse, error) {
	if req.Image.Image == slowImage {
		time.Sleep(1 * time.Second)
	}

	resp, err := r.ImageService.PullImage(ctx, req.Image, req.Auth, req.SandboxConfig)
	if err != nil {
		return nil, err
	}
	for _, img := range r.availableImages {
		if img.Spec.Image == resp {
			r.pulledImages++
			return &runtimeapi.PullImageResponse{
				ImageRef: resp,
			}, nil
		}
	}

	return nil, status.Error(codes.NotFound, "image not found")
}

// RemoveImage implements v1.ImageServiceServer.
func (*FakeRuntime) RemoveImage(context.Context, *runtimeapi.RemoveImageRequest) (*runtimeapi.RemoveImageResponse, error) {
	panic("unimplemented")
}

// Start starts the fake remote runtime.
func (f *FakeRuntime) Start(listener net.Listener) error {
	go func() {
		_ = f.server.Serve(listener)
	}()
	return nil
}

func (f *FakeRuntime) imagesPulled() int {
	return f.pulledImages
}

// returns a TCP listener listening against the next available port
// on the system bound to localhost.
func newTestListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:")
}

// Stop stops the fake remote runtime.
func (f *FakeRuntime) Stop() {
	f.server.Stop()
}
