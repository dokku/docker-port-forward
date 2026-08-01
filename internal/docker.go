package internal

import (
	"context"
	"fmt"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	dockerClient "github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// DockerClientInterface is the subset of the Docker client API we use. Option
// parameters use the split-out github.com/moby/moby/client types; return
// values keep the github.com/moby/moby/api types so callers stay decoupled
// from the client's result wrappers.
type DockerClientInterface interface {
	Close() error
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerList(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error)
	ContainerLogs(ctx context.Context, containerID string, options dockerClient.ContainerLogsOptions) (io.ReadCloser, error)
	ContainerRemove(ctx context.Context, containerID string, options dockerClient.ContainerRemoveOptions) error
	ContainerStart(ctx context.Context, containerID string, options dockerClient.ContainerStartOptions) error
	ContainerStop(ctx context.Context, containerID string, options dockerClient.ContainerStopOptions) error
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	ImageInspect(ctx context.Context, imageID string) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options dockerClient.ImagePullOptions) (io.ReadCloser, error)
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
	res, err := d.cli.ContainerCreate(ctx, dockerClient.ContainerCreateOptions{
		Config:           config,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
		Platform:         platform,
		Name:             containerName,
	})
	if err != nil {
		return container.CreateResponse{}, err
	}
	return container.CreateResponse{ID: res.ID, Warnings: res.Warnings}, nil
}

// ContainerInspect inspects a container by ID or name.
func (d *DockerClient) ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error) {
	res, err := d.cli.ContainerInspect(ctx, containerID, dockerClient.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, err
	}
	return res.Container, nil
}

// ContainerList lists containers.
func (d *DockerClient) ContainerList(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
	res, err := d.cli.ContainerList(ctx, options)
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// ContainerLogs returns a container's log stream.
func (d *DockerClient) ContainerLogs(ctx context.Context, containerID string, options dockerClient.ContainerLogsOptions) (io.ReadCloser, error) {
	logs, err := d.cli.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return nil, err
	}
	return logs, nil
}

// ContainerRemove removes a container.
func (d *DockerClient) ContainerRemove(ctx context.Context, containerID string, options dockerClient.ContainerRemoveOptions) error {
	_, err := d.cli.ContainerRemove(ctx, containerID, options)
	return err
}

// ContainerStart starts a container.
func (d *DockerClient) ContainerStart(ctx context.Context, containerID string, options dockerClient.ContainerStartOptions) error {
	_, err := d.cli.ContainerStart(ctx, containerID, options)
	return err
}

// ContainerStop stops a container.
func (d *DockerClient) ContainerStop(ctx context.Context, containerID string, options dockerClient.ContainerStopOptions) error {
	_, err := d.cli.ContainerStop(ctx, containerID, options)
	return err
}

// ContainerWait blocks until the container enters the given condition.
func (d *DockerClient) ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	res := d.cli.ContainerWait(ctx, containerID, dockerClient.ContainerWaitOptions{Condition: condition})
	return res.Result, res.Error
}

// ImageInspect inspects an image.
func (d *DockerClient) ImageInspect(ctx context.Context, imageID string) (image.InspectResponse, error) {
	res, err := d.cli.ImageInspect(ctx, imageID)
	if err != nil {
		return image.InspectResponse{}, err
	}
	return res.InspectResponse, nil
}

// ImagePull pulls an image from a registry.
func (d *DockerClient) ImagePull(ctx context.Context, ref string, options dockerClient.ImagePullOptions) (io.ReadCloser, error) {
	rc, err := d.cli.ImagePull(ctx, ref, options)
	if err != nil {
		return nil, err
	}
	return rc, nil
}
