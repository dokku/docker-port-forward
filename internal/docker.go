package internal

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// DockerClientInterface is the subset of the Docker client API we use.
type DockerClientInterface interface {
	Close() error
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ImageInspect(ctx context.Context, imageID string) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
}

// DockerClient wraps the real Docker client with the interface we use.
type DockerClient struct {
	cli *dockerClient.Client
}

// NewDockerClient returns a Docker client using the environment-configured daemon.
func NewDockerClient() (DockerClientInterface, error) {
	cli, err := dockerClient.NewClientWithOpts(
		dockerClient.FromEnv,
		dockerClient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("error creating Docker client: %v", err)
	}
	return &DockerClient{cli: cli}, nil
}

// Close closes the underlying Docker client connection.
func (d *DockerClient) Close() error {
	return d.cli.Close()
}

// ContainerCreate creates a new container.
func (d *DockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	return d.cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, platform, containerName)
}

// ContainerInspect inspects a container by ID or name.
func (d *DockerClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	return d.cli.ContainerInspect(ctx, containerID)
}

// ContainerList lists containers.
func (d *DockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	return d.cli.ContainerList(ctx, options)
}

// ContainerLogs returns a container's log stream.
func (d *DockerClient) ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error) {
	return d.cli.ContainerLogs(ctx, containerID, options)
}

// ContainerRemove removes a container.
func (d *DockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	return d.cli.ContainerRemove(ctx, containerID, options)
}

// ContainerStart starts a container.
func (d *DockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	return d.cli.ContainerStart(ctx, containerID, options)
}

// ContainerStop stops a container.
func (d *DockerClient) ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error {
	return d.cli.ContainerStop(ctx, containerID, options)
}

// ContainerWait blocks until the container enters the given condition.
func (d *DockerClient) ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	return d.cli.ContainerWait(ctx, containerID, condition)
}

// ImageInspect inspects an image.
func (d *DockerClient) ImageInspect(ctx context.Context, imageID string) (image.InspectResponse, error) {
	return d.cli.ImageInspect(ctx, imageID)
}

// ImagePull pulls an image from a registry.
func (d *DockerClient) ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	return d.cli.ImagePull(ctx, ref, options)
}
