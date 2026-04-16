package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
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
				ContainerJSONBase: &container.ContainerJSONBase{
					ID:    "sha-foo",
					Name:  "/foo",
					State: &container.State{Running: true},
				},
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
				ContainerJSONBase: &container.ContainerJSONBase{
					ID:    "sha-foo",
					Name:  "/foo",
					State: &container.State{Running: false},
				},
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
			// Assert the filter was set correctly for project + service.
			values := options.Filters.Get("label")
			foundProject := false
			foundService := false
			for _, v := range values {
				if v == ComposeProjectLabel+"=proj" {
					foundProject = true
				}
				if v == ComposeServiceLabel+"=web" {
					foundService = true
				}
			}
			if !foundProject || !foundService {
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
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
				ContainerJSONBase: &container.ContainerJSONBase{
					ID:    "sha-web",
					Name:  "/web",
					State: &container.State{Running: true},
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
	if got.ContainerID != "sha-web" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolveTarget_AutoFallsBackToService(t *testing.T) {
	listCalled := false
	cli := &mockDockerClient{
		containerInspect: func(ctx context.Context, id string) (container.InspectResponse, error) {
			return container.InspectResponse{}, errdefs.NotFound(errors.New("no such container"))
		},
		containerList: func(ctx context.Context, options container.ListOptions) ([]container.Summary, error) {
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
			return container.InspectResponse{}, errdefs.NotFound(errors.New("no such container"))
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
