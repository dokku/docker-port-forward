package internal

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	dockerClient "github.com/moby/moby/client"
)

// fakeAPIClient implements dockerClient.APIClient by embedding the interface
// and overriding only the methods exercised by the test.
type fakeAPIClient struct {
	dockerClient.APIClient

	closed    bool
	inspected string
}

func (f *fakeAPIClient) Close() error {
	f.closed = true
	return nil
}

func (f *fakeAPIClient) ContainerInspect(ctx context.Context, id string, options dockerClient.ContainerInspectOptions) (dockerClient.ContainerInspectResult, error) {
	f.inspected = id
	return dockerClient.ContainerInspectResult{Container: container.InspectResponse{ID: "full-" + id}}, nil
}

func TestWrapDockerClient_PassesThrough(t *testing.T) {
	fake := &fakeAPIClient{}
	cli := WrapDockerClient(fake)

	info, err := cli.ContainerInspect(context.Background(), "abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.inspected != "abc" || info.ID != "full-abc" {
		t.Fatalf("inspect not passed through: inspected=%q id=%q", fake.inspected, info.ID)
	}

	if err := cli.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if !fake.closed {
		t.Fatal("expected Close to be passed through")
	}
}
