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

	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	fakemco "github.com/openshift/client-go/machineconfiguration/clientset/versioned/fake"
	mcfginformers "github.com/openshift/client-go/machineconfiguration/informers/externalversions"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	apitest "k8s.io/cri-api/pkg/apis/testing"
	"k8s.io/klog/v2"
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

func TestEnsurePinnedImageSetForNode(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name               string
		nodes              []runtime.Object
		pinnedImageSets    []runtime.Object
		machineConfigPools []runtime.Object
		wantRequeue        bool
		wantErr            error
		wantNotFound       bool
	}{
		{
			name: "happy path",
			pinnedImageSets: []runtime.Object{
				fakePinnedImageSet(
					"worker",
					uid0,
					map[string]string{
						"pools.operator.machineconfiguration.openshift.io/worker": "",
					},
					[]mcfgv1.PinnedImageRef{},
				),
			},
			machineConfigPools: []runtime.Object{
				fakeWorkerPool,
				fakeMasterPool,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
				fakeStableStorageMasterNode,
			},
		},
		{
			name: "label selector mismatch",
			pinnedImageSets: []runtime.Object{
				fakePinnedImageSet(
					"worker",
					uid0,
					map[string]string{
						"pinnedimageset-bogus": "true",
					},
					[]mcfgv1.PinnedImageRef{},
				),
			},
			machineConfigPools: []runtime.Object{
				fakeWorkerPool,
				fakeMasterPool,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
				fakeStableStorageMasterNode,
			},
			wantRequeue: false,
			wantErr:     errNoMatchingPool,
		},
		{
			name: "labels match 2 pools and local node",
			pinnedImageSets: []runtime.Object{
				fakePinnedImageSet(
					"worker",
					uid0,
					map[string]string{
						"pinnedimageset-opt-in": "true",
					},
					[]mcfgv1.PinnedImageRef{},
				),
			},
			machineConfigPools: []runtime.Object{
				fakeWorkerPool,
				fakeMasterPool,
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
				fakeStableStorageMasterNode,
			},
			wantRequeue: false,
		},
		{
			name: "labels match 2 pools no matching node",
			pinnedImageSets: []runtime.Object{
				fakePinnedImageSet(
					"worker",
					uid0,
					map[string]string{
						"pinnedimageset-opt-in": "true",
					},
					[]mcfgv1.PinnedImageRef{},
				),
			},
			machineConfigPools: []runtime.Object{
				fakeWorkerPool,
				fakeMasterPool,
			},
			nodes: []runtime.Object{
				fakeUnstableStorageInfraNode,
			},
			wantRequeue: false,
			wantErr:     errNoMatchingPool,
		},
		{
			name: "retryable error simulate node lister failure",
			pinnedImageSets: []runtime.Object{fakePinnedImageSet(
				"worker",
				uid0,
				map[string]string{
					"pinnedimageset-opt-in": "true",
				},
				[]mcfgv1.PinnedImageRef{},
			),
				fakePinnedImageSet(
					"master",
					uid1,
					map[string]string{
						"pinnedimageset-opt-in": "true",
					},
					[]mcfgv1.PinnedImageRef{},
				),
			},
			wantRequeue:  true,
			wantNotFound: true,
			wantErr:      nil,
		},
		{
			name: "multiple pinnedimagesets per pool",
			pinnedImageSets: []runtime.Object{
				fakePinnedImageSet(
					"worker",
					uid0,
					map[string]string{
						"pinnedimageset-opt-in": "true",
					},
					[]mcfgv1.PinnedImageRef{},
				),
				fakePinnedImageSet(
					"master",
					uid1,
					map[string]string{
						"pinnedimageset-opt-in": "true",
					},
					[]mcfgv1.PinnedImageRef{},
				),
			},
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
				fakeStableStorageMasterNode,
			},
			machineConfigPools: []runtime.Object{
				fakeWorkerPool,
				fakeMasterPool,
			},
			wantRequeue: true,
			wantErr:     errMultiplePinnedImageSets,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mcObj := append(tt.machineConfigPools, tt.pinnedImageSets...)
			fakeMCOClient := fakemco.NewSimpleClientset(mcObj...)
			fakeClient := fake.NewSimpleClientset(tt.nodes...)
			sharedInformers := mcfginformers.NewSharedInformerFactory(fakeMCOClient, noResyncPeriodFunc())
			informerFactory := informers.NewSharedInformerFactory(fakeClient, noResyncPeriodFunc())
			imageSetInformer := sharedInformers.Machineconfiguration().V1().PinnedImageSets()
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
			for _, set := range tt.pinnedImageSets {
				err := imageSetInformer.Informer().GetStore().Add(set)
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

			p := &PrefetchManager{
				nodeName:       localNodeName,
				imageSetLister: imageSetInformer.Lister(),
				mcpLister:      mcpInformer.Lister(),
				nodeLister:     nodeInformer.Lister(),
			}

			pinnedImageSet, ok := tt.pinnedImageSets[0].(*mcfgv1.PinnedImageSet)
			require.True(ok)

			requeue, err := p.ensurePinnedImageSetForNode(pinnedImageSet)
			require.Equal(tt.wantRequeue, requeue)
			if tt.wantNotFound == true {
				require.True(errors.IsNotFound(err))
				return
			}
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

func TestWorkerPool(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name                   string
		nodes                  []runtime.Object
		pinnedImageRefs        []mcfgv1.PinnedImageRef
		runtimeAvailableImages []string
		prefetchTimeout        time.Duration
		expectedImagesPulled   int
		wantRequeue            bool
		wantErr                error
	}{
		{
			name: "happy path",
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
				fakeStableStorageMasterNode,
			},
			runtimeAvailableImages: []string{
				availableImage0,
			},
			pinnedImageRefs: []mcfgv1.PinnedImageRef{
				availableImage0,
			},
			prefetchTimeout:     prefetchTimeout,
			expectedImagesPulled: 1,
		},
		{
			name: "no images to prefetch",
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
				fakeStableStorageMasterNode,
			},
			prefetchTimeout:      prefetchTimeout,
		},
		{
			name: "image pull timeout",
			nodes: []runtime.Object{
				fakeStableStorageWorkerNode,
				fakeStableStorageMasterNode,
			},
			runtimeAvailableImages: []string{
				slowImage,
			},
			pinnedImageRefs: []mcfgv1.PinnedImageRef{
				slowImage,
			},
			prefetchTimeout: 5 * time.Second,
			wantRequeue:     true,
			wantErr:         errFailedToPullImage,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.prefetchTimeout)
			defer cancel()

			fakeClient := fake.NewSimpleClientset(tt.nodes...)
			informerFactory := informers.NewSharedInformerFactory(fakeClient, noResyncPeriodFunc())
			nodeInformer := informerFactory.Core().V1().Nodes()

			informerFactory.Start(ctx.Done())
			informerFactory.WaitForCacheSync(ctx.Done())

			for _, node := range tt.nodes {
				err := nodeInformer.Informer().GetIndexer().Add(node)
				require.NoError(err)
			}

			authFilePath := filepath.Join(t.TempDir(), "auth.json")
			err := os.WriteFile(authFilePath, []byte(authFileContents), 0644)
			require.NoError(err)

			localNode, ok := tt.nodes[0].(*corev1.Node)
			require.True(ok)
			fakeNodeWriter := newFakeNodeWriter(fakeClient, localNode)

			// setup fake cri runtime
			runtime := newFakeRuntime([]string{}, tt.runtimeAvailableImages)
			listener, err := newTestListener()
			require.NoError(err)
			err = runtime.Start(listener)
			require.NoError(err)
			defer runtime.Stop()
			runtimeEndpoint := listener.Addr().String()

			backoff := wait.Backoff{
				Steps:    1,
				Duration: 10 * time.Millisecond,
				Factor:   retryFactor,
				Cap:      1 * time.Second,
			}

			p := &PrefetchManager{
				nodeName:        localNode.Name,
				authFilePath:    authFilePath,
				nodeLister:      nodeInformer.Lister(),
				nodeWriter:      fakeNodeWriter,
				runtimeEndpoint: runtimeEndpoint,
				backoff:         backoff,
			}
			err = p.startWorkerPool(ctx, tt.pinnedImageRefs)
			require.Equal(tt.expectedImagesPulled, runtime.imagesPulled())
			if tt.wantErr != nil {
				require.ErrorIs(err, tt.wantErr)
				return
			}
			require.NoError(err)
		})
	}
}

const (
	uid0 = "00000000-0000-0000-0000-000000000000"
	uid1 = "11111111-1111-1111-1111-111111111111"
	availableImage0 = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	availableImage1 = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	unavailableImage0 = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	unavailableImage1 = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	// slowImage is a fake image that is used to block the image puller. This simulates a slow image pull of 10 seconds.
	slowImage = "quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

var (
	fakeStableStorageMasterNode = fakeNode(
		"master",
		"100Gi",
	).DeepCopy() // dont mutate the original

	fakeStableStorageWorkerNode = fakeNode(
		"worker",
		"100Gi",
	).DeepCopy()

	fakeUnstableStorageInfraNode = fakeNode(
		"infra",
		"1Gi",
	).DeepCopy()

	fakeWorkerPool = fakeMachineConfigPool(
		"worker",
		map[string]string{
			"pools.operator.machineconfiguration.openshift.io/worker": "",
			"pinnedimageset-opt-in":                                   "true",
		},
	).DeepCopy()

	fakeMasterPool = fakeMachineConfigPool(
		"master",
		map[string]string{
			"pools.operator.machineconfiguration.openshift.io/master": "",
			"pinnedimageset-opt-in":                                   "true",
		},
	).DeepCopy()
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
			Allocatable: corev1.ResourceList{
				corev1.ResourceStorage:          resource.MustParse(allocatableStorage),
				corev1.ResourceEphemeralStorage: resource.MustParse(allocatableStorage),
			},
		},
	}
}

func fakePinnedImageSet(role, uid string, labels map[string]string, images []mcfgv1.PinnedImageRef) *mcfgv1.PinnedImageSet {
	return &mcfgv1.PinnedImageSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("fake-%s-pinned-images", role),
			UID:  types.UID(uid),
		},
		Spec: mcfgv1.PinnedImageSetSpec{
			MachineConfigPoolSelector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			PinnedImages: images,
		},
	}
}

func fakeMachineConfigPool(name string, labels map[string]string) *mcfgv1.MachineConfigPool {
	return &mcfgv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: mcfgv1.MachineConfigPoolSpec{
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
		time.Sleep(10 * time.Second)
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

var _ NodeWriter = (*fakeNodeWriter)(nil)

func newFakeNodeWriter(client *fake.Clientset, node *corev1.Node) NodeWriter {
	return &fakeNodeWriter{
		node:   node,
		client: client,
	}
}

type fakeNodeWriter struct {
	client *fake.Clientset
	node   *corev1.Node
}

func (f *fakeNodeWriter) SetAnnotations(annos map[string]string) (*corev1.Node, error) {
	if f.node.Annotations == nil {
		f.node.Annotations = make(map[string]string)
	}

	for key, value := range annos {
		f.node.Annotations[key] = value
	}

	return f.client.CoreV1().Nodes().Update(context.Background(), f.node, metav1.UpdateOptions{})
}
func (f *fakeNodeWriter) SetDesiredDrainer(value string) error {
	panic("unimplemented")
}
func (f *fakeNodeWriter) SetDone(*stateAndConfigs) error {
	panic("unimplemented")
}
func (f *fakeNodeWriter) SetUnreconcilable(err error) error {
	panic("unimplemented")
}
func (f *fakeNodeWriter) SetDegraded(err error) error {
	panic("unimplemented")
}
func (f *fakeNodeWriter) SetWorking() error {
	panic("unimplemented")
}
func (f *fakeNodeWriter) Eventf(eventtype, reason, messageFmt string, args ...interface{}) {
	// no-op
}
func (f *fakeNodeWriter) Run(stop <-chan struct{}) {
	panic("unimplemented")
}
