package internal

import (
	"context"
	"fmt"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	dockerClient "github.com/moby/moby/client"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in       string
		wantType TargetType
		wantName string
		wantErr  bool
	}{
		{"container/foo", TargetTypeContainer, "foo", false},
		{"container/abc123", TargetTypeContainer, "abc123", false},
		{"service/web", TargetTypeService, "web", false},
		{"web", TargetTypeAuto, "web", false},
		{"  trimmed  ", TargetTypeAuto, "trimmed", false},
		{"", "", "", true},
		{"container/", "", "", true},
		{"service/", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseTarget(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Type != tc.wantType || got.Name != tc.wantName {
				t.Fatalf("got %+v, want type=%q name=%q", got, tc.wantType, tc.wantName)
			}
		})
	}
}

func TestResolveTarget_ContainerPrefix(t *testing.T) {
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			if id != "foo" {
				t.Fatalf("expected inspect id=foo, got %q", id)
			}
			return container.InspectResponse{
				ID:    "sha-foo",
				Name:  "/foo",
				State: &container.State{Running: true},
			}, nil
		},
	}
	got, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client: cli,
		Target: ParsedTarget{Type: TargetTypeContainer, Name: "foo"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ContainerID != "sha-foo" || got.ContainerName != "foo" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveTarget_ContainerNotRunning(t *testing.T) {
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ID:    "sha-foo",
				Name:  "/foo",
				State: &container.State{Running: false},
			}, nil
		},
	}
	_, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client: cli,
		Target: ParsedTarget{Type: TargetTypeContainer, Name: "foo"},
	})
	if err == nil {
		t.Fatal("expected error for non-running container")
	}
}

func TestResolveTarget_ServicePicksLowestInstance(t *testing.T) {
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			// Assert the filter was set correctly for project + service.
			values := options.Filters["label"]
			if !values[ComposeProjectLabel+"=proj"] || !values[ComposeServiceLabel+"=web"] {
				t.Fatalf("missing label filters in %v", values)
			}
			return []container.Summary{
				{
					ID:     "sha-2",
					Names:  []string{"/proj-web-2"},
					State:  "running",
					Labels: map[string]string{ComposeContainerNumLabel: "2"},
				},
				{
					ID:     "sha-1",
					Names:  []string{"/proj-web-1"},
					State:  "running",
					Labels: map[string]string{ComposeContainerNumLabel: "1"},
				},
			}, nil
		},
	}

	got, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client:      cli,
		Target:      ParsedTarget{Type: TargetTypeService, Name: "web"},
		ProjectName: "proj",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ContainerID != "sha-1" || got.ContainerName != "proj-web-1" {
		t.Fatalf("expected lowest-numbered replica, got %+v", got)
	}
}

func TestResolveTarget_ServiceNoRunning(t *testing.T) {
	cli := &mockDockerClient{
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			return []container.Summary{{ID: "x", State: "exited"}}, nil
		},
	}
	_, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client:      cli,
		Target:      ParsedTarget{Type: TargetTypeService, Name: "web"},
		ProjectName: "proj",
	})
	if err == nil {
		t.Fatal("expected error when no running container matches service")
	}
}

func TestResolveTarget_ServiceRequiresProject(t *testing.T) {
	_, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client: &mockDockerClient{},
		Target: ParsedTarget{Type: TargetTypeService, Name: "web"},
	})
	if err == nil {
		t.Fatal("expected error when project name missing")
	}
}

func TestResolveTarget_AutoContainerFirst(t *testing.T) {
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{
				ID:    "sha-web",
				Name:  "/web",
				State: &container.State{Running: true},
			}, nil
		},
	}
	got, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client:      cli,
		Target:      ParsedTarget{Type: TargetTypeAuto, Name: "web"},
		ProjectName: "proj",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ContainerID != "sha-web" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveTarget_AutoFallsBackToService(t *testing.T) {
	listCalled := false
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{}, fmt.Errorf("no such container: %w", cerrdefs.ErrNotFound)
		},
		containerList: func(ctx context.Context, options dockerClient.ContainerListOptions) ([]container.Summary, error) {
			listCalled = true
			return []container.Summary{
				{
					ID:     "sha-svc",
					Names:  []string{"/proj-web-1"},
					State:  "running",
					Labels: map[string]string{ComposeContainerNumLabel: "1"},
				},
			}, nil
		},
	}

	got, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client:      cli,
		Target:      ParsedTarget{Type: TargetTypeAuto, Name: "web"},
		ProjectName: "proj",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !listCalled {
		t.Fatal("expected fallback to ContainerList for service")
	}
	if got.ContainerID != "sha-svc" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveTarget_AutoNoMatchNoProject(t *testing.T) {
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{}, fmt.Errorf("no such container: %w", cerrdefs.ErrNotFound)
		},
	}
	_, err := ResolveTarget(context.Background(), ResolveTargetInput{
		Client: cli,
		Target: ParsedTarget{Type: TargetTypeAuto, Name: "web"},
	})
	if err == nil {
		t.Fatal("expected error when no container and no project available")
	}
}

// TestIsNotFound_RecognizesErrdefsWrappedError locks in the migration to
// github.com/containerd/errdefs: a not-found error carried by the client's
// errdefs sentinel must be recognized even when its message does not contain
// the "no such container" string that the fallback path matches on.
func TestIsNotFound_RecognizesErrdefsWrappedError(t *testing.T) {
	if !isNotFound(fmt.Errorf("resource missing: %w", cerrdefs.ErrNotFound)) {
		t.Fatal("expected containerd/errdefs not-found error to be recognized")
	}
	if isNotFound(fmt.Errorf("some unrelated failure")) {
		t.Fatal("unrelated error must not be treated as not-found")
	}
	if isNotFound(nil) {
		t.Fatal("nil error must not be treated as not-found")
	}
}
