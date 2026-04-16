package internal

import (
	"context"
	"io"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type mockDockerClient struct {
	DockerClientInterface

	containerCreate  func(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error)
	containerInspect func(ctx context.Context, id string) (container.InspectResponse, error)
	containerList    func(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	containerLogs    func(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error)
	containerRemove  func(ctx context.Context, id string, options container.RemoveOptions) error
	containerStart   func(ctx context.Context, id string, options container.StartOptions) error
	containerStop    func(ctx context.Context, id string, options container.StopOptions) error
	containerWait    func(ctx context.Context, id string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
	imageInspect     func(ctx context.Context, id string) (image.InspectResponse, error)
	imagePull        func(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
}

func (m *mockDockerClient) Close() error { return nil }

func (m *mockDockerClient) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, name string) (container.CreateResponse, error) {
	if m.containerCreate != nil {
		return m.containerCreate(ctx, config, hostConfig, networkingConfig, platform, name)
	}
	return container.CreateResponse{}, nil
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	if m.containerInspect != nil {
		return m.containerInspect(ctx, id)
	}
	return container.InspectResponse{}, nil
}

func (m *mockDockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
	if m.containerList != nil {
		return m.containerList(ctx, options)
	}
	return nil, nil
}

func (m *mockDockerClient) ContainerLogs(ctx context.Context, containerID string, options container.LogsOptions) (io.ReadCloser, error) {
	if m.containerLogs != nil {
		return m.containerLogs(ctx, containerID, options)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func (m *mockDockerClient) ContainerRemove(ctx context.Context, id string, options container.RemoveOptions) error {
	if m.containerRemove != nil {
		return m.containerRemove(ctx, id, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerStart(ctx context.Context, id string, options container.StartOptions) error {
	if m.containerStart != nil {
		return m.containerStart(ctx, id, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerStop(ctx context.Context, id string, options container.StopOptions) error {
	if m.containerStop != nil {
		return m.containerStop(ctx, id, options)
	}
	return nil
}

func (m *mockDockerClient) ContainerWait(ctx context.Context, id string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	if m.containerWait != nil {
		return m.containerWait(ctx, id, condition)
	}
	ch := make(chan container.WaitResponse, 1)
	ch <- container.WaitResponse{}
	errCh := make(chan error, 1)
	return ch, errCh
}

func (m *mockDockerClient) ImageInspect(ctx context.Context, id string) (image.InspectResponse, error) {
	if m.imageInspect != nil {
		return m.imageInspect(ctx, id)
	}
	return image.InspectResponse{}, nil
}

func (m *mockDockerClient) ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error) {
	if m.imagePull != nil {
		return m.imagePull(ctx, ref, options)
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// captureLogger accumulates log lines for assertions.
type captureLogger struct {
	info  []string
	warn  []string
	error []string
}

func (l *captureLogger) Info(m string)  { l.info = append(l.info, m) }
func (l *captureLogger) Warn(m string)  { l.warn = append(l.warn, m) }
func (l *captureLogger) Error(m string) { l.error = append(l.error, m) }
