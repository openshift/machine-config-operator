package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/util/workqueue"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	apitest "k8s.io/cri-api/pkg/apis/testing"

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemco "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/openshift/machine-config-operator/pkg/daemon/cri"
)

const (
	authFileContents = `{"auths":{
	"quay.io": {"auth": "Zm9vOmJhcg=="},
	"quay.io:443": {"auth": "Zm9vOmJheg=="},
	"registry.ci.openshift.org": {"auth": "Zm9vOnphcg=="},
	"ec2-18-226-52-10.us-east-2.compute.amazonaws.com:6002": {"auth": "em9vbTp6YW0="}
}}`

	registryConfig = `unqualified-search-registries = ["registry.access.redhat.com", "docker.io"]
short-name-mode = ""

[[registry]]
  prefix = ""
  location = "example.io/digest-example/release"
  blocked = true

  [[registry.mirror]]
    location = "registry.ci.openshift.org/ocp/release"
    pull-from-mirror = "digest-only"

[[registry]]
  prefix = ""
  location = "registry.stage.redhat.io"

  [[registry.mirror]]
    location = "ec2-18-226-52-10.us-east-2.compute.amazonaws.com:6002"
    pull-from-mirror = "digest-only"`
)

func TestRegistryAuth(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	authFilePath := filepath.Join(tmpDir, "auth.json")
	bogusFilePath := filepath.Join(tmpDir, "bogus.json")
	err := os.WriteFile(authFilePath, []byte(authFileContents), 0644)
	require.NoError(err)
	registryConfigPath := filepath.Join(tmpDir, "registries.conf")
	err = os.WriteFile(registryConfigPath, []byte(registryConfig), 0644)
	require.NoError(err)
	tests := []struct {
		name         string
		authFile     string
		image        string
		wantAuth     *runtimeapi.AuthConfig
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
			wantAuth: &runtimeapi.AuthConfig{
				Username: "foo",
				Password: "bar",
			},
		},
		{
			name:     "valid auth file with port",
			authFile: authFilePath,
			image:    "quay.io:443/openshift-release-dev/ocp-v4.0-art-dev@sha256:6134ca5fbcf8e4007de2d319758dcd7b4e5ded97a00c78a7ff490c1f56579d49",
			wantAuth: &runtimeapi.AuthConfig{
				Username: "foo",
				Password: "baz",
			},
		},
		{
			name:     "no valid registry auth for image",
			authFile: authFilePath,
			image:    "docker.io/no/auth",
			wantAuth: nil,
		},
		{
			name:     "mirrored registry send auth for mirror",
			authFile: authFilePath,
			image:    "example.io/digest-example/release@sha256:0704fcca246287435294dcf12b21825689c899c2365b6fc8d089d80efbae742a",
			wantAuth: &runtimeapi.AuthConfig{
				Username: "foo",
				Password: "zar",
			},
		},
		{
			name:     "multiple mirrored registry",
			authFile: authFilePath,
			image:    "registry.stage.redhat.io/digest-example/release@sha256:9ac31162cf7a997c1d3222c91f853de5d63c42981f8dc14e99beaf6aaaac4ecf",
			wantAuth: &runtimeapi.AuthConfig{
				Username: "zoom",
				Password: "zam",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := newRegistryAuth(tt.authFile, registryConfigPath)
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
			require.Equal(tt.wantAuth, cfg)
		})
	}
}

func TestPrefetchImageSets(t *testing.T) {
	require := require.New(t)

	// populate authfile and registry config
	tmpDir := t.TempDir()
	authFilePath := filepath.Join(tmpDir, "auth.json")
	err := os.WriteFile(authFilePath, []byte(authFileContents), 0644)
	require.NoError(err)
	registryConfigPath := filepath.Join(tmpDir, "registries.conf")
	err = os.WriteFile(registryConfigPath, []byte(registryConfig), 0644)
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
		wantNoSpace             bool
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
				&mcfgv1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1.PinnedImageRef{
							{
								Name: mcfgv1.ImageDigestFormat(availableImage),
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
				&mcfgv1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1.PinnedImageRef{
							{
								Name: mcfgv1.ImageDigestFormat(slowImage),
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
				&mcfgv1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1.PinnedImageRef{
							{
								Name: mcfgv1.ImageDigestFormat(availableImage),
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
				&mcfgv1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: mcfgv1.PinnedImageSetSpec{
						PinnedImages: []mcfgv1.PinnedImageRef{
							{
								Name: mcfgv1.ImageDigestFormat(availableImage),
							},
						},
					},
				},
			},
			registryAvailableImages: nil,
			wantErr:                 os.ErrNotExist,
		},
		{
			name:         "out of space should drain and return out of space error quickly",
			authFilePath: authFilePath,
			machineConfigPools: []runtime.Object{
				fakeWorkerPoolPinnedImageSets,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
			},
			wantValidTarget: true,
			imageSets: []runtime.Object{
				&mcfgv1.PinnedImageSet{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fake-pinned-images",
					},
					Spec: generateImageSetSpec(outOfDiskImage, 300),
				},
			},
			registryAvailableImages: nil,
			wantNoSpace:             true,
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
			imageSetInformer := sharedInformers.Machineconfiguration().V1().PinnedImageSets()
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
				registryCfgPath:  registryConfigPath,
				imageSetLister:   imageSetInformer.Lister(),
				imageSetSynced:   imageSetInformer.Informer().HasSynced,
				nodeLister:       nodeInformer.Lister(),
				nodeListerSynced: nodeInformer.Informer().HasSynced,
				prefetchCh:       make(chan prefetch, defaultPrefetchWorkers*2),
				backoff: wait.Backoff{
					Steps:    maxRetries,
					Duration: 10 * time.Millisecond,
					Factor:   retryFactor,
					Cap:      10 * time.Millisecond,
				},
				cache: newImageCache(256),
			}

			imageSets := make([]*mcfgv1.PinnedImageSet, len(tt.imageSets))
			for i, obj := range tt.imageSets {
				imageSet, ok := obj.(*mcfgv1.PinnedImageSet)
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
			if tt.wantNoSpace {
				require.True(isErrNoSpace(err))
				return
			}
			require.NoError(err)
			require.Equal(tt.wantPulledImages, runtime.imagesPulled())
		})
	}
}

func TestPinnedImageSetAddEventHandlers(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name               string
		nodes              []runtime.Object
		machineConfigPools []runtime.Object
		currentImageSet    *mcfgv1.PinnedImageSet
		wantCacheElements  int
	}{
		{
			name:               "happy path",
			machineConfigPools: []runtime.Object{fakeWorkerPoolPinnedImageSets},
			nodes:              []runtime.Object{fakeStableStorageWorkerNode},
			wantCacheElements:  1,
			currentImageSet:    workerPis,
		},
		{
			name:               "worker pool with no pinned image sets",
			machineConfigPools: []runtime.Object{fakeWorkerPoolNoPinnedImageSets},
			nodes:              []runtime.Object{fakeStableStorageWorkerNode},
			wantCacheElements:  0,
			currentImageSet:    workerPis,
		},
		{
			name:               "worker pool master pinned image sets",
			machineConfigPools: []runtime.Object{fakeWorkerPoolNoPinnedImageSets},
			nodes:              []runtime.Object{fakeStableStorageWorkerNode},
			wantCacheElements:  0,
			currentImageSet:    masterPis,
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

			nodeName := tt.nodes[0].(*corev1.Node).Name
			p := &PinnedImageSetManager{
				nodeName:   nodeName,
				nodeLister: nodeInformer.Lister(),
				mcpLister:  mcpInformer.Lister(),
				queue: workqueue.NewTypedRateLimitingQueueWithConfig[string](
					workqueue.DefaultTypedControllerRateLimiter[string](),
					workqueue.TypedRateLimitingQueueConfig[string]{Name: "pinned-image-set-manager"}),
				backoff: wait.Backoff{
					Steps:    maxRetries,
					Duration: 10 * time.Millisecond,
					Factor:   retryFactor,
					Cap:      10 * time.Millisecond,
				},
			}
			p.enqueueMachineConfigPool = p.enqueue
			p.addPinnedImageSet(tt.currentImageSet)
			require.Equal(tt.wantCacheElements, p.queue.Len())
		})
	}
}

func TestCheckNodeAllocatableStorage(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name           string
		node           runtime.Object
		pinnedImageSet *mcfgv1.PinnedImageSet
		// uncompressed is x 2
		imagesSizeCompressed        string
		minFreeStorageAfterPrefetch string
		wantErr                     error
	}{
		{
			name:                        "happy path",
			node:                        fakeNode([]string{"worker"}, "25Gi"),
			pinnedImageSet:              workerPis,
			imagesSizeCompressed:        "5Gi",
			minFreeStorageAfterPrefetch: "10Gi",
		},
		{
			name:                        "not enough storage",
			node:                        fakeNode([]string{"worker"}, "25Gi"),
			pinnedImageSet:              workerPis,
			imagesSizeCompressed:        "10Gi",
			minFreeStorageAfterPrefetch: "10Gi",
			wantErr:                     errInsufficientStorage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fakeClient := fake.NewSimpleClientset(tt.node)
			informerFactory := informers.NewSharedInformerFactory(fakeClient, noResyncPeriodFunc())
			nodeInformer := informerFactory.Core().V1().Nodes()

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())

			err := nodeInformer.Informer().GetIndexer().Add(tt.node)
			require.NoError(err)

			// setup fake cri runtime with client
			runtime := newFakeRuntime([]string{availableImage}, []string{})
			listener, err := newTestListener()
			require.NoError(err)
			err = runtime.Start(listener)
			require.NoError(err)
			defer runtime.Stop()
			runtimeEndpoint := listener.Addr().String()

			criClient, err := cri.NewClient(ctx, runtimeEndpoint)
			require.NoError(err)

			node := tt.node.(*corev1.Node)
			p := &PinnedImageSetManager{
				nodeName:                 node.Name,
				cache:                    newImageCache(10),
				nodeLister:               nodeInformer.Lister(),
				criClient:                criClient,
				minStorageAvailableBytes: resource.MustParse(tt.minFreeStorageAfterPrefetch),
			}

			imageSize := resource.MustParse(tt.imagesSizeCompressed)
			size, ok := imageSize.AsInt64()
			require.True(ok)

			// populate the cache with the pinned image set image size
			p.cache.Add("image1", imageInfo{Name: "image1", Size: size})

			err = p.checkNodeAllocatableStorage(ctx, tt.pinnedImageSet)
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

func TestEnsureCrioPinnedImagesConfigFile(t *testing.T) {
	require := require.New(t)
	tmpDir := t.TempDir()
	imageNames := []string{
		"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}

	newCfgBytes, err := createCrioConfigFileBytes(imageNames)
	require.NoError(err)
	testCfgPath := filepath.Join(tmpDir, "50-pinned-images")
	err = os.WriteFile(testCfgPath, newCfgBytes, 0644)
	require.NoError(err)

	pinnedImageSetManager := PinnedImageSetManager{
		systemdManager: newMockSystemdManager(nil),
	}

	err = pinnedImageSetManager.ensureCrioPinnedImagesConfigFile(testCfgPath, imageNames)
	require.NoError(err)

	imageNames = append(imageNames, "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	err = pinnedImageSetManager.ensureCrioPinnedImagesConfigFile(testCfgPath, imageNames)
	require.ErrorIs(err, os.ErrPermission) // this error is from atomic writer attempting to write.
}

func fakePinnedImageSet(name, image string, labels map[string]string) *mcfgv1.PinnedImageSet {
	return &mcfgv1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: mcfgv1.PinnedImageSetSpec{
			PinnedImages: []mcfgv1.PinnedImageRef{
				{
					Name: mcfgv1.ImageDigestFormat(image),
				},
			},
		},
	}
}

var (
	masterPis                       = fakePinnedImageSet("master-set", "image1", map[string]string{"machineconfiguration.openshift.io/role": "master"})
	workerPis                       = fakePinnedImageSet("worker-set", "image1", map[string]string{"machineconfiguration.openshift.io/role": "worker"})
	fakeStableStorageWorkerNode     = fakeNode([]string{"worker"}, "100Gi")
	fakeWorkerPoolPinnedImageSets   = fakeMachineConfigPool("worker", []mcfgv1.PinnedImageSetRef{{Name: "worker-set"}})
	fakeWorkerPoolNoPinnedImageSets = fakeMachineConfigPool("worker", nil)
	availableImage                  = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	outOfDiskImage                  = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	// slowImage is a fake image that is used to block the image puller. This simulates a slow image pull of 1 seconds.
	slowImage = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func generateImageSetSpec(imageName string, count int) mcfgv1.PinnedImageSetSpec {
	pinnedImages := make([]mcfgv1.PinnedImageRef, count)
	for i := 0; i < count; i++ {
		pinnedImages[i] = mcfgv1.PinnedImageRef{
			Name: mcfgv1.ImageDigestFormat(imageName),
		}
	}

	return mcfgv1.PinnedImageSetSpec{
		PinnedImages: pinnedImages,
	}
}

func fakeNode(roles []string, allocatableStorage string) *corev1.Node {
	labels := map[string]string{}
	for _, role := range roles {
		labels[fmt.Sprintf("node-role.kubernetes.io/%s", role)] = ""
	}

	name := strings.Join(roles, "-")
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-" + name,
			Labels: labels,
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceStorage:          resource.MustParse(allocatableStorage),
				corev1.ResourceEphemeralStorage: resource.MustParse(allocatableStorage),
			},
		},
	}
}

func fakeMachineConfigPool(role string, pinnedImageSets []mcfgv1.PinnedImageSetRef) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name: role,
			Labels: map[string]string{
				fmt.Sprintf("pools.operator.machineconfiguration.openshift.io/%s", role): "",
			},
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
			PinnedImageSets: pinnedImageSets,
			MachineConfigSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"machineconfiguration.openshift.io/role": role,
				},
			},
			NodeSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					fmt.Sprintf("node-role.kubernetes.io/%s", role): "",
				},
			},
		},
	}
}

var _ runtimeapi.ImageServiceServer = (*FakeRuntime)(nil)

// FakeRuntime represents a fake remote container runtime.
type FakeRuntime struct {
	runtimeapi.UnimplementedImageServiceServer
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
	switch req.Image.Image {
	case outOfDiskImage:
		return nil, fmt.Errorf("writing blob: storing blob to file \"/var/tmp/container_images_storage708850218/3\": write /var/tmp/container_images_storage708850218/3: %v", syscall.ENOSPC)
	case slowImage:
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
