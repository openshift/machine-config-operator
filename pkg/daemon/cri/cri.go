package cri

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

const (
	// gRPC connection parameters taken from kubelet
	maxMsgSize           = 16 * 1024 * 1024 // 16MiB
	maxBackoffDelay      = 3 * time.Second
	baseBackoffDelay     = 100 * time.Millisecond
	minConnectionTimeout = 5 * time.Second
)

// NewClient creates a new container runtime image client.
func NewClient(ctx context.Context, target string) (*Client, error) {
	conn, err := newClientConn(ctx, target)
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:  conn,
		image: runtimeapi.NewImageServiceClient(conn),
	}, nil
}

type Client struct {
	conn  *grpc.ClientConn
	image runtimeapi.ImageServiceClient
}

func (c *Client) PullImage(ctx context.Context, image string, auth *runtimeapi.AuthConfig) error {
	resp, err := c.image.PullImage(ctx, &runtimeapi.PullImageRequest{
		Image: &runtimeapi.ImageSpec{
			Image: image,
		},
		Auth: auth,
	})
	if err != nil {
		statusErr, ok := status.FromError(err)
		// If the gRPC error code is unknown don't return the full error just
		// the actual message.
		if ok && statusErr.Code() == codes.Unknown {
			return errors.New(statusErr.Message())
		}
		return err
	}
	if resp.ImageRef == "" {
		return fmt.Errorf("imageRef for %s not set", image)
	}

	return nil
}

// ImageStatus returns true if the image exists in the container runtime.
func (c *Client) ImageStatus(ctx context.Context, image string) (bool, error) {
	resp, err := c.image.ImageStatus(ctx, &runtimeapi.ImageStatusRequest{
		Image: &runtimeapi.ImageSpec{Image: image},
	})
	if err != nil {
		return false, fmt.Errorf("failed to get image status for %q: %w", image, err)
	}

	if resp.Image != nil {
		return true, nil
	}

	return false, nil
}

// RemoveImage removes the image from the container runtime.
func (c *Client) RemoveImage(ctx context.Context, image string) error {
	_, err := c.image.RemoveImage(ctx, &runtimeapi.RemoveImageRequest{
		Image: &runtimeapi.ImageSpec{
			Image: image,
		},
	})
	return err
}

// ListImages returns a list of images in the container runtime.
func (c *Client) ListImages(ctx context.Context, filter string) ([]*runtimeapi.Image, error) {
	resp, err := c.image.ListImages(ctx, &runtimeapi.ListImagesRequest{
		Filter: &runtimeapi.ImageFilter{
			Image: &runtimeapi.ImageSpec{
				Image: filter,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return resp.Images, nil
}

// ImageFsInfo returns information about the filesystem that is used to store images.
func (c *Client) ImageFsInfo(ctx context.Context) (*runtimeapi.ImageFsInfoResponse, error) {
	return c.image.ImageFsInfo(ctx, &runtimeapi.ImageFsInfoRequest{})
}

// NewClientConn creates a new grpc client connection to the container runtime service. support unix, http and https scheme.
func newClientConn(ctx context.Context, target string) (conn *grpc.ClientConn, err error) {
	backoff := backoff.DefaultConfig
	backoff.BaseDelay = baseBackoffDelay
	backoff.MaxDelay = maxBackoffDelay
	connParams := grpc.ConnectParams{
		Backoff:           backoff,
		MinConnectTimeout: minConnectionTimeout,
	}

	network := "unix"
	if _, _, err := net.SplitHostPort(target); err == nil {
		// If the target has a valid address:port, assume tcp network.
		network = "tcp"
	}

	dialerFn := func(ctx context.Context, target string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}

	var dialOpts []grpc.DialOption
	dialOpts = append(dialOpts,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMsgSize),
		),
		grpc.WithConnectParams(connParams),
		grpc.WithContextDialer(dialerFn),
	)

	return grpc.DialContext(ctx, target, dialOpts...)
}

func (c *Client) Close() error {
	return c.conn.Close()
}
